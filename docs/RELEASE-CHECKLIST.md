# Release checklist

Every step is done by you, deliberately. Nothing here is run by the coder, and none of it needs a model, a token or a network service except where it says so.

## 1. Build and check
- [ ] `make check` is green (Go with `-race`, vet, gofmt, web lint, typecheck, `check:rtl`, vitest, build, OpenAPI drift).
- [ ] `go test ./internal/extwrite -run TestCensus` passes: no write path outside `internal/extwrite`, no Jira or Confluence writes.
- [ ] `git grep -il <company name> -- ':!docs'` is empty (no company hostnames in code or fixtures).
- [ ] No new Go module or npm package since the last release without a reason in `docs/DEVIATIONS.md`.

## 2. Pin the database image
- [ ] Pull `mongo:8` yourself, read its digest (`docker buildx imagetools inspect mongo:8`) and replace the image in `docker-compose.yml` with `mongo:8@sha256:<digest>`. Remove the `TODO(user)` line. The build never pulls.

## 3. Artefacts
- [ ] `make dist` writes `bin/orqen-release`, `bin/SHA256SUMS` and `bin/THIRD_PARTY_LICENSES.txt` (offline). Open the licenses file and check no module says "no license file".
- [ ] `shasum -a 256 -c bin/SHA256SUMS` from inside `bin/`.
- [ ] Code signing and notarization (macOS) are a step for you: sign `bin/orqen-release` with your Developer ID, notarize it, staple, and regenerate `SHA256SUMS` afterwards.

## 4. Safe to run
- [ ] Personal-state guard: hash `~/.config/catherd`, `~/.local/share/catherd` (logs excluded), `~/.claude/agents`, `~/.codex`, `~/Library/LaunchAgents` before and after a test run; they must be identical. `test -e ~/.openclaw` must not change.
- [ ] launchd: Orqen only records your opt-in and shows the plist; installing it is your command (Settings, Maintenance shows the plan). Check the plan before you load it.

## 5. Upgrade and recovery
- [ ] Before an upgrade: Settings, Backups, Back up now; Verify shows integrity ok.
- [ ] Restore drill into a scratch database, with Orqen stopped on the real one:
  `orqen restore --from <backup-dir> --dry-run --db-name <scratch-db> --data-dir <scratch-dir>`, then without `--dry-run`. Start a second instance on that database, sign in with `orqen login-link`, and check: automation is paused, held runs are listed, missing credentials are named. Enter one credential and Resume.
- [ ] Drop the scratch database and delete the scratch directory.

## 6. Hand-offs that stay with you
- the MongoDB digest, code signing and notarization, the real-tool trials (GitNexus, Strix, OpenClaw Wiki) and their license decisions, and the launchd install.
