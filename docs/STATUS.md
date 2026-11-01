# Orqen status (end of Sprint 7)

Date: 2026-10-29. HEAD at the time of writing: see `git log -1`. `make check` was green twice in a row on the final commit
(gofmt, vet, `go test -race ./...`, web lint, typecheck, `check:rtl`, vitest, build, OpenAPI drift).

## What exists

Sprints 1 to 7 are implemented and verified against fakes only (loopback, no real GitLab, Jira, Confluence, model, Codex,
catherd, Jev, Strix, GitNexus or Wiki): foundation and security model, setup wizard, connections, catherd profiles and
Keychain-backed keys, Jira intake and context packets, triage, Inbox, managed worktrees, Codex host adapter, verification,
the external-write gate (dry-run by default), independent review, MR readiness, versioned knowledge and the Wiki index
port, Confluence docs source, org graph, extensions catalog, role skills, optional GitNexus and Strix, backup and restore.
Per-sprint evidence: `docs/VERIFICATION-S2.md` to `docs/VERIFICATION-S6.md`. Deliberate differences from the plans:
`docs/DEVIATIONS.md`.

## Known gaps (not built, all recorded in DEVIATIONS)

- Sprint 7 closed the roles author (fake-verified only, assumed contract), automatic code-index refresh, automatic
  security scan, security counts in the tasks summary, and the raw-code translations.
- The Sprint 7 browser walk was done in part (publishing, merge on the fake, roles author session, Persian at 375 px on
  Knowledge). Not walked in a browser: automatic index and scan, licence flows, Graph code panel, Skills tabs, Maintenance,
  restore. Covered by Go and vitest tests. See `docs/VERIFICATION-S7.md`.
- Seven policy keys are read nowhere yet (deferred list in `internal/server/policy_wiring_test.go`, rows in DEVIATIONS).
- `orqen restore` was exercised by tests and an API/CLI walk against a throwaway database, not by a user on real data.

## What only you can close (Orqen never does these)

1. Real-tool contracts are assumed, never run: Memory Wiki CLI (`TestWikiReal`), Codex host spike
   (`TestCodexHostProbeReal`, `TestCodexCatherdSpikeReal`), `TestCatherdReal`, `TestJevReal`, GitNexus and Strix real tests.
   They skip unless their env variables are set; procedure in `docs/LIVE-TRIAL.md`.
2. Credentials: create your own read-only GitLab and Jira tokens and enter them only in the UI (Keychain). The Jev key that
   was pasted into a chat early on should be rotated; enter the new one only in Models, Orchestrator and keys.
3. Live external writes need a `make release` build, `ORQEN_ENV=prod`, the phrase "ENABLE LIVE WRITES", an allowlist and a
   separate write credential. Dev and test builds can only write to loopback fakes. Merging is always yours.
4. Your web edits (IconButton and friends) are committed by you in 381237a; this work never changed them. Wire the new
   policy labels and pages where noted in `docs/DEVIATIONS.md`.
5. Pushing is yours to ask for: the only remote is your GitHub one. Everything up to the Sprint 7 walk notes was pushed on request.
