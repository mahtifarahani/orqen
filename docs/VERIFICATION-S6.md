# Sprint 6 (Go) verification status

Honest status. Only what was actually run is marked as run. Last completed work package: WP104 (all server work packages). The web work packages WP105 to WP110 are not started.

| WP | Scope | Status | Checks seen to finish |
|----|-------|--------|-----------------------|
| 93 | internal/extensions | done, committed | go test -race ./internal/extensions: passed |
| 94 | internal/skills, rolebriefs s6.1, knowledge roles_reply | done, committed | go test of those packages: passed |
| 95 | internal/codegraph, fakehost gitnexus | done, committed | go test ./internal/codegraph passed (real test gated, not run) |
| 96 | internal/secscan, fakehost strix | done, committed | go test ./internal/secscan passed (real test gated, not run) |
| 97 | internal/backup | done, committed | go test ./internal/backup: passed |
| 98 | policy PDV 5, mrready security_scan, runstate restored, readiness | done, committed | go test of the four packages: passed; coordinator fixed two pinned tests |
| 99 | server extensions, credentials, health, wiki indexFor | done, committed | go test -race of the 3 new server tests (TestExtensionLicenseGateLifecycle, TestExtensionWikiThroughCatalog, TestExtensionCredentialNeverStored): passed; TestContract, TestContractRefs, TestWiki*, internal/backup, internal/extensions: passed; pnpm typecheck: passed |
| 100 | server skills, run pinning, brief injection | done, committed | go test -race of the 3 new server tests (TestSkills*): passed; TestContract, TestContractRefs, four existing context/implement stage tests (TestContextStageBuildsAPacket, TestRolePacketsReceive, TestS4ImplementCommitsTheWorkOnce, TestS4EmptyChangeClimbsOnceThenAsks): passed; rolebriefs, skills, backup: passed; pnpm typecheck: passed |
| 101 | server code graph (indexes, impact, packet item) | done, committed | go test -race TestCodeIndex* (3 tests): passed; TestContract, TestContractRefs passed. `sweepCodeIndexAuto` not implemented (see DEVIATIONS) |
| 102 | server security scans | done, committed | go test -race TestSecurityScan* (3 tests): passed; TestContract passed. `sweepSecurityAuto` and tasks.go summary counts not implemented (see DEVIATIONS) |
| 103 | server backups, schedule, prune, verify, support bundle | done, committed | go test -race TestBackup* (3 tests): passed; internal/backup and internal/extwrite census passed |
| 104 | orqen restore CLI, post-restore check, restore lock | done, committed | go test -race TestRestore* (3 tests): passed; the CLI binary itself (cmd/orqen restore) was built (go build, go vet) but NOT run end to end against a throwaway database |
| 105 to 108 | web pages (Extensions, Skills, Security, Settings) | NOT started | none (the generated API client for WP99 to WP104 exists in web/src/api/generated; pnpm typecheck passed after generation) |
| 109 | fakes and walk fixtures, release docs | NOT started | none |
| 110 | browser walk | NOT run | none |

Seen finishing (Sprint 6 web, WP105-WP110): pnpm typecheck; oxlint (only warnings in files I did not write); vitest 33 files / 162 tests at the final run; check:rtl ok; pnpm build ok (after WP108); make dist, make build, go build ./..., go vet ./internal/server, go test ./internal/extwrite -run TestCensus; go test -race on TestContract, TestExtensionLicenseGateLifecycle and TestExtensionCredentialNeverStored; TestWalkSetup against a live loopback instance.
TestExtensionWikiThroughCatalog flaked (about a third of runs at WP101). Cause: a transaction read an older revision than a plain read just before it (DB.Txn causality); fixed in d5b7c6d. The earlier "15 of 15" claim rested on a retry in the test and is not relied on; a later repeated run result was lost, so it is unknown.
Walk (API, CLI and Browser pane, loopback only, fakes on 47500, orqen on 47501/47502, memory credential store): extensions license gate, detect/test/enable, binding, license revoke; graph index and impact query; Strix scans (budget stop, findings, waive, issue draft, tree-modifying scan leaves the worktree clean after the scan); skills (poisoned fixture flags, acknowledgement, bindings, resolve); backups (settings, run, verify, prune preview, no secrets in the folder); restore drill via CLI (paused/held/credentials missing); support bundle has no key, token or home paths; Extensions, Security and Settings Backups/Maintenance in the Browser pane at 800x600 and Persian at 375x812 (rtl, no horizontal overflow apart from the focus-only skip link).
NOT run or NOT shown: make check; the full ./internal/server package; any real tool or service; the graph staleness banner and Context tab index_stale (the fake GitLab returns no commit sha, so only the server test covers it); the packet exclusion for a new run after license revoke; recommended skill set on a fresh project (already bound); a new run to show the Roles-tab skills manifest and brief preamble; restore followed by setting a credential and Resume; a pre-walk hash of personal state (only an after check that nothing there changed in the last 4 hours, and ~/.openclaw is absent); browser-pane clicks through every dialog (the dialogs are covered by vitest).

Known gaps in what exists:
- The WP99 to WP104 routes have no web UI yet; the 13 new policy keys have no labels in the policy panel (that file is the user's uncommitted edit).
- The tests for WP99/WP100 use scripted fake binaries and direct pinSkills/skillsFor calls; the full run pipeline with skills bound was exercised only through the unchanged existing stage tests.


## Coordinator walk (2026-10-29, isolated instance, fakes only)

Environment: `make build` binary on port 47401, own database `orqen_verify_s6c` (dropped afterwards), memory credential store, `cmd/orqen-fakes` on 47400 and the
fakehost wrappers, setup through `TestWalkSetup`. `make check` was green on HEAD `008fc15` before this walk (exit 0).

| Step | Result |
|---|---|
| Tasks page load, request count | **Bug found and fixed.** The event stream replayed the whole history on load and invalidated per event: 200+ aborted `tasks/summary` refetches. Now a replay triggers one refresh once it settles and live bursts are coalesced (leading edge, 150 ms): 14 API calls with 2 `tasks/summary` on a forced full replay. Idle: 0 requests in 5 s. Tests: `sse.test.ts`, `sse-batch.test.ts`. |
| Extensions page | Pass (catalog cards, Not registered, Manage; Orqen installs nothing). |
| Security page | Pass (not ready, no binary registered, policy summary). |
| Settings, Backups, Back up now | **Bug found and fixed.** The backup succeeded (107 KB) but the list stayed empty: backup events carry no organization, so the org stream never announces them. The tab now refreshes the lists when the tracked operation ends. Verified with a second backup. |
| Persian | Checked earlier in the session for the Sprint 5 pages and by the coder for the Sprint 6 pages; not repeated here. |

Not walked by me: Skills tabs, the Graph code panel, Maintenance, restore drill in the browser, licence flows, the Roles-tab skills manifest.
