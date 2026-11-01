package orggraph

import (
	"sort"
	"strings"
	"time"
)

// RepoIn is one repository as read at a commit. Files holds the contents of the root manifests, CODEOWNERS and the
// openapi files that exist (an openapi file may be present with empty content).
type RepoIn struct {
	Key     string // <connection>:<provider project id>
	Label   string
	WebURL  string
	Head    string
	Files   map[string]string
	Missing bool // the read failed: no inferred edge is produced for this repo and none is marked stale
}
type ProjectIn struct {
	ID, Name     string
	RepoKeys     []string
	JiraProjects []string
	Commands     []string // approved command keys, tested_by edges from the primary repo
	PrimaryRepo  string
}
type DocIn struct {
	Key, Label      string
	SourceVersionID string
	Links           []string
}
type TaskIn struct {
	Key, Label, RepoKey string
}

type BuildIn struct {
	Build    int
	Now      time.Time
	Projects []ProjectIn
	Repos    []RepoIn
	Docs     []DocIn
	Tasks    []TaskIn
}

// Derive produces nodes and edges. Configuration edges are verified 1.0; manifest, openapi, CODEOWNERS and doc-link
// edges are inferred with the confidences of K32. It never matches names approximately.
func Derive(in BuildIn) ([]Node, []Edge) {
	nodes := map[string]Node{}
	addNode := func(k NodeKind, key, label string) string {
		n := Node{Kind: k, Key: key, Label: label}
		if cur, ok := nodes[n.ID()]; !ok || cur.Label == "" {
			nodes[n.ID()] = n
		}
		return n.ID()
	}
	var edges []Edge
	add := func(from, to string, rel Relation, st Status, conf float64, src, sha string, ev ...Evidence) {
		edges = append(edges, Edge{From: from, To: to, Relation: rel, Status: st, Confidence: conf, Evidence: ev, SourceID: src,
			CommitSHA: sha, ObservedAt: in.Now, LastSeenBuild: in.Build, CreatedBy: "system"})
	}
	repoByKey := map[string]RepoIn{}
	for _, r := range in.Repos {
		repoByKey[r.Key] = r
		addNode(KindRepo, r.Key, r.Label)
	}
	for _, p := range in.Projects {
		pid := addNode(KindProject, p.ID, p.Name)
		for _, rk := range p.RepoKeys {
			add(pid, addNode(KindRepo, rk, repoByKey[rk].Label), RelMapsTo, StatusVerified, 1, "", "", Evidence{Kind: "config", Excerpt: "project mapping"})
		}
		for _, jp := range p.JiraProjects {
			add(pid, addNode(KindJiraProject, jp, jp), RelMapsTo, StatusVerified, 1, "", "", Evidence{Kind: "config", Excerpt: "project mapping"})
		}
		if p.PrimaryRepo != "" {
			rid := addNode(KindRepo, p.PrimaryRepo, repoByKey[p.PrimaryRepo].Label)
			for _, c := range p.Commands {
				tid := addNode(KindTest, p.PrimaryRepo+":"+c, c)
				add(rid, tid, RelTestedBy, StatusVerified, 1, "", "", Evidence{Kind: "config", Excerpt: "approved command " + c})
			}
		}
	}
	for _, t := range in.Tasks {
		if t.RepoKey == "" {
			continue
		}
		add(addNode(KindTask, t.Key, t.Label), addNode(KindRepo, t.RepoKey, repoByKey[t.RepoKey].Label), RelImplements, StatusVerified, 1, "", "", Evidence{Kind: "config", Excerpt: "task run"})
	}

	// Manifests: identity -> repos that declare it.
	type decl struct {
		repo string
		m    Manifest
	}
	var manifests []decl
	owners := map[string][]string{}
	for _, r := range in.Repos {
		if r.Missing {
			continue
		}
		for _, f := range ManifestFiles {
			content, ok := r.Files[f]
			if !ok {
				continue
			}
			var m Manifest
			switch f {
			case "go.mod":
				m = ParseGoMod(f, content)
			case "package.json":
				m = ParsePackageJSON(f, content)
			default:
				m = ParsePom(f, content)
			}
			manifests = append(manifests, decl{r.Key, m})
			for _, id := range m.Identities {
				owners[id] = appendUnique(owners[id], r.Key)
			}
		}
	}
	for _, d := range manifests {
		for _, dep := range d.m.Deps {
			rs := owners[dep.Identity]
			if len(rs) != 1 || rs[0] == d.repo { // unknown identity, ambiguous identity, or itself
				continue
			}
			r := repoByKey[d.repo]
			add(NodeID(KindRepo, d.repo), NodeID(KindRepo, rs[0]), RelDependsOn, StatusInferred, 0.9, NodeID(KindRepo, d.repo), r.Head,
				Evidence{Kind: "manifest", Path: d.m.Path, CommitSHA: r.Head, Excerpt: excerpt(dep.Line)})
		}
	}
	for _, r := range in.Repos {
		if r.Missing {
			continue
		}
		rid := NodeID(KindRepo, r.Key)
		for _, p := range OpenAPIPaths {
			if _, ok := r.Files[p]; ok {
				aid := addNode(KindAPI, r.Key+":"+p, p)
				add(rid, aid, RelExposesAPI, StatusInferred, 0.8, rid, r.Head, Evidence{Kind: "openapi", Path: p, CommitSHA: r.Head})
			}
		}
		for _, p := range CodeownersPaths {
			content, ok := r.Files[p]
			if !ok {
				continue
			}
			for _, o := range ParseCodeowners(content) {
				add(rid, addNode(KindOwner, o, o), RelOwnedBy, StatusInferred, 0.7, rid, r.Head, Evidence{Kind: "codeowners", Path: p, CommitSHA: r.Head, Excerpt: "* " + o})
			}
			break
		}
	}
	// Documents that link a repository's catalog web_url exactly.
	byURL := map[string]string{}
	for _, r := range in.Repos {
		if u := trimURL(r.WebURL); u != "" {
			byURL[u] = r.Key
		}
	}
	for _, d := range in.Docs {
		did := addNode(KindDoc, d.Key, d.Label)
		seen := map[string]bool{}
		for _, l := range d.Links {
			if rk, ok := byURL[trimURL(l)]; ok && !seen[rk] {
				seen[rk] = true
				add(did, NodeID(KindRepo, rk), RelDocuments, StatusInferred, 0.6, did, d.SourceVersionID, Evidence{Kind: "doc_link", SourceVersionID: d.SourceVersionID, Excerpt: excerpt(l)})
			}
		}
	}
	ns := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		ns = append(ns, n)
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i].ID() < ns[j].ID() })
	sortEdges(edges)
	// An identical edge from two sources keeps the first (sorted order is stable).
	out := edges[:0]
	for i, e := range edges {
		if i > 0 && e.Key() == edges[i-1].Key() {
			continue
		}
		out = append(out, e)
	}
	return ns, out
}

func trimURL(u string) string { return strings.TrimRight(strings.TrimSpace(u), "/") }

func appendUnique(xs []string, x string) []string {
	for _, v := range xs {
		if v == x {
			return xs
		}
	}
	return append(xs, x)
}
