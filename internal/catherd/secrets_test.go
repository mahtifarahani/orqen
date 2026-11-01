package catherd

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestVarsWithAllowlistAndPersonalHasNoInjection(t *testing.T) {
	rt := rtWith(nil)
	e := NewOrgEnv("/data", orgA)
	got := e.VarsWith(rt, SecretEnv{"XAI_API_KEY": "x-1", "TYPESAFE_API_KEY": "t-1"})
	if n := len(got); n != len(e.Vars(rt))+2 || got[n-2] != "TYPESAFE_API_KEY=t-1" || got[n-1] != "XAI_API_KEY=x-1" {
		t.Fatal(got)
	}
	sync := e.SyncVarsWith(rt, SecretEnv{"ARTIFICIAL_ANALYSIS_API_KEY": "a-1"})
	if strings.Contains(strings.Join(sync, " "), "CATHERD_NO_SYNC") || sync[len(sync)-1] != "ARTIFICIAL_ANALYSIS_API_KEY=a-1" {
		t.Fatal(sync)
	}
	if len(e.VarsWith(rt, nil)) != len(e.Vars(rt)) {
		t.Fatal("no secrets must add nothing")
	}
	for _, bad := range []string{"ANTHROPIC_API_KEY", "PATH", "CATHERD_HOME", "typesafe_api_key"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s accepted", bad)
				}
			}()
			e.VarsWith(rt, SecretEnv{bad: "v"})
		}()
	}
	// PersonalEnv has no injection path: no VarsWith/SyncVarsWith, and it is not an *OrgEnv.
	pt := reflect.TypeOf(&PersonalEnv{})
	for _, m := range []string{"VarsWith", "SyncVarsWith"} {
		if _, ok := pt.MethodByName(m); ok {
			t.Fatal("PersonalEnv must not accept secrets:", m)
		}
	}
	for _, v := range NewPersonalEnv("/data").Vars(rt) {
		for _, n := range AllowedSecretEnv {
			if strings.HasPrefix(v, n+"=") {
				t.Fatal(v)
			}
		}
	}
	// Compile-time: injecting calls take *OrgEnv, so a ReadEnv (PersonalEnv) cannot be passed.
	var _ func(Port, context.Context, *OrgEnv, string, SecretEnv) = func(p Port, c context.Context, e *OrgEnv, h string, s SecretEnv) { _, _, _ = p.Doctor(c, e, h, s) }
}
func TestCLIScrubsInjectedValues(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin-catherd")
	// Prints the injected key on stdout (inside JSON) and stderr, and fails like a catherd error would.
	script := "#!/bin/sh\necho \"{\\\"ready\\\":true,\\\"leak\\\":\\\"$TYPESAFE_API_KEY\\\"}\"\necho \"E_BAD: token $TYPESAFE_API_KEY rejected\" >&2\nexit 3\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(context.Background(), RuntimeConfig{Bin: bin, Path: "/usr/bin:/bin", UserHome: dir, DataDir: dir, Getenv: func(string) string { return "" }})
	cli := &CLI{RT: rt, Neutral: dir}
	env := NewOrgEnv(dir, orgA)
	if err := env.Ensure(); err != nil {
		t.Fatal(err)
	}
	const sentinel = "orqen-SENTINEL-01TESTVALUE"
	raw, exit, err := cli.Doctor(context.Background(), env, HostCodex, SecretEnv{"TYPESAFE_API_KEY": sentinel})
	if err != nil || exit != 3 {
		t.Fatal(exit, err)
	}
	if strings.Contains(string(raw), sentinel) || !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatal(string(raw))
	}
	// A failing call wraps stderr into the error: scrubbed as well.
	script = "#!/bin/sh\necho \"E_BAD: token $XAI_API_KEY rejected\" >&2\nexit 1\n"
	if err = os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	err = cli.Create(context.Background(), env, HostCodex, "x", SecretEnv{"XAI_API_KEY": sentinel})
	if err == nil || strings.Contains(err.Error(), sentinel) || strings.Contains(err.(*Error).Message+err.(*Error).Fix, sentinel) {
		t.Fatal(err)
	}
	if err = cli.Create(context.Background(), env, "auto", "x", nil); err == nil || err.(*Error).Code != CodeRejected {
		t.Fatal("auto host must be refused:", err)
	}
}
func TestFakeRecordsEnvNamesAndHashesOnly(t *testing.T) {
	f := NewFake()
	env := NewOrgEnv("/data", orgA)
	const sentinel = "orqen-SENTINEL-01FAKEVALUE"
	if _, _, err := f.Doctor(context.Background(), env, HostCodex, SecretEnv{"TYPESAFE_API_KEY": sentinel}); err != nil {
		t.Fatal(err)
	}
	c := f.CallsOf("doctor")[0]
	if strings.Contains(c.Env, sentinel) || c.Env != "TYPESAFE_API_KEY="+SecretHash(sentinel) || c.Host != HostCodex {
		t.Fatal(c)
	}
	if n := c.EnvNames(); len(n) != 1 || n[0] != "TYPESAFE_API_KEY" {
		t.Fatal(n)
	}
}
