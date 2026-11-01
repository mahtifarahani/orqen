# Sprint 4 verification

Date: 2026-10-20. Build: HEAD after WP72 plus the fixes found in the walk (listed at the end). Environment: `make build` binary (dev channel,
`ORQEN_ENV` unset) on port 47401, isolated data dir and database `orqen_verify_s4` (dropped afterwards), `ORQEN_CREDENTIAL_STORE=memory`,
`cmd/orqen-fakes` on port 47400 (GitLab, Jira and smart-HTTP git, fake write token), and `cmd/orqen-fakehost` behind two wrapper scripts as
`--codex-bin` and `--catherd-bin`. Setup ran through the public API (`TestWalkSetup`, which skips without `ORQEN_WALK_URL`). Every checked UI
step used the Browser pane (800x600, no viewport emulation); steps marked "API" used the HTTP API.

**What could not be verified here, and why:** no real Codex App Server (gated tests written, never run), no real catherd, no real model, no
real GitLab or Jira (Jev likewise). The fake host speaks the same JSON-RPC and catherd CLI shapes the code reads, so wire-format drift against
the real tools is the main residual risk; `docs/LIVE-TRIAL.md` is the procedure for the user to close it (spike, attestation, optional live trial).

## Automated

| Item | Result |
|---|---|
| `make check` (gofmt, vet, `go test -race -timeout 40m ./...`, web lint, typecheck, `check:rtl`, vitest, build, openapi drift) | Green twice in a row without the test cache (server package 699 s and 739 s). Earlier runs found and fixed three flakes and the 10-minute default timeout (see DEVIATIONS). |
| Census test (AST scan: no write method or `push` outside the allowlisted files; Jira and Confluence have none) | Passed. |
| Decide table, loopback dialer (zero dials for a non-loopback target), env guard, kill switch, pause between decide and execute | Passed. |
| Default settings write nothing (`TestS4PublishDefaultSettingsWriteNothingAnywhere`: fake `Writes()` empty, `PushCount` 0, no receive-pack request, merge request API untouched) | Passed. |
| Write credential and host environment leak scans (Mongo, artifacts, logs, host process env and argv) | Passed. |
| Worker `git push` blocked by the isolated gitconfig | Passed. |
| Crash after admit, lease loss, lost response after MR create, unknown outcome (look-only reconcile then Inbox) | Passed (WP58, WP62, WP63 tests). |
| `TestCodexHostProbeReal`, `TestCodexCatherdSpikeReal`, `TestJevReal`, `TestCatherdReal` | Not run (user-gated; they skip without their variables). |

## Personal-state guard

`~/.config/catherd`, `~/.local/share/catherd` (excluding `logs/`), `~/.codex/{config.toml,auth.json,hooks.json}`, `~/.codex/sessions` count,
`~/.claude/agents`, `~/Library/LaunchAgents`, keychain `dev.orqen.credential` accounts and `127.0.0.1` internet passwords: **identical before and
after** the walk. Note: catherd's own daily log files under `~/.local/share/catherd/logs/` change whenever the user's own Claude Code or Codex
sessions start a catherd MCP server (boot events with those sessions' pids); they are excluded from the hash for that reason, and no other file
under either directory is newer than the first baseline.

## Walk checklist

| Item | Result |
|---|---|
| Host probe of the fake host through the real binary (`POST /host/probe`), enable automation | Passed. First attempt found that the production binary never created the host supervisor (fixed, see below). |
| ORQ-1 runs architect, worker, verifier, reviewer, publish (dry-run) to `awaiting_ci` with hold `dry_run`; DRY-RUN badge in the header, hold card, head badges | Passed (UI). |
| **Zero writes with default settings** | Passed: `GET /_fakes/gitlab/writes` returned `{"pushes":[],"receive_pack_requests":0,"writes":[]}`; the three operations were `dry_run` with reason `mode_dry_run`; `lsof` showed only 127.0.0.1 peers (Mongo 27018, fakes 47400, own listener). |
| Publishing tab: simulated operations, exact payload dialog (host, project, branch, refspec, commits, diffstat, intent hash), Esc closes | Passed (UI). |
| Settings, Publishing: allowlist group-1/proj-1, enable live writes with the typed phrase (the confirm button stays disabled until it matches), LIVE badge | Passed (UI). |
| Approve push and MR create in the dialog (exact payload, Next operation); note approved through the API | Passed. Fake log: exactly 1 push and 1 MR create and 1 note, all with token kind `write`. |
| Review round, `ready_for_merge` after the pipeline: pipeline running gives "Not ready", success with all jobs gives Ready (13 checks listed in the MR tab and the MR page, nav badge 1) | Passed (UI). |
| Second pusher (`push-as-other`): readiness lost (`head_match` stale "someone else pushed"), the foreign head is adopted, re-verified and re-reviewed (Roles tab shows verifier and reviewer round 2), a new pipeline is needed, then Ready again | Passed. The foreign push is recorded with token kind `other`; Orqen's own pushes with `write`. |
| Merge through the fake: run `merged`, no new write by Orqen (merge stays with the user) | Passed. |
| 409 paths: wrong intent hash answers `intent_changed`, stale If-Match `revision_conflict`, approve while paused `external_writes_paused` | Passed (API; UI dialog text covered by vitest). |
| fa and en, RTL: Settings Publishing, task Review and Roles tabs (sidebar on the right, mirrored layout) | Passed. Two findings fixed: untranslated verdict, and mixed bidi in the allowlist row. |
| Overview cards (merge requests, publishing, coding slot) | Passed; a merged MR was still counted as ready (fixed). |
| Env-guard proof with a second GitLab connection on `gitlab.example.invalid` | **Partly.** The connection can be created, but allowlisting it needs a project mapped through a catalog read from that host, which would dial a non-loopback host. The refusal itself is proven by `TestEnvGuardRefusesAPublicHostWithZeroDialsAndNoGitProcess` and the Decide table. |
| Failure injection through `PUT /_fakes/host/scenario` (WorkerLies, CrashAfterAdmit, HangAfterWrite, same-family reviewer, cancel during a role) | **Not walked.** The scenario was set but the file-backed run counters had already advanced, so the scripted indexes missed. These cases are covered by the automated tests above. |
| Keyboard: dialogs trap focus (Radix), Esc closes the operation dialog | Passed for Esc (UI); focus trap relies on Radix and is covered by its own behaviour, not re-measured. |

## Findings fixed during the walk

1. The production binary never created the Codex host supervisor (only tests injected one): `App.UseCodexHost`, called from `main`.
2. `orqen-fakehost` put run folders outside `<CATHERD_HOME>/data` (Orqen refused them): fixed in the fake.
3. The merge request document never showed the reviewed head (fixed in the review stage).
4. A note planned while the MR was still simulated had no MR iid and answered 404 once live: the iid is resolved at execution time (regression test `TestS4DryRunNoteFindsTheMergeRequestOnceLive`).
5. Simulated operations could not be approved after switching to live in the UI; the Overview card counted a merged MR as ready; verdict and reason texts were missing in fa/en.
6. Test flakiness: two S4 tests asserted the `dry_run` hold before it was set (now wait for it).

## Needs the user

Run the gated spike (`docs/LIVE-TRIAL.md`) and attest it before any real coding run; create a sandbox GitLab project and your own write token
for a live trial; decide whether `git.digikala.com` is ever involved (nothing here touched it).

The `~/.claude/agents` guard line hashes `ls -la`, whose `..` entry (`~/.claude`) is rewritten by the user's own sessions; the agents directory itself is empty and unchanged since Oct 3.
