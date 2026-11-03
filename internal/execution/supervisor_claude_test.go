package execution

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"orqen/internal/catherd"
	"orqen/internal/claudehost"
	"orqen/internal/codexhost"
)

type noKill struct{}

func (noKill) Kill()       {}
func (noKill) Wait() error { return nil }

func TestSupervisorClaudeHost(t *testing.T) {
	cfg, _ := fixture(t)
	cfg.Host, cfg.ClaudeBin = catherd.HostClaudeCode, "claude"
	var started atomic.Int64
	var gotEnv []string
	var gotArgs []string
	s := &Supervisor{StartClaude: func(_ context.Context, _ string, args, env []string, _ string) (io.ReadCloser, claudehost.Proc, error) {
		started.Add(1)
		gotArgs, gotEnv = args, env
		lines := []string{
			`{"type":"system","subtype":"init","session_id":"s"}`,
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"a","name":"mcp__catherd__status","input":{}}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a","content":"{\"ok\":true}"}]}}`,
			`{"type":"result","is_error":false}`,
		}
		return io.NopCloser(strings.NewReader(strings.Join(lines, "\n") + "\n")), noKill{}, nil
	}}
	defer s.StopAll()
	c, err := s.Client(context.Background(), orgID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c2, _ := s.Client(context.Background(), orgID, cfg); c2 != c {
		t.Fatal("the same client should be reused")
	}
	th, _ := c.StartThread(context.Background(), codexhost.ThreadParams{})
	call, err := c.RunDirective(context.Background(), th.ID, codexhost.NewDirective("status", nil), 2*time.Second)
	if err != nil || call.Tool != "status" || started.Load() != 1 {
		t.Fatal(call, err)
	}
	env := "\n" + strings.Join(gotEnv, "\n")
	for _, want := range []string{"CLAUDE_CONFIG_DIR=" + ClaudeConfigDir(cfg.DataDir, orgID), "CATHERD_ORCHESTRATION_HOST=claude-code", "\nUSER=", "\nLOGNAME="} {
		if !strings.Contains(env, want) {
			t.Fatalf("missing %s in the claude environment", want)
		}
	}
	if strings.Contains(env, "CODEX_HOME") || !strings.Contains(strings.Join(gotArgs, " "), `"CATHERD_ORCHESTRATION_HOST":"claude-code"`) {
		t.Fatal("the claude host must not see CODEX_HOME and must start catherd for claude-code:", env)
	}
	// Claude Code needs its binary: without one the host is unavailable, never a silent codex fallback.
	s.Stop(orgID)
	cfg.ClaudeBin = ""
	if _, err = s.Client(context.Background(), orgID, cfg); err == nil {
		t.Fatal("a missing claude binary must fail")
	}
}
