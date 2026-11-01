package codexhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Client is an initialized app-server connection.
type Client struct {
	conn *Conn
	Init InitializeResult

	mu       sync.Mutex
	watchers map[string]*queue[routed]
	locks    map[string]*sync.Mutex
	usage    map[string]TokenUsage
	effort   map[string]string
}

type routed struct {
	ev  *Event
	req *ServerRequest
}

// Dial initializes the connection and starts routing notifications per thread.
func Dial(ctx context.Context, rwc io.ReadWriteCloser, info ClientInfo) (*Client, error) {
	conn := NewConn(rwc)
	c := &Client{conn: conn, watchers: map[string]*queue[routed]{}, locks: map[string]*sync.Mutex{}, usage: map[string]TokenUsage{}, effort: map[string]string{}}
	if err := conn.Call(ctx, MInitialize, initializeParams{ClientInfo: info}, &c.Init); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.Notify(MInitialized, nil); err != nil {
		_ = conn.Close()
		return nil, err
	}
	go c.route()
	return c, nil
}

// Close ends the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Done is closed when the app-server connection ended.
func (c *Client) Done() <-chan struct{} { return c.conn.Done() }

type envelope struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

func (c *Client) watcher(thread string) *queue[routed] {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.watchers[thread]
}

func (c *Client) route() {
	for in := range c.conn.Inbound() {
		if in.Request != nil {
			req := *in.Request
			var env envelope
			_ = json.Unmarshal(req.Params, &env)
			if w := c.watcher(env.ThreadID); w != nil {
				w.push(routed{req: &req})
			} else {
				declineRequest(req) // nobody is waiting: never approve anything
			}
			continue
		}
		ev := *in.Event
		var env envelope
		_ = json.Unmarshal(ev.Params, &env)
		if ev.Method == NThreadTokenUsage {
			var n struct {
				ThreadID   string `json:"threadId"`
				TokenUsage struct {
					Total struct {
						InputTokens           int64 `json:"inputTokens"`
						CachedInputTokens     int64 `json:"cachedInputTokens"`
						OutputTokens          int64 `json:"outputTokens"`
						ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
						TotalTokens           int64 `json:"totalTokens"`
					} `json:"total"`
				} `json:"tokenUsage"`
			}
			if json.Unmarshal(ev.Params, &n) == nil {
				b := n.TokenUsage.Total
				c.mu.Lock()
				c.usage[n.ThreadID] = TokenUsage{Input: b.InputTokens, CachedInput: b.CachedInputTokens, Output: b.OutputTokens, Reasoning: b.ReasoningOutputTokens, Total: b.TotalTokens}
				c.mu.Unlock()
			}
			continue
		}
		if w := c.watcher(env.ThreadID); w != nil {
			e := ev
			w.push(routed{ev: &e})
		}
	}
	c.closeWatchers()
}

func (c *Client) closeWatchers() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, w := range c.watchers {
		w.close()
	}
}

// declineRequest answers a server request with the safest refusal for its kind.
func declineRequest(req ServerRequest) {
	switch req.Method {
	case RCommandApproval, RFileApproval:
		_ = req.Respond(map[string]any{"decision": "decline"})
	case RLegacyExec, RLegacyPatch:
		_ = req.Respond(map[string]any{"decision": "denied"})
	case RPermissions:
		_ = req.Respond(map[string]any{"permissions": map[string]any{}})
	case RUserInput:
		_ = req.Respond(map[string]any{"answers": map[string]any{}})
	case RDynamicTool:
		_ = req.Respond(map[string]any{"contentItems": []any{}, "success": false})
	case RElicitation:
		_ = req.Respond(map[string]any{"action": "decline"})
	default:
		_ = req.RespondError(-32601, "method not supported by Orqen")
	}
}

func threadFrom(resp threadResponse) Thread {
	t := Thread{ID: resp.Thread.ID, Model: resp.Model, CLIVersion: resp.Thread.CLIVersion, Turns: resp.Thread.Turns}
	if resp.ReasoningEffort != nil {
		t.Effort = *resp.ReasoningEffort
	}
	return t
}

// StartThread starts the orchestrator thread: no approvals, a read-only sandbox, Orqen's base instructions.
func (c *Client) StartThread(ctx context.Context, p ThreadParams) (Thread, error) {
	var resp threadResponse
	instr := p.BaseInstructions
	if instr == "" {
		instr = BaseInstructions
	}
	err := c.conn.Call(ctx, MThreadStart, threadStartParams{ApprovalPolicy: "never", Sandbox: "read-only", BaseInstructions: instr, Config: p.Config, Cwd: p.Cwd, Model: p.Model, ServiceName: "orqen"}, &resp)
	if err != nil {
		return Thread{}, err
	}
	t := threadFrom(resp)
	c.mu.Lock()
	c.effort[t.ID] = p.Effort
	c.mu.Unlock()
	return t, nil
}

// ResumeThread reopens a thread with its history.
func (c *Client) ResumeThread(ctx context.Context, id string) (Thread, error) {
	var resp threadResponse
	if err := c.conn.Call(ctx, MThreadResume, threadResumeParams{ThreadID: id, ApprovalPolicy: "never", Sandbox: "read-only"}, &resp); err != nil {
		return Thread{}, err
	}
	return threadFrom(resp), nil
}

// SetEffort sets the reasoning effort used for later turns of a thread.
func (c *Client) SetEffort(thread, effort string) {
	c.mu.Lock()
	c.effort[thread] = effort
	c.mu.Unlock()
}

// Usage returns the cumulative token usage last reported for the thread.
func (c *Client) Usage(thread string) (TokenUsage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	u, ok := c.usage[thread]
	return u, ok
}

// Interrupt asks the server to stop a turn.
func (c *Client) Interrupt(ctx context.Context, thread, turn string) error {
	return c.conn.Call(ctx, MTurnInterrupt, turnInterruptParams{ThreadID: thread, TurnID: turn}, nil)
}

func (c *Client) threadLock(id string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := c.locks[id]
	if l == nil {
		l = &sync.Mutex{}
		c.locks[id] = l
	}
	return l
}

var ignorable = map[string]bool{"userMessage": true, "agentMessage": true, "reasoning": true, "plan": true, "contextCompaction": true, "hookPrompt": true, "functionCallOutput": true}

type itemNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Item     Item   `json:"item"`
}

// RunDirective sends one directive as a turn and returns the one validated catherd call. The model's prose is never
// used. A deviation (a command, a file change, another tool, a second call) interrupts the turn.
func (c *Client) RunDirective(ctx context.Context, threadID string, d Directive, timeout time.Duration) (ToolCall, error) {
	lock := c.threadLock(threadID)
	lock.Lock()
	defer lock.Unlock()
	w := newQueue[routed]()
	c.mu.Lock()
	c.watchers[threadID] = w
	effort := c.effort[threadID]
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.watchers, threadID)
		c.mu.Unlock()
		w.close()
	}()
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var started struct {
		Turn Turn `json:"turn"`
	}
	if err := c.conn.Call(tctx, MTurnStart, turnStartParams{ThreadID: threadID, Effort: effort, Input: []userInput{{Type: "text", Text: d.Text}}}, &started); err != nil {
		var rpc *RPCError
		if errors.As(err, &rpc) {
			return ToolCall{}, err // refused before anything started
		}
		return ToolCall{}, fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
	}
	turnID := started.Turn.ID
	interrupt := func() {
		ictx, icancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer icancel()
		_ = c.Interrupt(ictx, threadID, turnID)
	}
	var (
		call      *Item
		argsOK    bool
		finished  bool
		deviation string
	)
	deviate := func(detail string) (ToolCall, error) {
		interrupt()
		return ToolCall{}, &DeviationError{Detail: detail}
	}
	result := func() (ToolCall, error) {
		tc := ToolCall{ThreadID: threadID, TurnID: turnID, ItemID: call.ID, Server: call.Server, Tool: call.Tool, Arguments: call.Arguments}
		if call.Error != nil {
			if te := parseToolError([]byte(call.Error.Message)); te != nil {
				return tc, te
			}
			return tc, &ToolError{Code: "E_HOST_TOOL", Message: call.Error.Message}
		}
		if call.Status == "failed" && call.Result == nil {
			return tc, &ToolError{Code: "E_HOST_TOOL", Message: "the tool call failed"}
		}
		if call.Result != nil {
			payload, text := resultPayload(call.Result)
			tc.Result, tc.Text = payload, text
			if te := parseToolError(payload); te != nil {
				return tc, te
			}
			if payload == nil {
				if te := parseToolError([]byte(text)); te != nil {
					return tc, te
				}
			}
		}
		return tc, nil
	}
	for {
		r, ok := w.pop(tctx)
		if !ok {
			if tctx.Err() != nil {
				interrupt()
			}
			return ToolCall{}, fmt.Errorf("%w: the turn did not finish", ErrOutcomeUnknown)
		}
		if r.req != nil {
			switch r.req.Method {
			case RElicitation:
				var p struct {
					ServerName string `json:"serverName"`
				}
				_ = json.Unmarshal(r.req.Params, &p)
				if p.ServerName == "catherd" && call != nil && argsOK {
					_ = r.req.Respond(map[string]any{"action": "accept"})
				} else {
					declineRequest(*r.req)
					deviation = "elicitation for an unexpected call"
				}
			default:
				declineRequest(*r.req)
				deviation = "approval requested for " + r.req.Method
			}
			if deviation != "" {
				return deviate(deviation)
			}
			continue
		}
		ev := r.ev
		var env envelope
		_ = json.Unmarshal(ev.Params, &env)
		if env.TurnID != "" && env.TurnID != turnID {
			continue // a stray notification of another turn
		}
		switch ev.Method {
		case NItemStarted, NItemCompleted:
			var n itemNotification
			if json.Unmarshal(ev.Params, &n) != nil {
				continue
			}
			it := n.Item
			if it.Type == "mcpToolCall" {
				if it.Server != "catherd" || it.Tool != d.Tool {
					return deviate("tool " + it.Server + "." + it.Tool)
				}
				if call != nil && call.ID != it.ID {
					return deviate("a second tool call")
				}
				if !d.argsMatch(it.Arguments) {
					interrupt()
					c := it
					call = &c
					return ToolCall{ThreadID: threadID, TurnID: turnID, ItemID: it.ID, Server: it.Server, Tool: it.Tool, Arguments: it.Arguments}, ErrArgsMismatch
				}
				argsOK = true
				cp := it
				call = &cp
				continue
			}
			if !ignorable[it.Type] {
				return deviate("item " + it.Type)
			}
		case NTurnCompleted:
			var n struct {
				Turn Turn `json:"turn"`
			}
			if json.Unmarshal(ev.Params, &n) != nil || n.Turn.ID != turnID {
				continue
			}
			finished = true
			switch {
			case call == nil && n.Turn.Status == "completed":
				return ToolCall{}, ErrNoToolCall
			case call == nil:
				msg := n.Turn.Status
				if n.Turn.Error != nil {
					msg += ": " + n.Turn.Error.Message
				}
				return ToolCall{}, fmt.Errorf("%w (%s)", ErrNoToolCall, msg)
			case call.Status == "inProgress" || n.Turn.Status == "interrupted" && call.Status != "completed" && call.Status != "failed":
				return ToolCall{}, fmt.Errorf("%w: the turn ended while the tool call was running", ErrOutcomeUnknown)
			}
			_ = finished
			return result()
		}
	}
}

var versionRE = regexp.MustCompile(`\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.]+)?`)

// Probe reads the host's login state, models and MCP servers. It creates no thread and no turn.
func (c *Client) Probe(ctx context.Context) (ProbeResult, error) {
	out := ProbeResult{UserAgent: c.Init.UserAgent, CodexHome: c.Init.CodexHome}
	var acct struct {
		Account *struct {
			Type string `json:"type"`
		} `json:"account"`
		RequiresOpenaiAuth bool `json:"requiresOpenaiAuth"`
	}
	if err := c.conn.Call(ctx, MAccountRead, accountReadParams{}, &acct); err != nil {
		return out, err
	}
	out.RequiresAuth = acct.RequiresOpenaiAuth
	if acct.Account != nil {
		out.LoggedIn, out.AccountType = true, acct.Account.Type
	}
	var models struct {
		Data []struct {
			Model string `json:"model"`
			ID    string `json:"id"`
		} `json:"data"`
	}
	if err := c.conn.Call(ctx, MModelList, modelListParams{}, &models); err != nil {
		return out, err
	}
	for _, m := range models.Data {
		name := m.Model
		if name == "" {
			name = m.ID
		}
		out.Models = append(out.Models, name)
	}
	var servers struct {
		Data []struct {
			Name       string                     `json:"name"`
			Tools      map[string]json.RawMessage `json:"tools"`
			ToolsError *string                    `json:"toolsError"`
		} `json:"data"`
	}
	if err := c.conn.Call(ctx, MMcpServerStatuses, mcpStatusParams{Detail: "toolsAndAuthOnly"}, &servers); err != nil {
		return out, err
	}
	for _, s := range servers.Data {
		m := MCPServer{Name: s.Name}
		for t := range s.Tools {
			m.Tools = append(m.Tools, t)
		}
		sortStrings(m.Tools)
		if s.ToolsError != nil {
			m.ToolsError = *s.ToolsError
		}
		out.MCPServers = append(out.MCPServers, m)
	}
	return out, nil
}

// Version extracts the Codex version from the server's user agent, or "".
func (r ProbeResult) Version() string { return versionRE.FindString(r.UserAgent) }

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// resultPayload returns the structured content, else the JSON parsed from the first text content.
func resultPayload(r *McpResult) (json.RawMessage, string) {
	if len(r.StructuredContent) > 0 && string(r.StructuredContent) != "null" {
		return r.StructuredContent, ""
	}
	for _, raw := range r.Content {
		var c struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &c) != nil || c.Type != "text" {
			continue
		}
		t := strings.TrimSpace(c.Text)
		if json.Valid([]byte(t)) {
			return json.RawMessage(t), ""
		}
		return nil, c.Text
	}
	return nil, ""
}

// parseToolError recognizes catherd's {code, message, fix} error object.
func parseToolError(raw []byte) *ToolError {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Fix     string `json:"fix"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Fix     string `json:"fix"`
		} `json:"error"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &e) != nil {
		return nil
	}
	if e.Error != nil && strings.HasPrefix(e.Error.Code, "E_") {
		return &ToolError{Code: e.Error.Code, Message: e.Error.Message, Fix: e.Error.Fix}
	}
	if strings.HasPrefix(e.Code, "E_") {
		return &ToolError{Code: e.Code, Message: e.Message, Fix: e.Fix}
	}
	return nil
}
