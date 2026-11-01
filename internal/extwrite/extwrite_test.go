package extwrite

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
)

func base() DecideInput {
	return DecideInput{
		Op:         OpView{Action: MRCreate, ConnectionID: "con_1", ProviderProjectID: "1", TargetURL: "http://127.0.0.1:4000/api/v4", IntentHash: "h1"},
		Settings:   Settings{Mode: ModeLive, Allowlist: []AllowEntry{{"con_1", "1"}}},
		Scope:      ScopeLoopbackOnly,
		PolicyMode: PolicyManual, HasCredential: true,
		Approval: &Approval{IntentHash: "h1"},
	}
}

func TestDecideTableAndPrecedence(t *testing.T) {
	type row struct {
		name   string
		mut    func(*DecideInput)
		want   Outcome
		reason string
	}
	rows := []row{
		{"all green", func(*DecideInput) {}, OutcomeExecute, ReasonApproved},
		{"auto policy", func(in *DecideInput) { in.PolicyMode, in.Approval = PolicyAuto, nil }, OutcomeExecute, ReasonPolicyAuto},
		{"kill switch beats everything", func(in *DecideInput) { in.KillSwitch = true }, OutcomeDryRun, ReasonKillSwitch},
		{"kill switch beats env guard", func(in *DecideInput) { in.KillSwitch = true; in.Op.TargetURL = "https://gitlab.example.invalid" }, OutcomeDryRun, ReasonKillSwitch},
		{"paused", func(in *DecideInput) { in.Settings.Paused = true }, OutcomeDryRun, ReasonPaused},
		{"unsupported action", func(in *DecideInput) { in.Op.Action = JiraComment }, OutcomeRefused, ReasonActionRefused},
		{"jira transition refused even in live", func(in *DecideInput) { in.Op.Action = JiraTransition }, OutcomeRefused, ReasonActionRefused},
		{"env guard beats live", func(in *DecideInput) { in.Op.TargetURL = "https://gitlab.example.invalid/api/v4" }, OutcomeRefusedEnv, ReasonEnvGuard},
		{"env guard beats dry run mode", func(in *DecideInput) {
			in.Settings.Mode = ModeDryRun
			in.Op.TargetURL = "https://gitlab.example.invalid"
		}, OutcomeRefusedEnv, ReasonEnvGuard},
		{"git push needs an IP literal", func(in *DecideInput) { in.Op.Action = GitPush; in.Op.TargetURL = "http://localhost:4000/g.git" }, OutcomeRefusedEnv, ReasonEnvGuard},
		{"git push IP literal ok", func(in *DecideInput) { in.Op.Action = GitPush; in.Op.TargetURL = "http://127.0.0.1:4000/g.git" }, OutcomeExecute, ReasonApproved},
		{"ipv6 loopback", func(in *DecideInput) { in.Op.TargetURL = "http://[::1]:4000/api" }, OutcomeExecute, ReasonApproved},
		{"localhost name for http", func(in *DecideInput) { in.Op.TargetURL = "http://localhost:4000/api" }, OutcomeExecute, ReasonApproved},
		{"lookalike host", func(in *DecideInput) { in.Op.TargetURL = "http://127.0.0.1.evil.example/api" }, OutcomeRefusedEnv, ReasonEnvGuard},
		{"userinfo trick", func(in *DecideInput) { in.Op.TargetURL = "http://127.0.0.1@evil.example/api" }, OutcomeRefusedEnv, ReasonEnvGuard},
		{"dry run mode", func(in *DecideInput) { in.Settings.Mode = ModeDryRun }, OutcomeDryRun, ReasonModeDryRun},
		{"empty mode is dry run", func(in *DecideInput) { in.Settings.Mode = "" }, OutcomeDryRun, ReasonModeDryRun},
		{"not allowlisted project", func(in *DecideInput) { in.Op.ProviderProjectID = "2" }, OutcomeDryRun, ReasonNotAllowlisted},
		{"not allowlisted connection", func(in *DecideInput) { in.Op.ConnectionID = "con_2" }, OutcomeDryRun, ReasonNotAllowlisted},
		{"empty allowlist", func(in *DecideInput) { in.Settings.Allowlist = nil }, OutcomeDryRun, ReasonNotAllowlisted},
		{"no credential", func(in *DecideInput) { in.HasCredential = false }, OutcomeDryRun, ReasonNoCredential},
		{"policy off", func(in *DecideInput) { in.PolicyMode = PolicyOff }, OutcomeDryRun, ReasonPolicyOff},
		{"manual without approval", func(in *DecideInput) { in.Approval = nil }, OutcomeAwaitingApproval, ReasonNeedsApproval},
		{"manual with approval of another hash", func(in *DecideInput) { in.Approval = &Approval{"other"} }, OutcomeAwaitingApproval, ReasonNeedsApproval},
		{"manual with empty approval hash", func(in *DecideInput) { in.Approval = &Approval{""}; in.Op.IntentHash = "" }, OutcomeAwaitingApproval, ReasonNeedsApproval},
		{"any host scope accepts a public host", func(in *DecideInput) {
			in.Scope = ScopeAnyHost
			in.Op.TargetURL = "https://gitlab.example.invalid/api/v4"
		}, OutcomeExecute, ReasonApproved},
	}
	for _, r := range rows {
		in := base()
		r.mut(&in)
		d := Decide(in)
		if d.Outcome() != r.want || len(d.Reasons()) == 0 || d.Reasons()[0] != r.reason {
			t.Errorf("%s: got %s %v, want %s %s", r.name, d.Outcome(), d.Reasons(), r.want, r.reason)
		}
	}
}

func TestCurrentScope(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	// Under the test binary the scope is always loopback only, even if every other condition holds.
	old := BuildChannel
	defer func() { BuildChannel = old }()
	BuildChannel = "release"
	if got := CurrentScope(env(map[string]string{"ORQEN_ENV": "prod"})); got != ScopeLoopbackOnly {
		t.Fatalf("test binary must be loopback only, got %s", got)
	}
	BuildChannel = "dev"
	for _, e := range []map[string]string{{}, {"ORQEN_ENV": "prod"}, {"ORQEN_ENV": "test"}, {"ORQEN_ENV": "prod", "ORQEN_DEV": "true"}} {
		if CurrentScope(env(e)) != ScopeLoopbackOnly {
			t.Fatalf("dev channel must be loopback only: %v", e)
		}
	}
	g := CurrentGuard(env(map[string]string{"ORQEN_ENV": "weird", "ORQEN_EXTERNAL_WRITES": "OFF"}))
	if g.OrqenEnv != "dev" || !g.KillSwitch || g.BuildChannel != "dev" || g.Scope != ScopeLoopbackOnly {
		t.Fatalf("%+v", g)
	}
	if KillSwitch(env(map[string]string{})) || !KillSwitch(env(map[string]string{"ORQEN_EXTERNAL_WRITES": " off "})) {
		t.Fatal("kill switch")
	}
}

func TestDialerRefusesPublicAddressWithoutDialing(t *testing.T) {
	dialed := 0
	d := &Dialer{Scope: ScopeLoopbackOnly,
		Lookup: func(_ context.Context, host string) ([]net.IP, error) {
			if host == "rebind.example.invalid" {
				return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("203.0.113.9")}, nil
			}
			return []net.IP{net.ParseIP("203.0.113.9")}, nil
		},
		Dial: func(context.Context, string, string) (net.Conn, error) {
			dialed++
			return nil, errors.New("must not dial")
		},
	}
	for _, addr := range []string{"gitlab.example.invalid:443", "rebind.example.invalid:80", "203.0.113.9:443", "[2001:db8::1]:443"} {
		if _, err := d.DialContext(context.Background(), "tcp", addr); !errors.Is(err, ErrNotLoopback) {
			t.Fatalf("%s: %v", addr, err)
		}
	}
	if dialed != 0 || d.Dials() != 0 {
		t.Fatalf("dialed %d times", dialed)
	}
	// A loopback name is resolved by us and the literal IP is dialed.
	var got string
	d2 := &Dialer{Scope: ScopeLoopbackOnly,
		Lookup: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("::1")}, nil },
		Dial:   func(_ context.Context, _, addr string) (net.Conn, error) { got = addr; return nil, errors.New("stop") },
	}
	_, _ = d2.DialContext(context.Background(), "tcp", "localhost:8080")
	if got != "[::1]:8080" || d2.Dials() != 1 {
		t.Fatalf("%q %d", got, d2.Dials())
	}
	// Any-host scope does not restrict.
	d3 := &Dialer{Scope: ScopeAnyHost, Dial: func(_ context.Context, _, addr string) (net.Conn, error) { got = addr; return nil, errors.New("stop") }}
	_, _ = d3.DialContext(context.Background(), "tcp", "203.0.113.9:443")
	if got != "203.0.113.9:443" {
		t.Fatal(got)
	}
}

func TestClientNeverFollowsRedirectsOrUsesProxy(t *testing.T) {
	c := NewClient(ScopeLoopbackOnly, nil, nil)
	if err := c.CheckRedirect(nil, nil); err == nil {
		t.Fatal("redirects must not be followed")
	}
	tr := c.Transport.(*http.Transport)
	if tr.Proxy != nil {
		t.Fatal("a loopback-only write transport must ignore proxies")
	}
}

func TestGateExecuteRedecides(t *testing.T) {
	in := base()
	calls := 0
	g := &Gate{Scope: ScopeLoopbackOnly, Load: func(context.Context, OpView) (DecideInput, error) { calls++; return in, nil },
		Credential: func(context.Context, string) (string, error) { return "sentinel-token", nil }}
	op := in.Op
	d, err := g.Decide(context.Background(), op)
	if err != nil || d.Outcome() != OutcomeExecute {
		t.Fatal(d, err)
	}
	ran := false
	if err := g.Execute(context.Background(), d, op, func(p *Permit) error {
		ran = true
		c, e := p.Credential()
		if c != "sentinel-token" || e != nil {
			t.Fatal("credential")
		}
		return nil
	}); err != nil || !ran {
		t.Fatal(err, ran)
	}
	// The org is paused between Decide and Execute: no write.
	in.Settings.Paused = true
	ran = false
	err = g.Execute(context.Background(), d, op, func(*Permit) error { ran = true; return nil })
	var np *NotPermittedError
	if !errors.As(err, &np) || ran || np.Decision.Reasons()[0] != ReasonPaused {
		t.Fatal(err, ran)
	}
	// A dry-run decision cannot be executed even when the inputs are green now.
	in.Settings.Paused = false
	in.Settings.Mode = ModeDryRun
	dd, _ := g.Decide(context.Background(), op)
	in.Settings.Mode = ModeLive
	if err = g.Execute(context.Background(), dd, op, func(*Permit) error { ran = true; return nil }); !errors.As(err, &np) || ran {
		t.Fatal("a non-execute decision must not run", err)
	}
	// The intent changed after the decision.
	op2 := op
	op2.IntentHash = "h2"
	in.Approval = &Approval{"h2"}
	if err = g.Execute(context.Background(), d, op2, func(*Permit) error { ran = true; return nil }); !errors.As(err, &np) || ran {
		t.Fatal("a changed intent must not run", err)
	}
	if !strings.Contains(np.Error(), "not permitted") {
		t.Fatal(np.Error())
	}
}

func TestIntentHashAndMarker(t *testing.T) {
	a := IntentHash(map[string]any{"b": 1, "a": []string{"x"}})
	b := IntentHash(map[string]any{"a": []string{"x"}, "b": 1})
	if a != b || len(a) != 64 || a == IntentHash(map[string]any{"a": []string{"y"}, "b": 1}) {
		t.Fatal(a, b)
	}
	m := Marker("run_1", "mr_create:run_1")
	if !strings.HasPrefix(m, "<!-- orqen:run=run_1 intent=") || !strings.HasSuffix(m, " -->") || m != Marker("run_1", "mr_create:run_1") {
		t.Fatal(m)
	}
}
