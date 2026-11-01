package extensions

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// TestCensusExecOnlyInRunGo: no process is started in internal/{extensions,codegraph,secscan} outside
// internal/extensions/run.go, and Exec takes a Permit.
func TestCensusExecOnlyInRunGo(t *testing.T) {
	root := repoRoot(t)
	scanned := 0
	for _, pkg := range []string{"extensions", "codegraph", "secscan"} {
		dir := filepath.Join(root, "internal", pkg)
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			scanned++
			f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if perr != nil {
				t.Errorf("%s: %v", rel, perr)
				return nil
			}
			if rel != "internal/extensions/run.go" {
				for _, imp := range f.Imports {
					if imp.Path.Value == `"os/exec"` {
						t.Errorf("%s imports os/exec; only internal/extensions/run.go may start processes", rel)
					}
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "exec" && strings.HasPrefix(sel.Sel.Name, "Command") && rel != "internal/extensions/run.go" {
						t.Errorf("%s: exec.%s outside run.go", rel, sel.Sel.Name)
					}
				}
				return true
			})
			return nil
		})
	}
	if scanned == 0 {
		t.Fatal("census scanned nothing")
	}
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, "internal/extensions/run.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "Exec" && fn.Recv == nil {
			found = true
			if len(fn.Type.Params.List) < 3 {
				t.Fatal("Exec must take (ctx, Permit, Spec)")
			}
			if id, ok := fn.Type.Params.List[1].Type.(*ast.Ident); !ok || id.Name != "Permit" {
				t.Fatal("Exec's second parameter must be a Permit")
			}
		}
	}
	if !found {
		t.Fatal("Exec not found")
	}
}

// TestCensusNoWriteToolsAreWired: nothing outside this package reads a manifest into extwrite, policy or tools.
func TestCensusManifestsNotWiredToWrites(t *testing.T) {
	root := repoRoot(t)
	for _, pkg := range []string{"extwrite", "policy", "catherd"} {
		_ = filepath.Walk(filepath.Join(root, "internal", pkg), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			raw, _ := os.ReadFile(path)
			if strings.Contains(string(raw), `"orqen/internal/extensions"`) {
				t.Errorf("%s imports internal/extensions", path)
			}
			return nil
		})
	}
}

// TestCensusDependenciesUnchanged: Sprint 6 adds no Go module and no npm package. The hashes live in
// testdata/deps.sha256 and change only together with a DEVIATIONS line.
func TestCensusDependenciesUnchanged(t *testing.T) {
	root := repoRoot(t)
	want, err := os.ReadFile("testdata/deps.sha256")
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]string{}
	for _, f := range []string{"go.mod", "web/package.json"} {
		raw, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		have[f] = hex.EncodeToString(sum[:])
	}
	for _, line := range strings.Split(strings.TrimSpace(string(want)), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			t.Fatalf("bad line %q", line)
		}
		if have[parts[1]] != parts[0] {
			t.Errorf("%s changed (sha256 %s); Sprint 6 adds no dependency. Update testdata/deps.sha256 together with a DEVIATIONS line", parts[1], have[parts[1]])
		}
		delete(have, parts[1])
	}
	if len(have) != 0 {
		t.Errorf("files without a recorded hash: %v", have)
	}
}
