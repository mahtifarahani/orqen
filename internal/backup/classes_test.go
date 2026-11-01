package backup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const serverDir = "../server"

// collectionNames returns the collection names found as string literals in internal/server: the index list and the
// Coll("...") calls of db.go, plus every Coll("...") call of the other non-test files.
func collectionNames(t *testing.T) (migrate, all map[string]bool) {
	t.Helper()
	migrate, all = map[string]bool{}, map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(serverDir, "*.go"))
	if len(files) < 20 {
		t.Fatalf("found only %d server files", len(files))
	}
	for _, p := range files {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		isDB := filepath.Base(p) == "db.go"
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Coll" && len(x.Args) == 1 {
					if l, ok := x.Args[0].(*ast.BasicLit); ok && l.Kind == token.STRING {
						name, _ := strconv.Unquote(l.Value)
						all[name] = true
						if isDB {
							migrate[name] = true
						}
					}
				}
			case *ast.CompositeLit:
				// the index list: {"coll", "name", key(...), ...} or {coll: "x", ...}
				if !isDB {
					return true
				}
				for i, e := range x.Elts {
					switch v := e.(type) {
					case *ast.BasicLit:
						if i == 0 && v.Kind == token.STRING && len(x.Elts) >= 5 {
							name, _ := strconv.Unquote(v.Value)
							migrate[name], all[name] = true, true
						}
					case *ast.KeyValueExpr:
						if id, ok := v.Key.(*ast.Ident); ok && id.Name == "coll" {
							if l, ok := v.Value.(*ast.BasicLit); ok {
								name, _ := strconv.Unquote(l.Value)
								migrate[name], all[name] = true, true
							}
						}
					}
				}
			}
			return true
		})
	}
	return migrate, all
}

func TestEveryCollectionIsClassified(t *testing.T) {
	migrate, all := collectionNames(t)
	if len(migrate) < 40 {
		t.Fatalf("parsed only %d collections from db.go", len(migrate))
	}
	var missing []string
	for _, set := range []map[string]bool{migrate, all} {
		for name := range set {
			if _, ok := Classes[name]; !ok {
				missing = append(missing, name)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("unclassified collections (add them to backup.Classes as include or exclude): %v", missing)
	}
	for name, c := range Classes {
		if c != Include && c != Exclude {
			t.Errorf("%s has class %q", name, c)
		}
	}
}

func TestRestoreOrderCoversExactlyTheIncludedCollections(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range IncludedCollections() {
		if seen[n] {
			t.Errorf("%s twice in the restore order", n)
		}
		seen[n] = true
		if !IsIncluded(n) {
			t.Errorf("%s is ordered but not included", n)
		}
	}
	for n, c := range Classes {
		if c == Include && !seen[n] {
			t.Errorf("%s is included but not in the restore order", n)
		}
	}
	if IncludedCollections()[0] != "organizations" {
		t.Fatal("organizations first")
	}
	for _, secretHolder := range []string{"sessions", "one_time_tokens", "idempotency_records", "jobs"} {
		if IsIncluded(secretHolder) {
			t.Errorf("%s must be excluded", secretHolder)
		}
	}
	if _, err := os.Stat(filepath.Join(serverDir, "db.go")); err != nil {
		t.Fatal(err)
	}
}
