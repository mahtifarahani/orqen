package codegraph

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// The deterministic engine behind Fake and the fake CLI. The tree's files declare their edges with a line
// `// imports: a.go, b.go` (or `# imports: ...`) and their symbols with `// symbol: Name`.

type fileEntry struct {
	Imports []string `json:"imports"`
	Symbols []string `json:"symbols"`
}

type graphFile struct {
	Files map[string]fileEntry `json:"files"`
}

var (
	importsRe = regexp.MustCompile(`^\s*(?://|#)\s*imports:\s*(.+)$`)
	symbolRe  = regexp.MustCompile(`^\s*(?://|#)\s*symbol:\s*([A-Za-z_][A-Za-z0-9_.]*)\s*$`)
)

const indexName = "index.json"

func analyzeTree(tree, out string) (AnalyzeResult, error) {
	g := graphFile{Files: map[string]fileEntry{}}
	var bytes, symbols int
	err := filepath.Walk(tree, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !info.Mode().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(tree, p)
		rel = filepath.ToSlash(rel)
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		e := fileEntry{Imports: []string{}, Symbols: []string{}}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if m := importsRe.FindStringSubmatch(sc.Text()); m != nil {
				for _, i := range strings.Split(m[1], ",") {
					if i = strings.TrimSpace(i); i != "" {
						e.Imports = append(e.Imports, i)
					}
				}
			}
			if m := symbolRe.FindStringSubmatch(sc.Text()); m != nil {
				e.Symbols = append(e.Symbols, m[1])
				symbols++
			}
		}
		sort.Strings(e.Imports)
		sort.Strings(e.Symbols)
		g.Files[rel] = e
		bytes += int(info.Size())
		return nil
	})
	if err != nil {
		return AnalyzeResult{}, err
	}
	if err = os.MkdirAll(out, 0o700); err != nil {
		return AnalyzeResult{}, err
	}
	raw, _ := json.Marshal(g)
	if err = os.WriteFile(filepath.Join(out, indexName), raw, 0o600); err != nil {
		return AnalyzeResult{}, err
	}
	return AnalyzeResult{Files: len(g.Files), Symbols: symbols, Bytes: bytes}, nil
}

func loadGraph(index string) (graphFile, error) {
	var g graphFile
	raw, err := os.ReadFile(filepath.Join(index, indexName))
	if err != nil {
		return g, err
	}
	return g, json.Unmarshal(raw, &g)
}

func targetFiles(g graphFile, target, kind string) []string {
	if kind == KindFile {
		if _, ok := g.Files[target]; ok {
			return []string{target}
		}
		return nil
	}
	var out []string
	for p, e := range g.Files {
		for _, s := range e.Symbols {
			if s == target {
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

func impactOf(index, target, kind string, depth, limit int) (ImpactResult, error) {
	g, err := loadGraph(index)
	if err != nil {
		return ImpactResult{}, err
	}
	res := ImpactResult{Target: target, Items: []Impacted{}}
	roots := targetFiles(g, target, kind)
	seen := map[string]bool{}
	for _, r := range roots {
		seen[r] = true
	}
	var all []Impacted
	// direct imports of the target
	for _, r := range roots {
		for _, i := range g.Files[r].Imports {
			if !seen[i] {
				all = append(all, Impacted{Path: i, Relation: RelImports, Depth: 1})
			}
		}
	}
	frontier := roots
	for d := 1; d <= depth; d++ {
		var next []string
		for p, e := range g.Files {
			if seen[p] {
				continue
			}
			for _, i := range e.Imports {
				for _, f := range frontier {
					if i == f {
						seen[p] = true
						all = append(all, Impacted{Path: p, Relation: RelImportedBy, Depth: d})
						next = append(next, p)
					}
				}
			}
		}
		frontier = next
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Depth != all[j].Depth {
			return all[i].Depth < all[j].Depth
		}
		if all[i].Relation != all[j].Relation {
			return all[i].Relation < all[j].Relation
		}
		return all[i].Path < all[j].Path
	})
	if len(all) > limit {
		all, res.Truncated = all[:limit], true
	}
	res.Items = append(res.Items, all...)
	return res, nil
}

func contextOf(index, target string, limit int) (ContextResult, error) {
	g, err := loadGraph(index)
	if err != nil {
		return ContextResult{}, err
	}
	e := g.Files[target]
	res := ContextResult{Target: target, Imports: []string{}, ImportedBy: []string{}, Symbols: append([]string{}, e.Symbols...)}
	res.Imports = append(res.Imports, e.Imports...)
	for p, o := range g.Files {
		for _, i := range o.Imports {
			if i == target {
				res.ImportedBy = append(res.ImportedBy, p)
			}
		}
	}
	sort.Strings(res.ImportedBy)
	clip := func(in []string) []string {
		if len(in) > limit {
			return in[:limit]
		}
		return in
	}
	res.Imports, res.ImportedBy, res.Symbols = clip(res.Imports), clip(res.ImportedBy), clip(res.Symbols)
	return res, nil
}

// Fake is the in-process Index for tests. It uses the same engine as the fake CLI, so its answers validate against
// schema/v1. Failure injection: FailVerb (version, analyze, impact, context) and WrongSchema.
type Fake struct {
	mu          sync.Mutex
	FailVerb    string
	WrongSchema bool
	VersionText string
	Calls       []string
}

var _ Index = (*Fake)(nil)

func (f *Fake) enter(verb string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, verb)
	if f.FailVerb == verb {
		return errf(KindFailed, verb, "scenario: %s fails", verb)
	}
	if f.WrongSchema {
		return errf(KindInvalidOutput, verb, "scenario: wrong schema")
	}
	return nil
}

func (f *Fake) Version(ctx context.Context) (Health, error) {
	if err := f.enter("version"); err != nil {
		return Health{}, err
	}
	v := f.VersionText
	if v == "" {
		v = "fake-1.0"
	}
	return Health{Version: v, Schema: "v1"}, nil
}

func (f *Fake) Analyze(ctx context.Context, tree, out string) (AnalyzeResult, error) {
	if err := f.enter("analyze"); err != nil {
		return AnalyzeResult{}, err
	}
	return analyzeTree(tree, out)
}

func (f *Fake) Impact(ctx context.Context, index, target, kind string, depth, limit int) (ImpactResult, error) {
	if err := f.enter("impact"); err != nil {
		return ImpactResult{}, err
	}
	if !ValidTarget(kind, target) {
		return ImpactResult{}, errf(KindBadPath, "impact", "invalid %s target", kind)
	}
	return impactOf(index, target, kind, depth, limit)
}

func (f *Fake) Context(ctx context.Context, index, target string, limit int) (ContextResult, error) {
	if err := f.enter("context"); err != nil {
		return ContextResult{}, err
	}
	return contextOf(index, target, limit)
}
