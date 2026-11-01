package kindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// engine is the deterministic wiki used by both Fake and the fake CLI: it is the executable spec of the assumed
// contract (ingest syncs the input directory, search ranks by token overlap, get caps the text).
type engine struct {
	Docs  map[string]fakeDoc `json:"docs"`
	Synth map[string]string  `json:"synth"`
	// Writes counts document (re)writes: ingest is idempotent on (id, content_hash), so an unchanged document is not
	// a write.
	Writes int `json:"writes"`
}
type fakeDoc struct {
	ID    string `json:"id"`
	Hash  string `json:"hash"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

func newEngine() *engine { return &engine{Docs: map[string]fakeDoc{}, Synth: map[string]string{}} }

func parseFrontMatter(b []byte) (map[string]string, string) {
	s := string(b)
	meta := map[string]string{}
	if !strings.HasPrefix(s, "---\n") {
		return meta, s
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return meta, s
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		i := strings.Index(line, ": ")
		if i < 0 {
			continue
		}
		var v string
		if json.Unmarshal([]byte(line[i+2:]), &v) != nil {
			v = line[i+2:]
		}
		meta[line[:i]] = v
	}
	return meta, rest[end+5:]
}

func readDocs(dir string) (docs []fakeDoc, rejected []Doc, err error) {
	err = filepath.Walk(dir, func(p string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() || !strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".tmp") {
			return werr
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		meta, body := parseFrontMatter(b)
		if meta["id"] == "" || meta["content_hash"] == "" {
			rejected = append(rejected, Doc{ID: filepath.Base(p), Reason: "missing id or content_hash"})
			return nil
		}
		docs = append(docs, fakeDoc{ID: meta["id"], Hash: meta["content_hash"], Title: meta["title"], Text: body})
		return nil
	})
	sort.Slice(docs, func(i, j int) bool { return docs[i].ID < docs[j].ID })
	return
}

func (e *engine) ingest(dir string) (IngestResult, error) {
	docs, rejected, err := readDocs(dir)
	if err != nil {
		return IngestResult{}, err
	}
	res := IngestResult{Accepted: []Doc{}, Rejected: append([]Doc{}, rejected...)}
	if res.Rejected == nil {
		res.Rejected = []Doc{}
	}
	seen := map[string]bool{}
	for _, d := range docs {
		seen[d.ID] = true
		if cur, ok := e.Docs[d.ID]; !ok || cur.Hash != d.Hash {
			e.Docs[d.ID] = d
			e.Writes++
		}
		res.Accepted = append(res.Accepted, Doc{ID: d.ID, Hash: d.Hash})
	}
	// Only knowledge documents are synced; scope index pages live under synthesis.
	for id := range e.Docs {
		if !seen[id] {
			delete(e.Docs, id)
			e.Writes++
		}
	}
	return res, nil
}

func (e *engine) synthesis(file string) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	meta, _ := parseFrontMatter(b)
	h := sha256.Sum256(b)
	e.Synth[meta["id"]] = hex.EncodeToString(h[:])
	return nil
}

func (e *engine) compile() CompileResult {
	ids := make([]string, 0, len(e.Docs))
	for id := range e.Docs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(id + ":" + e.Docs[id].Hash + "\n"))
	}
	return CompileResult{Pages: len(ids) + len(e.Synth), Hash: hex.EncodeToString(h.Sum(nil))}
}

func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func (e *engine) search(q string, limit int) []Hit {
	qt := tokens(q)
	hits := []Hit{}
	if len(qt) == 0 {
		return hits
	}
	for _, d := range e.Docs {
		title, text := map[string]bool{}, map[string]bool{}
		for _, t := range tokens(d.Title) {
			title[t] = true
		}
		for _, t := range tokens(d.Text) {
			text[t] = true
		}
		score := 0.0
		for _, t := range qt {
			if title[t] {
				score += 2
			}
			if text[t] {
				score++
			}
		}
		if score > 0 {
			snip := strings.TrimSpace(d.Text)
			if len(snip) > 160 {
				snip = snip[:160]
			}
			hits = append(hits, Hit{ID: d.ID, Score: score / float64(len(qt)), Snippet: snip})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func (e *engine) get(id string, maxBytes int) (Slice, bool) {
	d, ok := e.Docs[id]
	if !ok {
		return Slice{}, false
	}
	text := d.Text
	if maxBytes > 0 && len(text) > maxBytes {
		text = text[:maxBytes]
		for len(text) > 0 && !validUTF8Tail(text) {
			text = text[:len(text)-1]
		}
	}
	return Slice{ID: d.ID, ContentHash: d.Hash, Text: text}, true
}

func validUTF8Tail(s string) bool {
	for i, r := range s {
		if r == unicode.ReplacementChar && len(s)-i < 4 {
			return false
		}
	}
	return true
}

// Fake is the in-memory KnowledgeIndex for tests, with failure injection per verb.
type Fake struct {
	mu sync.Mutex
	// Down makes every verb fail as unavailable.
	Down bool
	// Fail makes one verb (by name) fail with the given error.
	Fail map[string]error
	// LintErrors is the number of synthetic lint errors.
	LintErrors int
	// Before runs before every verb (after the failure checks); tests use it to interleave actors.
	Before func(verb string)
	// SearchOverride replaces the ranking (used to simulate a foreign-org hit).
	SearchOverride func(q string) []Hit
	// Calls lists the verbs in call order.
	Calls []string

	state map[string]*engine
}

var _ Index = (*Fake)(nil)

func NewFake() *Fake { return &Fake{Fail: map[string]error{}, state: map[string]*engine{}} }

func (f *Fake) enter(verb string, v Vault) (*engine, error) {
	f.mu.Lock()
	hook := f.Before
	f.Calls = append(f.Calls, verb)
	down, ferr := f.Down, f.Fail[verb]
	f.mu.Unlock()
	if hook != nil {
		hook(verb)
	}
	if down {
		return nil, errf(KindUnavailable, verb, "fake wiki is down")
	}
	if ferr != nil {
		return nil, ferr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == nil {
		f.state = map[string]*engine{}
	}
	if f.state[v.Dir] == nil {
		f.state[v.Dir] = newEngine()
	}
	return f.state[v.Dir], nil
}

func (f *Fake) Health(_ context.Context, v Vault) (Health, error) {
	if _, err := f.enter("version", v); err != nil {
		return Health{}, err
	}
	return Health{Version: "fake-1.0", Schema: "v1"}, nil
}
func (f *Fake) Ingest(_ context.Context, v Vault, dir string) (IngestResult, error) {
	e, err := f.enter("ingest", v)
	if err != nil {
		return IngestResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return e.ingest(dir)
}
func (f *Fake) RegisterSynthesis(_ context.Context, v Vault, file string) error {
	e, err := f.enter("synthesis", v)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return e.synthesis(file)
}
func (f *Fake) Compile(_ context.Context, v Vault) (CompileResult, error) {
	e, err := f.enter("compile", v)
	if err != nil {
		return CompileResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return e.compile(), nil
}
func (f *Fake) Lint(_ context.Context, v Vault) (LintResult, error) {
	if _, err := f.enter("lint", v); err != nil {
		return LintResult{}, err
	}
	r := LintResult{Errors: []Issue{}, Warnings: []Issue{}}
	for i := 0; i < f.LintErrors; i++ {
		r.Errors = append(r.Errors, Issue{Path: "knowledge/x.md", Code: "lint_error", Message: "synthetic lint error"})
	}
	return r, nil
}
func (f *Fake) Search(_ context.Context, v Vault, q string, limit int) ([]Hit, error) {
	e, err := f.enter("search", v)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SearchOverride != nil {
		return f.SearchOverride(q), nil
	}
	return e.search(q, limit), nil
}
func (f *Fake) Get(_ context.Context, v Vault, id string, maxBytes int) (Slice, error) {
	e, err := f.enter("get", v)
	if err != nil {
		return Slice{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := e.get(id, maxBytes)
	if !ok {
		return Slice{}, errf(KindFailed, "get", "no such document")
	}
	return s, nil
}

// Docs returns the sorted ids currently indexed for a vault (tests).
func (f *Fake) Docs(v Vault) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	if e := f.state[v.Dir]; e != nil {
		for id := range e.Docs {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// Writes returns the number of document writes for a vault (idempotency tests).
func (f *Fake) Writes(v Vault) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.state[v.Dir]; e != nil {
		return e.Writes
	}
	return 0
}
