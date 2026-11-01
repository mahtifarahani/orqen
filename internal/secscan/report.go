package secscan

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"orqen/internal/contextpkt"
	"orqen/internal/extensions"
)

//go:embed schema/v1/*.json
var schemaFS embed.FS

// SchemaFile returns the embedded report schema.
func SchemaFile(name string) ([]byte, error) { return schemaFS.ReadFile("schema/v1/" + name + ".json") }

// Report statuses.
const (
	StatusCompleted      = "completed"
	StatusBudgetExceeded = "budget_exceeded"
	StatusMaxTurns       = "max_turns"
	StatusError          = "error"
)

// Finding is one finding of the report, scrubbed and clipped.
type Finding struct {
	Title       string `json:"title"`
	Severity    string `json:"severity"`
	CWE         string `json:"cwe,omitempty"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Description string `json:"description,omitempty"`
	Proof       string `json:"proof,omitempty"`
}

// Report is a parsed report.json.
type Report struct {
	Status string `json:"status"`
	Scope  struct {
		Target string `json:"target"`
	} `json:"scope"`
	Turns    int       `json:"turns,omitempty"`
	CostUSD  float64   `json:"cost_usd,omitempty"`
	Findings []Finding `json:"findings"`
}

// Limits of stored finding text.
const (
	MaxTitle = 200
	MaxBody  = 4000
)

const secretMark = "[secret]"

// Redact removes the known secret values and the secret-looking patterns from text.
func Redact(s string, known []string) string {
	for _, k := range known {
		if len(k) >= 4 {
			s = strings.ReplaceAll(s, k, secretMark)
		}
	}
	return contextpkt.Scrub(s)
}

func clip(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// Parse validates raw against the report schema and returns the report with every text field redacted (known secret
// values and patterns) and clipped. The raw bytes the caller stores should be the redacted JSON of the result.
func Parse(raw []byte, known []string) (Report, error) {
	var r Report
	sb, err := SchemaFile("report")
	if err != nil {
		return r, err
	}
	if err = extensions.ValidateJSON(sb, raw); err != nil {
		return r, fmt.Errorf("report: %w", err)
	}
	if err = json.Unmarshal(raw, &r); err != nil {
		return r, fmt.Errorf("report: %w", err)
	}
	for i := range r.Findings {
		f := &r.Findings[i]
		f.Title = clip(strings.TrimSpace(Redact(f.Title, known)), MaxTitle)
		f.Description = clip(Redact(f.Description, known), MaxBody)
		f.Proof = clip(Redact(f.Proof, known), MaxBody)
		f.File = strings.TrimPrefix(Redact(f.File, known), "./")
	}
	return r, nil
}

// FindingKey identifies a finding across scans of the same run: sha256 of cwe, file, line and title.
func FindingKey(f Finding) string {
	sum := sha256.Sum256([]byte(f.CWE + "\x00" + f.File + "\x00" + strconv.Itoa(f.Line) + "\x00" + f.Title))
	return hex.EncodeToString(sum[:])
}
