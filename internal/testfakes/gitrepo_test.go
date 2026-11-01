package testfakes_test

import (
	"net/http/httptest"
	"orqen/internal/testfakes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitClone(t *testing.T, url, askUser, askPass string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(t.TempDir(), "askpass.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncase \"$1\" in *sername*) printf %s \"$FAKE_USER\";; *) printf %s \"$FAKE_PASS\";; esac\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-c", "credential.helper=", "-c", "protocol.allow=never", "-c", "protocol.http.allow=always", "clone", "-q", url, filepath.Join(dir, "out"))
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=" + script, "FAKE_USER=" + askUser, "FAKE_PASS=" + askPass, "LANG=C"}
	out, err := cmd.CombinedOutput()
	return filepath.Join(dir, "out") + "|" + string(out), err
}
func TestGitReposCloneWithRightTokenOnly(t *testing.T) {
	if !testfakes.GitAvailable() {
		t.Skip("git-http-backend is not available")
	}
	repos, err := testfakes.NewGitRepos(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(repos.Handler())
	defer srv.Close()
	url := srv.URL + "/gitlab/group-1/proj-1.git"
	res, err := gitClone(t, url, "oauth2", repos.Token)
	if err != nil {
		t.Fatal(res, err)
	}
	dir, _, _ := strings.Cut(res, "|")
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err != nil || !strings.Contains(string(b), "vitest") {
		t.Fatal(err)
	}
	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(head)) != repos.SHA {
		t.Fatal(string(head), repos.SHA)
	}
	if res, err = gitClone(t, url, "oauth2", "wrong-token"); err == nil {
		t.Fatal("a wrong token cloned", res)
	}
	if res, err = gitClone(t, url, "someone", repos.Token); err == nil {
		t.Fatal("a wrong user cloned", res)
	}
}
