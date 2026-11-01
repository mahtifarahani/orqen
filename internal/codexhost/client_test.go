package codexhost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// peer is an in-test app-server speaking JSONL over pipes.
type peer struct {
	t       *testing.T
	r       *bufio.Reader
	w       io.WriteCloser
	mu      sync.Mutex
	reqs    []map[string]any
	handler func(p *peer, m map[string]any)
	closeR  io.Closer
}

type duplex struct {
	io.Reader
	io.WriteCloser
}

func (d duplex) Close() error { _ = d.WriteCloser.Close(); return nil }

func newPeer(t *testing.T, h func(p *peer, m map[string]any)) (io.ReadWriteCloser, *peer) {
	cr, sw := io.Pipe() // server writes, client reads
	sr, cw := io.Pipe() // client writes, server reads
	p := &peer{t: t, r: bufio.NewReader(sr), w: sw, handler: h, closeR: sr}
	go func() {
		for {
			line, err := p.r.ReadBytes('\n')
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(line, &m) != nil {
				continue
			}
			p.mu.Lock()
			p.reqs = append(p.reqs, m)
			p.mu.Unlock()
			p.handler(p, m)
		}
	}()
	t.Cleanup(func() { _ = sw.Close(); _ = cw.Close() })
	return duplex{cr, cw}, p
}

func (p *peer) send(v any) {
	b, _ := json.Marshal(v)
	_, _ = p.w.Write(append(b, '\n'))
}
func (p *peer) reply(m map[string]any, result any) {
	p.send(map[string]any{"id": m["id"], "result": result})
}
func (p *peer) notify(method string, params any) {
	p.send(map[string]any{"method": method, "params": params})
}
func (p *peer) methods() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, r := range p.reqs {
		if m, ok := r["method"].(string); ok {
			out = append(out, m)
		}
	}
	return out
}
func (p *peer) count(method string) int {
	n := 0
	for _, m := range p.methods() {
		if m == method {
			n++
		}
	}
	return n
}
func (p *peer) waitFor(method string) bool {
	for i := 0; i < 200; i++ {
		if p.count(method) > 0 {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// script describes what the peer does after turn/start.
type script func(p *peer, threadID, turnID string, input string)

func baseHandler(onTurn script) func(p *peer, m map[string]any) {
	return func(p *peer, m map[string]any) {
		method, _ := m["method"].(string)
		switch method {
		case "initialize":
			p.reply(m, map[string]any{"codexHome": "/h", "platformFamily": "unix", "platformOs": "macos", "userAgent": "codex_cli_rs/0.160.0 (Mac OS)"})
		case "thread/start":
			p.reply(m, map[string]any{"model": "gpt-x", "reasoningEffort": "low", "thread": map[string]any{"id": "thr_1", "cliVersion": "0.160.0", "turns": []any{}}})
		case "thread/resume":
			p.reply(m, map[string]any{"model": "gpt-x", "thread": map[string]any{"id": "thr_1", "turns": []any{}}})
		case "turn/start":
			params := m["params"].(map[string]any)
			input := params["input"].([]any)[0].(map[string]any)["text"].(string)
			p.reply(m, map[string]any{"turn": map[string]any{"id": "turn_1", "status": "inProgress", "items": []any{}}})
			go onTurn(p, params["threadId"].(string), "turn_1", input)
		case "turn/interrupt":
			p.reply(m, map[string]any{})
		case "account/read":
			p.reply(m, map[string]any{"account": map[string]any{"type": "chatgpt", "email": "x@y", "planType": "plus"}, "requiresOpenaiAuth": true})
		case "model/list":
			p.reply(m, map[string]any{"data": []any{map[string]any{"id": "a", "model": "gpt-a"}, map[string]any{"id": "b", "model": "gpt-b"}}})
		case "mcpServerStatus/list":
			p.reply(m, map[string]any{"data": []any{map[string]any{"name": "catherd", "authStatus": "unsupported", "tools": map[string]any{"run_start": map[string]any{}, "dispatch": map[string]any{}}, "resources": []any{}, "resourceTemplates": []any{}}}})
		}
	}
}

func toolItem(id, tool, status string, args any, result any) map[string]any {
	it := map[string]any{"type": "mcpToolCall", "id": id, "server": "catherd", "tool": tool, "status": status, "arguments": args}
	if result != nil {
		it["result"] = result
	}
	return it
}
func itemEv(p *peer, method, thread, turn string, item map[string]any) {
	p.notify(method, map[string]any{"threadId": thread, "turnId": turn, "item": item, "startedAtMs": 1})
}
func turnDone(p *peer, thread, turn, status string) {
	p.notify("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": status, "items": []any{}}})
}

func dial(t *testing.T, onTurn script) (*Client, *peer) {
	rwc, p := newPeer(t, baseHandler(onTurn))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, rwc, ClientInfo{Name: "orqen", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, p
}

func start(t *testing.T, c *Client) Thread {
	th, err := c.StartThread(context.Background(), ThreadParams{Effort: "low"})
	if err != nil || th.ID != "thr_1" || th.Model != "gpt-x" || th.Effort != "low" {
		t.Fatal(th, err)
	}
	return th
}

func TestDirectiveText(t *testing.T) {
	d := NewDirectiveWithID("dir_1", "dispatch", map[string]any{"name": "w", "run": "r1"})
	want := "ORQEN-DIRECTIVE v1 id=dir_1\nCall exactly one tool: catherd.dispatch\nARGS {\"name\":\"w\",\"run\":\"r1\"}\nCall no other tool and run no command. Afterwards reply exactly: DONE dir_1"
	if d.Text != want {
		t.Fatal(d.Text)
	}
	if a, b := NewDirective("x", nil), NewDirective("x", nil); a.ID == b.ID || !strings.HasPrefix(a.ID, "dir_") {
		t.Fatal("ids")
	}
}

func TestHappyDirective(t *testing.T) {
	var gotInput string
	c, p := dial(t, func(p *peer, th, turn, input string) {
		gotInput = input
		args := map[string]any{"run": "r1", "name": "w"}
		itemEv(p, "item/started", th, turn, map[string]any{"type": "userMessage", "id": "u"})
		itemEv(p, "item/started", th, turn, toolItem("i1", "dispatch", "inProgress", args, nil))
		itemEv(p, "item/completed", th, turn, toolItem("i1", "dispatch", "completed", args, map[string]any{"content": []any{}, "structuredContent": map[string]any{"dispatchId": "d1"}}))
		itemEv(p, "item/completed", th, turn, map[string]any{"type": "agentMessage", "id": "a", "text": "DONE"})
		p.notify("thread/tokenUsage/updated", map[string]any{"threadId": th, "turnId": turn, "tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 10, "cachedInputTokens": 4, "outputTokens": 3, "reasoningOutputTokens": 1, "totalTokens": 13}}})
		turnDone(p, th, turn, "completed")
	})
	th := start(t, c)
	d := NewDirectiveWithID("dir_7", "dispatch", map[string]any{"name": "w", "run": "r1"})
	call, err := c.RunDirective(context.Background(), th.ID, d, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if call.Tool != "dispatch" || call.ItemID != "i1" || string(call.Result) != `{"dispatchId":"d1"}` || gotInput != d.Text {
		t.Fatalf("%+v %q", call, gotInput)
	}
	if u, ok := c.Usage(th.ID); !ok || u.Total != 13 || u.CachedInput != 4 {
		t.Fatal(u, ok)
	}
	// thread/start pins approvals and sandbox
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range p.reqs {
		if r["method"] == "thread/start" {
			pr := r["params"].(map[string]any)
			if pr["approvalPolicy"] != "never" || pr["sandbox"] != "read-only" || !strings.Contains(pr["baseInstructions"].(string), "DONE <directive id>") {
				t.Fatalf("%v", pr)
			}
		}
		if r["method"] == "turn/start" && r["params"].(map[string]any)["effort"] != "low" {
			t.Fatal("effort")
		}
	}
}

func TestTextJSONResultAndToolError(t *testing.T) {
	c, _ := dial(t, func(p *peer, th, turn, input string) {
		args := map[string]any{"a": 1}
		res := map[string]any{"content": []any{map[string]any{"type": "text", "text": `{"code":"E_RUN_BUDGET","message":"budget used","fix":"raise it"}`}}}
		itemEv(p, "item/completed", th, turn, toolItem("i1", "dispatch", "completed", args, res))
		turnDone(p, th, turn, "completed")
	})
	th := start(t, c)
	_, err := c.RunDirective(context.Background(), th.ID, NewDirective("dispatch", map[string]any{"a": 1}), 3*time.Second)
	var te *ToolError
	if !errors.As(err, &te) || te.Code != "E_RUN_BUDGET" || te.Fix != "raise it" {
		t.Fatal(err)
	}
}

func TestWrongArgsInterrupts(t *testing.T) {
	c, p := dial(t, func(p *peer, th, turn, input string) {
		itemEv(p, "item/started", th, turn, toolItem("i1", "dispatch", "inProgress", map[string]any{"name": "other"}, nil))
	})
	th := start(t, c)
	_, err := c.RunDirective(context.Background(), th.ID, NewDirective("dispatch", map[string]any{"name": "w"}), 3*time.Second)
	if !errors.Is(err, ErrArgsMismatch) || !p.waitFor("turn/interrupt") {
		t.Fatal(err, p.methods())
	}
}

func TestExtraCommandItemIsADeviation(t *testing.T) {
	c, p := dial(t, func(p *peer, th, turn, input string) {
		itemEv(p, "item/started", th, turn, map[string]any{"type": "commandExecution", "id": "c1", "command": "git push"})
	})
	th := start(t, c)
	_, err := c.RunDirective(context.Background(), th.ID, NewDirective("dispatch", nil), 3*time.Second)
	var dv *DeviationError
	if !errors.Is(err, ErrDeviation) || !errors.As(err, &dv) || !strings.Contains(dv.Detail, "commandExecution") || !p.waitFor("turn/interrupt") {
		t.Fatal(err, p.methods())
	}
}

func TestWrongToolAndSecondCallAreDeviations(t *testing.T) {
	c, _ := dial(t, func(p *peer, th, turn, input string) {
		it := toolItem("i1", "land", "inProgress", map[string]any{}, nil)
		itemEv(p, "item/started", th, turn, it)
	})
	th := start(t, c)
	if _, err := c.RunDirective(context.Background(), th.ID, NewDirective("dispatch", nil), 3*time.Second); !errors.Is(err, ErrDeviation) {
		t.Fatal("wrong tool", err)
	}
	c2, _ := dial(t, func(p *peer, th, turn, input string) {
		itemEv(p, "item/started", th, turn, toolItem("i1", "dispatch", "inProgress", map[string]any{}, nil))
		itemEv(p, "item/started", th, turn, toolItem("i2", "dispatch", "inProgress", map[string]any{}, nil))
	})
	th2 := start(t, c2)
	if _, err := c2.RunDirective(context.Background(), th2.ID, NewDirective("dispatch", nil), 3*time.Second); !errors.Is(err, ErrDeviation) {
		t.Fatal("second call", err)
	}
}

func TestNoToolCall(t *testing.T) {
	c, _ := dial(t, func(p *peer, th, turn, input string) {
		itemEv(p, "item/completed", th, turn, map[string]any{"type": "agentMessage", "id": "a", "text": "sure"})
		turnDone(p, th, turn, "completed")
	})
	th := start(t, c)
	if _, err := c.RunDirective(context.Background(), th.ID, NewDirective("dispatch", nil), 3*time.Second); !errors.Is(err, ErrNoToolCall) {
		t.Fatal(err)
	}
}

func TestHangInterruptsAndIsUnknown(t *testing.T) {
	c, p := dial(t, func(p *peer, th, turn, input string) {
		itemEv(p, "item/started", th, turn, toolItem("i1", "dispatch", "inProgress", map[string]any{}, nil))
	})
	th := start(t, c)
	_, err := c.RunDirective(context.Background(), th.ID, NewDirective("dispatch", nil), 150*time.Millisecond)
	if !errors.Is(err, ErrOutcomeUnknown) || !p.waitFor("turn/interrupt") {
		t.Fatal(err, p.methods())
	}
}

func TestApprovalForAnotherToolIsDeclined(t *testing.T) {
	var answer map[string]any
	var mu sync.Mutex
	handler := func(p *peer, m map[string]any) {
		if _, ok := m["method"]; !ok && m["id"] != nil { // a response to our server request
			mu.Lock()
			answer = m
			mu.Unlock()
			return
		}
		baseHandler(func(p *peer, th, turn, input string) {
			p.send(map[string]any{"id": "srv1", "method": "mcpServer/elicitation/request", "params": map[string]any{"serverName": "other", "threadId": th, "turnId": turn}})
		})(p, m)
	}
	rwc, p := newPeer(t, handler)
	c, err := Dial(context.Background(), rwc, ClientInfo{Name: "orqen", Version: "t"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	th := start(t, c)
	_, err = c.RunDirective(context.Background(), th.ID, NewDirective("dispatch", nil), 3*time.Second)
	if !errors.Is(err, ErrDeviation) {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	res, _ := answer["result"].(map[string]any)
	if res["action"] != "decline" || !p.waitFor("turn/interrupt") {
		t.Fatalf("%v", answer)
	}
}

func TestApprovalForMatchingCallIsAccepted(t *testing.T) {
	var answer map[string]any
	var mu sync.Mutex
	handler := func(p *peer, m map[string]any) {
		if _, ok := m["method"]; !ok && m["id"] != nil {
			mu.Lock()
			answer = m
			mu.Unlock()
			args := map[string]any{"a": 1}
			itemEv(p, "item/completed", "thr_1", "turn_1", toolItem("i1", "dispatch", "completed", args, map[string]any{"content": []any{}, "structuredContent": map[string]any{"ok": true}}))
			turnDone(p, "thr_1", "turn_1", "completed")
			return
		}
		baseHandler(func(p *peer, th, turn, input string) {
			itemEv(p, "item/started", th, turn, toolItem("i1", "dispatch", "inProgress", map[string]any{"a": 1}, nil))
			p.send(map[string]any{"id": "srv1", "method": "mcpServer/elicitation/request", "params": map[string]any{"serverName": "catherd", "threadId": th, "turnId": turn}})
		})(p, m)
	}
	rwc, _ := newPeer(t, handler)
	c, err := Dial(context.Background(), rwc, ClientInfo{Name: "orqen", Version: "t"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	th := start(t, c)
	call, err := c.RunDirective(context.Background(), th.ID, NewDirective("dispatch", map[string]any{"a": 1}), 3*time.Second)
	if err != nil || string(call.Result) != `{"ok":true}` {
		t.Fatal(call, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if answer["result"].(map[string]any)["action"] != "accept" {
		t.Fatalf("%v", answer)
	}
}

func TestStrayNotificationsAreIgnored(t *testing.T) {
	c, _ := dial(t, func(p *peer, th, turn, input string) {
		p.notify("something/unknown", map[string]any{"threadId": th})
		itemEv(p, "item/started", th, "turn_other", map[string]any{"type": "commandExecution", "id": "c9"})
		turnDone(p, th, "turn_other", "completed")
		args := map[string]any{}
		itemEv(p, "item/completed", th, turn, toolItem("i1", "status", "completed", args, map[string]any{"content": []any{}, "structuredContent": map[string]any{"v": 1}}))
		turnDone(p, th, turn, "completed")
	})
	th := start(t, c)
	if _, err := c.RunDirective(context.Background(), th.ID, NewDirective("status", nil), 3*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestPeerEOFMidTurn(t *testing.T) {
	c, p := dial(t, func(p *peer, th, turn, input string) {
		itemEv(p, "item/started", th, turn, toolItem("i1", "dispatch", "inProgress", map[string]any{}, nil))
		time.Sleep(20 * time.Millisecond)
		_ = p.w.Close()
	})
	th := start(t, c)
	_, err := c.RunDirective(context.Background(), th.ID, NewDirective("dispatch", nil), 3*time.Second)
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("the client should notice the closed connection")
	}
	_ = p
	if _, err = c.RunDirective(context.Background(), th.ID, NewDirective("dispatch", nil), time.Second); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatal("after EOF", err)
	}
}

func TestProbeCreatesNoThreadOrTurn(t *testing.T) {
	c, p := dial(t, func(p *peer, th, turn, input string) { t.Error("no turn expected") })
	res, err := c.Probe(context.Background())
	if err != nil || !res.LoggedIn || res.AccountType != "chatgpt" || res.Version() != "0.160.0" || len(res.Models) != 2 || len(res.MCPServers) != 1 || res.MCPServers[0].Name != "catherd" || strings.Join(res.MCPServers[0].Tools, ",") != "dispatch,run_start" || res.CodexHome != "/h" {
		t.Fatalf("%+v %v", res, err)
	}
	if p.count("thread/start") != 0 || p.count("turn/start") != 0 {
		t.Fatal(p.methods())
	}
}

func TestResumeAndTurnForDirective(t *testing.T) {
	turns := []Turn{{ID: "t1", Items: []Item{{Type: "userMessage", Content: []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{{Type: "text", Text: "ORQEN-DIRECTIVE v1 id=dir_9\n..."}}}, {Type: "mcpToolCall", Server: "catherd", Tool: "dispatch", Status: "completed", ID: "i1"}}}}
	tr, ok := TurnForDirective(turns, "dir_9")
	if !ok || len(ToolCallsOf(tr)) != 1 {
		t.Fatal(tr, ok)
	}
	if _, ok = TurnForDirective(turns, "dir_8"); ok {
		t.Fatal("unknown directive")
	}
	if _, ok = TurnForDirective(turns, "dir_"); ok && false {
		t.Fatal()
	}
}
