# PLAN-S2-GO: Sprint 2 on the Go backend (supersedes the Rust-specific parts of PLAN-S2.md)

**Status.** This document replaces every Rust-specific instruction in `docs/PLAN-S2.md`: file paths, crate names, `with_txn`/`ScopedCollection`/utoipa, `cargo` commands, and the WP17–WP30 file lists and acceptance commands. The following carry over unchanged, and this document points to them by section number instead of repeating them:
- **§0**: catherd 1.5.0 facts, with the corrections in §C below.
- **§1**: decisions D36–D66. The content stays; only the implementation language changes. Overrides are listed in §A.3.
- **§2**: data model and indexes, with the two numbering fixes in §A.4.
- **§3**: API shapes, events and error codes, plus the additions in §B.
- **§4**: frontend.
- **§5**: algorithms (readiness pseudo-code in WP24, publish/recovery in WP23, validate staging in WP23, policy resolution in WP21, catalog refresh in WP18), with these exceptions: WP21 numbering, the WP22 env and TMPDIR rule, and the job registry.
- **§6–§8**: DoD, risks and deferrals, as amended in §E and §F. The setup wizard is **no longer deferred**.

**What I verified in the Go tree (commit `6375d7c`):**
- `crates/` is **untracked leftover**: `git ls-files crates` is empty. The first Go WP deletes it from the working tree (`rm -rf crates`, after a look) and adds `/crates/` to `.gitignore`, so no one edits it by mistake.
- Patterns the new code must reuse, all in `internal/server`:
  - `DB.Txn` (driver `WithTransaction`, snapshot read concern / majority write concern)
  - org scoping by explicit `bson.M{"_id":id,"org_id":org,...}` filters (there is no `ScopedCollection`)
  - `revisionUpdate[T]` (If-Match, 409 with `current`)
  - `listPage[T]` (`_id` cursor)
  - `idempotent(w,r,scope,route,key,hash,f)`, which handles `DuplicateActiveJob` and returns 202 `deduplicated`
  - `enqueue`, `acquire`, `fenced(ctx,lease,f)` (its first statement is the fenced job touch), `completeJob`, `recordFailure` (it fails the linked operation)
  - `AppendEvent(ctx,org,kind,subject,payload,rev,op,system)` with redaction
  - `storeBlob` (first) plus `recordArtifact` (inside the fenced transaction)
  - `probeClient(conn)` (proxy and CA)
  - `problem(code)`/`validation(field,msg)`/`writeError`
  - `Route{Method,Path,Handler}` registered on Go 1.22+ `ServeMux` with `apiMiddleware` (org load, ID-prefix checks for `conId/opId/artId`)
  - DTOs are `DTO() bson.M` methods
- **The job runner is hard-wired:** `runWorkers` acquires only `[]string{"connection_test"}`, and `runJob` calls `runConnectionTest` directly. A handler registry is required (WP17 step 0).
- **The contract is hand-authored:** `api/openapi.json` (OpenAPI 3.1, embedded by `api/spec.go`). `TestContract` (`internal/server/server_test.go`) checks:
  - the route count equals `len(a.Routes())`
  - every route exists in the spec
  - the property sets of a **hand-listed** map of request structs (by `json` tag) and DTOs (by `DTO()` keys)

  `make openapi` = `go run ./cmd/orqen openapi > web/openapi.json && pnpm -C web gen:api` (orval). `make openapi-check` diffs both.

---

## A. Go-specific decisions

### A.1 Package layout (least risk: no refactor of Sprint 1 code)

| Where | What | Rule |
|---|---|---|
| `internal/server` (existing package, **new files only** plus small edits to `app.go`, `queue.go`, `db.go`, `config.go`, `probe.go`, `models.go`) | Mongo docs, DTOs, HTTP handlers, jobs, transactions, events: the **glue** | Handlers and jobs stay `App` methods, exactly like Sprint 1. No exported API churn. |
| `internal/providers` (new) | GitLab and Jira REST clients, the `CodeHost` and `IssueSource` interfaces, `ProviderError` kinds, the auth matrix, Link/offset pagination | Must not import `internal/server`. Pure `net/http`. |
| `internal/catherd` (new) | `OrgEnv`/`PersonalEnv`, `Runtime` (bin, version, child PATH), `Port` interface, `CLI` impl, `Fake` impl, stderr parsing, `MergePatch`/`PatchBetween`, `Diff`, the 1.5.0 patch-schema mirror, `Issues`, `Independence`, catalog availability | Must not import `internal/server`. |
| `internal/policy` (new) | Registry, override validation, `Resolve` with source | Pure |
| `internal/readiness` (new) | `Evaluate(Inputs) Result`. Inputs are plain structs that `internal/server` fills from its docs. | Pure |
| `internal/sysprobe` (new) | Detect-only system checks (§B.2) behind an injectable `Runner`/`Dialer`/`FS` | Pure. Keychain check in a `_darwin.go` file with `cgo` and a nocgo fallback, mirroring `credentials_darwin*.go`. |
| `internal/wizard` (new) | Step registry, criteria and consequence evaluation (pure) | Pure |
| `internal/testfakes` (new) | `FakeGitLab`, `FakeJira` (httptest-style servers under `/gitlab` and `/jira`) | Used by tests and by the dev-only `cmd/orqen-fakes`. |

Seam rule: new packages export only plain types and functions. `internal/server` converts its docs into their input structs. This keeps the pure logic unit-testable under `go test -short` with no Mongo, and leaves Sprint 1 files untouched apart from the listed wiring edits.

### A.2 Libraries and mechanics

**HTTP (providers):** stdlib `net/http`. Extract `probeClient` into `providers.NewClient(Options{Timeout, ProxyURL, CustomCAPEM})` and have `probe.go` call it. API calls set `CheckRedirect: func(...) error { return http.ErrUseLastResponse }`, so any 3xx becomes `unexpected_redirect` (D37). Every call uses `http.NewRequestWithContext`. The response body goes through `io.LimitReader` (8 MiB JSON, 256 KiB raw files with a `truncated` flag). Per-connection concurrency is a `chan struct{}` semaphore in `App.limiter map[string]chan struct{}` (mutex-guarded, keyed by `connID+":"+maxConc`) (D39).

**catherd process** (`internal/catherd/cli.go`):
```go
cmd := exec.CommandContext(ctx, rt.Bin, args...)
cmd.Dir = env.WorkDir()                       // empty non-git dir
cmd.Env = env.Vars(rt)                        // explicit slice => nothing inherited (Go's env_clear)
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) } // kill the process group: doctor spawns `catherd mcp`
cmd.WaitDelay = 2 * time.Second
stdout, stderr: bytes.Buffer behind a limitWriter (16 MiB / 64 KiB)
```
Timeouts are applied with `context.WithTimeout` (30 s; doctor 180 s; catalog refresh 120 s; sync 180 s). `OrgEnv.Vars` returns exactly:
- `PATH=rt.ChildPath`, `HOME=rt.UserHome`, `TMPDIR=<org>/tmp` (**changed from PLAN-S2 WP22**; see §C.1)
- `LANG`/`LC_ALL` if set, `HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY` if set
- `NO_COLOR=1`, `CATHERD_LOG=warn`
- `CATHERD_HOME=<org>/home`, `CATHERD_CLAUDE_AGENTS_DIR=<org>/claude-agents`
- `CATHERD_NO_SYNC=1` (omitted only for `catalog sync`)

`PersonalEnv.Vars` has the same base, `TMPDIR=<data>/catherd/_personal/tmp`, cwd `<data>/catherd/_personal/work`, plus only the inherited `CATHERD_HOME/CATHERD_CONFIG_DIR/CATHERD_DATA_DIR/XDG_CONFIG_HOME/XDG_DATA_HOME`, or `ORQEN_PERSONAL_CATHERD_HOME` for tests.

Type safety (D52 in Go):
```go
type ReadEnv interface{ Vars(*Runtime) []string; WorkDir() string }
type Port interface {
  Version(ctx) (string, error)
  ListProfiles(ctx, ReadEnv) ([]ProfileRow, error)
  ShowRaw(ctx, ReadEnv, name string) (map[string]any, error)
  ShowResolved(ctx, *OrgEnv, name string) (map[string]any, error)
  Create(ctx, *OrgEnv, name string) error
  SetKey(ctx, *OrgEnv, name, key string, value any) (SetOutcome, error)
  Validate(ctx, *OrgEnv, name string) (Validation, error)
  Remove(ctx, *OrgEnv, name string) error
  CatalogList(ctx, *OrgEnv) ([]byte, error)
  CatalogRefresh(ctx, *OrgEnv) (json.RawMessage, error)
  CatalogSync(ctx, *OrgEnv, force bool) (json.RawMessage, error)
  Doctor(ctx, *OrgEnv) (json.RawMessage, int, error)
}
```
Write methods accept only `*OrgEnv`. Its constructor `NewOrgEnv(dataDir, orgID string)` panics unless `orgID` matches `^org_[0-9A-HJKMNP-TV-Z]{26}$` and the result is under `filepath.Join(dataDir,"catherd")`.

**Child PATH** (`Runtime`): `ORQEN_CATHERD_PATH` if set. Otherwise merge, deduplicated in order:
1. the login-shell PATH, read once with `$SHELL -ilc 'printf "__P__%s" "$PATH"'` (5 s timeout)
2. the process `PATH`
3. `~/.bun/bin`, `~/.local/bin`, `~/.opencode/bin`, `/opt/homebrew/bin`, `/usr/local/bin`, `/usr/bin`, `/bin`

This follows reference-project `packages/extension/src/gateway/env.ts` and fixes PLAN-S2 risk 3 for launchd/GUI starts. `Bin` = `ORQEN_CATHERD_BIN`, else the first `catherd` found on the child PATH.

**JSON validation:** hand-written Go validators, with no JSON-Schema library. The 1.5.0 patch schema is mirrored as Go data in `catherd/schema.go`: allowed keys per level, enums, and the rung regex. Request bodies are decoded with `decodeJSON(...,strict=true)`.

**Diff and merge:** about 80 lines of Go in `catherd/merge.go` and `catherd/diff.go` over `map[string]any`, decoded with `json.Decoder.UseNumber()` so integers are preserved. Hashes use the existing `canonicalJSON` → `sha256Hex` (move `canonicalJSON` into a tiny `internal/canon` package, re-exported by a one-line wrapper in `idempotency.go`, so `internal/catherd` can share it without importing `server`).

**Globs (D46):** `github.com/bmatcuk/doublestar/v4` (`ValidatePattern`). This is the **only new module**.

**Versions:** a 20-line `parseSemver` in `internal/sysprobe/semver.go`, also used by `catherd.Runtime`.

**Lint/format:** unchanged `make check` (`gofmt -l`, `go vet`, `go test -race ./...`, web checks, openapi-check). Add the new dirs to the `gofmt -l` list (`cmd internal api web/assets.go` already covers `internal/*`). No golangci-lint (not installed, not adopted).

**cgo/Keychain:**
- Tests always use `CredentialStore="memory"`.
- `sysprobe` Keychain detection uses `credentials_darwin.go`'s query path. It only *reads* a non-existent probe item, which gives `errSecItemNotFound` without a prompt.
- The opt-in `POST /system/keychain-probe` puts, gets and deletes `cred_PREFLIGHT…` and may prompt. It runs only on a user click.
- Builds without cgo report `keychain: fail credential_store_unavailable` with the fix hint "build with cgo and Xcode CLT (`xcode-select --install`), or use ORQEN_CREDENTIAL_STORE=memory".
- The `make` targets already set `SDKROOT`. Direct `go test` may need `SDKROOT=$(xcrun --sdk macosx --show-sdk-path)`.

**Test split:**
- `go test -short ./...` covers all `internal/{providers,catherd,policy,readiness,sysprobe,wizard,canon}` tests (httptest, fake runner) and the existing unit tests.
- Mongo integration tests live in `internal/server/*_test.go` through `newTestApp(t, runner)` (they skip on `-short` and fail loudly without the replica set).
- The real catherd tests skip unless `ORQEN_TEST_CATHERD_BIN` is set. The Keychain test skips unless `ORQEN_TEST_KEYCHAIN=1`.

### A.3 Changes to D36–D66 (everything else applies as written)

- **D50** gets a reference-project-backed rationale (§C).
- **D51:** `TMPDIR` is the org's own dir. `CATHERD_ROLE` must never reach a child; `cmd.Env` being explicit guarantees this, and a unit test asserts it.
- **D63:** fakes are `internal/testfakes` (Go `httptest`) and `catherd.Fake`.
- **D64:** the new module is `doublestar/v4`. `globset` and `base64` notes are void; the stdlib `encoding/base64` is used.
- **D65:** `SchemaVersion = 2`. This deliberately makes the old Rust binary refuse the DB (it exits 3); record this in DEVIATIONS.
- **D66:** unchanged, but the client is generated from the **hand-authored** `api/openapi.json` (§A.5).

### A.4 Numbering fix (from DATABASE-RD finding 2)

Never use `max+1` and never rely on a duplicate-key retry; `DB.Txn` does not retry an ordinary duplicate-key error.
- Policy versions: inside the publish transaction, `FindOneAndUpdate(policy_drafts{_id, org_id, revision: IfMatch}, {$inc:{next_number:1, revision:1}}, After)`. `number = next_number-1`. Then insert the version. The unique index `(org_id, scope_kind, scope_id, number)` stays as a guard.
- Profile versions already use `model_profiles.next_number`. The same `$inc` and the version insert share one `DB.Txn`.
- Test both with 10 concurrent publishes: numbers are 1..10 with no gaps, and losers get 409 `revision_conflict`.

### A.5 Adding routes to the contract (every WP that adds routes)

1. **Server side.**
   - Add `Route{"METHOD","/api/v1/orgs/{orgId}/…",a.handler}` to `Routes()`.
   - Add new path-ID prefixes to the `apiMiddleware` list (`prjId:prj`, `mpfId:mpf`, `mpvId:mpv`, `polId:pol`, `rcsId:rcs`). This is a one-line extension of the existing slice; `stepKey` is validated in its handler.
   - Route templates used in `requestHash` must equal the path string exactly.
2. **Spec side.** Edit `api/openapi.json` by hand, in the existing style:
   - `operationId` = the Go handler name in snake_case (this becomes orval's hook name, e.g. `refresh_catalog` → `useRefreshCatalog`)
   - `tags`, path params with the `$ref` ID schemas, the `Idempotency-Key`/`If-Match`/`X-CSRF-Token` header params as in `test_connection`
   - the `default` → `Problem` response, and `security:[{sessionCookie:[]}]`
   - every request and DTO as a named component; list endpoints use `Page_<Dto>`
   - free-form JSON (`doc`, `resolved`, `scores`) as `{"type":"object","additionalProperties":true}` plus a named `CatherdProfileDoc` schema with typed known fields and `additionalProperties: true`
3. **Contract test.** Add each request struct to the `TestContract` request map and each `DTO()` producer to the DTO map. Add `TestContractRefs` (new, WP17), which:
   - resolves every `$ref`
   - checks operationIds are unique and snake_case
   - checks every mutating op declares `X-CSRF-Token`
   - checks every route whose handler calls `idempotencyKey(r,true)` declares `Idempotency-Key` as required. Use a table `idemRequired = map[string]bool{"POST /api/v1/orgs/{orgId}/…":true}` kept next to `Routes()` and asserted both ways.
4. Run `make openapi` and commit `api/openapi.json`, `web/openapi.json` and `web/src/api/generated/` together. `make openapi-check` must be clean.

### A.6 Job registry (prerequisite, WP17 step 0)

```go
type JobHandler func(ctx context.Context, job JobDoc, lease Lease) error
func (a *App) Handle(kind string, h JobHandler)     // called in NewApp for all built-in kinds; tests may override before Run
// runWorkers: kinds := a.handlerKinds(); acquire(..., kinds, now); runJob: h := a.handlers[job.Kind]; if h == nil → recordFailure(permanent "unknown_kind")
```
Kinds:
- `connection_test`, `catalog_refresh`, `mapping_check`, `contract_import`
- `catherd_doctor`, `catherd_catalog`, `profile_validate`, `profile_publish`, `profile_import_personal`
- `system_preflight`

`recordFailure` maps `*providers.Error` to `JobFailure{Code:"provider_"+kind, Retryable: kind∈{network_unavailable, rate_limited}, RetryAfter}` (PLAN-S2 §3 last line).

---

## B. Additions now in Sprint 2

### B.1 First-run setup wizard

**State** (collection `setup_wizards`, one doc per org; `_id = "wiz_" + orgID[4:]` keeps the prefix style):
```go
type WizardDoc struct {
  ID, OrgID string; Version int /*1*/
  Steps []WizardStep   // fixed order below
  CurrentStep string; Dismissed bool
  CompletedAt *time.Time; Revision int64; CreatedAt, UpdatedAt time.Time
}
type WizardStep struct { Key, Status string /* todo|done|skipped */; SkipReason *string; UpdatedAt *time.Time }
```
- Index: unique `(org_id)`.
- **No eager creation.** `GET` synthesizes the default (all `todo`, `current_step:"organization"`, `revision:0`) when the doc is missing. The first mutation upserts with filter `{org_id, revision: 0}` via `$setOnInsert`, or uses `revisionUpdate` when the doc exists. Sprint 1 `createOrg` stays untouched.

**Steps, in order, with server criteria** (`internal/wizard`, pure; inputs are loaded by `internal/server`):

| Key | Embeds (existing APIs) | Criteria for **done** (re-checked on every GET) | Consequences if skipped (codes, i18n `wizard.consequence.*`) |
|---|---|---|---|
| `organization` | `PATCH /orgs/{orgId}` (name, locale, timezone) | org exists (always true); completion is an explicit confirm | none |
| `connections` | connections CRUD, credential, `POST …/test` (WP17) | ≥1 gitlab connection and ≥1 jira connection with `readiness.unattended_ready` (status `verified`). Confluence/Figma shown "adapter not available yet" and never required. | `discovery_unavailable`, `automation_blocked` |
| `projects` | Discover + `from-catalog` (WP18/19), mapping + `mapping/check` (WP19), contract import/review (WP20), commands (WP19) | ≥1 project whose readiness has none of: `primary_repo_missing`, `target_branch_missing`, `jira_mapping_missing`, `jira_filter_missing`, `mapping_*`, `contract_*`, `test_command_missing`, `command_not_approved` | `automation_blocked` |
| `profile` | model profiles: create/import-personal, editor, validate, publish (WP23); binding (WP24) | ≥1 published version that is automation-ready (D56/D60) **and** bound to ≥1 project from the projects step | `automation_blocked`, `no_runs` |
| `policy` | org policy draft/publish (WP21) | the org policy has ≥1 published version. "Use product defaults" publishes v1 with `{}` overrides. | `defaults_apply` (all gates manual; not blocking) |
| `system` | preflight (§B.2) | a preflight report from the last 24 h with **no `fail` on `required` checks** | `issues_undetected` |
| `review` | per-project `GET /readiness`, `POST …/automation/enable` | always completable ("Finish") | none |

Each step DTO also carries `missing[{code, detail}]` (from the criteria) and `attention: bool`. `attention` is true when the stored status is `done` but the criteria fail now, so there is never a stale "done".

**Skip semantics (invariants, as doc comments plus tests):**
1. Skip changes only `steps[k].status`. It never writes any domain doc, never sets `automation_enabled`, never publishes, binds or approves anything.
2. The §4 readiness gate (`internal/readiness`, WP24) **does not read wizard state**. A unit test asserts that `readiness.Inputs` has no wizard field. An integration test asserts that skipping every step leaves every project's readiness blockers identical.
3. `complete` is refused (409 `wizard_step_incomplete`, `errors` = missing) unless the criteria hold. `skip` is always allowed. `reopen` sets the step back to `todo`.
4. `review/finish` is allowed with skipped steps. It sets `completed_at` but keeps listing pending steps (`pending_count` = todo+skipped+attention).

**API** (under `/api/v1/orgs/{orgId}`, tag `setup`; IM = If-Match on the wizard revision, `"0"` for a synthesized doc):

| Method + path | Headers | Body → Response |
|---|---|---|
| GET `/setup` (`get_setup`) | — | `SetupWizardDto{steps[{key,status,missing[],consequences[],attention,updated_at?}], current_step, dismissed, completed_at?, pending_count, revision}` |
| PATCH `/setup` (`patch_setup`) | IM | `{current_step?, dismissed?}` → SetupWizardDto (resume position, "hide wizard") |
| POST `/setup/steps/{stepKey}/complete` (`complete_setup_step`) | IM | → SetupWizardDto, or 409 `wizard_step_incomplete` |
| POST `/setup/steps/{stepKey}/skip` (`skip_setup_step`) | IM | `{reason?: string≤500}` → SetupWizardDto |
| POST `/setup/steps/{stepKey}/reopen` (`reopen_setup_step`) | IM | → SetupWizardDto |
| POST `/setup/finish` (`finish_setup`) | IM | → SetupWizardDto |

An unknown `stepKey` gives 404. Events: `setup.step_completed|skipped|reopened`, `setup.finished`, all with `revision`. New error code: `wizard_step_incomplete` (409).

**First-run redirect** (`web/src/features/home-redirect.tsx`):
- no orgs → `/setup/new`: the organization step, which reuses the `CreateOrgDialog` form fields inline; on 201 it navigates to `/orgs/:id/setup?step=connections`
- else, the last org's `GET /setup`: if `!dismissed && completed_at == null` → `/orgs/:id/setup`, else overview
- bootstrap success already navigates to `/` (→ HomeRedirect)

**UI** (`web/src/features/setup/`):
- `setup-page.tsx` (route `/orgs/:orgId/setup`, plus `/setup/new`)
- `stepper.tsx`: an `<ol>` with `aria-current="step"`, each item showing status text plus an icon (never color alone). It sits on the start side with logical utilities; chevrons use `rtl:rotate-180`.
- `step-footer.tsx`:
  - **Back**
  - **Skip** opens a confirm dialog listing `consequences` and the live `missing` items, then calls skip
  - **Next** calls `complete`; a 409 shows the missing list inline with links into the embedded section
  - "Save and exit" PATCHes `current_step` and goes to Overview
- **Per-step panels embed the real pages** via an `embedded` prop (hides the page header and changes "done" navigation): `ConnectionsPage`, `DiscoverPage` + `ProjectDetailPage` (tabs mapping, commands, contract), a `ProfileQuickStart` (create blank or import personal, then the `ProfileEditorPage` embedded, then validate/publish/bind to the selected projects), the policy editor (org), `SystemCheckPanel` (§B.2), and `ReviewPanel` (readiness per project + Enable).
- **Resume:** on load, go to `?step=` or `current_step`. Each Next or Back PATCHes `current_step`.
- **Re-enter:** Settings → "Setup" link, and the Overview card.
- Every panel has the PLAN-S2 §4 states (loading, empty, blocked, degraded, failed).
- **Overview "Pending setup" card** (`features/overview/pending-setup-card.tsx`): shown when `pending_count > 0`. It lists each todo, skipped or attention step with its consequence text and a "Resume setup" button (go to that step).
- **i18n:** `wizard.{title,steps.<key>.{title,description},status.{todo,done,skipped,attention},actions.{back,next,skip,finish,saveExit,resume},skipDialog.*,consequence.<code>,missing.<code>}`, `overview.pendingSetup.*`, `problems.wizard_step_incomplete`. Both `fa` and `en`; `i18n.test.ts` enforces equal key sets.

### B.2 System access and preflight step (detect-only)

`internal/sysprobe`. Every check returns:
```go
type Check struct {
  ID, Group string
  Status string // pass|warn|fail|skipped
  Detail string; FixCode string; FixCommand []string /*displayed, never executed*/
  Required bool; Blocks []string /*feature codes*/; DurationMS int64
}
```
Execution is **only** `--version` or `status`-style reads, `hello`, DNS/TCP/TLS dials, `os.Stat`, and temp-file create/remove **inside Orqen's own data dir**. Never installers, `launchctl`, `defaults`, `security add-*`, or `sudo`. A unit test greps that the `sysprobe` argv tables contain no forbidden verbs (`install`, `add`, `bootstrap`, `load`, `login`, `set`, `upgrade`). Each subprocess has a 10 s timeout, the env from `catherd.Runtime` (child PATH), and output capped at 64 KiB.

| ID | Probe | Required | Blocks (feature codes) |
|---|---|---|---|
| `mongo.replica_set` | existing `DB.ReplicaSet` + `hello` member count | yes | `everything` |
| `docker.engine` | `docker version --format {{.Server.Version}}` (fail is only a warn); `docker context show`; OrbStack detected via `/Applications/OrbStack.app` or `orb` on PATH (info only) | no | `commands_needing_docker`, `compose_mongo` |
| `keychain.access` | darwin+cgo: query the absent item (no prompt) → pass. Store `memory` → warn "secrets lost on restart". No cgo → fail. Button "Test write" calls `POST /system/keychain-probe`. | yes when `CredentialStore=="keychain"` | `credential_persistence` |
| `bin.catherd` | `catherd --version` in window 1.5.x (D61) | yes | `profile_publish`, `automation` |
| `bin.bun` | `bun --version` ≥ 1.4.0 | yes (catherd is a Bun script) | `profile_publish` |
| `bin.codex` | `codex --version` ≥ 0.159.2 (host) / 0.157.0 (worker); then `codex login status` exit code → warn if logged out | no (warn) | `coding_runs` (Sprint 4) |
| `bin.claude` | `claude --version` ≥ 2.1.282; `claude auth status --json` loggedIn → warn | no, unless a bound profile has `claude-code:` rungs, then yes | `claude_code_roles` |
| `bin.git` | `git --version` ≥ 2.30 | no (warn) | `managed_workspaces` (Sprint 3) |
| `bin.node` | `node --version` ≥ 22 | no | `mcp_connectors` (deferred) |
| `fs.data_dir`, `fs.artifacts`, `fs.catherd_home`, `fs.workspaces` | `MkdirAll` (0700 for catherd), write+fsync+remove `.orqen-probe-<ulid>`, `statfs` free space (warn < 2 GiB) | data/artifacts/catherd yes; workspaces no | `artifacts`, `profile_publish`, `managed_workspaces` |
| `net.<conId>` per active connection | DNS → TCP → TLS, reusing the probe stages without auth, through the connection's proxy and CA. Also: CA PEM `NotAfter` (warn < 30 days, fail if expired) and proxy reachability. A host resolving to RFC1918/CGNAT (100.64/10) with a failed connect gives fix code `vpn_required_maybe`. | no | `connection:<id>` |
| `env.proxy` | report `HTTPS_PROXY`/`NO_PROXY` presence (values redacted to scheme+host) | no (info → `pass`) | — |
| `launchd.start_at_login` | `os.Stat(~/Library/LaunchAgents/dev.orqen.server.plist)`; if present, `launchctl print gui/<uid>/dev.orqen.server` (read-only) | no; `skipped` unless the user opted in | `start_at_login` |

Jobs and API (tag `system`):
- `POST /system/preflight` (`run_preflight`, Idem) → 202. Job `system_preflight` (dedupe `system_preflight`, max 2 attempts). It runs checks concurrently (errgroup limit 4) outside transactions, stores the artifact `system_preflight_report`, then in a fenced transaction upserts `system_states{_id:"sys_"+orgID[4:], org_id, last_preflight{op_id, checked_at, checks[], artifact_id}}` and appends event `system.preflight_completed`. Re-check means a new operation. It is **never on a timer**; GET only reads the stored report.
- `GET /system/preflight` (`get_preflight`) → `PreflightDto{checked_at?, operation_id?, checks[]}`. With no report, `{checked_at:null, checks:[]}`.
- `POST /system/keychain-probe` (`probe_keychain`, Idem, sync 200 `{status, detail}`): user-triggered put/get/delete of `cred_` + a ULID probe ref. It may show an OS prompt; the UI says so beforehand.
- `GET /system/launchd` (`get_launchd_plan`) → `{opted_in, plist_path:"~/Library/LaunchAgents/dev.orqen.server.plist", plist_xml, install_commands[], uninstall_commands[]}`. The plist is **generated text only**: ProgramArguments = `os.Executable()` + `serve`, plus `ORQEN_DATA_DIR`/`ORQEN_CATHERD_PATH` env. The commands are displayed for the user to run themselves: `cp …`, `launchctl bootstrap gui/$(id -u) …`, `launchctl bootout …`. Orqen never writes into `~/Library/LaunchAgents` and never runs `launchctl` write verbs.
- `PATCH /system/launchd` (IM on `system_states`) `{opted_in: bool}` records the opt-in only.

The `system` wizard step runs `run_preflight` on entry if no report from the last 24 h exists (one key per entry intent). **`catherd doctor` is not part of preflight.** It writes state and spawns `catherd mcp` (reference-project `docs/research/catherd-doctor-setup.md` "Side effects"), so it stays an explicit button (WP22), shown in the same panel.

Fix-hint rendering: `wizard.fix.<FixCode>` text plus `FixCommand` displayed in `<Ltr>` monospace with a Copy button, and a **Re-check** button (a new operation).

---

## C. What reference-project informs (read-only reference; not a dependency, no styles copied)

**C.1 Corrections and additions to the PLAN-S2 §0 facts:**
1. **`CATHERD_ROLE` and a role-scratch `TMPDIR` make catherd refuse writes with `E_ROLE_SCOPE`.** Sources: `reference-project/docs/research/catherd-1.5-upgrade.md` §2; `packages/extension/src/gateway/env.ts` `STRIPPED_PREFIXES`. PLAN-S2 WP22 passed the inherited `TMPDIR` through; Orqen now sets `TMPDIR` to its own `<org>/tmp` and never inherits `CATHERD_ROLE` (the explicit `cmd.Env`). This is a unit test.
2. **doctor writes more than locks and logs:** it rewrites the backends' model listings (discovery), and `catalog refresh` does the same (`docs/research/catherd-doctor-setup.md` §1 and §2). Consequences:
   - after a `catherd_doctor` job, Orqen re-runs `catalog list` in the same job to refresh the snapshot (so `listed` is current)
   - doctor is never run on a timer
   - reference-project also records "probes up to 15 s each", so the 180 s doctor timeout stays
3. **`catalog_query`/`catalog list` details:**
   - `treatLike` is an object `{like, source: "shipped"|"user"}`, not a string
   - `name` can be `null` for listing-only models
   - a rung whose values are all `confidence: "inferred"` is *displayed* as unscored by reference-project

   Source: `docs/architecture/gateway.md` "Contract details found live". Orqen's `CatalogDto` rung therefore uses `treat_like?: {like, source}`, `name: string|null`, plus a derived `inferred_only: bool` badge. Availability (D57) stays keyed on `enabled`, so catherd decides scoredness.
4. **A refused `profile_set` (MCP) returns structured `{saved:false, errors[{path,message,fix}], …}`** (`packages/compat/fixtures/1.5.0/mcp-profile_set-refused.json`). This confirms D50's trade-off: the CLI gives only stderr text, which is why the staging profile is followed by `profile validate --json` for structured issues.
5. **No validation of unsaved drafts and no `expect` on saves are public gaps** (`docs/research/public-surface-gaps.md` #3 and #5). This confirms PLAN-S2's staging-profile design and the immutable `o-{slug}-v{n}` names, which make `expect` unnecessary.
6. **The host can also be set via the env `CATHERD_ORCHESTRATION_HOST`** (reference-project sets `claude-code`, `env.ts`). Orqen keeps the explicit `--host codex` flag, never sets that env, and never inherits it.

**C.2 Confirms the adapter decisions:**
- **`catherd mcp` reconciles all runs at start and writes files** (`docs/decisions/0006-long-lived-mcp-per-repo.md`, `docs/architecture/gateway.md` invariants). This is why Orqen stays CLI-only (D50) and must never claim or mark-read runs in Sprint 4. Adopt reference-project's forbidden-tool list from `packages/extension/src/gateway/mcp-tools.ts` as Sprint 4 guidance.
- **Detectors must have no side effects, and installers run only on a user click** (`AGENTS.md` hard rule 5; `packages/extension/src/setup/detect.ts`, which uses `bunx --no-install`). This is mirrored in §B.2. Orqen goes further: it offers no installers at all, only commands for the user to copy.
- **Version gating:** reference-project refuses to run outside `packages/compat/compat.json`, as Orqen's D61 window does. Each Orqen release records `catherd` 1.5.x fixtures recorded **by Orqen** from the real binary in a temp home. reference-project fixtures are a shape reference only; they are not copied.
- **Merge patch:** `packages/protocol/src/profile-patch.ts` implements the same RFC 7396 `mergePatch`/`describePatch` against a view shape. This validates D55/WP23's `PatchBetween` and the path-flattened diff.

**C.3 UX borrowed for Models & catherd** (behavior only; Orqen uses raw shadcn and its own tokens). Source: `docs/architecture/webview.md` (Profile and Models rows):
- **Role rows:** a switch, name and "starting model +n more", expanding in place. Access as a 3-option segmented control (read-only / workspace-write / full) with the **enforcement word** next to it (from `profile show --json` `enforcement`). An ordered ladder with ↑ ↓ ✕. **Add model** opens a search list from the catalog; unscored entries ask for a treat-like and are disabled in Orqen until mapped (treat-like editing stays deferred, so show the reason). A start-on (default) rung select.
- **Settings as plain labelled rows:** Routing (cost/speed segmented, Jev switch), Budget (plus Orqen's explicit "no cap" acknowledgement, D60), Limits (idle, wall, heavy slots, preflight switch), Failover (with catherd's stand-in note), Billing, Isolation, Notify. A `NumberField` keeps half-typed input local and commits only positive values.
- **Models page:** search, role/backend pill filters, a "scored only" switch, and rung chips with text glyphs: usable ✓, "≈ treated like X", "? unscored", plus the cost tier and a "not offered by this account" warning. Orqen adds the D57 reason text and `inferred_only`.
- **Not borrowed:** reference-project auto-saves straight into catherd (600 ms) and "picking a profile makes it globally active". Orqen must not do either (SPEC §6): drafts live in Mongo, and Publish creates an immutable org-namespace profile that is never activated.
- **Setup UX:** item levels "gate / start / optional / info" (`docs/architecture/setup.md` "Items and levels") map onto Orqen preflight `required` + `blocks[]`. The last doctor report is persisted and shown without re-running, which Orqen already does in `catherd_states.last_doctor`.

---

## D. Work packages (Go)

**Common to all WPs:**
- Run `make fmt`, then `make check`.
- Backend WPs that add routes follow §A.5 and end with `make openapi`.
- Integration tests need `scripts/dev-mongo.sh start`.
- Commit messages are conventional, ending with the attribution line.

**Commit order** (one committer at a time; lane A = Go, lane B = web):

`WP17 → WP18 → WP19 → WP25 → WP20 → WP21 → WP22 → WP32 → WP23 → WP24 → WP31 → WP26 → WP27 → WP28 → WP29 → WP33 → WP34 → WP30`

- Lane B may develop in parallel against a locally regenerated client, but commits only after the lane-A WP whose contract it consumes is committed. It rebases, runs `make openapi-check`, and never edits `api/`, `internal/`, `cmd/` or the generated client.
- Lane A edits `web/` only through `make openapi` output.

### WP17: Seam, job registry, providers, real connection-test stages
**Files:**
- `.gitignore` (`/crates/`)
- `internal/canon/canon.go` (moved `canonicalJSON`)
- `internal/server/jobs.go` (registry §A.6)
- `queue.go` (`runWorkers`/`runJob` use the registry)
- `internal/providers/{client.go, errors.go, auth.go, gitlab.go, jira.go, iface.go}`
- `internal/testfakes/{gitlab.go, jira.go}`
- `internal/server/probe.go` (stages auth/identity/capabilities/sample_access for gitlab and jira via providers; `NewClient` replaces `probeClient`)
- `models.go` (`ConnectionDoc` gets `AuthUsername *string`, `Identity *ProviderIdentity`, `Capabilities *CapabilitySnapshot`, `LastTest.TestedMaterialHash`; status `verified`)
- `connections.go` (PATCH `auth_username`; readiness reasons per PLAN-S2 WP17)
- `db.go` (`SchemaVersion=2`, `indexes v2` appended)
- `api/openapi.json`, plus `TestContractRefs`

**Signatures:**
```go
type CodeHost interface {
  Identity(ctx) (Identity, error); Capabilities(ctx) (Capabilities, error)
  ListProjectsPage(ctx, cursor string) (ProjectsPage, error)
  GetProject(ctx, id string) (Project, error); ListBranches(ctx, pid, search string, limit int) ([]Branch, error)
  GetBranch(ctx, pid, name string) (Branch, error); ListTree(ctx, pid, ref, path string) ([]TreeEntry, error)
  ReadFileRaw(ctx, pid, ref, path string, max int64) ([]byte, bool, error); ResolveCommit(ctx, pid, ref string) (string, error)
}
type IssueSource interface {
  Identity(ctx) (Identity, error); Capabilities(ctx) (Capabilities, error)
  ListProjects(ctx, query string, startAt, limit int) (JiraProjectsPage, error)
  GetProject(ctx, id string) (JiraProject, error)
  ListBoards(ctx, projectID string, startAt, limit int) (BoardsPage, error)
  GetBoard(ctx, id string) (Board, error); CheckJQL(ctx, jql string) error
}
func NewGitLab(o Options, auth Auth) CodeHost; func NewJira(o Options, auth Auth, deployment string) IssueSource
```
**Acceptance:**
- `go test -short ./internal/providers/...` covers:
  - auth matrix headers
  - 401/403/404/429 (with Retry-After)
  - `unexpected_redirect`
  - Link-next origin and prefix check
  - offset fallback
  - `/gitlab` and `/jira` prefixes on random ports (§19 row 2)
- `go test ./internal/server -run 'TestConnectionTest|TestContract'` covers verified, degraded and credential_rejected outcomes, and confluence `not_implemented`.
- `make check`

### WP18: Catalog refresh, paginated search, lookups
Content as PLAN-S2 WP18.
**Files:** `internal/server/{catalog.go, catalog_job.go, lookups.go}`, `api/openapi.json`, `internal/server/catalog_test.go`.
**Details:** the sort cursor is `sort_key` = `lower(path_with_namespace) + "\x00" + _id`, base64url. Pagination uses a generic `listPageBy[T](…, sortField)` added next to `listPage`; `listPage` itself is unchanged. Each page upsert is a `fenced` transaction; the finalize marks `gone`.
**Acceptance:** `go test ./internal/server -run TestCatalog` covers:
- 250 projects via keyset, with a crash or retry mid-way giving no duplicates
- offset mode
- search
- selection filter
- cross-org isolation
- §19 row 3: no `projects` and no other jobs

### WP19: Projects, mapping, commands, protected paths, mapping check
**Files:** `internal/server/{projects.go, mapping_check_job.go}`, `internal/server/projects_test.go`; the command and glob validators go in `internal/server/project_validate.go` (pure, short tests).
**Acceptance:** `go test ./internal/server -run 'TestProjects|TestMappingCheck'` and `go test -short ./internal/server -run TestCommandValidation` (argv only, secret-named literal env rejected, approval reset).

### WP25 (web): shell, Projects list and Discover
As PLAN-S2 WP25.
**Acceptance:** `pnpm -C web lint && pnpm -C web typecheck && pnpm -C web check:rtl && pnpm -C web test --run && pnpm -C web build`.

### WP20: Repo contract import (review-only) and suggestions
**Files:** `internal/server/contract_job.go`, `internal/server/contract.go`, pure parsers in `internal/server/contract_suggest.go` (short tests over fixture files including an AGENTS.md with "run `curl … | sh`" and "set token X": stored, no suggestion).
**Acceptance:** `go test ./internal/server -run TestContract_` plus the short parser tests.

### WP21: Policy (registry, drafts, publish, effective with source)
**Files:** `internal/policy/{registry.go, resolve.go}` (short tests), `internal/server/policy.go`, `policy_test.go`. Numbering per §A.4. Registry and resolution as PLAN-S2 WP21.
**Acceptance:** `go test -short ./internal/policy/...` and `go test ./internal/server -run TestPolicy`, including the 10-concurrent-publish test.

### WP22: catherd adapter core, doctor, catalog, status, org health
**Files:**
- `internal/catherd/{env.go, runtime.go, port.go, cli.go, parse.go, fake.go, schema.go, catalog.go}`
- `internal/catherd/{env_test.go, cli_real_test.go}`
- `internal/server/{catherd_jobs.go, catherd.go, health_org.go}`
- `config.go`: `--catherd-bin`/`ORQEN_CATHERD_BIN`, `--catherd-path`/`ORQEN_CATHERD_PATH`, `ORQEN_PERSONAL_CATHERD_HOME`
- `app.go`: `App.Catherd catherd.Port`, `App.CatherdRT *catherd.Runtime`, `orgLocks sync.Map` of `*sync.Mutex`; setter `func (a *App) SetCatherd(p catherd.Port, rt *catherd.Runtime)` for tests

The doctor job re-runs `catalog list` afterwards (§C.1.2).
**Acceptance:**
- `go test -short ./internal/catherd/...` asserts:
  - `cmd.Env` exactly (no `CATHERD_ROLE`, `CODEX_*`, `CLAUDE_CODE_*`, parent `CATHERD_CONFIG_DIR`/`DATA_DIR`; `TMPDIR` = org tmp)
  - homes are under `data/catherd/org_…`
  - parse fixtures
- `go test ./internal/server -run TestCatherd` (fake)
- `ORQEN_TEST_CATHERD_BIN=$HOME/.bun/bin/catherd go test ./internal/catherd -run TestReal -count=1 -v`, which runs PLAN-S2 real tests (a)–(d) and the personal-manifest guard

### WP32: System preflight backend (new)
Depends on WP22's `Runtime` (child PATH, catherd version).
**Files:**
- `internal/sysprobe/{check.go, runner.go, semver.go, binaries.go, network.go, fs.go, docker.go, launchd.go, keychain_darwin.go, keychain_darwin_nocgo.go, keychain_other.go}` and their short tests (fake runner, fake dialer, temp FS, a forbidden-verb test)
- `internal/server/{preflight.go, preflight_job.go}`
- `db.go` (index `system_states(org_id)` unique)
- `api/openapi.json`

**Acceptance:**
- `go test -short ./internal/sysprobe/...`
- `go test ./internal/server -run TestPreflight`:
  - a report with `bin.catherd` fail when bin is unset
  - `net.<conId>` pass against the testfake
  - an expired CA fails
  - Re-check creates a new operation; GET never runs probes
  - the launchd plan returns text, and the test asserts that no file appears under a temp `HOME/Library/LaunchAgents`
- `ORQEN_TEST_KEYCHAIN=1 go test ./internal/server -run TestKeychainProbe` (opt-in)

### WP23: Model profiles (draft, Orqen issues, staging validate, diff, publish, rollback, personal import)
Algorithms exactly as PLAN-S2 WP23 steps 1–10, with these changes:
- `next_number` uses `$inc` in the same transaction (§A.4)
- the job bodies call `a.Catherd` **outside** transactions; only finalizes are `fenced`
- the per-org mutex comes from `orgLocks`

**Files:**
- `internal/catherd/{merge.go, diff.go, issues.go, independence.go}` (short tests, with merge vectors recorded from the real binary into `internal/catherd/testdata/`)
- `internal/server/{model_profiles.go, profile_validate_job.go, profile_publish_job.go, profile_import_job.go, profile_sweep.go}`
- `model_profiles_test.go`

**Acceptance:**
- `go test -short ./internal/catherd/...`
- `go test ./internal/server -run TestModelProfiles`: all PLAN-S2 WP23 fake tests, including crash injection via `catherd.Fake.CrashAfterSets(3)` and the hash-match finalize branch
- `ORQEN_TEST_CATHERD_BIN=$HOME/.bun/bin/catherd go test ./internal/server -run TestCatherdReal -count=1 -v`: tests (e)–(h), including the personal manifest identity and the real `~/.config/catherd` manifest before and after

### WP24: Binding, readiness gate, enable/disable, org health
**Files:** `internal/readiness/evaluate.go` (pure, PLAN-S2 WP24 pseudo-code verbatim; **no wizard input**), `internal/server/{readiness.go, automation.go}`, `readiness_test.go`.
**Acceptance:** `go test -short ./internal/readiness/...` and `go test ./internal/server -run TestReadiness` (PLAN-S2 WP24 tests 1–7).

### WP31: Setup wizard backend (new)
Depends on WP17–WP24 and WP32.
**Files:** `internal/wizard/{steps.go, criteria.go}` (pure, short tests), `internal/server/{wizard.go, wizard_test.go}`, `db.go` (index `setup_wizards(org_id)` unique), `api/openapi.json`.
**Criteria inputs** are built from existing loaders (connections readiness, project readiness results, published versions plus bindings, policy versions, `system_states.last_preflight`).
**Acceptance:** `go test -short ./internal/wizard/...` and `go test ./internal/server -run TestWizard`:
- a synthesized GET has revision 0
- complete gives 409 with `missing` until the criteria hold
- skip of every step leaves every `GET /projects/{id}/readiness` byte-identical (the authority invariant)
- `attention` appears when a verified connection loses its credential
- stale If-Match gives 409
- cross-org 404

### WP26 / WP27 / WP28 / WP29 (web)
As PLAN-S2, with these additions:
- **WP27** applies §C.3 (role rows, segmented access with the enforcement word, ↑↓✕ ladder, chip glyphs, `treat_like{like,source}`, `inferred_only` badge).
- Every page gains an `embedded?: boolean` prop (no header; an `onDone` callback). Tests assert that embedded mode renders without the page title.

### WP33 (web): Setup wizard UI (new)
Depends on WP31 and WP25–WP29 (it embeds those pages).
**Files:**
- `web/src/features/setup/{setup-page.tsx, new-org-step.tsx, stepper.tsx, step-footer.tsx, skip-dialog.tsx, profile-quick-start.tsx, review-panel.tsx}`
- `web/src/api/hooks/setup.ts`
- `router.tsx` (`/setup/new`, `/orgs/:orgId/setup`)
- `features/home-redirect.tsx`
- i18n `en`/`fa`
- tests `test/setup.test.tsx`

**Acceptance tests (vitest, mocked hooks):**
- the redirect logic for 0 orgs, an incomplete wizard and a dismissed wizard
- Skip shows consequences and calls skip only after confirm
- Next on 409 renders the `missing` list
- resume opens `current_step`
- `fa` sets `dir=rtl` with the stepper on the right, and chevrons get the `rtl:rotate-180` class

Then `pnpm` lint, typecheck, check:rtl, test and build.

### WP34 (web): System check panel and Overview "Pending setup" card (new)
Depends on WP32 and WP33.
**Files:** `features/setup/system-check-panel.tsx` (groups, status text + icon, fix text, `FixCommand` in `<Ltr>` with Copy, Re-check via `useOperationRunner`, Keychain "Test write" with a pre-warning, launchd opt-in showing the plist and commands), `features/overview/pending-setup-card.tsx`, `api/hooks/system.ts`.
**Tests:**
- no button ever calls an endpoint other than the documented ones (assert `fetch` URLs)
- Copy does not execute anything
- the card lists skipped steps with consequence text

**Acceptance:** same as WP33.

### WP30: Verification and docs (last)
PLAN-S2 WP30 steps, re-expressed:
1. `make check`.
2. `ORQEN_TEST_CATHERD_BIN=$HOME/.bun/bin/catherd go test ./internal/catherd ./internal/server -run 'TestReal|TestCatherdReal' -count=1 -v`.
3. The personal-hash guard before and after: `find ~/.config/catherd -type f -exec shasum {} + | sort | shasum`, plus `ls -la ~/.claude/agents`.
4. The manual Browser-pane checklist with `go run ./cmd/orqen-fakes` (a dev-only main serving `internal/testfakes` and printing its URLs) and `ORQEN_CREDENTIAL_STORE=memory`. Add these items to PLAN-S2 WP30's list:
   - **First run:** bootstrap → `/setup/new` → org → connections (both verified) → **skip** projects (the consequence dialog appears) → profile → policy "use defaults" → system check (Re-check; fix hints; launchd shows commands only, and `~/Library/LaunchAgents` is unchanged) → review. Overview shows "Pending setup: projects". Resume from the card, finish projects, and enable automation from review.
   - fa/en, RTL and keyboard through the stepper.
5. Update `docs/DEVIATIONS.md` (Sprint 2 Go section, including schema v2 refusing the Rust binary), `docs/VERIFICATION.md` and the README (new env vars, `orqen-fakes`).

---

## E. Definition of Done changes (add to PLAN-S2 §6; all other rows stand, with "WP" references now meaning the Go WPs above)

| Requirement | Shown by |
|---|---|
| First-run setup wizard, all 7 steps, each skippable, resumable and re-enterable; server-side state | WP31 tests (criteria, skip, reopen, attention, If-Match), WP33 UI tests, WP30 checklist first-run item |
| Skip never marks anything ready; §4 gate authoritative | WP31 "skip-all leaves readiness byte-identical" test; `readiness.Inputs` has no wizard field (unit test) |
| System access/preflight is detect-only with pass/warn/fail/skipped, fix hints, Re-check and feature mapping; nothing installed, no system settings changed | WP32 forbidden-verb test, launchd-no-file test, Keychain probe only on explicit request; WP34 fetch-URL test; WP30 checklist |
| Personal catherd config untouched, including role-env leakage | WP22 env test (no `CATHERD_ROLE`, own `TMPDIR`), WP23 real tests (g) and (h), WP30 hash guard |
| Contract kept in sync | `TestContract` + `TestContractRefs` + `make openapi-check` green |
| Policy and profile version numbering correct under concurrency | §A.4 tests (WP21 and WP23) |

## F. Risks (additions to PLAN-S2 §7)

1. **The hand-authored OpenAPI can drift from the handlers on fields that `TestContract` does not list** (for example response headers). Mitigation: §A.5 maps plus `TestContractRefs`; every new request/DTO must be registered (code review checklist).
2. **`internal/server` keeps growing as one package.** It is accepted for Sprint 2 because the pure logic lives in new packages. Splitting the glue is a post-Sprint 2 refactor with no behavior change.
3. **Schema v2 makes the old Rust binary refuse the database.** This is intentional; it is documented in DEVIATIONS.
4. **Login-shell PATH probing runs `$SHELL -ilc` once.** A slow or broken shell rc delays the first catherd call by up to 5 s. The fallback is the process PATH plus the well-known dirs, and preflight reports which PATH was used.
5. **Keychain detection** may still prompt on some macOS configurations when querying. The query targets Orqen's own service only, and the write probe is explicit.
6. **The VPN hint is heuristic** (private or CGNAT address plus failed connect). It is worded as "may be required" and never shown as a certainty.
7. **Embedding full pages in the wizard** couples it to their layout. The `embedded` prop is the only seam; WP33 comes after WP25–WP29, so the embedded pages are already final.

## G. Needs the user (in addition to PLAN-S2 §8)

- Confirm that schema v2 retiring Rust-binary compatibility is acceptable.
- Approve `github.com/bmatcuk/doublestar/v4` as the only new Go module.
- Confirm the launchd label `dev.orqen.server` and the plist path; Orqen only displays commands.
- The real GitLab/Jira tokens and the decisions on the Jev/AA keys are unchanged from PLAN-S2 §8.

**Deferred (removed from PLAN-S2 §8):** "Setup wizard page" is now in Sprint 2. Still deferred: installers of any kind (by design, never), Confluence/Figma adapters, treat-like editing, and Playwright.

### Critical files
- internal/server/queue.go (registry; `fenced`, `recordFailure`)
- internal/server/app.go (`Routes()`, `apiMiddleware` ID prefixes, App wiring)
- internal/server/idempotency.go and internal/server/db.go (`idempotent`, `Txn`, `revisionUpdate`, migrations v2)
- api/openapi.json and internal/server/server_test.go (`TestContract`)
- internal/server/probe.go (client extraction, adapter stages)
- /Users/you/node_modules/catherd-cli/src/domain/profile.ts and /Users/you/node_modules/catherd-cli/src/infra/paths.ts (patch schema and state-dir rules to mirror)
- reference-project reference: /Users/you/Projects/reference-project/packages/extension/src/gateway/env.ts, /Users/you/Projects/reference-project/docs/research/catherd-1.5-upgrade.md, /Users/you/Projects/reference-project/docs/research/catherd-doctor-setup.md, /Users/you/Projects/reference-project/docs/research/public-surface-gaps.md, /Users/you/Projects/reference-project/docs/architecture/gateway.md, /Users/you/Projects/reference-project/docs/architecture/webview.md, /Users/you/Projects/reference-project/docs/architecture/setup.md, /Users/you/Projects/reference-project/packages/protocol/src/profile-patch.ts, /Users/you/Projects/reference-project/packages/compat/fixtures/1.5.0/mcp-profile_set-refused.json, /Users/you/Projects/reference-project/docs/decisions/0006-long-lived-mcp-per-repo.md

**Research safety:** catherd ran only with `CATHERD_HOME` and `CATHERD_CLAUDE_AGENTS_DIR` under `.../scratchpad/catherd-research`. The `~/.config/catherd` aggregate sha stayed `7818e850…` before and after, and `~/.claude/agents` stayed empty. reference-project and the Go tree were only read.
