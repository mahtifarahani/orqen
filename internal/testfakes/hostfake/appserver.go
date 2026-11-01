package hostfake

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AppServer is a fake `codex app-server` speaking the protocol subset Orqen uses. It executes directives against
// a Catherd and never starts a model.
type AppServer struct {
	Cat *Catherd
	// UserAgent is reported by initialize (the version after the slash is what Orqen reads).
	UserAgent string
	// NotLoggedIn makes account/read report no account.
	NotLoggedIn bool
	// HideTools removes tools from the catherd MCP server listing; NoCatherd removes the server.
	HideTools []string
	NoCatherd bool

	Spawns, Resumes, TurnStarts, Interrupts atomic.Int64
	turnSeq                                 atomic.Int64

	mu     sync.Mutex
	active map[string]chan struct{}
}

// NewAppServer makes a fake app-server over a catherd fake.
func NewAppServer(cat *Catherd) *AppServer {
	return &AppServer{Cat: cat, UserAgent: "orqen_fake_codex/0.160.0 (test)", active: map[string]chan struct{}{}}
}

type duplex struct {
	io.Reader
	io.WriteCloser
}

func (d duplex) Close() error { return d.WriteCloser.Close() }

// Pipe starts a connection and returns the client end. Closing it ends the connection; calling Pipe again simulates
// a restarted app-server over the same state.
func (s *AppServer) Pipe() io.ReadWriteCloser {
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	go func() {
		s.Serve(sr, sw)
		_ = sw.Close()
	}()
	return duplex{cr, cw}
}

var (
	reDirID   = regexp.MustCompile(`id=(dir_[0-9a-zA-Z]+)`)
	reDirTool = regexp.MustCompile(`catherd\.([a-z_]+)`)
)

type conn struct {
	s   *AppServer
	w   io.WriteCloser
	wmu sync.Mutex
	// turns started on this connection, interrupted when it ends
	mine   map[string]chan struct{}
	closed chan struct{}
	once   sync.Once
}

func (c *conn) send(v any) {
	b, _ := json.Marshal(v)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, _ = c.w.Write(append(b, '\n'))
}

func (c *conn) notify(method string, params any) {
	c.send(map[string]any{"method": method, "params": params})
}

func (c *conn) kill() { c.once.Do(func() { close(c.closed); _ = c.w.Close() }) }

// Serve handles one connection until the reader ends.
func (s *AppServer) Serve(r io.Reader, w io.WriteCloser) {
	s.Spawns.Add(1)
	c := &conn{s: s, w: w, mine: map[string]chan struct{}{}, closed: make(chan struct{})}
	defer func() {
		s.mu.Lock()
		for id, ch := range c.mine {
			select {
			case <-ch:
			default:
				close(ch)
			}
			delete(s.active, id)
		}
		s.mu.Unlock()
		c.kill()
	}()
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var m struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(line, &m) == nil && m.Method != "" && len(m.ID) > 0 {
				s.handle(c, m.ID, m.Method, m.Params)
			}
		}
		if err != nil {
			return
		}
	}
}

func (c *conn) reply(id json.RawMessage, result any) {
	c.send(map[string]any{"id": id, "result": result})
}
func (c *conn) fail(id json.RawMessage, code int, msg string) {
	c.send(map[string]any{"id": id, "error": map[string]any{"code": code, "message": msg}})
}

var catherdTools = []string{"run_start", "profile_get", "write_run_file", "read_run_file", "route", "dispatch", "result", "climb", "land", "cancel", "status", "peek"}

func (s *AppServer) handle(c *conn, id json.RawMessage, method string, params json.RawMessage) {
	switch method {
	case "initialize":
		home := os.Getenv("CODEX_HOME")
		if home == "" {
			home = "/nonexistent/codex-home"
		}
		c.reply(id, map[string]any{"codexHome": home, "platformFamily": "unix", "platformOs": "macos", "userAgent": s.UserAgent})
	case "thread/start":
		var p struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(params, &p)
		model := p.Model
		if model == "" {
			model = "fake-orchestrator-model"
		}
		tid := s.Cat.threadNew(model)
		c.reply(id, map[string]any{"model": model, "modelProvider": "fake", "reasoningEffort": "low", "approvalPolicy": "never", "sandbox": map[string]any{"type": "readOnly"},
			"thread": map[string]any{"id": tid, "cliVersion": "0.160.0", "turns": []any{}}})
	case "thread/resume":
		s.Resumes.Add(1)
		var p struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(params, &p)
		if s.Cat.unreadableHistory() {
			c.fail(id, -32000, "thread history cannot be read")
			return
		}
		t, ok := s.Cat.threadGet(p.ThreadID)
		if !ok {
			c.fail(id, -32600, "no rollout found for thread id "+p.ThreadID)
			return
		}
		turns := make([]any, 0, len(t.Turns))
		for _, x := range t.Turns {
			turns = append(turns, map[string]any{"id": x.ID, "status": x.Status, "items": x.Items})
		}
		c.reply(id, map[string]any{"model": t.Model, "modelProvider": "fake", "approvalPolicy": "never", "sandbox": map[string]any{"type": "readOnly"}, "thread": map[string]any{"id": t.ID, "cliVersion": "0.160.0", "turns": turns}})
	case "turn/start":
		s.TurnStarts.Add(1)
		var p struct {
			ThreadID string `json:"threadId"`
			Input    []struct {
				Text string `json:"text"`
			} `json:"input"`
		}
		_ = json.Unmarshal(params, &p)
		if _, ok := s.Cat.threadGet(p.ThreadID); !ok {
			c.fail(id, -32600, "unknown thread")
			return
		}
		turnID := fmt.Sprintf("turn_%d", s.turnSeq.Add(1))
		ch := make(chan struct{})
		s.mu.Lock()
		s.active[turnID] = ch
		c.mine[turnID] = ch
		s.mu.Unlock()
		text := ""
		if len(p.Input) > 0 {
			text = p.Input[0].Text
		}
		c.reply(id, map[string]any{"turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}})
		go s.runTurn(c, p.ThreadID, turnID, text, ch)
	case "turn/interrupt":
		s.Interrupts.Add(1)
		var p struct {
			TurnID string `json:"turnId"`
		}
		_ = json.Unmarshal(params, &p)
		s.mu.Lock()
		if ch, ok := s.active[p.TurnID]; ok {
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
		s.mu.Unlock()
		c.reply(id, map[string]any{})
	case "account/read":
		if s.NotLoggedIn {
			c.reply(id, map[string]any{"account": nil, "requiresOpenaiAuth": true})
			return
		}
		c.reply(id, map[string]any{"account": map[string]any{"type": "apiKey"}, "requiresOpenaiAuth": false})
	case "model/list":
		c.reply(id, map[string]any{"data": []any{map[string]any{"id": "fake-orchestrator-model", "model": "fake-orchestrator-model"}}})
	case "mcpServerStatus/list":
		data := []any{}
		if !s.NoCatherd {
			tools := map[string]any{}
			for _, t := range catherdTools {
				hidden := false
				for _, h := range s.HideTools {
					hidden = hidden || h == t
				}
				if !hidden {
					tools[t] = map[string]any{"name": t}
				}
			}
			data = append(data, map[string]any{"name": "catherd", "authStatus": "unsupported", "tools": tools, "resources": []any{}, "resourceTemplates": []any{}})
		}
		c.reply(id, map[string]any{"data": data})
	default:
		c.fail(id, -32601, "method not found: "+method)
	}
}

func (s *AppServer) runTurn(c *conn, threadID, turnID, text string, interrupted chan struct{}) {
	userItem := map[string]any{"type": "userMessage", "id": turnID + "_u", "content": []any{map[string]any{"type": "text", "text": text}}}
	history := []map[string]any{userItem}
	finish := func(status string) {
		s.Cat.threadAddTurn(threadID, Turn{ID: turnID, Status: status, Items: history})
		c.notify("turn/completed", map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID, "status": status, "items": []any{}}})
	}
	tid := func(extra map[string]any) map[string]any {
		extra["threadId"], extra["turnId"], extra["startedAtMs"] = threadID, turnID, time.Now().UnixMilli()
		return extra
	}
	c.notify("turn/started", map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}})
	c.notify("item/started", tid(map[string]any{"item": userItem}))
	toolM := reDirTool.FindStringSubmatch(text)
	idM := reDirID.FindStringSubmatch(text)
	var args map[string]any
	if i := strings.Index(text, "\nARGS "); i >= 0 {
		rest := text[i+6:]
		if j := strings.Index(rest, "\n"); j >= 0 {
			rest = rest[:j]
		}
		_ = json.Unmarshal([]byte(rest), &args)
	}
	if toolM == nil || idM == nil || args == nil {
		c.notify("item/completed", tid(map[string]any{"item": map[string]any{"type": "agentMessage", "id": turnID + "_a", "text": "ACK"}}))
		finish("completed")
		return
	}
	tool, dirID := toolM[1], idM[1]
	mode := s.Cat.nextInject(tool)
	itemID := turnID + "_t"
	call := func(a map[string]any, status string, result any) map[string]any {
		it := map[string]any{"type": "mcpToolCall", "id": itemID, "server": "catherd", "tool": tool, "status": status, "arguments": a}
		if result != nil {
			it["result"] = result
		}
		return it
	}
	hang := func(status string) {
		select {
		case <-interrupted:
		case <-c.closed:
			return
		case <-time.After(30 * time.Second):
		}
		finish(status)
	}
	execute := func() map[string]any {
		out, terror := s.Cat.Call(tool, args, threadID)
		if terror != nil {
			payload := map[string]any{"code": terror.Code, "message": terror.Message, "fix": terror.Fix}
			b, _ := json.Marshal(payload)
			return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}, "structuredContent": payload}
		}
		var text string
		if s2, ok := out.(string); ok {
			text = s2
		} else {
			b, _ := json.Marshal(out)
			text = string(b)
		}
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
	}
	switch {
	case mode == "no_tool_call":
		c.notify("item/completed", tid(map[string]any{"item": map[string]any{"type": "agentMessage", "id": turnID + "_a", "text": "I will do that."}}))
		finish("completed")
	case mode == "wrong_args":
		wrong := map[string]any{"unexpected": true}
		for k, v := range args {
			wrong[k] = v
		}
		c.notify("item/started", tid(map[string]any{"item": call(wrong, "inProgress", nil)}))
		hang("interrupted")
	case mode == "extra_command":
		cmd := map[string]any{"type": "commandExecution", "id": turnID + "_c", "command": "git push origin HEAD", "commandActions": []any{}, "cwd": "/", "status": "inProgress"}
		c.notify("item/started", tid(map[string]any{"item": cmd}))
		hang("interrupted")
	case mode == "hang":
		c.notify("item/started", tid(map[string]any{"item": call(args, "inProgress", nil)}))
		hang("interrupted")
	case mode == "hang_after_admit":
		execute()
		c.notify("item/started", tid(map[string]any{"item": call(args, "inProgress", nil)}))
		history = append(history, call(args, "completed", nil))
		hang("interrupted")
	case mode == "crash_after_admit":
		execute()
		history = append(history, call(args, "completed", nil))
		s.Cat.threadAddTurn(threadID, Turn{ID: turnID, Status: "interrupted", Items: history})
		c.kill()
	case strings.HasPrefix(mode, "tool_error:"):
		code := strings.TrimPrefix(mode, "tool_error:")
		payload := map[string]any{"code": code, "message": "injected " + code, "fix": "fix it"}
		b, _ := json.Marshal(payload)
		res := map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}, "structuredContent": payload}
		c.notify("item/started", tid(map[string]any{"item": call(args, "inProgress", nil)}))
		c.notify("item/completed", tid(map[string]any{"item": call(args, "completed", res)}))
		history = append(history, call(args, "completed", res))
		finish("completed")
	default:
		c.notify("item/started", tid(map[string]any{"item": call(args, "inProgress", nil)}))
		res := execute()
		done := call(args, "completed", res)
		c.notify("item/completed", tid(map[string]any{"item": done}))
		history = append(history, done)
		c.notify("item/completed", tid(map[string]any{"item": map[string]any{"type": "agentMessage", "id": turnID + "_a", "text": "DONE " + dirID}}))
		n := int64(s.turnSeq.Load())
		c.notify("thread/tokenUsage/updated", map[string]any{"threadId": threadID, "turnId": turnID, "tokenUsage": map[string]any{
			"total": map[string]any{"inputTokens": 200 * n, "cachedInputTokens": 100 * n, "outputTokens": 20 * n, "reasoningOutputTokens": 0, "totalTokens": 220 * n},
			"last":  map[string]any{"inputTokens": 200, "cachedInputTokens": 100, "outputTokens": 20, "reasoningOutputTokens": 0, "totalTokens": 220}}})
		finish("completed")
	}
}
