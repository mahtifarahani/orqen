package kindex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"orqen/internal/contextpkt"
)

// DocIn is one verified knowledge version as projected into the vault.
type DocIn struct {
	OrgID, RecordID, VersionID, Type, ScopeKey string
	Title, Body                                string
	Claims                                     []string // supported claim texts
	ContentHash, VerifiedAt                    string
	Pinned, Critical                           bool
}

func q(s string) string { b, _ := json.Marshal(s); return string(b) }

func scrub(s string, secrets []string) string {
	s = contextpkt.Scrub(s)
	for _, sec := range secrets {
		if len(sec) >= 6 {
			s = strings.ReplaceAll(s, sec, "[secret]")
		}
	}
	return s
}

// RenderDoc renders the projection file: a front matter block (each value a JSON string, valid YAML) and a body. Text is
// scrubbed of secrets and known connection secrets. Source text never appears here, only claim-backed knowledge.
func RenderDoc(d DocIn, secrets []string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\norg_id: %s\nrecord_id: %s\ntype: %s\nscope: %s\ntitle: %s\ncontent_hash: %s\nverified_at: %s\npinned: %v\ncritical: %v\n",
		q(d.VersionID), q(d.OrgID), q(d.RecordID), q(d.Type), q(d.ScopeKey), q(scrub(d.Title, secrets)), q(d.ContentHash), q(d.VerifiedAt), d.Pinned, d.Critical)
	b.WriteString("---\n")
	b.WriteString("# " + scrub(d.Title, secrets) + "\n\n")
	if d.Body != "" {
		b.WriteString(scrub(d.Body, secrets) + "\n\n")
	}
	if len(d.Claims) > 0 {
		b.WriteString("## Claims\n")
		for _, c := range d.Claims {
			b.WriteString("- " + scrub(c, secrets) + "\n")
		}
	}
	return b.String()
}

// DocRelPath is the file of a version inside the projection directory.
func DocRelPath(versionID string) string { return filepath.Join("knowledge", versionID+".md") }

// ScopeIndexEntry is one line of an Orqen-authored scope index page (deterministic, no model).
type ScopeIndexEntry struct{ VersionID, Title, Type string }

// RenderScopeIndex renders the synthesis page of one scope. Entries are sorted so the output is stable.
func RenderScopeIndex(orgID, scopeKey string, es []ScopeIndexEntry, secrets []string) string {
	sorted := append([]ScopeIndexEntry{}, es...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].VersionID < sorted[j].VersionID })
	var b strings.Builder
	fmt.Fprintf(&b, "---\nid: %s\norg_id: %s\nscope: %s\nkind: scope_index\n---\n# Scope %s\n\n", q("scope:"+scopeKey), q(orgID), q(scopeKey), scopeKey)
	for _, e := range sorted {
		fmt.Fprintf(&b, "- [%s] %s (%s)\n", e.Type, scrub(e.Title, secrets), e.VersionID)
	}
	return b.String()
}

// ScopeIndexRelPath is the file of a scope index page.
func ScopeIndexRelPath(scopeKey string) string {
	return filepath.Join("scopes", strings.NewReplacer(":", "_", "/", "_").Replace(scopeKey)+".md")
}

// WriteAtomic writes a file below base (tmp then rename).
func WriteAtomic(base, rel, content string) error {
	p := filepath.Join(base, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
