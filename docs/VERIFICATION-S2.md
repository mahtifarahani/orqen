# Sprint 2 verification

Date: 2026-10-16. Build: the committed tree after WP34 plus the verification fixes. Environment: the built binary
(`make build`) on an isolated data directory and a separate MongoDB database (`orqen_verify`) on the dev replica set,
`ORQEN_CREDENTIAL_STORE=memory`, `cmd/orqen-fakes` as GitLab and Jira (`go run ./cmd/orqen-fakes`), Browser pane at
1280x900. Connections, credentials and the 409 setup used a few `fetch` calls from the page console where the UI
form would only have repeated Sprint 1 behavior; each is noted. Every other step used the UI.

## Automated

| Item | Result |
|---|---|
| `make check` (gofmt, vet, `go test -race`, web lint, typecheck, `check:rtl`, vitest, build, openapi drift) | Passed before the walk. The web gates were re-run (77 tests) after every later fix. |
| Real catherd 1.5.0 tests `TestRealVersionListAndCatalog`, `TestRealStagingProfileRoundTrip`, `TestCatherdReal` | Passed. |
| Personal catherd hash guard (`~/.config/catherd`, `~/.local/share/catherd`, `~/.claude/agents`, plus `~/Library/LaunchAgents` listing) before and after the walk and the real tests | Identical. |

## Manual checklist

| Item | Result |
|---|---|
| Bootstrap, then `/` goes to `/setup/new` (no organization) | Passed (bootstrap itself went to `/orgs`; fixed to `/`). |
| Create the organization in the wizard, land on Connections | Passed. The organization step was left "to do"; fixed so creating it confirms the step. |
| GitLab and Jira connections reach `verified` with identity, server and capability chips in the test panel (custom port and `/gitlab`, `/jira` prefixes) | Passed (connections and credentials were created by `fetch`). |
| Connections Next, Projects Skip with the consequence dialog (consequences and live missing list, reason box), step shows Skipped | Passed. |
| Model profile step: new profile, embedded editor; Next with an incomplete step lists what is missing (409) and stays; Skip | Passed. |
| Policy "Use product defaults" publishes v1; Next completes | Passed. |
| System check: runs once on entry, groups with pass/warn/fail/skipped words, fix hints, copyable commands, Re-check, Keychain "Test write" warning text; launchd opt-in shows the plist and commands only | Passed. `~/Library/LaunchAgents` listing unchanged. The Keychain "Test write" button was not pressed (it would touch the OS store); the memory store was reported as a warning with its fix hint. |
| Review step lists pending steps; Finish; Overview "Pending setup" lists skipped steps with consequence text; Resume opens the step | Passed. |
| Resume: finish Projects via Discover (refresh gave 250 projects), add `proj-1`, open it in the embedded editor | Passed. |
| Mapping tab: Jira connection, project lookup (ORQ, WEB, OPS), JQL, save; Check mapping (4 results passed) | Passed. The check result did not appear until reload; fixed. |
| 409 on mapping save: conflict alert with the user's input kept; "Keep my changes" then Save succeeds | Passed. |
| Import contract: AGENTS.md is listed and shown as text but yields no suggestion; suggestions for lint, test and build offered; Mark reviewed; accept the pnpm test suggestion (unapproved) and Approve | Passed. |
| Models catalog: "List" refresh on real catherd (48 models); filters; disabled rows show the availability reason; picker adds a rung | Passed. Only `claude` and `claude-code` models exist on this machine. |
| Profile validation result shown in the editor | Passed after a fix (the result was not shown until reload). Orqen's own checks reported the draft invalid (reviewer shares the coders' model family, catalog lacks the default codex rungs), so catherd was not called. |
| Publish, rollback, diff against a published version | Not verifiable here: publishing needs a draft that Orqen's checks accept, which needs two model families in the catalog. Covered by unit tests (disabled Publish when stale, diff paths LTR) and the real-catherd Go tests (publish and import against temporary homes). The draft-versus-latest diff sheet was opened in fa and rendered. |
| `catherd doctor`, bind a profile, enable automation | Not run: doctor spawns catherd's helper process and is excluded by the safety rule. Readiness showed exactly the two expected blockers (`profile not bound`, `doctor not run`) with the Enable button disabled and the reasons listed. |
| fa and en: Overview, Models (catalog, profile editor, diff sheet), Settings policy tab, Health, project mapping tab, setup system step, stepper | Passed after the root-cause fix below (see "RTL root cause"). Containers, tabs, filter rows, tables and the sheet (opens on the end side, the left in fa) flow from the right; URLs, ids, model ids, commands and JSON stay left-to-right. |
| Keyboard: Escape closes a dialog and focus returns to its opener; Tab and Enter move through the stepper; a step change moves focus to the step heading; Select opens with the keyboard | Passed. |
| Real GitLab and Jira tokens | Not verifiable: none exist. Only the fakes were used. |

## RTL root cause

Radix primitives (Tabs, Select, Switch, menus) do not read `<html dir>` and stamp `dir="ltr"` on their own root when no
`DirectionProvider` is mounted. Everything inside `<Tabs>` (the whole Models page, the project tabs, Settings) therefore
flowed left-to-right in Persian while the text stayed right-aligned. `LocaleDirection` now mounts the provider at the
app root, `check:rtl` fails if it is missing, and `direction.test.tsx` renders Tabs under `fa` and `en`.

## Fixes made while verifying

- Bootstrap success goes to `/` so the first-run redirect applies.
- Creating the organization in the wizard confirms its step.
- The Overview connection counter now includes `verified`.
- Mapping check and profile validation results are read from live data (they change without a revision bump).
- Fix hints for `claude_logged_out`, `codex_logged_out`, `credential_store_memory`, `docker_not_running`,
  `catherd_version_unsupported` and `ca_expired`.

## Not covered

Confluence and Figma adapters (not built), treat-like editing (deferred), Playwright (deferred), the macOS Keychain
write probe, launchd installation (by design never done by Orqen).

## Addendum 2 (WP35-WP40): orchestrator host and catherd keys

Date: 2026-10-19. Build: HEAD after WP39. Environment: `make build` binary on port 4391, database `orqen_verify_s2a2` (dropped afterwards),
`ORQEN_CREDENTIAL_STORE=memory`, `cmd/orqen-fakes` as GitLab, a **stub catherd** script (not the real one, kept outside the repo, logs
argv shape and the NAMES of injected secret variables only) as `ORQEN_CATHERD_BIN`, Browser pane 800x600 (no viewport emulation used).
Setup (organization, GitLab connection, project, profile publish, binding) used an HTTP script; every checked step below used the UI.

| Item | Result |
|---|---|
| `make check` (gofmt, vet, race tests, web lint/typecheck/check:rtl/vitest 95 tests/build, openapi drift) | Passed. One unrelated discover test was flaky once under load and passed on re-run. |
| WP35 leak test `TestSecretLeak` (sentinel per kind through doctor, sync, listings, validate, publish, check; API responses, all Mongo documents, DataDir files, slog, Fake calls, no `credentials.json`; present only in the MemoryStore) | Passed. |
| Real catherd 1.5.0: `TestCatherdRealSecrets` (isolated temp home and temp HOME, sentinel `TYPESAFE_API_KEY`, one real `doctor`), `TestCatherdReal`, `TestRealVersionListAndCatalog`, `TestRealStagingProfileRoundTrip` | Passed. Doctor exit 3 (codex not logged in in the temp HOME) with a `jev` row; sentinel on no disk file. |
| Personal-state hash guard (`~/.config/catherd`, `~/.local/share/catherd`, `ls -la ~/.claude/agents`, `ls -la ~/Library/LaunchAgents`) before and after the real tests and after the browser walk | Identical. |
| Placeholder Jev key: Set (dialog closes, value in no DOM or log), Check shows Refused (real Jev answered 401 to the placeholder), Remove | Passed. |
| Real `orqen` binary injected `TYPESAFE_API_KEY` into `doctor` only while the key was set (stub logged the variable name); placeholder in no server log and no data file | Passed. |
| Switch host to claude-code: dialog with adapter notice, policy draft opens with the value prefilled, UI Publish; project readiness shows `profile_host_mismatch` and `host_adapter_unavailable` | Passed. |
| "Republish for current host" in the profile editor creates v2 "Built for claude-code", v1 unchanged; switch back to codex via the same flow clears both blockers | Passed. |
| Doctor per host from the card (codex and claude-code each keep their own report) | Passed. |
| fa and en: Orchestrator & keys tab | Passed; in fa the cards and table flow from the right, env names stay left-to-right. |
| `security find-generic-password -s dev.orqen.credential` lists only `cred_*` accounts | **Not verified**: the walk used the memory store. Needs the user with the Keychain store. |
| Real Jev/AA/xAI/Gemini valid keys; real catherd on `--host claude-code` for publish | Not verifiable (no keys; real catalog has one model family). Doctor/publish per host covered by the stub and Fake tests. |
| Wizard profile-step cards, key preflight rows | Covered by vitest (WP39); not walked in the browser. |
| `opencode auth list --format json` in the preflight | Fake-runner tests only; not run against the machine's opencode (it talks to a running background service). |
