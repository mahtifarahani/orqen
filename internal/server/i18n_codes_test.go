package server

import (
	"encoding/json"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"orqen/internal/kauthor"
	"orqen/internal/knowledge"
	"orqen/internal/policy"
	"orqen/internal/runstate"
	"orqen/internal/secscan"
)

var updateCodes = flag.Bool("update", false, "rewrite web/src/test/fixtures/server-codes.json")

const codesFixture = "../../web/src/test/fixtures/server-codes.json"

// constStrings returns the string values of the constants of a Go file whose names start with prefix.
func constStrings(t *testing.T, file, prefix string) []string {
	t.Helper()
	tree, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	ast.Inspect(tree, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if strings.HasPrefix(name.Name, prefix) && i < len(vs.Values) {
				if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
					s, _ := strconv.Unquote(bl.Value)
					out = append(out, s)
				}
			}
		}
		return true
	})
	return out
}

// problemCodes collects every problem code the server can answer: the switch of problem(), problem("x") calls and
// AppError{Code: "x"} literals in non-test files.
func problemCodes(t *testing.T) []string {
	t.Helper()
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	set := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		tree, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		str := func(e ast.Expr) {
			if bl, ok := e.(*ast.BasicLit); ok && bl.Kind == token.STRING {
				if s, err := strconv.Unquote(bl.Value); err == nil && regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(s) {
					set[s] = true
				}
			}
		}
		ast.Inspect(tree, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				if x.Name.Name == "problem" && f == "errors.go" {
					ast.Inspect(x.Body, func(m ast.Node) bool {
						if cc, ok := m.(*ast.CaseClause); ok {
							for _, e := range cc.List {
								str(e)
							}
						}
						return true
					})
				}
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "problem" && len(x.Args) == 1 {
					str(x.Args[0])
				}
			case *ast.CompositeLit:
				if id, ok := x.Type.(*ast.Ident); ok && id.Name == "AppError" {
					for _, el := range x.Elts {
						if kv, ok := el.(*ast.KeyValueExpr); ok {
							if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Code" {
								str(kv.Value)
							}
						}
					}
				}
			}
			return true
		})
	}
	delete(set, "ok")
	return keysOf(set)
}

// readinessCodes collects the first argument of block("…") and warn("…") calls of the readiness evaluator.
func readinessCodes(t *testing.T) []string {
	t.Helper()
	tree, err := parser.ParseFile(token.NewFileSet(), "../readiness/evaluate.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	ast.Inspect(tree, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && (id.Name == "block" || id.Name == "warn") && len(c.Args) > 0 {
				if bl, ok := c.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
					s, _ := strconv.Unquote(bl.Value)
					set[s] = true
				}
			}
		}
		return true
	})
	return keysOf(set)
}

func keysOf(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// i18nKeys builds the list of translation keys the web needs for every server-defined code (Y19).
func i18nKeys(t *testing.T) []string {
	set := map[string]bool{}
	add := func(prefix string, codes ...string) {
		for _, c := range codes {
			set[prefix+c] = true
		}
	}
	add("problems.", problemCodes(t)...)
	add("extensions.unusable.", constStrings(t, "../extensions/permit.go", "Reason")...)
	for _, k := range policy.Registry {
		set["policy.keys."+strings.ReplaceAll(k.Key, ".", "_")+".label"] = true
		set["policy.keys."+strings.ReplaceAll(k.Key, ".", "_")+".help"] = true
		add("policy.values.", k.Enum...)
	}
	add("run.hold.", runstate.Holds...)
	add("context.excluded.", constStrings(t, "../contextpkt/build.go", "Reason")...)
	add("context.excluded.", "index_stale", "index_missing")
	add("skills.excluded.", constStrings(t, "../skills/resolve.go", "Reason")...)
	add("knowledgePage.neverAuto.", knowledge.AllNeverAuto...)
	add("readiness.reasons.", readinessCodes(t)...)
	for _, c := range secscan.Completions {
		set["security.completion."+string(c)] = true
	}
	add("codeGraph.state.", "queued", "indexing", "ready", "failed", "license_revoked", "removed")
	add("knowledgePage.author.state.", kauthor.States...)
	add("knowledgePage.author.code.", kauthor.Codes...)
	return keysOf(set)
}

// TestI18nCodeCensus keeps the list of translation keys the web must have in step with the server's codes. The golden
// file is read by web/src/test/server-codes.test.ts, which checks every key exists in the en and fa trees.
func TestI18nCodeCensus(t *testing.T) {
	keys := i18nKeys(t)
	if len(keys) < 150 {
		t.Fatalf("the census found only %d keys", len(keys))
	}
	want, _ := json.MarshalIndent(map[string]any{"keys": keys}, "", "  ")
	want = append(want, '\n')
	if *updateCodes {
		if err := os.MkdirAll(filepath.Dir(codesFixture), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(codesFixture, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(codesFixture)
	if err != nil || string(got) != string(want) {
		t.Fatal("web/src/test/fixtures/server-codes.json is out of date: run go test ./internal/server -run TestI18nCodeCensus -update")
	}
}
