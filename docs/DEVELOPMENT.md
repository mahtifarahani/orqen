# Development guide

Technical setup, verification fixtures and implementation notes for Orqen.
Paths and commands below are relative to the repository root.

## Prerequisites

- Go 1.25 or newer, Node 22 and pnpm
- `mongod` and `mongosh` (Homebrew), or Docker. MongoDB must run as a **replica set** for
  transactions. `scripts/dev-mongo.sh` starts a project-local single-node replica set on
  `127.0.0.1:27018`; `docker-compose.yml` provides the Docker equivalent.
- macOS Keychain builds require cgo and Apple developer tools (`xcode-select --install`).
- The default `keychain` credential store uses the OS credential store (macOS Keychain).
  `ORQEN_CREDENTIAL_STORE=memory` avoids prompts, but loses secrets on restart.

## Quickstart

```sh
scripts/dev-mongo.sh start      # start | stop | status | reset
pnpm -C web install
make web-build
make dev-server                # API and UI on http://127.0.0.1:4280
make dev-web                   # optional Vite HMR on http://localhost:5173
```

On first start, the server prints a one-time bootstrap link and writes it with mode `0600` to
`<data dir>/bootstrap-url.txt` (development: `.orqen-dev/data/bootstrap-url.txt`). Open the link
and press "Claim this Orqen". The token stays in the URL fragment and expires after one hour.

To sign in from another browser after claiming the owner:

```sh
make login-link                 # one-time link, expires after 15 minutes
```

## Build and run

```sh
make build                     # builds React, then embeds it into bin/orqen
ORQEN_DATA_DIR=.orqen-dev/data ./bin/orqen serve
./bin/orqen migrate             # apply idempotent MongoDB migrations and exit
```

The binary serves its embedded UI independently of the working directory. In development
(`ORQEN_DEV=true`), it reads `web/dist` from disk. Build the UI before compiling a release.
A fresh checkout can compile before building the UI, but returns a helpful 503 until it is built.
Catherd settings: `ORQEN_CATHERD_BIN`, `ORQEN_CATHERD_PATH` (the PATH catherd runs with; default is the login-shell PATH merged with the process PATH and common install dirs) and, for tests only, `ORQEN_PERSONAL_CATHERD_HOME`.
Configuration is in `.env.example`: the CLI accepts the corresponding kebab-case flags before
or after the command. Environment variables are read directly; `.env` files are not auto-loaded.

API docs: <http://127.0.0.1:4280/api/docs>. OpenAPI: `/api/v1/openapi.json`.
Foreign Host headers are rejected even for docs and public routes.

## Manual verification with fake upstreams

`go run ./cmd/orqen-fakes` serves the deterministic GitLab (`/gitlab`, 250 projects) and Jira (`/jira`) fakes from
`internal/testfakes` on loopback and prints their URLs and test tokens. It is development-only and never part of the
`orqen` binary. Typical walk: run it, start `orqen serve` with `ORQEN_CREDENTIAL_STORE=memory`, a throwaway
`ORQEN_DATA_DIR` and `ORQEN_DB_NAME`, add connections whose allowed host is `127.0.0.1`. Results of the Sprint 2 walk:
`docs/VERIFICATION-S2.md`.

### Sprint 4 fakes

`orqen-fakes` also sets a separate fake **write token** (printed at start; it doubles as the git push token), starts a pipeline
for every pushed commit, and serves loopback admin endpoints: `POST /_fakes/gitlab/pipeline {project, sha?, status, jobs?}`,
`POST /_fakes/gitlab/push-as-other {project, branch}`, `POST /_fakes/gitlab/merge {project, iid}`, `GET /_fakes/gitlab/writes`
(every write the fake received, with the token kind, plus the recorded pushes), `POST /_fakes/gitlab/hang-after-write
{path_prefix, count}` and `PUT /_fakes/host/scenario` (writes `<--fakehost-dir>/scenario.json`). Its git repository holds
`scripts/check.sh` (passes once `src/feature.txt` contains `orqen-feature`) and `scripts/lint.sh`.

`go run ./cmd/orqen-fakehost` replaces the Codex CLI and the catherd CLI: `orqen-fakehost app-server|codex|catherd ...`
with all state in `$ORQEN_FAKEHOST_DIR`. Point `ORQEN_CODEX_BIN` and `ORQEN_CATHERD_BIN` at two wrapper scripts, for example
`#!/bin/sh` / `export ORQEN_FAKEHOST_DIR=/tmp/fh` / `exec /path/to/orqen-fakehost codex "$@"` (and `catherd "$@"`). No model, no
network, no real Codex or catherd is involved.

### Sprint 5 fakes (knowledge, graph, Confluence)

`orqen-fakes` seeds a graph by default (`-seed-graph=false` turns it off): GitLab project 2 serves a `package.json` named
`@walk/web` that depends on `demo`, the package project 1 provides, and project 1 serves a `CODEOWNERS`. `PUT /_fakes/gitlab/file
{project, path, content}` overrides any root file of one project. Confluence pages 1001 (architecture), 1002 (poisoned) and 1003
(links a repo) are served at `/confluence` (Server) and `/cloud` (Cloud).

`TestWalkSetup` (skips without `ORQEN_WALK_URL`) now also creates a Confluence connection mapped to project 1 (page 1001), rewrites
the fake Jira issue ORQ-1 so its description links that page (the run stores a source version and the packet gets a design
reference), and adds project 2 so a graph rebuild finds one inferred `depends_on` edge. With `ORQEN_WALK_LIVE=1` and
`ORQEN_WALK_WRITE` it switches external writes to live for the fake GitLab project only. Knowledge from a merged run then needs
no hand-made draft: let ORQ-1 reach the publish approval, approve it in the Inbox, then `POST /_fakes/gitlab/merge
{"project":1,"iid":<iid>}`; the merge watcher records the merge and the Knowledge page shows the proposed drafts.

## Jira intake, triage and managed worktrees (Sprint 3)

Enabled projects with a Jira mapping are polled every `intake.poll_interval_secs` (default 120 s) by a scheduler inside the
runner. Each eligible issue is read in full, snapshotted, and becomes a **run** that advances stage by stage
(`context` -> `triage` -> `plan` -> `plan_gate` -> `start_gate` -> `workspace` -> `queued`). A run that needs a person stops with
one Inbox question; answering resumes the same run. The last stage prepares a managed git worktree under
`<data dir>/workspaces/<org>/` (bare mirrors in `mirrors/`, one worktree per run in `runs/`); coding itself is Sprint 4.

- **Requirements:** `git` 2.30 or newer (the `workspace` stage reports `git_unavailable` otherwise). Git runs with its own
  `HOME`, `GIT_CONFIG_GLOBAL=/dev/null`, `credential.helper=` and a `GIT_ASKPASS` helper (`<data dir>/bin/orqen-git-askpass`,
  fixed, holds no secret), so your global git config, the macOS keychain helper and personal checkouts are never involved.
  Tokens reach git only through the environment of one process.
- **Jobs:** `intake_poll`, `issue_ingest`, `run_advance` (one active job per run), `workspace_cleanup`, `workspace_discard`.
- **Settings:** `intake.*`, `decision.jev` (`auto` asks Jev only when a key is set, `off` never), `risk.area.*`,
  `workspace.min_free_mb` (2048), `workspace.max_retained` (10), `context.packet_bytes` (16 KiB).
- **Pause:** `PUT /automation` pauses an organization: no polls, no new evaluations, runs hold at the next stage boundary.
- **Branches:** `orqen/<issue-key>/<run-suffix>`.
- External issue text (descriptions, comments, repository documents) is always treated as data: it is wrapped in marked
  source blocks, scrubbed of secrets and never changes policy, tools or credentials.
- The gated real Jev test is `ORQEN_TEST_JEV=1 go test ./internal/decision -run TestJevReal -count=1 -v` with
  `TYPESAFE_API_KEY` exported (one inference). Results of the Sprint 3 walk: `docs/VERIFICATION-S3.md`.

## API client

`api/openapi.json` is the authoritative OpenAPI 3.1 contract, embedded in the Go binary. The
server's route and request schema tests check it against the implementation. `orqen openapi`
exports the same bytes to `web/openapi.json`, and orval generates the committed TypeScript types
and TanStack Query hooks in `web/src/api/generated/`.

```sh
make openapi                   # export the contract and generate the client
make openapi-check             # fail on spec/client drift
```

## Checks

```sh
make check                     # Go formatting, vet, race tests; web checks/build; API drift
```

Unit tests run without MongoDB. Integration tests use a unique database per test and need
`scripts/dev-mongo.sh start`; they fail clearly if MongoDB is unavailable. To run only unit tests,
use `go test -short ./...`. Set `ORQEN_TEST_MONGO_URI` to use another replica set.
The opt-in OS credential-store test uses `ORQEN_TEST_KEYCHAIN=1 go test ./internal/server -run TestKeychain`.

## Web conventions

- Logical Tailwind utilities only (`ms-`, `pe-`, `start-`, `text-start`); `pnpm -C web check:rtl`
  fails on physical ones. URLs, IDs and code are wrapped in `<Ltr>`.
- Persian and English strings live in `web/src/i18n/{fa,en}.json` and must have identical keys. Sprint 6 strings live in `web/src/i18n/s6/*.{en,fa}.json`, are merged at startup (a key in the base files wins) and are checked for identical key sets by `s6-i18n.test.ts`.
- No inline scripts or CDN assets in the app UI; the server sends a Content Security Policy.
- Credentials are write-only through the API; the UI never reads secrets back.

## Sprint 4: coding, review and publishing (test-only)

Orqen can drive a Codex App Server host and catherd roles, verify the result, and prepare a Draft MR. Safety model:

- Writes to GitLab go only through `internal/extwrite`. The default mode is `dry_run`: the MR is simulated and the exact payload is recorded. Live mode needs the typed phrase, an allowlisted project, a separate write credential you enter yourself, and a per-operation approval.
- Dev and test builds can only write to loopback addresses (`ORQEN_ENV` and the build channel decide the scope). `make release` builds the release channel.
- Jira and Confluence are never written.
- Settings: `ORQEN_CODING_SLOTS` (default 1), `ORQEN_CODEX_BIN`, `ORQEN_EXTERNAL_WRITES=off` (kill switch).
- The Codex host is probed from Settings (`POST /host/probe`); the real host also needs your spike attestation (see docs/LIVE-TRIAL.md). Gated real tests stay off unless their env vars are set.

### Claude Code as the orchestration host

Set the organization policy `orchestrator.host` to `claude-code` and Orqen drives a headless Claude Code instead of the Codex App Server (`internal/claudehost`). There is no long-lived process: every directive is one `claude -p` turn on a persistent session (`--session-id`, then `--resume`), with the built-in tools off, only the `catherd` MCP server loaded (`catherd mcp`, `CATHERD_ORCHESTRATION_HOST=claude-code`) and only the directed tool allowed. The turn is held to the same rule as on Codex: one catherd call with exactly the directed arguments, else a deviation.

- Binary: `ORQEN_CLAUDE_BIN` / `--claude-bin`, else the first `claude` on the host PATH. Claude Code 2.1.282 or newer.
- Login is isolated per organization, like `CODEX_HOME`: Orqen runs claude with `HOME=<data>/host-home/<org>` and `CLAUDE_CONFIG_DIR=<data>/claude/<org>/config`. Log in once with those two variables set, for example `HOME=<data>/host-home/<org> CLAUDE_CONFIG_DIR=<data>/claude/<org>/config claude auth login`. Until then the probe reports `host_login_missing`.
  On macOS Claude keeps the login in the user keychain, found through `HOME`; Orqen links `<data>/host-home/<org>/Library/Keychains` to the real one for the Claude host only (without it, login fails with "Couldn't save your login").
- The probe (`POST /host/probe`) reads `claude --version`, `claude auth status` and the tool list of `catherd mcp`. It sends no prompt.
- Reconciling a lost directive reads the session transcript under `CLAUDE_CONFIG_DIR/projects/*/<session>.jsonl`.

## Sprint 6: extensions, skills, security scans and backups (test-only)

Orqen never downloads or installs anything. An extension is a tool you installed yourself; Orqen records the path and the checksum.

- **Extensions** (Extensions in the sidebar). Catalog: OpenClaw Wiki, GitLab MCP, mcp-atlassian, Figma MCP (detect-only, no Enable), GitNexus and Strix. Each is registered, detected, tested and enabled per organization and bound per project. A changed binary is reported as `changed` and needs a new test. Installs are immutable and can be rolled back. Credentials an extension needs are write-only.
- **GitNexus license gate.** GitNexus uses a noncommercial license. Detect stays disabled until you read the notice, pick your basis, write a note and type the extension id. The acceptance is recorded and can be revoked; a revoked license excludes the code graph from packets and marks its indexes `license_revoked`.
- **Skills** (Models, Skills tab; project Skills tab). Short instruction files per role. An imported skill is linted (secret-like values block approval, other flags need acknowledgement) and is a draft until approved. A run pins the skills it starts with (task Roles tab shows the manifest). Skill text is framed guidance, never authority.
- **Security scans** (Security page, task Security tab). Strix scans the reviewed commit in a local clone, only when you start it or policy says `manual`/`auto`. The model and the budget are never defaulted: set `security.model` and `security.budget_mode`/`security.budget_usd` in Settings, Policy. A scan that stopped on budget or turns, timed out or produced invalid output is **incomplete** and never passes the gate, even with exit code 0. A scan that changes files keeps the change as an artifact and never touches your worktree. A blocking finding can be waived with a written reason; "Issue draft" only shows text.
- **Backups and restore** (Settings, Backups and Maintenance). A backup copies the database and the artifacts into a folder, with a checksum manifest; it contains no passwords or tokens (a scanner stops it if a secret-like value is found). Verify shows what a restore would do. Restoring is a command, never an API call: stop Orqen, then `orqen restore --from <backup-dir> --dry-run` and, when satisfied, without `--dry-run` into an **empty** database and data directory. After a restore automation is paused, runs that were in progress are held (`restored`), credentials are missing until you enter them again and model profiles may need publishing again.
- **Fakes for the walk.** `orqen-fakehost gitnexus|strix` stand in for the two tools (scenario keys `gitnexus.*` and `strix.*`, e.g. `{"strix":{"scenario":"findings"}}` set through `PUT /_fakes/host/scenario`). `ORQEN_WALK_URL=... ORQEN_WALK_FAKEHOST=<orqen-fakehost binary> ORQEN_WALK_FAKEHOST_DIR=<dir> go test ./internal/server -run TestWalkSetup` also writes the two wrapper scripts (paths in the output file), sets the security policy and imports one poisoned skill.
- **Release.** `make dist` builds the release binary and writes `bin/SHA256SUMS` and `bin/THIRD_PARTY_LICENSES.txt` offline. See `docs/RELEASE-CHECKLIST.md`; the MongoDB image digest pin is yours to supply (`docker-compose.yml`).
