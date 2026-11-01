// Package orggraph derives the organization graph (K32) from Orqen configuration and repository manifests. It is pure:
// the server collects the inputs (manifests read at a commit, mapping, catalog, Confluence links) and stores the result.
// No fuzzy matching: a dependency edge needs an exact, unique declared identity.
package orggraph

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type NodeKind string

const (
	KindProject     NodeKind = "project"
	KindRepo        NodeKind = "repo"
	KindService     NodeKind = "service"
	KindAPI         NodeKind = "api"
	KindDomain      NodeKind = "domain"
	KindOwner       NodeKind = "owner"
	KindDoc         NodeKind = "doc"
	KindTask        NodeKind = "task"
	KindTest        NodeKind = "test"
	KindJiraProject NodeKind = "jira_project"
)

var NodeKinds = []NodeKind{KindProject, KindRepo, KindService, KindAPI, KindDomain, KindOwner, KindDoc, KindTask, KindTest, KindJiraProject}

type Relation string

const (
	RelMapsTo          Relation = "maps_to"
	RelDependsOn       Relation = "depends_on"
	RelExposesAPI      Relation = "exposes_api"
	RelOwnedBy         Relation = "owned_by"
	RelDocuments       Relation = "documents"
	RelBelongsToDomain Relation = "belongs_to_domain"
	RelImplements      Relation = "implements"
	RelTestedBy        Relation = "tested_by"
	RelRelated         Relation = "related"
)

var Relations = []Relation{RelMapsTo, RelDependsOn, RelExposesAPI, RelOwnedBy, RelDocuments, RelBelongsToDomain, RelImplements, RelTestedBy, RelRelated}

type Status string

const (
	StatusVerified Status = "verified"
	StatusInferred Status = "inferred"
	StatusStale    Status = "stale"
	StatusRejected Status = "rejected"
)

func ValidRelation(r Relation) bool {
	for _, x := range Relations {
		if x == r {
			return true
		}
	}
	return false
}

func ValidNodeKind(k NodeKind) bool {
	for _, x := range NodeKinds {
		if x == k {
			return true
		}
	}
	return false
}

// ValidateUserEdge checks a user-created edge (graph_edge_invalid otherwise).
func ValidateUserEdge(from, to string, rel Relation) error {
	switch {
	case !ValidRelation(rel):
		return fmt.Errorf("unknown relation %q", rel)
	case from == "" || to == "" || from == to:
		return fmt.Errorf("an edge needs two different nodes")
	}
	return nil
}

// NodeID is "<kind>:<natural key>".
func NodeID(k NodeKind, key string) string { return string(k) + ":" + key }

type Node struct {
	Kind  NodeKind
	Key   string
	Label string
}

func (n Node) ID() string { return NodeID(n.Kind, n.Key) }

type Evidence struct {
	Kind            string `json:"kind"` // manifest | config | codeowners | openapi | doc_link | user
	SourceVersionID string `json:"source_version_id,omitempty"`
	ArtifactID      string `json:"artifact_id,omitempty"`
	Path            string `json:"path,omitempty"`
	CommitSHA       string `json:"commit_sha,omitempty"`
	Excerpt         string `json:"excerpt,omitempty"`
}

type Edge struct {
	From, To   string
	Relation   Relation
	Status     Status
	Confidence float64
	Evidence   []Evidence
	CommitSHA  string // commit (or source version) of SourceID the edge was observed at
	// SourceID is the node whose files produced the edge (a repo or a doc); freshness compares its current head.
	SourceID      string
	ObservedAt    time.Time
	StaleSince    time.Time
	LastSeenBuild int
	CreatedBy     string // system | user
}

func (e Edge) Key() string { return e.From + "|" + string(e.Relation) + "|" + e.To }

func excerpt(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func sortEdges(es []Edge) {
	sort.Slice(es, func(i, j int) bool { return es[i].Key() < es[j].Key() })
}
