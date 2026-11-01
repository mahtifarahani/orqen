# Sprint 5 verification (status: partial browser walk run; Sprint 5 not complete)

This file lists what the Sprint 5 walk must check against an isolated Orqen (own port, own MongoDB database, the fakes
from `cmd/orqen-fakes` and `cmd/orqen-fakehost`, no real service). The coder did not run the walk; the automated tests
named below cover the same behavior at the API level.

## Walk checklist (to run by a person or a later session)
1. Open Knowledge: the empty state explains that drafts appear after a merge. The Wiki badge shows the fallback mode
   (no Wiki binary is configured).
2. Run a task to merge in the fakes (`GET` fake Jira write log stays empty). A proposal appears; the task draft shows its
   claims with evidence labels and the origin "written by Orqen from evidence".
3. Verify the draft: status becomes Verified, an outbox row exists, the search finds it (mode fallback).
4. Create a conflicting draft (same fact, other value): Verify answers a conflict; the conflict card offers keep / accept
   (note required) / archive both.
5. Roll back to version 1: a new version 3 is verified through the gate.
6. Switch the UI to Persian: layout mirrors, all strings are Persian, no horizontal overflow at 375 px.

## Covered by automated tests (no browser)
- Lifecycle with rollback, org isolation, conflict plus claim gate: `internal/server/s5_knowledge_test.go`.
- Source ingest, latest flip, K16 stale marking: `s5_sources_test.go`.
- Wiki down, recovery, rebuild, foreign hit dropped, not configured: `s5_wiki_test.go`.
- Role packets (worker/reviewer), worker-only critical rule, oversize stop, brief contains the packet: `s5_context_test.go`.
- Merge to proposal to promotion, no GitLab write: `s5_proposals_test.go`.

## Covered by automated tests, second pass
- Org graph server: `s5_graph_test.go`. Compaction, benchmark, retention, metrics: `s5_ops_test.go`.
- Docs-evidence readiness warnings, linked-page fetch, foreign host out_of_scope, poisoned page: `s5_sources_test.go`.
- Vault flock, lease fence, sweep: `s5_wiki_test.go`.
- Web: typecheck, oxlint (warnings only, none in new files), vitest 129 passed, check:rtl ok, build ok.

## Browser walk (run 2026-10-28, partial)

Environment: `make build` binary (dev channel) on port 47401, own database `orqen_verify_s5` (dropped afterwards), memory credential store, `cmd/orqen-fakes` on 47400 and
`cmd/orqen-fakehost` behind catherd and codex wrapper scripts, setup through `TestWalkSetup`. Loopback only; no real GitLab, Jira, Confluence, model or Wiki. The
run for ORQ-1 reached Awaiting CI in dry-run. Browser pane 800x600, and 375x812 for the Persian check.

| Step | Result |
|---|---|
| Knowledge, empty state and fallback badge | Pass (no console errors). |
| Draft created through the API with run evidence, shown in the list and detail | Pass. A reference that does not resolve in the org is refused with 422. |
| Verify a draft whose claim has only a run reference | Refused 422 `knowledge_claims_unsupported` (by design). The UI always sent no attestations, so such a draft could never be verified. **Fixed**: per-claim attestation fields. |
| Verify with an attestation | Pass: Verified, claim Supported, outbox shows "1 changes waiting for the index" and `wiki_not_configured`. |
| Search for the verified record | Pass (fallback mode). |
| Conflict card, rollback | NOT walked (covered by `s5_knowledge_test.go` only). |
| Graph: empty state, Rebuild | Job succeeded (5 nodes, 4 edges) but the list stayed empty until a reload. **Fixed**: the rebuild refreshes the queries again after 2, 5 and 10 seconds. |
| Graph canvas | Rendered, but the nodes were blank white boxes on the dark theme. **Fixed**: `colorMode` follows the app theme and nodes use the card colors. Verified after the fix. Edge list with Verified badge works. |
| Context tab of the run | Pass: sizes against the cap, sections, unmapped Confluence link "Out of scope", role packet manifest (shown as raw JSON). Knowledge section was empty because the run predates the knowledge. |
| Persian at 375 px | Pass: `dir=rtl`, no horizontal overflow, strings translated. Leftovers: the raw code `wiki_not_configured` is not translated, and "1 changes" has no singular form. |

Not walked: conflict resolution, rollback in the browser, the Wiki (never installed), Confluence pages in the Context tab, the consumption table (not built).

## Third pass (fixtures, usage, sweeps)
Automated: `TestSeededFakeGraphFindsTheExactDependency` (GitLab fake -> default repo reader -> inferred `depends_on`), `TestUsageRollupOnTerminalRunsAndRexContext`, `TestDailyCompactionSweepQueuesOncePerOrgAndDay`, `TestPinnedRuleAppliesToRolesInPackets`, the extended `TestGraphBuild...`.
The seeded-graph test found a real bug: the default repo reader treated a missing manifest (HTTP 404) as an error, so a graph rebuild failed for any repository without `go.mod`; fixed.
Fixture check (API/DB only, not the browser): `TestWalkSetup` ran against a built binary on port 47421 with the fakes on 47420 and database `orqen_verify_s5b` (dropped afterwards, both processes stopped by their own pids). With `ORQEN_WALK_LIVE=1` the ORQ-1 run reached `publish_approval`, and a `confluence_page` source version (page 1001) was stored. Not walked: approving the publish, merging through `POST /_fakes/gitlab/merge`, and the resulting drafts; a graph rebuild in the UI.

## Status table
| Item | Status |
|---|---|
| Browser walk (Knowledge, Graph, Context tab) | Partly run, see above. |
| WP90 fixtures: seeded graph, Confluence page, live writes for the fake | Done and checked up to `publish_approval`; merge-to-knowledge not walked. |
| Graph page canvas rendering | Verified in the browser after the theme fix. |
| Wiki CLI contract v1 | Assumed; gated `TestWikiReal` never run. |
| Usage rollup, rex.context, applies_to_roles, daily compaction sweep, graph_neighbors | Built and tested at server level; not seen in a browser. |
| Context tab list/consumption table | Built; vitest/typecheck only, not walked in a browser. |
| Roles author (writer/kverifier, Detached) | NOT built (see DEVIATIONS). |
