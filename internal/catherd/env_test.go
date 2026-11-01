package catherd

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const orgA = "org_01ARZ3NDEKTSV4RRFFQ69G5FAV"
const orgB = "org_01BX5ZZKBKACTAV9WEVGEMMVRY"

func rtWith(env map[string]string) *Runtime {
	return NewRuntime(context.Background(), RuntimeConfig{Path: "/child/bin:/usr/bin", Bin: "/child/bin/catherd", UserHome: "/home/u", DataDir: "/data", Getenv: func(k string) string { return env[k] }})
}
func TestOrgEnvIsExact(t *testing.T) {
	rt := rtWith(map[string]string{
		"CATHERD_ROLE": "worker", "CODEX_THREAD_ID": "t", "CLAUDE_CODE_SSE_PORT": "1", "CATHERD_CONFIG_DIR": "/personal/config", "CATHERD_DATA_DIR": "/personal/data",
		"CATHERD_ORCHESTRATION_HOST": "claude-code", "TMPDIR": "/role/scratch", "LANG": "en_US.UTF-8", "HTTPS_PROXY": "http://p:3128", "ANTHROPIC_API_KEY": "sk-secret",
	})
	e := NewOrgEnv("/data", orgA)
	want := []string{"PATH=/child/bin:/usr/bin", "HOME=/home/u", "TMPDIR=/data/catherd/" + orgA + "/tmp", "LANG=en_US.UTF-8", "HTTPS_PROXY=http://p:3128", "NO_COLOR=1", "CATHERD_LOG=warn",
		"CATHERD_HOME=/data/catherd/" + orgA + "/home", "CATHERD_CLAUDE_AGENTS_DIR=/data/catherd/" + orgA + "/claude-agents", "CATHERD_NO_SYNC=1"}
	if got := e.Vars(rt); !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	sync := e.SyncVars(rt)
	if strings.Contains(strings.Join(sync, " "), "CATHERD_NO_SYNC") {
		t.Fatal("sync must not set CATHERD_NO_SYNC")
	}
	for _, v := range e.Vars(rt) {
		for _, bad := range []string{"CATHERD_ROLE", "CODEX_", "CLAUDE_CODE_", "CATHERD_CONFIG_DIR", "CATHERD_DATA_DIR", "ORCHESTRATION_HOST", "sk-secret", "/role/scratch"} {
			if strings.Contains(v, bad) {
				t.Fatal("leaked", v)
			}
		}
	}
	if e.WorkDir() != "/data/catherd/"+orgA+"/work" {
		t.Fatal(e.WorkDir())
	}
}
func TestOrgNamespacesAreSeparateAndValidated(t *testing.T) {
	a, b := NewOrgEnv("/data", orgA), NewOrgEnv("/data", orgB)
	if a.Home() == b.Home() || !strings.HasPrefix(a.Home(), "/data/catherd/org_") {
		t.Fatal(a.Home(), b.Home())
	}
	for _, bad := range []string{"", "../x", "org_short", "org_" + strings.Repeat("I", 26), orgA + "/../x"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%q accepted", bad)
				}
			}()
			NewOrgEnv("/data", bad)
		}()
	}
	dir := t.TempDir()
	e := NewOrgEnv(dir, orgA)
	if err := e.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{e.Home(), e.Agents(), e.WorkDir(), e.Tmp()} {
		if st, err := os.Stat(d); err != nil || st.Mode().Perm() != 0o700 {
			t.Fatal(d, err)
		}
	}
	if rel, _ := filepath.Rel(filepath.Join(dir, "catherd"), e.Home()); strings.HasPrefix(rel, "..") {
		t.Fatal(rel)
	}
}
func TestPersonalEnvPassesOnlyItsAllowlist(t *testing.T) {
	rt := rtWith(map[string]string{"CATHERD_HOME": "/p/home", "XDG_CONFIG_HOME": "/p/xdg", "CATHERD_ROLE": "x", "CATHERD_CLAUDE_AGENTS_DIR": "/p/agents"})
	got := NewPersonalEnv("/data").Vars(rt)
	has := strings.Join(got, "\n")
	if !strings.Contains(has, "CATHERD_HOME=/p/home") || !strings.Contains(has, "XDG_CONFIG_HOME=/p/xdg") || strings.Contains(has, "CATHERD_ROLE") || strings.Contains(has, "AGENTS_DIR") || !strings.Contains(has, "TMPDIR=/data/catherd/_personal/tmp") {
		t.Fatal(got)
	}
	rt.PersonalHome = "/tmp/fake-personal"
	got = NewPersonalEnv("/data").Vars(rt)
	if !strings.Contains(strings.Join(got, "\n"), "CATHERD_HOME=/tmp/fake-personal") || strings.Contains(strings.Join(got, "\n"), "/p/home") {
		t.Fatal(got)
	}
}
func TestChildPathMerge(t *testing.T) {
	rt := NewRuntime(context.Background(), RuntimeConfig{UserHome: "/h", Getenv: func(k string) string {
		if k == "PATH" {
			return "/usr/bin:/custom"
		}
		return ""
	}, LoginShellPath: func(context.Context) string { return "/login/bin:/usr/bin" }})
	parts := strings.Split(rt.ChildPath, ":")
	if parts[0] != "/login/bin" || parts[1] != "/usr/bin" || parts[2] != "/custom" || !contains(parts, "/h/.bun/bin") || rt.PathSource != "login_shell+process+known" {
		t.Fatal(rt.ChildPath, rt.PathSource)
	}
	seen := map[string]bool{}
	for _, p := range parts {
		if seen[p] {
			t.Fatal("duplicate", p)
		}
		seen[p] = true
	}
	if rt.Available() || rt.Supported() {
		t.Fatal("no version yet")
	}
	for v, want := range map[string]bool{"1.5.0": true, "1.5.9": true, "1.6.0": false, "1.4.9": false, "2.0.0": false} {
		rt.SetVersion(v)
		if rt.Supported() != want {
			t.Errorf("%s: %v", v, rt.Supported())
		}
	}
	explicit := NewRuntime(context.Background(), RuntimeConfig{Path: "/only", Getenv: func(string) string { return "" }})
	if explicit.ChildPath != "/only" || explicit.PathSource != "explicit" || explicit.Bin != "" {
		t.Fatal(explicit)
	}
}
func contains(l []string, v string) bool {
	i := sort.SearchStrings(append([]string{}, l...), v)
	for _, x := range l {
		if x == v {
			return true
		}
	}
	_ = i
	return false
}
