package verify

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"orqen/internal/gitdiff"
)

func checkout(t *testing.T) (dir, tmp string) {
	base := t.TempDir()
	dir = filepath.Join(base, "checkout")
	tmp = filepath.Join(base, "tmp")
	_ = os.MkdirAll(dir, 0o755)
	return
}

func spec(dir, tmp string, cmds ...Command) Spec {
	return Spec{Dir: dir, Tmp: tmp, Commands: cmds, BaseEnv: []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}, Sandbox: ModeEnforce, MaxOutput: 1 << 20}
}

func sh(key, script string) Command {
	return Command{Key: key, Argv: []string{"/bin/sh", "-c", script}, Timeout: 10 * time.Second}
}

func TestPassFailAndExitCodes(t *testing.T) {
	dir, tmp := checkout(t)
	res, err := Run(context.Background(), spec(dir, tmp, sh("ok", "echo hi"), sh("bad", "echo oops >&2; exit 3")))
	if err != nil || len(res) != 2 {
		t.Fatal(res, err)
	}
	if res[0].Outcome != OutcomePass || res[0].ExitCode != 0 || strings.TrimSpace(string(res[0].Stdout)) != "hi" || len(res[0].SHA256) != 64 {
		t.Fatalf("%+v", res[0])
	}
	if res[1].Outcome != OutcomeFail || res[1].ExitCode != 3 || !strings.Contains(string(res[1].Stderr), "oops") {
		t.Fatalf("%+v", res[1])
	}
}

func TestTimeoutKillsTheProcessGroup(t *testing.T) {
	dir, tmp := checkout(t)
	pid := filepath.Join(tmp, "child.pid")
	_ = os.MkdirAll(tmp, 0o700)
	c := sh("slow", "sleep 300 & echo $! > "+pid+"; wait")
	c.Timeout = 700 * time.Millisecond
	s := spec(dir, tmp, c)
	s.NoSandbox, s.Sandbox = true, ModeBestEffort // the pid file lives outside the checkout
	res, _ := Run(context.Background(), s)
	if !res[0].TimedOut || res[0].Outcome != OutcomeIncomplete || res[0].Reason != "timeout" {
		t.Fatalf("%+v", res[0])
	}
	b, err := os.ReadFile(pid)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	for i := 0; i < 50; i++ {
		if exec.Command("kill", "-0", strconv.Itoa(n)).Run() != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the child survived the timeout")
}

func TestOutputIsCappedAndScrubbed(t *testing.T) {
	dir, tmp := checkout(t)
	s := spec(dir, tmp, sh("noisy", "head -c 100000 /dev/zero | tr '\\0' x; echo SENTINEL-SECRET"))
	s.MaxOutput = 1000
	s.Scrub = func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("SENTINEL-SECRET"), []byte("[secret]")) }
	res, _ := Run(context.Background(), s)
	if len(res[0].Stdout) > 1000 || res[0].Outcome != OutcomePass {
		t.Fatalf("%d %+v", len(res[0].Stdout), res[0].Outcome)
	}
	s2 := spec(dir, tmp, sh("secret", "echo SENTINEL-SECRET"))
	s2.Scrub = s.Scrub
	res, _ = Run(context.Background(), s2)
	if strings.Contains(string(res[0].Stdout), "SENTINEL") || !strings.Contains(string(res[0].Stdout), "[secret]") {
		t.Fatalf("%q", res[0].Stdout)
	}
}

func TestSandboxDeniesWritesOutsideAndNetworkUnlessNeeded(t *testing.T) {
	if !SandboxAvailable() {
		t.Skip("sandbox-exec is not available here")
	}
	dir, tmp := checkout(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	res, _ := Run(context.Background(), spec(dir, tmp, sh("write-out", "echo x > "+outside), sh("write-in", "echo x > inside.txt"), sh("write-tmp", "echo x > $TMPDIR/t.txt")))
	if res[0].Outcome != OutcomeFail || fileExists(outside) {
		t.Fatalf("a write outside the checkout must be denied: %+v", res[0])
	}
	if res[1].Outcome != OutcomePass || !fileExists(filepath.Join(dir, "inside.txt")) || res[2].Outcome != OutcomePass || res[1].Sandbox != "enforced" {
		t.Fatalf("%+v %+v", res[1], res[2])
	}
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc is not available")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			c.Close()
		}
	}()
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	deny := sh("net-denied", "nc -z -w 2 127.0.0.1 "+port)
	allow := sh("net-allowed", "nc -z -w 2 127.0.0.1 "+port)
	allow.NeedsNetwork = true
	res, _ = Run(context.Background(), spec(dir, tmp, deny, allow))
	if res[0].Outcome != OutcomeFail || res[1].Outcome != OutcomePass {
		t.Fatalf("denied=%+v allowed=%+v", res[0], res[1])
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestSandboxUnavailableIsIncompleteWhenEnforced(t *testing.T) {
	dir, tmp := checkout(t)
	s := spec(dir, tmp, sh("a", "true"))
	s.NoSandbox = true
	res, _ := Run(context.Background(), s)
	if res[0].Outcome != OutcomeIncomplete || res[0].Reason != "sandbox_unavailable" {
		t.Fatalf("%+v", res[0])
	}
	s.Sandbox = ModeBestEffort
	res, _ = Run(context.Background(), s)
	if res[0].Outcome != OutcomePass || res[0].Sandbox != "best_effort" {
		t.Fatalf("%+v", res[0])
	}
}

func TestDockerAndCwdAndSpawnErrors(t *testing.T) {
	dir, tmp := checkout(t)
	_ = os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	docker := sh("docker", "true")
	docker.NeedsDocker = true
	escape := sh("escape", "pwd")
	escape.Cwd = ".."
	inpkg := sh("pkg", "pwd")
	inpkg.Cwd = "pkg"
	missing := Command{Key: "missing", Argv: []string{"/nonexistent/binary"}, Timeout: time.Second}
	s := spec(dir, tmp, docker, escape, inpkg, missing)
	s.NoSandbox, s.Sandbox = true, ModeBestEffort
	res, _ := Run(context.Background(), s)
	want := []struct{ outcome, reason string }{{OutcomeIncomplete, "docker_unavailable"}, {OutcomeIncomplete, "bad_cwd"}, {OutcomePass, ""}, {OutcomeIncomplete, "spawn_error"}}
	for i, w := range want {
		if res[i].Outcome != w.outcome || res[i].Reason != w.reason {
			t.Errorf("%s: %+v", res[i].Key, res[i])
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(string(res[2].Stdout)), "pkg") {
		t.Fatalf("%q", res[2].Stdout)
	}
	s.DockerOK = func() bool { return true }
	s.Commands = []Command{docker}
	if res, _ = Run(context.Background(), s); res[0].Outcome != OutcomePass {
		t.Fatalf("%+v", res[0])
	}
}

func TestClassifyTable(t *testing.T) {
	r := func(key, outcome string) Result { return Result{Key: key, Outcome: outcome} }
	cases := []struct {
		name       string
		head, base []Result
		allow      bool
		status     string
		class      map[string]string
	}{
		{"all pass", []Result{r("a", OutcomePass)}, []Result{r("a", OutcomePass)}, false, GatePass, map[string]string{"a": ClassPass}},
		{"new failure", []Result{r("a", OutcomeFail)}, []Result{r("a", OutcomePass)}, false, GateFail, map[string]string{"a": ClassNewFailure}},
		{"baseline failure fails", []Result{r("a", OutcomeFail)}, []Result{r("a", OutcomeFail)}, false, GateFail, map[string]string{"a": ClassBaselineFailure}},
		{"baseline failure allowed", []Result{r("a", OutcomeFail)}, []Result{r("a", OutcomeFail)}, true, GatePass, map[string]string{"a": ClassBaselineFailure}},
		{"timeout is incomplete", []Result{r("a", OutcomeIncomplete)}, []Result{r("a", OutcomePass)}, false, GateIncomplete, map[string]string{"a": ClassIncomplete}},
		{"fail beats incomplete", []Result{r("a", OutcomeFail), r("b", OutcomeIncomplete)}, []Result{r("a", OutcomePass), r("b", OutcomePass)}, false, GateFail, map[string]string{"a": ClassNewFailure, "b": ClassIncomplete}},
		{"no baseline for a failure is incomplete", []Result{r("a", OutcomeFail)}, nil, false, GateIncomplete, map[string]string{"a": ClassIncomplete}},
		{"incomplete baseline for a failure", []Result{r("a", OutcomeFail)}, []Result{r("a", OutcomeIncomplete)}, true, GateIncomplete, map[string]string{"a": ClassIncomplete}},
		{"no commands", nil, nil, false, GateIncomplete, map[string]string{}},
		{"pass needs no baseline", []Result{r("a", OutcomePass)}, nil, false, GatePass, map[string]string{"a": ClassPass}},
	}
	for _, c := range cases {
		g := Classify(c.head, c.base, c.allow)
		if g.Status != c.status {
			t.Errorf("%s: status %s, want %s (%v)", c.name, g.Status, c.status, g.Reasons)
		}
		for k, v := range c.class {
			if g.Classes[k] != v {
				t.Errorf("%s: %s is %s, want %s", c.name, k, g.Classes[k], v)
			}
		}
	}
}

func diffOf(t *testing.T, text string) gitdiff.Diff {
	d, err := gitdiff.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestWeakeningSignals(t *testing.T) {
	cases := []struct {
		name string
		diff string
		cmds []Command
		want []string
	}{
		{"deleted test", "diff --git a/x_test.go b/x_test.go\ndeleted file mode 100644\n--- a/x_test.go\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-func TestX(t *testing.T) {}\n-\n", nil, []string{"test_deleted"}},
		{"fewer cases", "diff --git a/src/a.test.ts b/src/a.test.ts\n--- a/src/a.test.ts\n+++ b/src/a.test.ts\n@@ -1,3 +1,1 @@\n-it('a', () => {})\n-it('b', () => {})\n+it('a', () => {})\n", nil, []string{"test_count_decreased"}},
		{"skip marker", "diff --git a/pkg/a_test.go b/pkg/a_test.go\n--- a/pkg/a_test.go\n+++ b/pkg/a_test.go\n@@ -1,1 +1,2 @@\n func TestA(t *testing.T) {\n+\tt.Skip(\"later\")\n", nil, []string{"skip_marker_added"}},
		{"only marker", "diff --git a/a.spec.js b/a.spec.js\n--- a/a.spec.js\n+++ b/a.spec.js\n@@ -1,1 +1,1 @@\n-it('a', f)\n+it.only('a', f)\n", nil, []string{"skip_marker_added"}},
		{"makefile", "diff --git a/Makefile b/Makefile\n--- a/Makefile\n+++ b/Makefile\n@@ -1,1 +1,1 @@\n-test:\n+test: true\n", nil, []string{"test_config_changed"}},
		{"ci config", "diff --git a/.gitlab-ci.yml b/.gitlab-ci.yml\n--- a/.gitlab-ci.yml\n+++ b/.gitlab-ci.yml\n@@ -1,1 +1,1 @@\n-a\n+b\n", nil, []string{"test_config_changed"}},
		{"package scripts", "diff --git a/package.json b/package.json\n--- a/package.json\n+++ b/package.json\n@@ -3,3 +3,3 @@\n   \"scripts\": {\n-    \"test\": \"vitest run\",\n+    \"test\": \"true\",\n", nil, []string{"test_config_changed"}},
		{"package dependencies are fine", "diff --git a/package.json b/package.json\n--- a/package.json\n+++ b/package.json\n@@ -3,3 +3,3 @@\n   \"dependencies\": {\n-    \"left-pad\": \"1.0.0\",\n+    \"left-pad\": \"1.0.1\",\n", nil, nil},
		{"command target", "diff --git a/scripts/check.sh b/scripts/check.sh\n--- a/scripts/check.sh\n+++ b/scripts/check.sh\n@@ -1,1 +1,1 @@\n-exit 1\n+exit 0\n", []Command{{Argv: []string{"/bin/sh", "scripts/check.sh"}}}, []string{"command_target_changed"}},
		{"feature file is fine", "diff --git a/src/feature.txt b/src/feature.txt\nnew file mode 100644\n--- /dev/null\n+++ b/src/feature.txt\n@@ -0,0 +1 @@\n+orqen-feature\n", []Command{{Argv: []string{"/bin/sh", "scripts/check.sh"}}}, nil},
		{"added tests are fine", "diff --git a/b_test.go b/b_test.go\nnew file mode 100644\n--- /dev/null\n+++ b/b_test.go\n@@ -0,0 +1 @@\n+func TestB(t *testing.T) {}\n", nil, nil},
	}
	for _, c := range cases {
		got := Weakening(diffOf(t, c.diff), nil, c.cmds)
		var codes []string
		for _, s := range got {
			codes = append(codes, s.Code)
		}
		if strings.Join(codes, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: got %v, want %v", c.name, codes, c.want)
		}
	}
}
