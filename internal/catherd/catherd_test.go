package catherd

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestRefusalParsing(t *testing.T) {
	stderr := "error E_CONFIG_INVALID: the profile was not saved: roles.worker.enabled: the worker cannot be disabled; roles.reviewer.rungs: no usable rung; failover.codex:gpt-6-sol#high: bad: thing\nfix: catherd profile set roles.worker.enabled true\n"
	code, msg, fix := parseStderr(stderr)
	if code != "E_CONFIG_INVALID" || fix != "catherd profile set roles.worker.enabled true" {
		t.Fatal(code, msg, fix)
	}
	issues := refusalIssues(msg, fix)
	if len(issues) != 3 || issues[0].PathDisplay != "roles.worker.enabled" || issues[0].Message != "the worker cannot be disabled" || issues[1].PathDisplay != "roles.reviewer.rungs" {
		t.Fatalf("%+v", issues)
	}
	if !reflect.DeepEqual(issues[2].Path, []string{"failover", "codex:gpt-6-sol#high"}) || issues[2].Message != "bad: thing" || issues[0].Origin != "catherd" {
		t.Fatalf("%+v", issues[2])
	}
	one := refusalIssues("the profile was not saved: something odd", "")
	if len(one) != 1 || one[0].PathDisplay != "profile" {
		t.Fatalf("%+v", one)
	}
	if !reflect.DeepEqual(SplitPath("billing.opencode-go"), []string{"billing", "opencode-go"}) || !reflect.DeepEqual(SplitPath("roles.worker.access"), []string{"roles", "worker", "access"}) {
		t.Fatal("paths")
	}
}
func TestScrubText(t *testing.T) {
	got := ScrubText("failed with token=abcdef and Authorization: Bearer xyz " + strings.Repeat("A", 50) + strings.Repeat(" tail", 1000))
	if strings.Contains(got, "abcdef") || strings.Contains(got, strings.Repeat("A", 50)) || len(got) > 2048 {
		t.Fatal(got[:200])
	}
}
func TestMergePatchRFC7396(t *testing.T) {
	doc, _ := Decode([]byte(`{"a":{"b":1,"c":2},"l":[1,2],"keep":true}`))
	patch, _ := Decode([]byte(`{"a":{"b":null,"d":3},"l":[9],"new":{"x":1}}`))
	got := MergePatch(doc, patch)
	b, _ := json.Marshal(got)
	if string(b) != `{"a":{"c":2,"d":3},"keep":true,"l":[9],"new":{"x":1}}` {
		t.Fatal(string(b))
	}
	if doc["a"].(map[string]any)["b"] == nil {
		t.Fatal("target mutated")
	}
}
func TestPatchBetweenAndRoundTrip(t *testing.T) {
	a, _ := Decode(defaultProfileJSON)
	b := Clone(a).(map[string]any)
	b["budget"] = map[string]any{"usd": json.Number("5")}
	b["roles"].(map[string]any)["reviewer"].(map[string]any)["rungs"] = []any{"codex:gpt-6-luna#high"}
	delete(b["failover"].(map[string]any), "codex:gpt-6-luna#high")
	p := PatchBetween(a, b)
	if _, ok := p["name"]; ok || len(p) != 3 {
		t.Fatalf("%v", p)
	}
	if got := MergePatch(a, p); !same(got, b) {
		t.Fatal("merge(a, patch(a,b)) != b")
	}
	if len(PatchBetween(a, a)) != 0 {
		t.Fatal("identical documents give an empty patch")
	}
	if issues := ValidatePatch(p); len(issues) != 0 {
		t.Fatal(issues)
	}
}
func TestValidatePatch(t *testing.T) {
	bad := func(j string) []Issue {
		p, err := Decode([]byte(j))
		if err != nil {
			t.Fatal(err)
		}
		return ValidatePatch(p)
	}
	cases := map[string]string{
		`{"schema":1}`:                                  "unsupported_field",
		`{"roles":{"worker":{"surprise":1}}}`:           "unsupported_field",
		`{"roles":{"cook":{"enabled":true}}}`:           "unsupported_field",
		`{"roles":{"worker":{"access":"root"}}}`:        "unknown_value",
		`{"roles":{"worker":{"rungs":["codex:gpt#"]}}}`: "bad_rung",
		`{"objective":"fast"}`:                          "unknown_value",
		`{"budget":{"usd":-1}}`:                         "bad_value",
		`{"lock":{"heavy":0}}`:                          "bad_value",
		`{"notify":["email"]}`:                          "unknown_value",
		`{"failover":{"nope":"codex:gpt-6-sol#high"}}`:  "bad_rung",
		`{"harness":{"codex":{"isolated":"yes"}}}`:      "bad_value",
		`{"billing":{"bitcoin":"metered"}}`:             "unsupported_field",
	}
	for j, code := range cases {
		issues := bad(j)
		if len(issues) == 0 || issues[0].Code != code {
			t.Errorf("%s: %+v", j, issues)
		}
	}
	for _, ok := range []string{`{"roles":{"worker":{"defaultRung":null,"network":null,"timeouts":null}}}`, `{"lock":{"heavy":"cpus/2"}}`, `{"budget":{"usd":null}}`, `{"billing":{"codex":null}}`, `{"failover":{"codex:gpt-6-sol#high":null}}`} {
		if issues := bad(ok); len(issues) != 0 {
			t.Errorf("%s: %+v", ok, issues)
		}
	}
}
func TestRungGrammar(t *testing.T) {
	for _, s := range []string{"codex:gpt-6-sol#high", "opencode:opencode-go/gpt-6-luna#high", "claude:claude-opus-5-5#max"} {
		r, ok := ParseRung(s)
		if !ok || r.String() != s {
			t.Errorf("%s", s)
		}
	}
	for _, s := range []string{"", "gpt", "codex:#high", "codex:gpt#", "bing:gpt#high", "codex:-x#high", "codex:a b#high", "codex:gpt#-x"} {
		if _, ok := ParseRung(s); ok {
			t.Errorf("%q accepted", s)
		}
	}
}
func TestCatalogFixture(t *testing.T) {
	c, err := ParseCatalog(fakeCatalogJSON)
	if err != nil || len(c.Models) != 8 {
		t.Fatal(err, len(c.Models))
	}
	m, r, ok := c.Lookup("opencode:opencode/gpt-6-luna#high")
	if !ok || m.ID != "gpt-6-luna" || r.TreatLike == nil || r.TreatLike.Source == "" || m.Name == nil {
		t.Fatal(m, r, ok)
	}
	if fam, _ := c.Family("codex:gpt-6-luna#high"); fam != "gpt-6-luna" {
		t.Fatal(fam)
	}
	if _, ok := c.Family("codex:gpt-6-sool#high"); ok {
		t.Fatal("unknown model matched")
	}
	cases := []struct {
		rung, role string
		backends   map[string]string
		want       string
	}{
		{"codex:gpt-6-luna#high", "worker", nil, Available},
		{"opencode:opencode/gpt-6-sol#high", "worker", nil, NotOffered},
		{"codex:gpt-6-sol#ultra", "worker", nil, NoScore},
		{"codex:gpt-6-sol#high", "artist", nil, UnknownListing},
		{"claude:claude-opus-5-5#high", "artist", nil, RoleIncompatible},
		{"codex:gpt-6-sol#high", "worker", map[string]string{"codex": "fail"}, BackendMissing},
		{"codex:gpt-6-sol#high", "worker", map[string]string{"codex": "skip:missing"}, BackendMissing},
	}
	for _, tc := range cases {
		m, r, ok := c.Lookup(tc.rung)
		if !ok {
			t.Fatal(tc.rung)
		}
		if got := Availability(m, r, tc.role, tc.backends); got != tc.want {
			t.Errorf("%s/%s: %s want %s", tc.rung, tc.role, got, tc.want)
		}
	}
}
func TestFakeMirrorsCatherd(t *testing.T) {
	f := NewFake()
	env := NewOrgEnv(t.TempDir(), orgA)
	ctx := context.Background()
	if err := f.Create(ctx, env, HostCodex, "o-main-v1", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Create(ctx, env, HostCodex, "o-main-v1", nil); err == nil {
		t.Fatal("duplicate create")
	}
	out, err := f.SetKey(ctx, env, HostCodex, "o-main-v1", "budget", map[string]any{"usd": 5}, nil)
	if err != nil || !out.Saved {
		t.Fatal(out, err)
	}
	out, _ = f.SetKey(ctx, env, HostCodex, "o-main-v1", "roles", map[string]any{"worker": map[string]any{"enabled": false}}, nil)
	if out.Saved || len(out.Refused) != 1 || out.Refused[0].PathDisplay != "roles.worker.enabled" {
		t.Fatal(out)
	}
	if doc := f.Doc(env.Home(), "o-main-v1"); doc["roles"].(map[string]any)["worker"].(map[string]any)["enabled"] != true {
		t.Fatal("a refused set must not change the profile")
	}
	out, _ = f.SetKey(ctx, env, HostCodex, "o-main-v1", "roles", map[string]any{"reviewer": map[string]any{"rungs": []string{"codex:gpt-6-sool#high"}}}, nil)
	if out.Saved || len(out.Refused) != 1 {
		t.Fatal("unscored rung", out)
	}
	if _, err = f.SetKey(ctx, env, HostCodex, "o-main-v1", "surprise", 1, nil); err == nil || err.(*Error).Code != CodeRejected {
		t.Fatal("strict keys", err)
	}
	other := NewOrgEnv(t.TempDir(), orgB)
	if names := f.Names(other.Home()); len(names) != 0 {
		t.Fatal("homes must be separate", names)
	}
	v, _ := f.Validate(ctx, env, HostCodex, "o-main-v1", nil)
	if !v.Valid {
		t.Fatal(v)
	}
	f.CrashAfterSets(1)
	if _, err = f.SetKey(ctx, env, HostCodex, "o-main-v1", "objective", "speed", nil); err != nil {
		t.Fatal("the first set after arming succeeds", err)
	}
	if _, err = f.SetKey(ctx, env, HostCodex, "o-main-v1", "objective", "cost", nil); err == nil || strings.Contains(err.Error(), "E_") {
		t.Fatal("injected crash", err)
	}
	f.FailNext("remove", &Error{Code: "E_X", Message: "boom"})
	if err = f.Remove(ctx, env, "o-main-v1"); err == nil {
		t.Fatal("scripted failure")
	}
	if err = f.Remove(ctx, env, "o-main-v1"); err != nil {
		t.Fatal(err)
	}
	// Personal reads are served from the personal map and never recorded as writes.
	f.Personal["mine"] = map[string]any{"schema": json.Number("1"), "name": "mine"}
	pe := NewPersonalEnv(t.TempDir())
	rows, _ := f.ListProfiles(ctx, pe)
	if len(rows) != 1 || rows[0].Name != "mine" {
		t.Fatal(rows)
	}
	if raw, err := f.ShowRaw(ctx, pe, HostCodex, "mine"); err != nil || raw["name"] != "mine" {
		t.Fatal(raw, err)
	}
	for _, c := range f.Calls() {
		if c.Home == "personal" && !contains([]string{"list", "show_raw"}, c.Op) {
			t.Fatal("write against personal state", c)
		}
	}
}
func TestOnlyWriteMethodsTakeOrgEnv(t *testing.T) {
	// Compile-time: PersonalEnv satisfies ReadEnv but not the *OrgEnv parameters of the write methods.
	var _ ReadEnv = (*PersonalEnv)(nil)
	var _ ReadEnv = (*OrgEnv)(nil)
	if _, err := os.Stat("testdata/catalog-small.json"); err != nil {
		t.Fatal(err)
	}
}
