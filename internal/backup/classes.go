// Package backup is the pure core of Orqen backups and restores (SPEC sections 14 and 17): the on-disk format, the
// collection classification, the writer, reader and verifier, the secret scanner, the retention planner, the dry-run
// preview and the restore plan. It imports nothing from internal/server and holds no database code: the server
// supplies documents and blobs, this package decides what a backup contains and whether it is trustworthy.
//
// Integrity is a sha256 tree, not authenticity: anyone with disk access can rewrite a backup and its hashes.
package backup

import "sort"

// Class says whether a collection goes into a backup.
type Class string

const (
	Include Class = "include"
	Exclude Class = "exclude"
)

// Classes maps every collection to include or exclude. A test parses internal/server/db.go and the server's
// collection names and fails on an unclassified collection, so every future sprint must decide.
//
// Excluded collections are rebuildable, transient or hold secrets: sessions, one-time tokens, idempotency records,
// jobs, host state, the search cache, the Wiki outbox and vault state, code indexes (derived, on disk), backup and
// restore bookkeeping, schema_meta (recorded in the manifest) and the coding slots (recreated on demand).
var Classes = map[string]Class{
	// core
	"organizations": Include, "owners": Include, "connections": Include, "projects": Include, "catalog_projects": Include,
	"repo_contract_snapshots": Include, "policy_drafts": Include, "policy_versions": Include, "model_profiles": Include,
	"model_profile_versions": Include, "setup_wizards": Include, "system_states": Include, "catherd_states": Include,
	"catherd_secrets": Include, "automation_states": Include, "pinned_rules": Include,
	// runs and work
	"tracked_issues": Include, "issue_snapshots": Include, "runs": Include, "run_attempts": Include, "inbox_items": Include,
	"decisions": Include, "workspaces": Include, "intake_states": Include, "role_executions": Include,
	"review_findings": Include, "external_operations": Include, "merge_requests": Include, "verification_evidence": Include,
	"external_write_settings": Include, "publisher_credentials": Include,
	// knowledge and graph
	"source_versions": Include, "knowledge_records": Include, "knowledge_versions": Include, "knowledge_proposals": Include,
	"knowledge_conflicts": Include, "knowledge_usage": Include, "knowledge_benchmark_runs": Include, "graph_nodes": Include,
	"graph_edges": Include, "graph_meta": Include,
	// system
	"artifacts": Include, "operations": Include, "events": Include, "counters": Include,
	// Sprint 6
	"extension_states": Include, "extension_installs": Include, "extension_project_bindings": Include, "skills": Include,
	"skill_versions": Include, "skill_binding_sets": Include, "security_scans": Include, "security_findings": Include,
	"backup_settings": Include,
	// Sprint 7
	"knowledge_author_sessions": Include, "source_sync_states": Exclude,

	"sessions": Exclude, "one_time_tokens": Exclude, "idempotency_records": Exclude, "jobs": Exclude, "host_sessions": Exclude,
	"host_states": Exclude, "knowledge_search_cache": Exclude, "wiki_outbox": Exclude, "wiki_vault_states": Exclude,
	"code_indexes": Exclude, "backup_runs": Exclude, "restore_locks": Exclude, "restore_reports": Exclude, "schema_meta": Exclude,
	"coding_slots": Exclude,
}

// ExcludedDataDirs are the data-directory trees that never go into a backup: they are rebuildable or contain secrets.
var ExcludedDataDirs = []string{"catherd", "workspaces", "wiki", "codegraph", "scans", "bin", "bootstrap-url.txt", "backups"}

// IncludedCollections lists the included collections in restore order: organizations first, then the rest of the core,
// the work records, knowledge, the system collections and the Sprint 6 ones. The order is fixed, not alphabetical.
func IncludedCollections() []string {
	out := make([]string, len(restoreOrder))
	copy(out, restoreOrder)
	return out
}

var restoreOrder = []string{
	"organizations", "owners", "connections", "projects", "catalog_projects", "repo_contract_snapshots", "policy_drafts",
	"policy_versions", "model_profiles", "model_profile_versions", "setup_wizards", "system_states", "catherd_states",
	"catherd_secrets", "pinned_rules", "extension_states", "extension_installs", "extension_project_bindings",
	"skills", "skill_versions", "skill_binding_sets", "backup_settings",
	"tracked_issues", "issue_snapshots", "runs", "run_attempts", "inbox_items", "decisions", "workspaces", "intake_states",
	"role_executions", "review_findings", "external_operations", "merge_requests", "verification_evidence",
	"external_write_settings", "publisher_credentials", "security_scans", "security_findings",
	"source_versions", "knowledge_records", "knowledge_versions", "knowledge_proposals", "knowledge_author_sessions", "knowledge_conflicts",
	"knowledge_usage", "knowledge_benchmark_runs", "graph_nodes", "graph_edges", "graph_meta",
	"artifacts", "operations", "events", "counters",
	// automation_states is restored first, paused, by the restore itself (X33); it is listed last so a generic loop
	// that skips already-restored collections sees it only once.
	"automation_states",
}

// ExcludedCollections lists the excluded collections, sorted.
func ExcludedCollections() []string {
	var out []string
	for n, c := range Classes {
		if c == Exclude {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// IsIncluded reports whether a collection is part of a backup.
func IsIncluded(name string) bool { return Classes[name] == Include }
