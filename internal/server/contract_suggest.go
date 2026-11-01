package server

import (
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strings"
)

// CommandSuggestion is a deterministic proposal parsed from a repository manifest. Suggestions are data for review;
// nothing is approved or added to a project automatically.
type CommandSuggestion struct {
	Kind         string   `bson:"kind" json:"kind"`
	Key          string   `bson:"key" json:"key"`
	Argv         []string `bson:"argv" json:"argv"`
	Cwd          string   `bson:"cwd" json:"cwd"`
	SourceFile   string   `bson:"source_file" json:"source_file"`
	SourceDetail string   `bson:"source_detail" json:"source_detail"`
}

// contractReadable lists the files an import fetches; Markdown and CI files are stored for review only.
var contractReadable = []string{"AGENTS.md", "CLAUDE.md", "README.md", "package.json", "Makefile", "Cargo.toml", "go.mod", "pyproject.toml", ".gitlab-ci.yml"}
var contractManifests = []string{"package.json", "Makefile", "Cargo.toml", "go.mod", "pyproject.toml"}

var scriptKinds = []struct {
	kind  string
	names []string
}{
	{"lint", []string{"lint"}},
	{"typecheck", []string{"typecheck", "type-check", "tsc"}},
	{"test", []string{"test"}},
	{"build", []string{"build"}},
}

var setupCommands = map[string][]string{
	"pnpm": {"pnpm", "install", "--frozen-lockfile"},
	"npm":  {"npm", "ci"},
	"yarn": {"yarn", "install", "--frozen-lockfile"},
	"bun":  {"bun", "install", "--frozen-lockfile"},
}
var makeTarget = regexp.MustCompile(`(?m)^([A-Za-z0-9_.-]+)[ \t]*:([^=\n]|$)`)

func packageManager(names map[string]bool) string {
	switch {
	case names["pnpm-lock.yaml"]:
		return "pnpm"
	case names["yarn.lock"]:
		return "yarn"
	case names["bun.lock"] || names["bun.lockb"]:
		return "bun"
	}
	return "npm"
}

// suggestCommands parses manifests. files maps repository paths to content; listings maps a directory ("" for the
// root) to the file names it contains. Manifests are read from the package directory when it has them, else the root.
func suggestCommands(files map[string]string, listings map[string]map[string]bool, pkgPath string) []CommandSuggestion {
	dirs := []string{""}
	if pkgPath != "" {
		dirs = []string{pkgPath, ""}
	}
	dir := ""
	for _, d := range dirs {
		for _, m := range contractManifests {
			if _, ok := files[path.Join(d, m)]; ok {
				dir = d
				goto found
			}
		}
	}
found:
	cwd := dir
	if cwd == "" {
		cwd = "."
	}
	var out []CommandSuggestion
	used := map[string]bool{}
	seen := map[string]bool{}
	add := func(kind, suffix string, argv []string, file, detail string) {
		sig := kind + "\x00" + strings.Join(argv, "\x00")
		if seen[sig] {
			return
		}
		seen[sig] = true
		key := kind
		if suffix != "" && used[key] {
			key = kind + "-" + suffix
		}
		for n := 2; used[key]; n++ {
			key = kind + "-" + suffix + string(rune('0'+n%10))
		}
		used[key] = true
		out = append(out, CommandSuggestion{Kind: kind, Key: key, Argv: argv, Cwd: cwd, SourceFile: file, SourceDetail: detail})
	}
	rel := func(name string) string { return path.Join(dir, name) }
	if body, ok := files[rel("package.json")]; ok {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal([]byte(body), &pkg) == nil {
			pm := packageManager(listings[dir])
			if setup, ok := setupCommands[pm]; ok && len(pkg.Scripts) > 0 {
				add("setup", pm, append([]string{}, setup...), rel("package.json"), "lockfile selects "+pm)
			}
			for _, k := range scriptKinds {
				for _, name := range k.names {
					if _, ok := pkg.Scripts[name]; ok {
						add(k.kind, pm, []string{pm, "run", name}, rel("package.json"), "scripts."+name)
						break
					}
				}
			}
		}
	}
	if body, ok := files[rel("Makefile")]; ok {
		targets := map[string]bool{}
		for _, m := range makeTarget.FindAllStringSubmatch(body, -1) {
			targets[m[1]] = true
		}
		for _, k := range []string{"setup", "lint", "typecheck", "test", "build"} {
			if targets[k] {
				add(k, "make", []string{"make", k}, rel("Makefile"), "target "+k)
			}
		}
	}
	if _, ok := files[rel("Cargo.toml")]; ok {
		add("test", "cargo", []string{"cargo", "test", "--workspace"}, rel("Cargo.toml"), "Cargo workspace")
		add("lint", "cargo", []string{"cargo", "clippy", "--workspace", "--all-targets", "--", "-D", "warnings"}, rel("Cargo.toml"), "Cargo workspace")
		add("build", "cargo", []string{"cargo", "build", "--workspace"}, rel("Cargo.toml"), "Cargo workspace")
	}
	if _, ok := files[rel("go.mod")]; ok {
		add("test", "go", []string{"go", "test", "./..."}, rel("go.mod"), "Go module")
		add("build", "go", []string{"go", "build", "./..."}, rel("go.mod"), "Go module")
	}
	if body, ok := files[rel("pyproject.toml")]; ok && strings.Contains(body, "pytest") {
		add("test", "pytest", []string{"python", "-m", "pytest"}, rel("pyproject.toml"), "pytest configuration")
	}
	order := map[string]int{"setup": 0, "lint": 1, "typecheck": 2, "test": 3, "build": 4}
	sort.SliceStable(out, func(i, j int) bool { return order[out[i].Kind] < order[out[j].Kind] })
	return out
}
