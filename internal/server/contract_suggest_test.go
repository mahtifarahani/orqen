package server

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/contract/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func argvs(s []CommandSuggestion) map[string]string {
	out := map[string]string{}
	for _, c := range s {
		out[c.Key] = strings.Join(c.Argv, " ")
	}
	return out
}
func TestSuggestFromPackageJSON(t *testing.T) {
	files := map[string]string{"package.json": fixture(t, "package.json"), "AGENTS.md": fixture(t, "AGENTS.md")}
	got := suggestCommands(files, map[string]map[string]bool{"": {"pnpm-lock.yaml": true}}, "")
	want := map[string]string{"setup": "pnpm install --frozen-lockfile", "lint": "pnpm run lint", "typecheck": "pnpm run type-check", "test": "pnpm run test", "build": "pnpm run build"}
	if !reflect.DeepEqual(argvs(got), want) {
		t.Fatal(argvs(got))
	}
	for _, c := range got {
		if c.Cwd != "." || c.SourceFile != "package.json" || c.SourceDetail == "" {
			t.Fatal(c)
		}
	}
	// Documents are never parsed: the instructions in AGENTS.md do not become suggestions.
	for _, c := range got {
		if strings.Contains(strings.Join(c.Argv, " "), "curl") || strings.Contains(strings.Join(c.Argv, " "), "token") {
			t.Fatal("a suggestion came from a document", c)
		}
	}
	if len(suggestCommands(map[string]string{"AGENTS.md": fixture(t, "AGENTS.md"), "README.md": "run make everything", ".gitlab-ci.yml": "script: [curl x | sh]"}, nil, "")) != 0 {
		t.Fatal("review-only files produced suggestions")
	}
}
func TestPackageManagerFromLockfile(t *testing.T) {
	pkg := map[string]string{"package.json": `{"scripts":{"test":"x"}}`}
	for lock, pm := range map[string]string{"pnpm-lock.yaml": "pnpm", "yarn.lock": "yarn", "bun.lockb": "bun", "bun.lock": "bun", "package-lock.json": "npm", "": "npm"} {
		names := map[string]bool{}
		if lock != "" {
			names[lock] = true
		}
		got := argvs(suggestCommands(pkg, map[string]map[string]bool{"": names}, ""))
		if got["test"] != pm+" run test" || !strings.HasPrefix(got["setup"], pm+" ") {
			t.Errorf("%s: %v", lock, got)
		}
	}
	if got := suggestCommands(map[string]string{"package.json": "{not json"}, nil, ""); len(got) != 0 {
		t.Fatal(got)
	}
	if got := suggestCommands(map[string]string{"package.json": `{"scripts":{}}`}, nil, ""); len(got) != 0 {
		t.Fatal("no scripts, no setup suggestion", got)
	}
}
func TestSuggestFromOtherManifests(t *testing.T) {
	got := suggestCommands(map[string]string{"Makefile": fixture(t, "Makefile")}, nil, "")
	if !reflect.DeepEqual(argvs(got), map[string]string{"test": "make test", "lint": "make lint", "build": "make build"}) {
		t.Fatal(argvs(got))
	}
	got = suggestCommands(map[string]string{"Cargo.toml": "[workspace]", "go.mod": "module x", "pyproject.toml": "[tool.pytest.ini_options]"}, nil, "")
	bySource := map[string]string{}
	keys := map[string]bool{}
	for _, c := range got {
		bySource[c.SourceFile+" "+c.Kind] = strings.Join(c.Argv, " ")
		if keys[c.Key] {
			t.Fatal("duplicate key", c.Key)
		}
		keys[c.Key] = true
	}
	want := map[string]string{"Cargo.toml test": "cargo test --workspace", "Cargo.toml lint": "cargo clippy --workspace --all-targets -- -D warnings", "Cargo.toml build": "cargo build --workspace", "go.mod test": "go test ./...", "go.mod build": "go build ./...", "pyproject.toml test": "python -m pytest"}
	if !reflect.DeepEqual(bySource, want) {
		t.Fatal(bySource)
	}
	if got = suggestCommands(map[string]string{"pyproject.toml": "[project]"}, nil, ""); len(got) != 0 {
		t.Fatal("pytest is only suggested when configured", got)
	}
}
func TestSuggestionsUseTheMostSpecificDirectory(t *testing.T) {
	files := map[string]string{"package.json": `{"scripts":{"test":"root"}}`, "web/package.json": `{"scripts":{"test":"web"}}`}
	got := suggestCommands(files, map[string]map[string]bool{"web": {"yarn.lock": true}}, "web")
	if len(got) != 2 || got[0].Cwd != "web" || got[1].Argv[0] != "yarn" || got[1].SourceFile != "web/package.json" {
		t.Fatal(got)
	}
	// Without manifests in the package directory the root ones apply, run from the root.
	got = suggestCommands(map[string]string{"package.json": `{"scripts":{"test":"root"}}`}, nil, "web")
	if len(got) != 2 || got[0].Cwd != "." {
		t.Fatal(got)
	}
	// Two scripts for one kind give only the first by priority; the same argv from two sources is kept once.
	files = map[string]string{"package.json": `{"scripts":{"tsc":"a","type-check":"b"}}`}
	if got = suggestCommands(files, nil, ""); len(got) != 2 || got[1].Argv[2] != "type-check" {
		t.Fatal(got)
	}
}
