package server

import "os"

// walkS7 reports whether the Sprint 7 part of the manual walk is wanted (ORQEN_WALK_S7=1).
func walkS7() bool { return os.Getenv("ORQEN_WALK_S7") == "1" }

// walkPolicyS7 is the Sprint 7 part of the org policy of the walk: an automatic code index for the head of an active run,
// an automatic security scan after a passing review, and the roles knowledge author. It is merged into the single policy
// publish of TestWalkSetup (a publish replaces the overrides) before automation is enabled, because a run pins the policy
// version of that moment. The walk profile (goodProfileDoc) leaves the writer and verifier roles enabled, which the roles
// author needs. It sets no credential: the security model and budget come from walkPolicyS6, and nothing here reads or
// writes a secret.
func walkPolicyS7() map[string]any {
	return map[string]any{"codegraph.auto_index": "on_run", "automation.security_scan": "auto", "knowledge.author": "roles", "knowledge.learn_on_merge": true}
}
