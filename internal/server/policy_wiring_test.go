package server

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

	"orqen/internal/policy"
)

// deferredPolicyKeys are registry keys no code reads yet; each has a reason and a DEVIATIONS line (Y16). Wiring one
// removes it from this map: the census fails when a key listed here is read after all.
var deferredPolicyKeys = map[string]string{
	"automation.jira_write":              "Jira writes are not built; the key is reserved for the write gate",
	"docs.linked_pages_per_run":          "linked-page expansion per run is not built",
	"docs.max_page_bytes":                "pages are bounded by the connector page cap today",
	"host.idle_stop_secs":                "the host supervisor idle stop is process-wide, not per organization",
	"knowledge.retrieval.cache_ttl_secs": "retrieval has no cache to expire",
	"retention.derived_cache_days":       "derived caches are rebuilt on demand and never aged out",
	"review.require_different_model":     "independence is reported as a readiness fact; no stage refuses on it",
}

// stringLiterals returns every string literal of the non-test Go files of the given package directories.
func stringLiterals(t *testing.T, dirs ...string) map[string]bool {
	t.Helper()
	lits := map[string]bool{}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			tree, err := parser.ParseFile(fset, f, src, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(tree, func(n ast.Node) bool {
				if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if s, err := strconv.Unquote(bl.Value); err == nil {
						lits[s] = true
					}
				}
				return true
			})
		}
	}
	return lits
}

// TestEveryPolicyKeyIsRead: a policy key nobody reads is a setting that lies (Y16). A key counts as read when its
// name is a string literal in a non-test file, or a literal prefix ending in "." that the code extends.
func TestEveryPolicyKeyIsRead(t *testing.T) {
	lits := stringLiterals(t, ".", "../providers")
	var unread []string
	for _, k := range policy.Registry {
		read := lits[k.Key]
		for l := range lits {
			if !read && strings.HasSuffix(l, ".") && strings.HasPrefix(k.Key, l) {
				read = true
			}
		}
		if reason, deferred := deferredPolicyKeys[k.Key]; deferred {
			if read {
				t.Errorf("%s is read now: remove it from deferredPolicyKeys", k.Key)
			}
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s is deferred without a reason", k.Key)
			}
			continue
		}
		if !read {
			unread = append(unread, k.Key)
		}
	}
	sort.Strings(unread)
	if len(unread) > 0 {
		t.Fatalf("policy keys nothing reads (wire them or defer them with a reason):\n%s", strings.Join(unread, "\n"))
	}
}
