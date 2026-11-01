package codexhost

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"orqen/internal/canon"
	"sort"
	"strings"
)

// BaseInstructions are given to every orchestrator thread.
const BaseInstructions = `You are the hands of a program called Orqen. Orqen sends you directives. Each directive tells you to call exactly one tool of the "catherd" MCP server with exact arguments.
Rules:
- Call exactly the one tool the directive names, with exactly the given ARGS, and nothing else.
- Never run a shell command, never edit a file, never search the web, never call another tool.
- After the tool returns, reply exactly: DONE <directive id>.
- If a catherd message arrives that is not a directive, reply ACK and call nothing.`

// Directive is one instruction for one catherd tool call.
type Directive struct {
	ID   string
	Tool string
	Args map[string]any
	Text string
}

// NewDirective builds a directive with a fresh id and the canonical argument text.
func NewDirective(tool string, args map[string]any) Directive {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return NewDirectiveWithID("dir_"+hex.EncodeToString(b[:]), tool, args)
}

// NewDirectiveWithID is NewDirective with a given id (replays and tests).
func NewDirectiveWithID(id, tool string, args map[string]any) Directive {
	if args == nil {
		args = map[string]any{}
	}
	text := fmt.Sprintf("ORQEN-DIRECTIVE v1 id=%s\nCall exactly one tool: catherd.%s\nARGS %s\nCall no other tool and run no command. Afterwards reply exactly: DONE %s", id, tool, canon.JSON(args), id)
	return Directive{ID: id, Tool: tool, Args: args, Text: text}
}

// ArgsHash is a stable fingerprint of the arguments for storage (never the arguments themselves).
func (d Directive) ArgsHash() string { return canon.JSON(d.Args) }

func canonRaw(raw json.RawMessage) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	return canon.JSON(v), nil
}

// argsMatch compares the directive's arguments with what the host sent.
func (d Directive) argsMatch(raw json.RawMessage) bool {
	got, err := canonRaw(raw)
	if err != nil {
		return false
	}
	return got == canon.JSON(d.Args)
}

// TurnForDirective finds the turn whose user message carries the directive id.
func TurnForDirective(turns []Turn, id string) (Turn, bool) {
	needle := "id=" + id
	for _, t := range turns {
		for _, it := range t.Items {
			if it.Type != "userMessage" {
				continue
			}
			for _, c := range it.Content {
				if strings.Contains(c.Text, needle) {
					return t, true
				}
			}
		}
	}
	return Turn{}, false
}

// ToolCallsOf lists the completed catherd calls of a turn, sorted by item id for stability.
func ToolCallsOf(t Turn) []Item {
	var out []Item
	for _, it := range t.Items {
		if it.Type == "mcpToolCall" && it.Server == "catherd" && it.Status == "completed" {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
