# Sprint 3 verification

Date: 2026-10-19. Build: HEAD after WP52 plus the cleanup-sweep fix found in the walk. Environment: `make build` binary on port 47201,
isolated data dir and database `orqen_verify_s3` (dropped afterwards), `ORQEN_CREDENTIAL_STORE=memory`, `cmd/orqen-fakes` (Jira, GitLab and
smart-HTTP git) on port 47200, a **stub catherd** script kept outside the repo (file-backed profiles, canned catalog and doctor). Setup used an
HTTP script; every checked step below used the Browser pane (800x600, no viewport emulation) unless marked "API".

## Automated

| Item | Result |
|---|---|
| `make check` (gofmt, vet, `go test -race`, web lint/typecheck/`check:rtl`/vitest/build, openapi drift) | See the final report; run last. |
| Crash/recovery (`runHook` lease loss in context, triage and workspace stages) | Passed: one packet, one stage-completed event, one Jev call, one worktree and workspace document. |
| Duplicate poll, two runners on one database | Passed: one active poll job, one run per issue, never two active `run:<id>` jobs. |
| Injection (ORQ-1 comment with `token=abc123`) | Passed: stored and shown as data inside an external source block, scrubbed in the packet, policy unchanged. |
| Leak scans (Mongo, artifacts, data dir with mirrors and worktrees, slog) for the Jev, GitLab and Jira sentinels | Passed. |
| Personal-checkout test and personal-state hash guard (`~/.config/catherd`, `~/.local/share/catherd`, `~/.claude/agents`, LaunchAgents, keychain `git:` item) before, during and after the walk | Identical. |
| `TestJevReal` | Not run (user-gated). |

## Walk checklist

| Item | Result |
|---|---|
| Tasks fill after enabling (scheduler poll) | Passed: six tasks appeared within seconds. |
| ORQ-1 queued with a ready workspace; context tab (manifest, cap meter, exclusions) and workspace tab (branch, base SHA) | Passed. Confluence link shown as "Adapter not available", knowledge/experience as "Module not available". |
| ORQ-2 `missing_acceptance` in the Inbox with evidence; answer in the sheet; same run continues to queued | Passed. A second answer (API) gave 409 `inbox_item_stale`; no second run. The stale alert UI is covered by vitest, not walked. |
| ORQ-6 plan approval forced by sensitive risk; approve | Passed (answered the clarification and approved by API; the approval item showed in the Inbox). The Start button was not walked: the project overrides were auto, so Start is covered by Go and vitest tests. |
| ORQ-3 acceptance change via `/_fakes` | Passed (API): old run superseded, new run created. The History tab chain was not opened. |
| 429 from `/_fakes/jira/fail` | Passed (API): `rate_limited` recorded, recovered on the retry. The intake card badge was gone by the time the card was opened. |
| Token rotate, blocked, credential replaced | Passed (API): `blocked` then cleared and polling resumed. Card visuals covered by vitest. |
| Stop orqen-fakes, waiting network, restart | Not walked (the fakes were left running); covered by Go tests (Jira poll, context and workspace stages). |
| Pause: poll refused with `automation_paused`, unpause | Passed (API). The banner is covered by vitest. |
| Cancel a run; cancellation confirmed | Passed (API): confirmed immediately (no live lease). |
| Discard a preserved workspace | Passed in the UI: two-step confirm, snapshot kept, workspace Removed. |
| fa/en RTL in Tasks and Inbox | Passed: tables and filters flow from the right, keys stay left-to-right. Overview and the sheet side were checked by vitest only. |
| Keyboard: Escape closes the Inbox sheet | Passed. |

## Bug found and fixed in the walk

A cleanup job that was already running when a second run was cancelled skipped the second run's workspace, and the second request found the
job active and deduplicated away, so the clean workspace stayed `ready`. The scheduler sweep now enqueues cleanup whenever a finished run still has a live workspace.

## Not verified

Real Jira (Server 9.12, v2 API, PAT Bearer), real GitLab, real Jev and a real Codex host: no tokens or keys were available; everything used fakes. During
setup one run of the walk accidentally started the server without `ORQEN_CATHERD_BIN`, so the real catherd ran `doctor`, `catalog list` and a profile
round trip once, inside the isolated org home under the throwaway data dir; the personal-state guard was identical afterwards. The setup was then
reset and repeated with the stub.
