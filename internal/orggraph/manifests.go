package orggraph

import (
	"encoding/json"
	"encoding/xml"
	"regexp"
	"sort"
	"strings"
)

// Manifest is what a repository declares: the identities it provides and the dependencies it names.
type Manifest struct {
	Path       string
	Identities []string
	Deps       []Dep
}
type Dep struct{ Identity, Line string }

// ParseGoMod reads the module line and the require entries (single and block form). Versions are ignored.
func ParseGoMod(path, content string) Manifest {
	m := Manifest{Path: path}
	inBlock := false
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(stripComment(raw, "//"))
		switch {
		case line == "":
		case strings.HasPrefix(line, "module "):
			m.Identities = append(m.Identities, strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "module ")), `"`))
		case line == "require (":
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case inBlock:
			if f := strings.Fields(line); len(f) >= 1 {
				m.Deps = append(m.Deps, Dep{f[0], line})
			}
		case strings.HasPrefix(line, "require "):
			if f := strings.Fields(strings.TrimPrefix(line, "require ")); len(f) >= 1 {
				m.Deps = append(m.Deps, Dep{f[0], line})
			}
		}
	}
	return m
}

func stripComment(s, marker string) string {
	if i := strings.Index(s, marker); i >= 0 {
		return s[:i]
	}
	return s
}

// ParsePackageJSON reads name and dependencies/devDependencies/peerDependencies/optionalDependencies.
func ParsePackageJSON(path, content string) Manifest {
	m := Manifest{Path: path}
	var p struct {
		Name string            `json:"name"`
		D    map[string]string `json:"dependencies"`
		Dev  map[string]string `json:"devDependencies"`
		Peer map[string]string `json:"peerDependencies"`
		Opt  map[string]string `json:"optionalDependencies"`
	}
	if json.Unmarshal([]byte(content), &p) != nil {
		return m
	}
	if p.Name != "" {
		m.Identities = []string{p.Name}
	}
	var names []string
	seen := map[string]string{}
	for _, set := range []map[string]string{p.D, p.Dev, p.Peer, p.Opt} {
		for n, v := range set {
			if _, ok := seen[n]; !ok {
				names = append(names, n)
				seen[n] = v
			}
		}
	}
	sort.Strings(names)
	for _, n := range names {
		m.Deps = append(m.Deps, Dep{n, `"` + n + `": "` + seen[n] + `"`})
	}
	return m
}

// ParsePom reads groupId:artifactId (the group falls back to the parent's) and the dependency coordinates.
func ParsePom(path, content string) Manifest {
	m := Manifest{Path: path}
	type coord struct {
		G string `xml:"groupId"`
		A string `xml:"artifactId"`
	}
	var pom struct {
		coord
		Parent coord `xml:"parent"`
		Deps   struct {
			Dep []coord `xml:"dependency"`
		} `xml:"dependencies"`
	}
	dec := xml.NewDecoder(strings.NewReader(content))
	dec.Strict = false
	if dec.Decode(&pom) != nil {
		return m
	}
	g := pom.G
	if g == "" {
		g = pom.Parent.G
	}
	if g != "" && pom.A != "" {
		m.Identities = []string{g + ":" + pom.A}
	}
	for _, d := range pom.Deps.Dep {
		if d.G != "" && d.A != "" {
			id := strings.TrimSpace(d.G) + ":" + strings.TrimSpace(d.A)
			m.Deps = append(m.Deps, Dep{id, "<dependency>" + id + "</dependency>"})
		}
	}
	return m
}

// CodeownersPaths are the fixed locations, in lookup order.
var CodeownersPaths = []string{"CODEOWNERS", ".gitlab/CODEOWNERS", "docs/CODEOWNERS", ".github/CODEOWNERS"}

// OpenAPIPaths are the fixed paths that mark an exposed API.
var OpenAPIPaths = []string{"openapi.yaml", "openapi.yml", "openapi.json", "api/openapi.yaml", "api/openapi.yml", "api/openapi.json",
	"docs/openapi.yaml", "docs/openapi.yml", "docs/openapi.json", "swagger.yaml", "swagger.json"}

// ManifestFiles are the root manifests the builder reads.
var ManifestFiles = []string{"go.mod", "package.json", "pom.xml"}

var ownerRe = regexp.MustCompile(`^@?[A-Za-z0-9_.\-/]+(@[A-Za-z0-9_.\-]+)?$`)

// ParseCodeowners returns the owners of the last `*` rule (the default owner), without a model or pattern guessing.
func ParseCodeowners(content string) []string {
	var owners []string
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(stripComment(raw, "#"))
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "*" {
			continue
		}
		owners = owners[:0:0]
		for _, o := range f[1:] {
			if ownerRe.MatchString(o) || strings.Contains(o, "@") {
				owners = append(owners, o)
			}
		}
	}
	return owners
}
