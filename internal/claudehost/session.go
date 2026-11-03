package claudehost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"orqen/internal/codexhost"
)

// resultPayload returns the JSON parsed from a tool result text, else the text itself.
func resultPayload(text string) (json.RawMessage, string) {
	t := strings.TrimSpace(text)
	if t == "" {
		return nil, ""
	}
	if json.Valid([]byte(t)) {
		return json.RawMessage(t), ""
	}
	return nil, text
}

// transcriptPath finds the session's transcript below the isolated config directory.
func (c *Client) transcriptPath(id string) (string, bool) {
	if c.cfg.ConfigDir == "" || strings.ContainsAny(id, `/\`) || id == "" {
		return "", false
	}
	m, _ := filepath.Glob(filepath.Join(c.cfg.ConfigDir, "projects", "*", id+".jsonl"))
	if len(m) == 0 {
		return "", false
	}
	sort.Strings(m)
	return m[0], true
}

// history reads the session transcript as Codex-shaped turns, so the run engine reconciles a lost directive the same
// way on both hosts. A missing session is an *RPCError (the thread is gone).
func (c *Client) history(id string) ([]codexhost.Turn, error) {
	p, ok := c.transcriptPath(id)
	if !ok {
		return nil, &codexhost.RPCError{Code: -32600, Message: "claude session not found"}
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, &codexhost.RPCError{Code: -32600, Message: "claude session not readable"}
	}
	defer f.Close()
	return turnsFrom(f)
}

type transcriptLine struct {
	Type    string `json:"type"`
	UUID    string `json:"uuid"`
	Message struct {
		StopReason string          `json:"stop_reason"`
		Content    json.RawMessage `json:"content"`
	} `json:"message"`
}

// turnsFrom turns a session transcript into turns: a turn opens at each user text message; tool_use blocks become
// mcpToolCall items of the catherd server and are completed by their tool_result.
func turnsFrom(r io.Reader) ([]codexhost.Turn, error) {
	var (
		turns []codexhost.Turn
		items = map[string]*codexhost.Item{} // tool_use id -> item (points into turns)
		ended bool                           // the last assistant message ended the turn
	)
	rd := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := readLine(rd)
		if len(bytes.TrimSpace(line)) > 0 {
			var tl transcriptLine
			if json.Unmarshal(line, &tl) == nil {
				switch tl.Type {
				case "user":
					var s string
					if json.Unmarshal(tl.Message.Content, &s) == nil {
						turns = append(turns, userTurn(tl.UUID, s))
						ended = false
						break
					}
					for _, b := range blocksOf(tl.Message.Content) {
						switch b.Type {
						case "text":
							turns = append(turns, userTurn(tl.UUID, b.Text))
							ended = false
						case "tool_result":
							if it := items[b.ToolUseID]; it != nil {
								text := textOf(b.Content)
								if b.IsError {
									it.Status = "failed"
									it.Error = &struct {
										Message string `json:"message"`
									}{Message: text}
								} else {
									it.Status = "completed"
									raw, _ := json.Marshal(map[string]string{"type": "text", "text": text})
									it.Result = &codexhost.McpResult{Content: []json.RawMessage{raw}}
								}
							}
						}
					}
				case "assistant":
					if len(turns) == 0 {
						break
					}
					t := &turns[len(turns)-1]
					for _, b := range blocksOf(tl.Message.Content) {
						if b.Type == "tool_use" && strings.HasPrefix(b.Name, ToolPrefix) {
							t.Items = append(t.Items, codexhost.Item{Type: "mcpToolCall", ID: b.ID, Server: "catherd", Tool: strings.TrimPrefix(b.Name, ToolPrefix), Status: "inProgress", Arguments: b.Input})
						}
					}
					items = map[string]*codexhost.Item{}
					for i := range t.Items {
						items[t.Items[i].ID] = &t.Items[i]
					}
					ended = tl.Message.StopReason == "end_turn"
				}
			}
		}
		if err != nil {
			break
		}
	}
	for i := range turns {
		turns[i].Status = "completed"
	}
	if n := len(turns); n > 0 && !ended {
		turns[n-1].Status = "inProgress"
	}
	if turns == nil {
		turns = []codexhost.Turn{}
	}
	return turns, nil
}

func userTurn(id, text string) codexhost.Turn {
	var it codexhost.Item
	it.Type, it.ID = "userMessage", id
	it.Content = append(it.Content, struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "text", Text: text})
	return codexhost.Turn{ID: id, Items: []codexhost.Item{it}}
}

var versionRE = regexp.MustCompile(`\d+\.\d+\.\d+`)

// Probe reads the host's version, login state and the catherd tools. It sends no prompt: no turn, no model call.
func (c *Client) Probe(ctx context.Context) (codexhost.ProbeResult, error) {
	out := codexhost.ProbeResult{CodexHome: c.cfg.ConfigDir}
	ver, err := c.cfg.Output(ctx, c.cfg.Bin, []string{"--version"}, c.cfg.Env)
	if err != nil {
		return out, fmt.Errorf("claudehost: claude --version: %w", err)
	}
	if v := versionRE.FindString(string(ver)); v != "" {
		out.UserAgent = "claude-code/" + v
	}
	if raw, aerr := c.cfg.Output(ctx, c.cfg.Bin, []string{"auth", "status"}, c.cfg.Env); aerr == nil || len(raw) > 0 {
		var st struct {
			LoggedIn   bool   `json:"loggedIn"`
			AuthMethod string `json:"authMethod"`
		}
		if json.Unmarshal(raw, &st) == nil {
			out.LoggedIn, out.AccountType, out.RequiresAuth = st.LoggedIn, st.AuthMethod, !st.LoggedIn
		}
	}
	tools, err := c.cfg.ListTools(ctx, c.cfg.CatherdBin, c.catherdEnv())
	if err != nil {
		return out, fmt.Errorf("claudehost: catherd mcp: %w", err)
	}
	sort.Strings(tools)
	out.MCPServers = []codexhost.MCPServer{{Name: "catherd", Tools: tools}}
	return out, nil
}

func (c *Client) catherdEnv() []string {
	env := append([]string(nil), c.cfg.Env...)
	keys := make([]string, 0, len(c.cfg.CatherdEnv))
	for k := range c.cfg.CatherdEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+c.cfg.CatherdEnv[k])
	}
	return env
}

func execOutput(ctx context.Context, bin string, args, env []string) ([]byte, error) {
	if bin == "" {
		return nil, fmt.Errorf("claude was not found")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	return cmd.Output()
}

// mcpTools starts `catherd mcp`, performs the MCP handshake over stdio and lists its tools.
func mcpTools(ctx context.Context, bin string, env []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "mcp")
	cmd.Env = env
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	send := func(v any) error {
		b, _ := json.Marshal(v)
		_, e := in.Write(append(b, '\n'))
		return e
	}
	rd := bufio.NewReaderSize(out, 64<<10)
	await := func(id int) (json.RawMessage, error) {
		for {
			line, e := readLine(rd)
			var m struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if len(bytes.TrimSpace(line)) > 0 && json.Unmarshal(line, &m) == nil && m.ID != nil && *m.ID == id {
				if m.Error != nil {
					return nil, fmt.Errorf("%s", m.Error.Message)
				}
				return m.Result, nil
			}
			if e != nil {
				return nil, e
			}
		}
	}
	if err = send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "orqen", "version": "1"}}}); err != nil {
		return nil, err
	}
	if _, err = await(1); err != nil {
		return nil, err
	}
	if err = send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		return nil, err
	}
	if err = send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}); err != nil {
		return nil, err
	}
	raw, err := await(2)
	if err != nil {
		return nil, err
	}
	var res struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err = json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(res.Tools))
	for _, t := range res.Tools {
		names = append(names, t.Name)
	}
	return names, nil
}
