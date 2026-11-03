package claudehost

import (
	"context"
	"os"
	"testing"
	"time"

	"orqen/internal/codexhost"
)

// realConfig builds a Client for the real claude and catherd. It is skipped unless ORQEN_REAL_CLAUDE=1; then
// ORQEN_REAL_CLAUDE_HOME (the isolated HOME), ORQEN_REAL_CLAUDE_CONFIG_DIR and ORQEN_REAL_CATHERD_HOME name the host
// tree of an organization that already ran `claude auth login`.
func realClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("ORQEN_REAL_CLAUDE") != "1" {
		t.Skip("set ORQEN_REAL_CLAUDE=1 to run against the real claude and catherd")
	}
	need := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			t.Fatalf("%s is required", k)
		}
		return v
	}
	home, cfg, catHome := need("ORQEN_REAL_CLAUDE_HOME"), need("ORQEN_REAL_CLAUDE_CONFIG_DIR"), need("ORQEN_REAL_CATHERD_HOME")
	catherd := need("ORQEN_REAL_CATHERD_BIN")
	env := []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + cfg, "CATHERD_HOME=" + catHome, "CATHERD_ORCHESTRATION_HOST=claude-code", "PATH=" + os.Getenv("PATH"), "LANG=en_US.UTF-8", "USER=" + os.Getenv("USER"), "LOGNAME=" + os.Getenv("USER"), "TMPDIR=" + t.TempDir()}
	return New(Config{Bin: need("ORQEN_REAL_CLAUDE_BIN"), Env: env, Cwd: t.TempDir(), ConfigDir: cfg, CatherdBin: catherd,
		CatherdEnv: map[string]string{"CATHERD_HOME": catHome, "CATHERD_ORCHESTRATION_HOST": "claude-code", "CATHERD_NO_SYNC": "1", "CATHERD_LOG": "warn"}})
}

func TestRealProbe(t *testing.T) {
	c := realClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := c.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("version=%s loggedIn=%v method=%s catherd tools=%d", res.Version(), res.LoggedIn, res.AccountType, len(res.MCPServers[0].Tools))
	if res.Version() == "" || !res.LoggedIn || len(res.MCPServers[0].Tools) == 0 {
		t.Fatalf("%+v", res)
	}
}

// TestRealDirective sends one real directive (a read-only catherd status call) and checks the one call comes back.
// It uses the model, so it costs a few tokens.
func TestRealDirective(t *testing.T) {
	c := realClient(t)
	th, err := c.StartThread(context.Background(), codexhost.ThreadParams{Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	call, err := c.RunDirective(context.Background(), th.ID, codexhost.NewDirective("status", map[string]any{}), 3*time.Minute)
	if err != nil {
		t.Fatalf("%v (call=%+v)", err, call)
	}
	t.Logf("tool=%s result=%s text=%.120s", call.Tool, call.Result, call.Text)
	if call.Tool != "status" {
		t.Fatalf("%+v", call)
	}
}
