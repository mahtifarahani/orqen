package server

import (
	"strings"
	"testing"
	"time"
)

func cmd(key string, argv ...string) CommandInput {
	return CommandInput{Key: key, Kind: "test", Argv: argv}
}
func TestCommandValidation(t *testing.T) {
	now := time.Now()
	out, es := validateCommands([]CommandInput{cmd("test", "pnpm", "run", "test")}, nil, "web", now)
	if len(es) != 0 || out[0].Cwd != "web" || out[0].TimeoutSecs != 600 || out[0].Approved || len(out[0].Fingerprint) != 64 {
		t.Fatal(out, es)
	}
	// A shell string is one argv element and is never split or wrapped in sh -c.
	out, es = validateCommands([]CommandInput{cmd("test", "pnpm test && rm -rf /")}, nil, "", now)
	if len(es) != 0 || len(out[0].Argv) != 1 || out[0].Argv[0] != "pnpm test && rm -rf /" || out[0].Cwd != "." {
		t.Fatal(out, es)
	}
	bad := map[string]CommandInput{
		"commands[0].key":           {Key: "Bad Key", Kind: "test", Argv: []string{"a"}},
		"commands[0].kind":          {Key: "k", Kind: "deploy", Argv: []string{"a"}},
		"commands[0].argv":          {Key: "k", Kind: "test"},
		"commands[0].argv[0]":       {Key: "k", Kind: "test", Argv: []string{""}},
		"commands[0].cwd":           {Key: "k", Kind: "test", Argv: []string{"a"}, Cwd: ptr("../x")},
		"commands[0].timeout_secs":  {Key: "k", Kind: "test", Argv: []string{"a"}, TimeoutSecs: ptr(7201)},
		"commands[0].env[0].name":   {Key: "k", Kind: "test", Argv: []string{"a"}, Env: []EnvVarInput{{"lower", EnvSourceInput{Kind: "host", Var: ptr("HOME")}}}},
		"commands[0].env[0].source": {Key: "k", Kind: "test", Argv: []string{"a"}, Env: []EnvVarInput{{"NPM_TOKEN", EnvSourceInput{Kind: "literal", Value: ptr("abc")}}}},
		"commands[0].origin":        {Key: "k", Kind: "test", Argv: []string{"a"}, Origin: &CommandOriginInput{Kind: "contract"}},
	}
	for field, c := range bad {
		_, es := validateCommands([]CommandInput{c}, nil, "", now)
		found := false
		for _, e := range es {
			found = found || e.Field == field
		}
		if !found {
			t.Errorf("%s not reported: %v", field, es)
		}
	}
	if _, es = validateCommands([]CommandInput{cmd("a", "x"), cmd("a", "y")}, nil, "", now); len(es) != 1 || es[0].Field != "commands[1].key" {
		t.Fatal(es)
	}
	if _, es = validateCommands([]CommandInput{{Key: "k", Kind: "test", Argv: []string{strings.Repeat("a", 4097)}}}, nil, "", now); len(es) != 1 {
		t.Fatal(es)
	}
	okEnv := CommandInput{Key: "k", Kind: "test", Argv: []string{"a"}, Env: []EnvVarInput{{"NPM_TOKEN", EnvSourceInput{Kind: "host", Var: ptr("NPM_TOKEN")}}, {"CI", EnvSourceInput{Kind: "literal", Value: ptr("1")}}}}
	if out, es = validateCommands([]CommandInput{okEnv}, nil, "", now); len(es) != 0 || len(out[0].Env) != 2 {
		t.Fatal(out, es)
	}
}
func TestCommandApprovalRules(t *testing.T) {
	now := time.Now()
	first, _ := validateCommands([]CommandInput{{Key: "test", Kind: "test", Argv: []string{"pnpm", "test"}, Approved: ptr(true)}}, nil, "", now)
	if !first[0].Approved || first[0].ApprovedAt == nil {
		t.Fatal(first)
	}
	same, _ := validateCommands([]CommandInput{{Key: "test", Kind: "test", Argv: []string{"pnpm", "test"}}}, first, "", now.Add(time.Hour))
	if !same[0].Approved || !same[0].ApprovedAt.Equal(*first[0].ApprovedAt) {
		t.Fatal("unchanged command lost its approval", same)
	}
	changed, _ := validateCommands([]CommandInput{{Key: "test", Kind: "test", Argv: []string{"pnpm", "test", "--all"}}}, first, "", now)
	if changed[0].Approved || changed[0].ApprovedAt != nil {
		t.Fatal("changed argv must drop approval", changed)
	}
	envChanged, _ := validateCommands([]CommandInput{{Key: "test", Kind: "test", Argv: []string{"pnpm", "test"}, Env: []EnvVarInput{{"CI", EnvSourceInput{Kind: "literal", Value: ptr("1")}}}}}, first, "", now)
	if envChanged[0].Approved {
		t.Fatal("changed env must drop approval")
	}
	timeoutChanged, _ := validateCommands([]CommandInput{{Key: "test", Kind: "test", Argv: []string{"pnpm", "test"}, TimeoutSecs: ptr(30)}}, first, "", now)
	if timeoutChanged[0].Approved {
		t.Fatal("changed timeout must drop approval")
	}
	reapproved, _ := validateCommands([]CommandInput{{Key: "test", Kind: "test", Argv: []string{"pnpm", "test", "--all"}, Approved: ptr(true)}}, first, "", now)
	if !reapproved[0].Approved {
		t.Fatal("explicit approval")
	}
	revoked, _ := validateCommands([]CommandInput{{Key: "test", Kind: "test", Argv: []string{"pnpm", "test"}, Approved: ptr(false)}}, first, "", now)
	if revoked[0].Approved {
		t.Fatal("explicit revoke")
	}
}
func TestProtectedPathValidation(t *testing.T) {
	out, es := validateProtectedPaths([]ProtectedPathInput{{" .github/** ", "deny", nil}, {"**/*.pem", "review", ptr("keys")}})
	if len(es) != 0 || out[0].Pattern != ".github/**" {
		t.Fatal(out, es)
	}
	for _, p := range []string{"[", "", "/etc/**", "../x", "a/../b", strings.Repeat("a", 513)} {
		if _, es = validateProtectedPaths([]ProtectedPathInput{{p, "deny", nil}}); len(es) == 0 {
			t.Errorf("%q accepted", p)
		}
	}
	if _, es = validateProtectedPaths([]ProtectedPathInput{{"a", "allow", nil}}); len(es) != 1 {
		t.Fatal(es)
	}
	if _, es = validateProtectedPaths(make([]ProtectedPathInput, 201)); len(es) != 1 {
		t.Fatal(es)
	}
}
func TestPathAndBranchRules(t *testing.T) {
	for in, want := range map[string]string{"": "", ".": "", "web": "web", "web/": "web", "a//b/./c": "a/b/c"} {
		if got, ok := normalizeRelPath(in); !ok || got != want {
			t.Errorf("%q -> %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"/abs", "../x", "a/../../b", "a\\b", strings.Repeat("a", 256)} {
		if _, ok := normalizeRelPath(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
	for _, b := range []string{"main", "feature/x-1", "release_1.2"} {
		if !validBranchName(b) {
			t.Errorf("%q rejected", b)
		}
	}
	for _, b := range []string{"", "/x", "x/", "a..b", "a//b", "x.lock", "has space", "a:b"} {
		if validBranchName(b) {
			t.Errorf("%q accepted", b)
		}
	}
}
