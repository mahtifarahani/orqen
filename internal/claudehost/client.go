// Package claudehost drives a headless Claude Code process as Orqen's orchestration host. Claude Code has no
// long-lived app-server, so every directive is one `claude -p` turn on a persistent session. The turn is read from
// the stream-json output and held to the same rule as the Codex host: exactly one catherd tool call, with exactly the
// directed arguments, and nothing else. It never starts a process Orqen does not wait for.
package claudehost

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"orqen/internal/codexhost"
)

// MinClaudeVersion is the oldest Claude Code CLI Orqen supports (the headless `claude-code:` rungs of catherd).
const MinClaudeVersion = "2.1.282"

// ToolPrefix is how Claude Code names the tools of the catherd MCP server.
const ToolPrefix = "mcp__catherd__"

// BaseInstructions are appended to the system prompt of every orchestrator session.
const BaseInstructions = `You are the hands of a program called Orqen. Orqen sends you directives. Each directive tells you to call exactly one tool of the "catherd" MCP server with exact arguments.
Rules:
- Call exactly the one tool the directive names, with exactly the given ARGS, and nothing else.
- Never run a shell command, never edit a file, never search the web, never call another tool, never start a subagent.
- After the tool returns, reply exactly: DONE <directive id>.
- If a catherd message arrives that is not a directive, reply ACK and call nothing.`

// Proc is a running claude process.
type Proc interface {
	// Kill ends the process and its children.
	Kill()
	// Wait returns when the process ended.
	Wait() error
}

// StartFunc starts claude with an explicit environment and returns its stdout. Tests return a scripted stream.
type StartFunc func(ctx context.Context, bin string, args, env []string, cwd string) (io.ReadCloser, Proc, error)

// Config describes the host of one organization.
type Config struct {
	Bin string
	// Env is the complete environment of claude and everything under it. Nothing is inherited.
	Env []string
	Cwd string
	// ConfigDir is CLAUDE_CONFIG_DIR: the isolated login and the session transcripts live below it.
	ConfigDir  string
	CatherdBin string
	// CatherdEnv is the explicit environment of the catherd MCP server.
	CatherdEnv map[string]string
	Model      string

	Start StartFunc
	// Output runs a short command and returns its stdout (version, auth status). Tests replace it.
	Output func(ctx context.Context, bin string, args, env []string) ([]byte, error)
	// ListTools asks the catherd MCP server for its tools without any model call. Tests replace it.
	ListTools func(ctx context.Context, bin string, env []string) ([]string, error)
}

// Client is the Claude Code host of one organization.
type Client struct {
	cfg  Config
	done chan struct{}
	once sync.Once

	mu      sync.Mutex
	started map[string]bool // sessions that already have a first turn (use --resume)
	locks   map[string]*sync.Mutex
	usage   map[string]codexhost.TokenUsage
	effort  map[string]string
	model   map[string]string
}

var _ codexhost.Host = (*Client)(nil)

// New returns the host client. Nothing is started until a directive runs.
func New(cfg Config) *Client {
	if cfg.Start == nil {
		cfg.Start = ExecStart
	}
	if cfg.Output == nil {
		cfg.Output = execOutput
	}
	if cfg.ListTools == nil {
		cfg.ListTools = mcpTools
	}
	return &Client{cfg: cfg, done: make(chan struct{}), started: map[string]bool{}, locks: map[string]*sync.Mutex{}, usage: map[string]codexhost.TokenUsage{}, effort: map[string]string{}, model: map[string]string{}}
}

// Done is closed when the client was closed. A turn that dies does not end the host: the next turn starts anew.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close ends the client.
func (c *Client) Close() error { c.once.Do(func() { close(c.done) }); return nil }

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// StartThread reserves a session id. The session exists once its first directive ran.
func (c *Client) StartThread(_ context.Context, p codexhost.ThreadParams) (codexhost.Thread, error) {
	id := newUUID()
	model := p.Model
	c.mu.Lock()
	c.effort[id], c.model[id] = p.Effort, model
	c.mu.Unlock()
	return codexhost.Thread{ID: id, Model: model, Effort: p.Effort}, nil
}

// ResumeThread reopens a session from its transcript. A missing transcript is an *RPCError: the thread is gone.
func (c *Client) ResumeThread(_ context.Context, id string) (codexhost.Thread, error) {
	turns, err := c.history(id)
	if err != nil {
		return codexhost.Thread{}, err
	}
	c.mu.Lock()
	c.started[id] = true
	c.mu.Unlock()
	return codexhost.Thread{ID: id, Turns: turns}, nil
}

// SetEffort sets the reasoning effort used for later turns of a session.
func (c *Client) SetEffort(thread, effort string) {
	c.mu.Lock()
	c.effort[thread] = effort
	c.mu.Unlock()
}

// Usage returns the cumulative token usage of the session.
func (c *Client) Usage(thread string) (codexhost.TokenUsage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	u, ok := c.usage[thread]
	return u, ok
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

// mcpConfig is the --mcp-config value: the catherd server and nothing else.
func (c *Client) mcpConfig() string {
	b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"catherd": map[string]any{
		"type": "stdio", "command": c.cfg.CatherdBin, "args": []string{"mcp"}, "env": c.cfg.CatherdEnv,
	}}})
	return string(b)
}

// turnArgs are the flags of one directive turn: no built-in tool, only the directed catherd tool allowed, nothing
// that would prompt, and no setting of the machine.
func (c *Client) turnArgs(session string, resume bool, tool, text, effort, model string) []string {
	args := []string{"-p", text, "--output-format", "stream-json", "--verbose"}
	if resume {
		args = append(args, "--resume", session)
	} else {
		args = append(args, "--session-id", session)
	}
	args = append(args,
		"--restricted", "--tools", "", "--strict-mcp-config", "--mcp-config", c.mcpConfig(),
		"--allowedTools", ToolPrefix+tool,
		"--permission-mode", "dontAsk", "--permission-prompts", "none",
		"--append-system-prompt", BaseInstructions,
	)
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	return args
}

// streamEvent is one line of --output-format stream-json (the fields Orqen reads).
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Usage   struct {
		Input       int64 `json:"input_tokens"`
		CachedInput int64 `json:"cache_read_input_tokens"`
		CacheWrite  int64 `json:"cache_creation_input_tokens"`
		Output      int64 `json:"output_tokens"`
	} `json:"usage"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
	Text      string          `json:"text"`
}

func blocksOf(raw json.RawMessage) []contentBlock {
	var out []contentBlock
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// textOf joins the text of a tool_result content (a string or a list of text blocks).
func textOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var buf bytes.Buffer
	for _, b := range blocksOf(raw) {
		if b.Type == "text" {
			buf.WriteString(b.Text)
		}
	}
	return buf.String()
}

// RunDirective runs one directive as one claude turn and returns the one validated catherd call. The model's prose
// is never used. A deviation (another tool, a second call, other arguments) kills the process.
func (c *Client) RunDirective(ctx context.Context, threadID string, d codexhost.Directive, timeout time.Duration) (codexhost.ToolCall, error) {
	lock := c.threadLock(threadID)
	lock.Lock()
	defer lock.Unlock()
	c.mu.Lock()
	resume, effort, model := c.started[threadID], c.effort[threadID], c.model[threadID]
	c.mu.Unlock()
	if model == "" {
		model = c.cfg.Model
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, proc, err := c.cfg.Start(tctx, c.cfg.Bin, c.turnArgs(threadID, resume, d.Tool, d.Text, effort, model), c.cfg.Env, c.cfg.Cwd)
	if err != nil {
		return codexhost.ToolCall{}, fmt.Errorf("claudehost: claude could not be started: %w", err) // nothing was sent
	}
	defer func() { proc.Kill(); _ = stdout.Close(); _ = proc.Wait() }()
	c.mu.Lock()
	c.started[threadID] = true // the session now exists, even if this turn fails
	c.mu.Unlock()

	var (
		turnID    string
		callID    string
		call      codexhost.ToolCall
		haveCall  bool
		haveReply bool
		replyErr  bool
		replyText string
	)
	finish := func() (codexhost.ToolCall, error) {
		if !haveCall {
			return codexhost.ToolCall{}, codexhost.ErrNoToolCall
		}
		if !haveReply {
			return call, fmt.Errorf("%w: the turn ended while the tool call was running", codexhost.ErrOutcomeUnknown)
		}
		if replyErr {
			if te := codexhost.ParseToolError([]byte(replyText)); te != nil {
				return call, te
			}
			return call, &codexhost.ToolError{Code: "E_HOST_TOOL", Message: replyText}
		}
		payload, text := resultPayload(replyText)
		call.Result, call.Text = payload, text
		if te := codexhost.ParseToolError(payload); te != nil {
			return call, te
		}
		if payload == nil {
			if te := codexhost.ParseToolError([]byte(text)); te != nil {
				return call, te
			}
		}
		return call, nil
	}
	deviate := func(detail string) (codexhost.ToolCall, error) {
		proc.Kill()
		return codexhost.ToolCall{}, &codexhost.DeviationError{Detail: detail}
	}

	sc := bufio.NewReaderSize(stdout, 64<<10)
	for {
		line, rerr := readLine(sc)
		if len(bytes.TrimSpace(line)) > 0 {
			var ev streamEvent
			if json.Unmarshal(line, &ev) == nil {
				switch ev.Type {
				case "system":
					if ev.Subtype == "init" {
						var init struct {
							SessionID string `json:"session_id"`
						}
						_ = json.Unmarshal(line, &init)
						turnID = init.SessionID
					}
				case "assistant":
					for _, b := range blocksOf(ev.Message.Content) {
						if b.Type != "tool_use" {
							continue
						}
						if b.Name != ToolPrefix+d.Tool {
							return deviate("tool " + b.Name)
						}
						if haveCall && callID != b.ID {
							return deviate("a second tool call")
						}
						call = codexhost.ToolCall{ThreadID: threadID, TurnID: turnID, ItemID: b.ID, Server: "catherd", Tool: d.Tool, Arguments: b.Input}
						if !d.ArgsMatch(b.Input) {
							proc.Kill()
							return call, codexhost.ErrArgsMismatch
						}
						callID, haveCall = b.ID, true
					}
				case "user":
					for _, b := range blocksOf(ev.Message.Content) {
						if b.Type == "tool_result" && haveCall && b.ToolUseID == callID {
							haveReply, replyErr, replyText = true, b.IsError, textOf(b.Content)
						}
					}
				case "result":
					c.recordUsage(threadID, ev)
					if !haveCall && ev.IsError {
						return codexhost.ToolCall{}, fmt.Errorf("%w (%s)", codexhost.ErrNoToolCall, ev.Result)
					}
					return finish()
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	if tctx.Err() != nil || haveCall && !haveReply || !haveCall {
		// The process ended or timed out without a result event: whatever it did is unknown.
		return call, fmt.Errorf("%w: the turn did not finish", codexhost.ErrOutcomeUnknown)
	}
	return finish()
}

func (c *Client) recordUsage(thread string, ev streamEvent) {
	u := ev.Usage
	c.mu.Lock()
	defer c.mu.Unlock()
	cur := c.usage[thread]
	cur.Input += u.Input + u.CacheWrite
	cur.CachedInput += u.CachedInput
	cur.Output += u.Output
	cur.Total = cur.Input + cur.CachedInput + cur.Output
	c.usage[thread] = cur
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		out = append(out, chunk...)
		if err != nil || !isPrefix {
			return out, err
		}
	}
}

// ExecStart starts the real claude CLI as its own process group.
func ExecStart(ctx context.Context, bin string, args, env []string, cwd string) (io.ReadCloser, Proc, error) {
	if bin == "" {
		return nil, nil, errors.New("claude was not found")
	}
	cmd := exec.Command(bin, args...)
	cmd.Env, cmd.Dir = env, cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, nil, err
	}
	p := &execProc{cmd: cmd}
	go func() { <-ctx.Done(); p.Kill() }()
	return stdout, p, nil
}

type execProc struct {
	cmd  *exec.Cmd
	once sync.Once
	err  error
}

func (p *execProc) Kill() {
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
}

func (p *execProc) Wait() error { p.once.Do(func() { p.err = p.cmd.Wait() }); return p.err }
