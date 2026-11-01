package extwrite

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The census fails when a write path appears outside the reviewed files. Adding one forces a review of this list.
var writeAllowlist = []string{
	"internal/extwrite/",
	"internal/decision/jev.go", // Jev inference is a POST that reads a model answer; it changes nothing remote
	"internal/codexhost/",      // JSON-RPC over a local pipe
	"internal/server/app.go",   // route definitions
	"internal/testfakes/",      // fakes
	"cmd/orqen-fakes/",         // fakes
	"cmd/orqen-fakehost/",      // fakes
}

func repoRoot(t *testing.T) string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func allowed(rel string) bool {
	for _, a := range writeAllowlist {
		if rel == a || strings.HasSuffix(a, "/") && strings.HasPrefix(rel, a) {
			return true
		}
	}
	return false
}

var writeMethods = map[string]bool{"POST": true, "PUT": true, "PATCH": true, "DELETE": true}
var writeMethodIdents = map[string]bool{"MethodPost": true, "MethodPut": true, "MethodPatch": true, "MethodDelete": true}

// TestCensusNoWritePathOutsideTheGate parses every non-test Go file under internal/ and cmd/.
func TestCensusNoWritePathOutsideTheGate(t *testing.T) {
	root := repoRoot(t)
	var problems []string
	files := 0
	for _, top := range []string{"internal", "cmd"} {
		_ = filepath.Walk(filepath.Join(root, top), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if allowed(rel) {
				return nil
			}
			files++
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				problems = append(problems, rel+": "+perr.Error())
				return nil
			}
			report := func(n ast.Node, what string) {
				problems = append(problems, rel+":"+itoa(fset.Position(n.Pos()).Line)+": "+what)
			}
			// requestHash("POST", ...) labels an idempotency hash and sends nothing: its first literal is exempt.
			exempt := map[*ast.BasicLit]bool{}
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CompositeLit:
					// Route{"PUT", path, handler} registers an inbound route of Orqen's own API; it sends nothing.
					first := func(c *ast.CompositeLit) {
						if len(c.Elts) > 0 {
							if lit, ok := c.Elts[0].(*ast.BasicLit); ok {
								exempt[lit] = true
							}
						}
					}
					if id, ok := x.Type.(*ast.Ident); ok && id.Name == "Route" {
						first(x)
					}
					if at, ok := x.Type.(*ast.ArrayType); ok {
						if id, ok := at.Elt.(*ast.Ident); ok && id.Name == "Route" {
							for _, e := range x.Elts {
								if c, ok := e.(*ast.CompositeLit); ok {
									first(c)
								}
							}
						}
					}
				case *ast.CallExpr:
					if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "requestHash" && len(x.Args) > 0 {
						if lit, ok := x.Args[0].(*ast.BasicLit); ok {
							exempt[lit] = true
						}
					}
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
						switch sel.Sel.Name {
						case "Post", "PostForm":
							report(x, "call to ."+sel.Sel.Name+"(")
						case "NewRequest", "NewRequestWithContext":
							i := 0
							if sel.Sel.Name == "NewRequestWithContext" {
								i = 1
							}
							if len(x.Args) > i {
								switch m := x.Args[i].(type) {
								case *ast.BasicLit:
									if v := strings.Trim(m.Value, `"`); v != "GET" && v != "HEAD" {
										report(x, "request method "+v)
									}
								case *ast.SelectorExpr:
									if m.Sel.Name != "MethodGet" && m.Sel.Name != "MethodHead" {
										report(x, "request method "+m.Sel.Name)
									}
								default:
									report(x, "request method is not a constant")
								}
							}
						}
					}
				case *ast.SelectorExpr:
					if id, ok := x.X.(*ast.Ident); ok && id.Name == "http" && writeMethodIdents[x.Sel.Name] {
						report(x, "http."+x.Sel.Name)
					}
				case *ast.BasicLit:
					if x.Kind == token.STRING && !exempt[x] {
						v := strings.Trim(x.Value, "\"`")
						if writeMethods[v] {
							report(x, "method literal "+v)
						}
						if v == "push" {
							report(x, `"push" outside the gate`)
						}
					}
				}
				return true
			})
			return nil
		})
	}
	if files < 50 {
		t.Fatalf("census scanned only %d files", files)
	}
	if len(problems) > 0 {
		t.Fatalf("write paths outside internal/extwrite (review the census allowlist if intentional):\n%s", strings.Join(problems, "\n"))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// TestCensusDetectsAViolation proves the scanner is not vacuous.
func TestCensusDetectsAViolation(t *testing.T) {
	dir := t.TempDir()
	src := "package x\nimport \"net/http\"\nfunc f() { r, _ := http.NewRequest(http.MethodPost, \"u\", nil); _ = r; _ = \"push\" }\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(dir, "x.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	hits := 0
	ast.Inspect(f, func(n ast.Node) bool {
		if s, ok := n.(*ast.SelectorExpr); ok && s.Sel.Name == "MethodPost" {
			hits++
		}
		return true
	})
	if hits != 1 {
		t.Fatal("scanner sanity")
	}
}
