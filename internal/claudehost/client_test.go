package claudehost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"orqen/internal/codexhost"
)

type fakeProc struct {
	mu     sync.Mutex
	killed bool
}

func (p *fakeProc) Kill()       { p.mu.Lock(); p.killed = true; p.mu.Unlock() }
func (p *fakeProc) Wait() error { return nil }
func (p *fakeProc) wasKilled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed
}

type script struct {
	lines []string
	proc  *fakeProc
	args  [][]string
}

func (s *script) start(_ context.Context, _ string, args, _ []string, _ string) (io.ReadCloser, Proc, error) {
	s.args = append(s.args, args)
	s.proc = &fakeProc{}
	return io.NopCloser(strings.NewReader(strings.Join(s.lines, "\n") + "\n")), s.proc, nil
}

func j(v any) string { b, _ := json.Marshal(v); return string(b) }

func initEv() string {
	return j(map[string]any{"type": "system", "subtype": "init", "session_id": "s1"})
}

func toolUse(id, name string, in map[string]any) string {
	return j(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": in}}}})
}

func toolResult(id, text string, isErr bool) string {
	return j(map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": isErr, "content": []any{map[string]any{"type": "text", "text": text}}}}}})
}

func resultEv(isErr bool, text string) string {
	return j(map[string]any{"type": "result", "subtype": "success", "is_error": isErr, "result": text, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "cache_read_input_tokens": 100}})
}

func newClient(s *script) *Client {
	return New(Config{Bin: "claude", CatherdBin: "catherd", Start: s.start})
}

func run(t *testing.T, c *Client, d codexhost.Directive) (codexhost.ToolCall, error) {
	t.Helper()
	th, _ := c.StartThread(context.Background(), codexhost.ThreadParams{})
	return c.RunDirective(context.Background(), th.ID, d, 5*time.Second)
}

func TestRunDirectiveReturnsTheOneCall(t *testing.T) {
	d := codexhost.NewDirective("dispatch", map[string]any{"run": "r1", "name": "w"})
	s := &script{lines: []string{initEv(), toolUse("tu1", ToolPrefix+"dispatch", map[string]any{"run": "r1", "name": "w"}), toolResult("tu1", `{"dispatch_id":"d1"}`, false), resultEv(false, "DONE")}}
	c := newClient(s)
	th, _ := c.StartThread(context.Background(), codexhost.ThreadParams{Effort: "high", Model: "opus"})
	call, err := c.RunDirective(context.Background(), th.ID, d, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if call.Tool != "dispatch" || !strings.Contains(string(call.Result), "d1") {
		t.Fatalf("%+v", call)
	}
	if u, ok := c.Usage(th.ID); !ok || u.Total != 115 {
		t.Fatalf("usage %+v", u)
	}
	a := strings.Join(s.args[0], " ")
	for _, want := range []string{"--session-id " + th.ID, "--restricted", "--allowedTools " + ToolPrefix + "dispatch", "--permission-mode dontAsk", "--model opus", "--effort high", "--strict-mcp-config"} {
		if !strings.Contains(a, want) {
			t.Fatalf("missing %q in %s", want, a)
		}
	}
	// A second directive on the same thread resumes the session.
	s.lines = []string{initEv(), toolUse("tu2", ToolPrefix+"dispatch", map[string]any{"run": "r1", "name": "w"}), toolResult("tu2", `{}`, false), resultEv(false, "DONE")}
	if _, err = c.RunDirective(context.Background(), th.ID, d, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if b := strings.Join(s.args[1], " "); !strings.Contains(b, "--resume "+th.ID) || strings.Contains(b, "--session-id") {
		t.Fatal(b)
	}
}

func TestRunDirectiveDeviations(t *testing.T) {
	d := codexhost.NewDirective("dispatch", map[string]any{"run": "r1"})
	cases := []struct {
		name  string
		lines []string
		want  error
		kill  bool
	}{
		{"another tool", []string{initEv(), toolUse("a", "Bash", map[string]any{"command": "ls"})}, codexhost.ErrDeviation, true},
		{"another catherd tool", []string{initEv(), toolUse("a", ToolPrefix+"land", map[string]any{})}, codexhost.ErrDeviation, true},
		{"a second call", []string{initEv(), toolUse("a", ToolPrefix+"dispatch", map[string]any{"run": "r1"}), toolUse("b", ToolPrefix+"dispatch", map[string]any{"run": "r1"})}, codexhost.ErrDeviation, true},
		{"other arguments", []string{initEv(), toolUse("a", ToolPrefix+"dispatch", map[string]any{"run": "other"})}, codexhost.ErrArgsMismatch, true},
		{"no call", []string{initEv(), resultEv(false, "DONE")}, codexhost.ErrNoToolCall, false},
		{"turn failed", []string{initEv(), resultEv(true, "overloaded")}, codexhost.ErrNoToolCall, false},
		{"stream ends mid-call", []string{initEv(), toolUse("a", ToolPrefix+"dispatch", map[string]any{"run": "r1"})}, codexhost.ErrOutcomeUnknown, false},
		{"stream ends with nothing", nil, codexhost.ErrOutcomeUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &script{lines: tc.lines}
			_, err := run(t, newClient(s), d)
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			if tc.kill && !s.proc.wasKilled() {
				t.Fatal("the process was not killed")
			}
		})
	}
}

func TestRunDirectiveToolErrors(t *testing.T) {
	d := codexhost.NewDirective("run_start", map[string]any{"repo": "x"})
	use := toolUse("a", ToolPrefix+"run_start", map[string]any{"repo": "x"})
	for name, lines := range map[string][]string{
		"is_error flag":   {initEv(), use, toolResult("a", `{"code":"E_PAUSED","message":"paused","fix":"resume"}`, true), resultEv(false, "")},
		"error in result": {initEv(), use, toolResult("a", `{"error":{"code":"E_PAUSED","message":"paused","fix":"resume"}}`, false), resultEv(false, "")},
	} {
		_, err := run(t, newClient(&script{lines: lines}), d)
		var te *codexhost.ToolError
		if !errors.As(err, &te) || te.Code != "E_PAUSED" || te.Fix != "resume" {
			t.Fatalf("%s: %v", name, err)
		}
	}
	_, err := run(t, newClient(&script{lines: []string{initEv(), use, toolResult("a", "boom", true), resultEv(false, "")}}), d)
	var te *codexhost.ToolError
	if !errors.As(err, &te) || te.Code != "E_HOST_TOOL" {
		t.Fatal(err)
	}
}

func TestStartFailureIsNotAnUnknownOutcome(t *testing.T) {
	c := New(Config{Bin: "claude", Start: func(context.Context, string, []string, []string, string) (io.ReadCloser, Proc, error) {
		return nil, nil, errors.New("exec failed")
	}})
	_, err := run(t, c, codexhost.NewDirective("dispatch", nil))
	if err == nil || errors.Is(err, codexhost.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
}

func TestHistoryReconcilesLikeCodex(t *testing.T) {
	dir := t.TempDir()
	c := New(Config{ConfigDir: dir})
	d := codexhost.NewDirectiveWithID("dir_1", "dispatch", map[string]any{"run": "r1"})
	lines := []string{
		j(map[string]any{"type": "user", "uuid": "u1", "message": map[string]any{"content": d.Text}}),
		toolUse("tu1", ToolPrefix+"dispatch", map[string]any{"run": "r1"}),
		toolResult("tu1", `{"dispatch_id":"d1"}`, false),
		j(map[string]any{"type": "assistant", "message": map[string]any{"stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": "DONE dir_1"}}}}),
	}
	proj := filepath.Join(dir, "projects", "-tmp-x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "sess-1.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	th, err := c.ResumeThread(context.Background(), "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	turn, ok := codexhost.TurnForDirective(th.Turns, "dir_1")
	if !ok || turn.Status != "completed" || len(codexhost.ToolCallsOf(turn)) != 1 {
		t.Fatalf("%+v", th.Turns)
	}
	// A call without a result yet is a started, not completed call: unknown.
	if err = os.WriteFile(filepath.Join(proj, "sess-2.jsonl"), []byte(strings.Join(lines[:2], "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	th, _ = c.ResumeThread(context.Background(), "sess-2")
	turn, _ = codexhost.TurnForDirective(th.Turns, "dir_1")
	if turn.Status != "inProgress" || len(codexhost.ToolCallsOf(turn)) != 0 || len(turn.Items) != 2 {
		t.Fatalf("%+v", turn)
	}
	// A session that does not exist is gone (an *RPCError), so the engine starts another one.
	_, err = c.ResumeThread(context.Background(), "nope")
	var rpc *codexhost.RPCError
	if !errors.As(err, &rpc) {
		t.Fatal(err)
	}
	if _, err = c.ResumeThread(context.Background(), "../escape"); !errors.As(err, &rpc) {
		t.Fatal(err)
	}
}

func TestProbe(t *testing.T) {
	c := New(Config{Bin: "claude", CatherdBin: "catherd", CatherdEnv: map[string]string{"A": "1"},
		Output: func(_ context.Context, _ string, args, _ []string) ([]byte, error) {
			if args[0] == "--version" {
				return []byte("2.1.283 (Claude Code)\n"), nil
			}
			return []byte(`{"loggedIn":true,"authMethod":"claude.ai"}`), nil
		},
		ListTools: func(context.Context, string, []string) ([]string, error) {
			return []string{"run_start", "dispatch"}, nil
		}})
	res, err := c.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Version() != "2.1.283" || !res.LoggedIn || res.AccountType != "claude.ai" || len(res.MCPServers) != 1 || res.MCPServers[0].Tools[0] != "dispatch" {
		t.Fatalf("%+v", res)
	}
}
