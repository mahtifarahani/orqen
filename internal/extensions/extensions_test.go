package extensions

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain doubles as the helper process (no real tool is ever started).
func TestMain(m *testing.M) {
	switch os.Getenv("EXT_HELPER") {
	case "env":
		env := os.Environ()
		sort.Strings(env)
		fmt.Println(strings.Join(env, "\n"))
		os.Exit(0)
	case "flood":
		for i := 0; i < 1000; i++ {
			fmt.Print(strings.Repeat("x", 100))
		}
		os.Exit(0)
	case "group":
		// start a grandchild in the same process group, print its pid, then hang
		pid, err := syscall.ForkExec("/bin/sleep", []string{"sleep", "60"}, &syscall.ProcAttr{Sys: &syscall.SysProcAttr{}})
		if err != nil {
			os.Exit(3)
		}
		fmt.Println(pid)
		time.Sleep(60 * time.Second)
		os.Exit(0)
	case "exit3":
		fmt.Fprint(os.Stderr, "boom")
		os.Exit(3)
	}
	os.Exit(m.Run())
}

func enabledSnap(m *Manifest) Snapshot {
	s := Snapshot{Status: StatusEnabled, Path: os.Args[0], Version: "1.0.0", SHA256: "abc", Credentials: map[string]bool{}}
	for _, c := range m.CredentialFields {
		s.Credentials[c.Field] = true
	}
	if m.Gated() {
		s.License = &Acceptance{LicenseID: m.License.SPDX}
	}
	return s
}

func TestCatalogIsValidAndReadOnly(t *testing.T) {
	all := All()
	if len(all) != 6 {
		t.Fatalf("catalog has %d entries, want 6", len(all))
	}
	for _, m := range all {
		if err := m.Validate(); err != nil {
			t.Error(err)
		}
		if len(m.WriteTools) != 0 {
			t.Errorf("%s has write tools", m.ID)
		}
	}
	g, _ := Get("gitnexus")
	if !g.Gated() || g.License.SPDX != "PolyForm-Noncommercial-1.0.0" {
		t.Fatalf("gitnexus gate: %+v", g.License)
	}
	bad := *g
	bad.WriteTools = []string{"push"}
	if bad.Validate() == nil {
		t.Fatal("a manifest with write tools must not validate")
	}
}

func TestStateMachine(t *testing.T) {
	for _, id := range []string{"mcp-atlassian", "gitlab-mcp", "figma-mcp"} {
		m, _ := Get(id)
		if CanEnable(m, StatusTested) == nil {
			t.Errorf("%s must not be enableable", id)
		}
	}
	w, _ := Get("openclaw-wiki")
	if err := CanEnable(w, StatusTested); err != nil {
		t.Fatal(err)
	}
	if _, err := Next(StatusLicenseRequired, ActDetect); err == nil {
		t.Fatal("detect must be refused from license_required")
	}
	steps := []struct {
		from Status
		act  Action
		want Status
	}{
		{StatusAbsent, ActRegister, StatusRegistered},
		{StatusRegistered, ActNeedLicense, StatusLicenseRequired},
		{StatusLicenseRequired, ActAcceptLicense, StatusRegistered},
		{StatusRegistered, ActDetectOK, StatusDetected},
		{StatusRegistered, ActDetectFail, StatusDetectFailed},
		{StatusDetected, ActTestOK, StatusTested},
		{StatusDetected, ActTestFail, StatusTestFailed},
		{StatusTested, ActEnable, StatusEnabled},
		{StatusEnabled, ActDisable, StatusDisabled},
		{StatusEnabled, ActChanged, StatusChanged},
		{StatusEnabled, ActRevokeLicense, StatusDisabled},
		{StatusChanged, ActDetectOK, StatusDetected},
	}
	for _, s := range steps {
		got, err := Next(s.from, s.act)
		if err != nil || got != s.want {
			t.Errorf("%s --%s--> %s (%v), want %s", s.from, s.act, got, err, s.want)
		}
	}
	for _, s := range []struct {
		from Status
		act  Action
	}{
		{StatusAbsent, ActDetect}, {StatusRegistered, ActEnable}, {StatusDetected, ActEnable}, {StatusChanged, ActEnable},
		{StatusChanged, ActTest}, {StatusRegistered, ActDisable}, {StatusAbsent, ActChanged}, {StatusTested, ActAcceptLicense},
	} {
		if _, err := Next(s.from, s.act); err == nil {
			t.Errorf("%s --%s--> must be refused", s.from, s.act)
		}
	}
	if len(Statuses) != 10 {
		t.Fatal("ten states")
	}
}

func TestEveryUsableReasonIsReachable(t *testing.T) {
	g, _ := Get("gitnexus")
	s, _ := Get("strix")
	w, _ := Get("openclaw-wiki")
	atl, _ := Get("mcp-atlassian")
	with := func(m *Manifest, f func(*Snapshot)) string {
		sn := enabledSnap(m)
		f(&sn)
		_, r := Usable(m, sn)
		return r
	}
	got := map[string]string{
		ReasonNotRegistered:     with(w, func(x *Snapshot) { x.Status = StatusAbsent }),
		ReasonLicenseRequired:   with(g, func(x *Snapshot) { x.License = nil }),
		ReasonLicenseRevoked:    with(g, func(x *Snapshot) { now := time.Now(); x.License.RevokedAt = &now }),
		ReasonNotDetected:       with(w, func(x *Snapshot) { x.Status = StatusRegistered }),
		ReasonNotTested:         with(w, func(x *Snapshot) { x.Status = StatusDetected }),
		ReasonDisabled:          with(w, func(x *Snapshot) { x.Status = StatusDisabled }),
		ReasonBinaryChanged:     with(w, func(x *Snapshot) { x.Status = StatusChanged }),
		ReasonCredentialMissing: with(s, func(x *Snapshot) { x.Credentials = nil }),
		ReasonConfigInvalid:     with(s, func(x *Snapshot) { x.Config = map[string]any{"nope": 1.0} }),
		ReasonProjectNotBound:   with(w, func(x *Snapshot) { x.ProjectScoped = true }),
	}
	for want, have := range got {
		if want != have {
			t.Errorf("reason %q was reported as %q", want, have)
		}
	}
	if len(got) != len(Reasons) {
		t.Fatal("a reason is not covered")
	}
	if r := with(atl, func(*Snapshot) {}); r != ReasonDisabled {
		t.Fatalf("a detect-only entry is never usable, got %q", r)
	}
	p, r := Usable(w, enabledSnap(w))
	if r != "" || !p.Valid() || p.Extension() != "openclaw-wiki" {
		t.Fatalf("an enabled entry must yield a permit: %q", r)
	}
	if (Permit{}).Valid() {
		t.Fatal("the zero permit is invalid")
	}
}

func TestLicenseAcceptance(t *testing.T) {
	g, _ := Get("gitnexus")
	w, _ := Get("openclaw-wiki")
	notice := NoticeSHA("notice text")
	now := time.Now()
	ok := AcceptInput{Basis: BasisNoncommercial, Note: "personal evaluation", ConfirmPhrase: "gitnexus", NoticeSHA256: notice}
	if a, err := Accept(g, ok, []string{notice}, now); err != nil || a.LicenseID != "PolyForm-Noncommercial-1.0.0" || a.Basis != BasisNoncommercial {
		t.Fatalf("accept: %+v %v", a, err)
	}
	code := func(m *Manifest, in AcceptInput, known ...string) string {
		_, err := Accept(m, in, known, now)
		if le, ok := err.(*LicenseError); ok {
			return le.Code
		}
		return ""
	}
	bad := ok
	bad.ConfirmPhrase = "GitNexus"
	if code(g, bad, notice) != "confirm_phrase_mismatch" {
		t.Error("phrase must match exactly")
	}
	bad = ok
	bad.NoticeSHA256 = NoticeSHA("other")
	if code(g, bad, notice) != "license_notice_mismatch" {
		t.Error("notice digest must match")
	}
	for _, f := range []func(*AcceptInput){func(i *AcceptInput) { i.Basis = "x" }, func(i *AcceptInput) { i.Note = "  " }, func(i *AcceptInput) { i.Note = strings.Repeat("a", 2001) }} {
		bad = ok
		f(&bad)
		if code(g, bad, notice) != "license_input_invalid" {
			t.Error("basis and note are required")
		}
	}
	if code(w, ok, notice) != "license_not_required" {
		t.Error("an ungated entry has no license")
	}
}

func TestConfigSchema(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"n"}, "properties": map[string]any{
		"n": map[string]any{"type": "integer", "minimum": 1.0, "maximum": 3.0},
		"s": map[string]any{"type": "string", "maxLength": 3.0, "enum": []any{"ab", "abc"}},
		"b": map[string]any{"type": "boolean"},
	}}
	if err := CheckSchema(schema); err != nil {
		t.Fatal(err)
	}
	if got := ValidateConfig(schema, map[string]any{"n": 2.0, "s": "ab", "b": true}); len(got) != 0 {
		t.Fatalf("valid config: %v", got)
	}
	got := ValidateConfig(schema, map[string]any{"n": 9.0, "s": "zzzz", "x": 1.0})
	codes := map[string]string{}
	for _, i := range got {
		codes[i.Path] += i.Code + ","
	}
	if codes["n"] != "maximum," || codes["s"] != "enum,max_length," || codes["x"] != "unknown_property," {
		t.Fatalf("issues: %v", codes)
	}
	if got = ValidateConfig(schema, map[string]any{}); len(got) != 1 || got[0].Code != "required" {
		t.Fatalf("required: %v", got)
	}
	if got = ValidateConfig(nil, map[string]any{"a": 1.0}); len(got) != 1 {
		t.Fatalf("nil schema takes an empty config only: %v", got)
	}
	if CheckSchema(map[string]any{"type": "tuple"}) == nil {
		t.Fatal("unsupported type")
	}
}

func TestBuildEnvPanicsOutsideAllowlist(t *testing.T) {
	env := BuildEnv([]string{"A=1"}, []string{"K"}, map[string]string{"K": "v"})
	if strings.Join(env, ",") != "A=1,K=v" {
		t.Fatalf("env: %v", env)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("an unknown name must panic")
		}
	}()
	BuildEnv(nil, []string{"K"}, map[string]string{"OTHER": "v"})
}

func helperPermit(t *testing.T) Permit {
	w, _ := Get("openclaw-wiki")
	p, r := Usable(w, enabledSnap(w))
	if r != "" {
		t.Fatal(r)
	}
	return p
}

func TestExecEnvironmentIsExactlyTheBuiltList(t *testing.T) {
	t.Setenv("PARENT_ONLY_VALUE", "must-not-leak")
	p := helperPermit(t)
	want := []string{"EXT_HELPER=env", "LANG=C.UTF-8"}
	res := Exec(context.Background(), p, Spec{Bin: p.Path(), Env: want, Timeout: 10 * time.Second})
	if res.Err != nil || res.Exit != 0 {
		t.Fatalf("exec: %+v", res)
	}
	got := strings.Split(strings.TrimSpace(string(res.Stdout)), "\n")
	sort.Strings(want)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("child env %v, want exactly %v", got, want)
	}
	// a nil env is empty, not inherited
	res = Exec(context.Background(), p, Spec{Bin: p.Path(), Env: nil, Timeout: 10 * time.Second})
	if strings.Contains(string(res.Stdout), "PARENT_ONLY_VALUE") {
		t.Fatal("nil env inherited the parent environment")
	}
}

func TestExecRefusals(t *testing.T) {
	p := helperPermit(t)
	if r := Exec(context.Background(), Permit{}, Spec{Bin: "/bin/true"}); r.Err == nil {
		t.Error("no permit, no exec")
	}
	if r := Exec(context.Background(), p, Spec{Bin: "/bin/true"}); r.Err != ErrBinMismatch {
		t.Errorf("binary must equal the permit: %v", r.Err)
	}
	if r := Exec(context.Background(), p, Spec{Bin: "relative"}); r.Err != ErrNotAbsolute {
		t.Errorf("relative path: %v", r.Err)
	}
	r := Exec(context.Background(), p, Spec{Bin: p.Path(), Env: []string{"EXT_HELPER=exit3"}})
	if r.Exit != 3 || r.StderrTail != "boom" || r.Err != nil {
		t.Errorf("exit code and stderr tail: %+v", r)
	}
}

func TestExecTimeoutKillsTheProcessGroup(t *testing.T) {
	p := helperPermit(t)
	start := time.Now()
	res := Exec(context.Background(), p, Spec{Bin: p.Path(), Env: []string{"EXT_HELPER=group"}, Timeout: 1500 * time.Millisecond})
	if !res.TimedOut {
		t.Fatalf("expected a timeout: %+v", res)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatal("the timeout did not stop the process")
	}
	var pid int
	if _, err := fmt.Sscan(strings.TrimSpace(string(res.Stdout)), &pid); err != nil || pid <= 0 {
		t.Fatalf("grandchild pid not printed: %q", res.Stdout)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("grandchild %d survived the timeout", pid)
}

func TestExecStdoutCap(t *testing.T) {
	p := helperPermit(t)
	res := Exec(context.Background(), p, Spec{Bin: p.Path(), Env: []string{"EXT_HELPER=flood"}, StdoutCap: 1000})
	if !res.Overflow || len(res.Stdout) != 1000 {
		t.Fatalf("cap: overflow=%v len=%d", res.Overflow, len(res.Stdout))
	}
}

func TestSandboxProfiles(t *testing.T) {
	dir := t.TempDir()
	a := DenyNetworkWriteUnder(dir, "/var/run/docker.sock")
	if !strings.Contains(a, "(deny network*)") || !strings.Contains(a, "(deny file-write*)") || !strings.Contains(a, "docker.sock") {
		t.Fatalf("profile: %s", a)
	}
	if b := WriteUnder(dir); strings.Contains(b, "network") {
		t.Fatalf("WriteUnder must leave the network alone: %s", b)
	}
}

func TestValidateJSONIsStrictAndHandlesArrays(t *testing.T) {
	schema := []byte(`{"type":"object","required":["items"],"properties":{"items":{"type":"array","maxItems":2,"items":{"type":"object","required":["p"],"properties":{"p":{"type":"string"}}}}}}`)
	if err := ValidateJSON(schema, []byte(`{"items":[{"p":"a"}]}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"items":[{"p":1}]}`, `{"items":[{}]}`, `{"items":[{"p":"a","x":1}]}`, `{"items":[1,2,3]}`, `{"items":"no"}`, `{}`, `{"items":[],"extra":1}`, `nope`} {
		if err := ValidateJSON(schema, []byte(bad)); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
}

func TestProbePermit(t *testing.T) {
	g, _ := Get("gitnexus")
	if _, why := Probe(g, Snapshot{Status: StatusLicenseRequired, Path: "/x"}, "ab"); why != ReasonLicenseRequired {
		t.Fatalf("gated without license: %q", why)
	}
	now := time.Now()
	if _, why := Probe(g, Snapshot{Status: StatusRegistered, Path: "/x", License: &Acceptance{RevokedAt: &now}}, "ab"); why != ReasonLicenseRevoked {
		t.Fatalf("revoked: %q", why)
	}
	p, why := Probe(g, Snapshot{Status: StatusRegistered, Path: "/x", License: &Acceptance{}}, "ab")
	if why != "" || !p.Valid() || p.SHA256() != "ab" {
		t.Fatalf("probe: %v %q", p, why)
	}
	w, _ := Get("openclaw-wiki")
	if _, why := Probe(w, Snapshot{Status: StatusAbsent}, "ab"); why != ReasonNotRegistered {
		t.Fatalf("absent: %q", why)
	}
}

func TestGatedEntriesHaveNotices(t *testing.T) {
	for _, m := range All() {
		if m.Gated() && len(NoticeDigests(m.ID)) != 2 {
			t.Fatalf("%s has no notice in both locales", m.ID)
		}
	}
}
