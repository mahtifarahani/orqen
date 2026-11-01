# Orqen: Implementation Plan, First Build Slice ("Sprint 1 + Shell")

> Historical Rust implementation record. The Go migration supersedes its backend paths, commands and dependency choices; see [GO-MIGRATION.md](GO-MIGRATION.md) and [README](../README.md).

Source: `docs/SPEC.md` (read in full). Machine facts I verified read-only:
- Docker Compose v5.5.0 is installed, but the Docker daemon (OrbStack socket) is **not running**.
- The Homebrew `mongod` 8.3.7 on `127.0.0.1:27017` is **standalone**: `db.hello().setName` is absent, so it cannot run transactions.
- Nothing listens on 27018 or 4280.
- No `~/.cargo` or `~/.rustup`; no `protoc`.
- Apple Silicon (arm64), macOS 27.0.1, Xcode CLT present.
- `codex` and `claude` are on PATH; `jev` and `openclaw` are not; `catherd` is 1.5.0.

---

## 0. Slice decision

**Chosen slice: SPEC §18 Sprint 1, plus a minimal frontend shell, driven end to end by one real use case: the "Connection Test" operation.**

I accept the default with three changes:

1. **No synthetic demo endpoints.** Jobs/lease/fencing, idempotency, SSE, operations (202) and the artifact store are all exercised by `POST /connections/{id}/test`. That operation is a real job. It runs the network, TLS, HTTP-reachability and redirect-scope stages against whatever base URL the user enters, so it works against any self-hosted Jira or GitLab with no account. It also persists a report artifact.
   - Stages that need provider adapters (auth, identity, capabilities, sample access) report `not_implemented`. They are never reported as passed.
   - This lets every infrastructure piece be verified through real behaviour.
2. **Deferred out of Sprint 1:**
   - org template export/import
   - a separate `outbox` collection (in this slice the `jobs` collection *is* the transactional outbox: domain write and job enqueue happen in one transaction)
   - launchd
   - backup
3. **Spike 0 is user-run** (§5 has exact steps). It needs the real Codex host and real accounts. It does not block this slice, because the slice has no dependency on catherd or the host.

---

## 1. Key decisions

| # | Topic | Decision | Reason |
|---|---|---|---|
| D1 | Rust install | Official rustup: `curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs \| sh -s -- -y --profile minimal --default-toolchain stable -c rustfmt -c clippy`. **Needs user approval** (download + execute). Every later shell command prefixes `. "$HOME/.cargo/env" &&`. | Canonical installer; shell state does not persist between coder commands. |
| D2 | Toolchain pin | `rust-toolchain.toml` with `channel = "<exact X.Y.Z printed by rustc --version after install>"`, `components = ["rustfmt","clippy"]`, `profile = "minimal"`. Edition **2024**. | Reproducible builds; the exact version is recorded rather than guessed. |
| D3 | Cargo layout | Workspace with **one crate**, `crates/orqen-server` (lib + `[[bin]] name = "orqen"`). The workspace root holds `[workspace.dependencies]` and `[workspace.lints]`. Future adapters become new crates. | One crate means low friction for a medium coder. The workspace is ready to split later. Module boundaries come from Rust modules (D5). |
| D4 | Crate versions policy | Add with `cargo add <crate>@<MAJOR>` (majors in D24). Commit `Cargo.lock`. No `*`. All versions live in `[workspace.dependencies]`. Before committing, `cargo tree -d` must show no duplicate `bson`. Use `mongodb::bson` (the re-export) and never add `bson` directly. | Latest compatible minor versions; the APIs in this plan assume these majors. |
| D5 | Module boundaries (§14 subset) | Top-level modules: `organizations`, `connections`, `credentials` (infra used by connections), `queue`, `operations`, `events`, `artifacts`, `idempotency`, `auth`, plus infra (`db`, `http`, `error`, `ids`, `config`, `cli`, `app`, `openapi`). Each domain module has `model.rs` (Mongo docs), `service.rs` (the only cross-module API), and `routes.rs` (HTTP + DTOs). A module **must not** touch another module's collection directly; it calls that module's service. **No stub modules** for workflow, projects, host_adapter, etc. | Follows §14 without dead code. |
| D6 | MongoDB for dev/tests | A **separate project-local mongod** running as a single-node replica set on `127.0.0.1:27018`, rs name `orqen-rs`, dbpath `.orqen-dev/mongo`, started by `scripts/dev-mongo.sh`. The Homebrew instance on 27017 is untouched. `docker-compose.yml` (mongo:8 single-node RS on 127.0.0.1:27018) is also shipped as the §3 product default, but it is verified by the user later because the Docker daemon is not running. Default URI: `mongodb://127.0.0.1:27018/?replicaSet=orqen-rs&directConnection=true`. | Transactions need a replica set; Docker is down; the Homebrew service must not be reconfigured. |
| D7 | Replica-set guard | At startup the server runs `hello`. If `setName` is missing it exits with code 2 and the message `MongoDB is not a replica set; transactions are required (see scripts/dev-mongo.sh)`. | Fail fast instead of failing mid-transaction. |
| D8 | Test DB isolation | Each integration test uses DB `orqen_test_<ulid>` on `ORQEN_TEST_MONGO_URI` (default as in D6) and drops it at the end (best effort). If Mongo is unreachable the test **panics** with `run: scripts/dev-mongo.sh start`. It never silently skips. | Deterministic, parallel-safe. |
| D9 | IDs | String `_id` = type prefix + ULID: `org_`, `con_`, `cred_`, `job_`, `op_`, `art_`, `sess_`, `idem_`, `tok_`. Newtypes live in `ids.rs`, with `serde` + `ToSchema`, `parse()` validation (`^prefix_[0-9A-HJKMNP-TV-Z]{26}$`), and `new()`. Events use a numeric `seq` (D16). | Sortable (cursor pagination), URL-safe, self-describing. A wrong-type ID gives 404 or 422 instead of matching another collection. |
| D10 | External identity | `IssueIdentity { org_id, connection_id, provider_issue_id }` and `RepoIdentity { org_id, connection_id, provider_project_id }` are defined now (types + unit tests, no collection yet). Their canonical string is `"{org}/{con}/{provider_id}"`. Keys, URLs and namespaces are **never** part of identity. | §4: a Jira key alone is not unique. |
| D11 | Org scoping | Every org-owned document has `org_id`. All access goes through `ScopedCollection<T>`, built only from an `OrgScope`. The `OrgScope` axum extractor reads `{orgId}` from the path and loads the org (404 if missing or archived). A record from another org returns **404, never 403**. | §14: scope comes from the path and the record together. |
| D12 | Credentials | `trait CredentialStore` (async-trait) with `put(ref, secret)`, `exists(ref)`, `get(ref)`, `delete(ref)`. Implementations: `KeychainStore` (crate `keyring` v3, feature `apple-native`, service `dev.orqen.credential`, account = `cred_…`) and `MemoryStore` (tests only). Selected by `ORQEN_CREDENTIAL_STORE=keychain\|memory`; default `keychain`, and `memory` logs a WARN. Each replacement creates a **new** `cred_` ref, writes it, updates the doc (If-Match), then deletes the old ref. Mongo stores only `credential_ref`, `credential_state`, `credential_updated_at`. Secrets are never returned, logged, or put in events or artifacts. | §17. The trait makes tests Keychain-free. |
| D13 | Owner bootstrap | If no owner exists at startup, the server creates a one-time bootstrap token (32 random bytes, base64url; only its SHA-256 is stored; expires in 1 h; earlier unused bootstrap tokens are invalidated). It prints `http://127.0.0.1:{port}/bootstrap#token=…` and writes it to `{data_dir}/bootstrap-url.txt` (mode 0600). The fragment keeps the token out of server logs. `orqen login-link` (CLI) creates a 15-minute `login` token for new browsers. | Single local owner (§17) with a recovery path. |
| D14 | Session / CSRF / origin | **Bind:** loopback only (reject non-loopback bind). **Host:** must be `127.0.0.1:{port}`, `localhost:{port}`, `[::1]:{port}`, or a configured dev host; otherwise 421 (prevents DNS rebinding). **Origin:** every non-GET/HEAD/OPTIONS request needs an `Origin` header in the allowlist (`http://127.0.0.1:{port}`, `http://localhost:{port}`, plus `ORQEN_DEV_ORIGINS`, default `http://localhost:5173` only when `--dev`); otherwise 403 `origin_rejected`. **Session:** cookie `orqen_session_{port}`, HttpOnly, SameSite=Strict, Path=/, Max-Age 30 d; the DB stores its SHA-256. **CSRF:** a synchronizer token per session, returned by `GET /api/v1/auth/status`; the `X-CSRF-Token` header is required on non-safe methods once authenticated and compared in constant time (`subtle`). | Defence in depth for a browser-reachable loopback service. Plain http on loopback means no `Secure`/`__Host-` prefix, so the port suffix avoids collisions between instances. |
| D15 | Error model | `AppError` (thiserror) is rendered as RFC 9457 `application/problem+json`: `{type:"https://orqen.dev/problems/{code}", title, status, code, detail, request_id, errors?: [{field, message}], current?: <resource>}`. Separately, `ProviderError { kind: ProviderErrorKind, code: String, message, retry_after_ms: Option<u64> }` with kinds exactly as in §14: `PermissionDenied, CredentialExpired, NetworkUnavailable, RateLimited, UnsupportedCapability, WriteOutcomeUnknown, InvalidOutput`, and `retryable()` true only for `NetworkUnavailable` and `RateLimited`. | Typed and machine-readable; the frontend switches on `code`. |
| D16 | Events + SSE cursor | Collection `events` with a gap-free, commit-ordered `seq: i64`, taken from `counters {_id:"events"}` by `$inc` **inside the same transaction** as the domain write. Concurrent writers hit a write conflict on the counter doc and retry, so seq order equals commit order. SSE `GET /api/v1/orgs/{orgId}/events/stream`: cursor = `max(Last-Event-ID, ?after)`; replay with `seq > cursor` in batches of 500; then wait on an in-process `tokio::sync::Notify` (signalled after each commit) or 2 s, whichever comes first, and re-query. `id:` = seq, `retry: 3000`, keep-alive comment every 15 s. No change streams. | Correct resume without missed events and no race; simple. |
| D17 | Idempotency | Header `Idempotency-Key` (1–255 visible ASCII). Collection `idempotency_records`, unique on `(scope, route, key)`. The request hash covers method, route template, path params and canonical JSON. Full algorithm in WP6. **Required** on `POST /connections/{id}/test`; **optional** on `POST /orgs` and `POST /connections`. 24 h TTL. On replay, the stored response is returned with `Idempotent-Replayed: true`. Plus a business-level dedupe: a partial unique index on active jobs `(org_id, dedupe_key)`. | §14/§16: duplicate Start has no second effect. |
| D18 | Revisions | Every mutable doc has `revision: i64` (starts at 1). Responses carry `ETag: "{revision}"` and `revision` in the body. PATCH/PUT/DELETE require `If-Match: "{n}"`: missing gives 428 `precondition_required`, a mismatch gives 409 `revision_conflict` with `current`. The update is a single `find_one_and_update({_id, org_id, revision: n}, {$set…, $inc:{revision:1}})`. | §14/§16: no lost updates. |
| D19 | Jobs lease/fencing | Mongo `jobs` with `fence: i64` incremented on every acquisition. Every write a job makes happens in a transaction whose first statement is a fenced touch of the job doc. Full invariants and pseudo-code in WP8. Defaults: lease TTL 30 s, heartbeat 10 s, max attempts 5, backoff `min(2^attempts s, 300 s) ± 20 %` jitter. All store methods take `now` explicitly, so tests are deterministic. | §15/§16. Snapshot isolation plus a write conflict on the job doc makes stale holders fail safely. |
| D20 | Operations | `operations` collection: `{_id: op_, org_id, kind, subject:{kind,id}, status: pending\|running\|succeeded\|failed, job_id, result: Option<json>, problem: Option<Problem>, created_at, updated_at, revision}`. Long commands return **202** with an `OperationDto` and a `Location` header. | §14. |
| D21 | Artifact store | Per-org content-addressed files: `{data_dir}/artifacts/{org_id}/sha256/{hh}/{hh}/{hex64}`, plus temp dir `{data_dir}/artifacts/.tmp/`. Write path: stream to temp, compute SHA-256 while streaming, enforce the size cap (default 64 MiB), fsync, atomic rename (if the target exists, delete the temp), fsync the parent dir, then upsert the Mongo record (unique on `(org_id, sha256)`). Read paths are computed only from the record's `org_id` + validated hex and never from user input. | §15; per-org directories keep isolation and later deletion simple. |
| D22 | OpenAPI → TS | `utoipa` v5 + `utoipa-axum` `OpenApiRouter`/`routes!`. `orqen openapi` prints JSON to stdout, saved as `web/openapi.json` (committed). TS types via **`openapi-typescript`** into `web/src/api/schema.d.ts` (committed); client via **`openapi-fetch`**; React hooks hand-written on TanStack Query. A drift check regenerates both and runs `git diff --exit-code`. **Superseded:** the client is generated with orval (see docs/DEVIATIONS.md). | Smallest typed toolchain with no Java generator; no drift (§14). |
| D23 | Serving the UI | `rust-embed` v8 with `#[folder = "$CARGO_MANIFEST_DIR/../../web/dist"]` and `#[allow_missing = true]`. Debug builds read from disk; release builds embed. Non-`/api` GETs serve the file, else fall back to `index.html` (SPA). `/assets/*` gets `Cache-Control: public, max-age=31536000, immutable`; `index.html` gets `no-cache`. If dist is missing, `/` returns a 503 text page: "UI not built: run make web-build". | One executable (§3). |
| D24 | Rust crates (major) | `axum@0.8` (macros), `tokio@1` (full), `tower@0.5`, `tower-http@0.6` (trace, request-id, set-header, limit), `serde@1` (derive), `serde_json@1` (**without** `preserve_order`, so maps are sorted for canonical hashing), `mongodb@3`, `utoipa@5` (axum_extras), `utoipa-axum@0.2`, `thiserror@2`, `anyhow@1` (main/cli only), `tracing@0.1`, `tracing-subscriber@0.3` (env-filter, json), `clap@4` (derive, env), `ulid@1`, `sha2@0.10`, `hex@0.4`, `base64@0.22`, `rand@0.9`, `subtle@2`, `url@2`, `reqwest@0.12` (default-features=false; rustls-tls, json, stream), `keyring@3` (apple-native), `rust-embed@8`, `mime_guess@2`, `futures@0.3`, `tokio-util@0.7`, `tokio-stream@0.1` (sync), `async-trait@0.1`, `chrono@0.4` + `chrono-tz@0.10` (timezone validation only), `dirs@6`. Dev: `reqwest` (+cookies), `tempfile@3`. If a newer major exists, still use these majors. | Mainstream, matches §14. |
| D25 | Time | Mongo models use `bson::DateTime`. DTOs use RFC 3339 strings (`bson::DateTime::try_to_rfc3339_string`). chrono is used only for timezone parsing. | Avoids bson/chrono feature coupling. |
| D26 | Transactions helper | Own retry loop, `db::txn::with_txn` (WP2 pseudo-code). No `and_run`. | Avoids closure-lifetime pitfalls. |
| D27 | Frontend stack | pnpm; Vite (latest) + React 19 + TS strict; Tailwind v4 via `@tailwindcss/vite`; shadcn/ui (latest CLI, style `radix-nova` if offered, otherwise `new-york`; base color neutral; RTL enabled); lucide-react; **react-router v7** (`createBrowserRouter`, library mode); **@tanstack/react-query v5**; **i18next + react-i18next**; sonner (toasts); `@fontsource-variable/vazirmatn` for fa (bundled, no CDN). | Mainstream, simple, with §13 RTL support. |
| D28 | Locale / RTL | Active locale = `localStorage["orqen.locale"]`, else the current org's `locale`, else `en`. `<html lang dir>` is set from the locale (`fa` gives `rtl`). Only logical Tailwind utilities (`ms-/me-/ps-/pe-/start-/end-/text-start/text-end`); a script fails the build on physical ones. URLs, IDs, code and commands are wrapped in an `<Ltr>` component (`<bdi dir="ltr" class="font-mono">`). Dates and numbers via `Intl` with `fa-IR` or `en-US`. | §13. |
| D29 | Theme | `ThemeProvider` with light, dark and system; a `class="dark"` on `<html>`; stored in `localStorage["orqen.theme"]`. | §13. |
| D30 | Tests | **Rust unit tests** (no Mongo): ids, URL normalization, problem mapping, canonical hash, backoff, redaction, identity. **Rust integration tests** (`crates/orqen-server/tests/`, need D6 Mongo): real server on `127.0.0.1:0`, a reqwest client with a cookie store and an `Origin` header, and an axum fake upstream server on an ephemeral port. **Web**: vitest + Testing Library + jsdom. **Manual UI checklist** via the Browser preview. Playwright is deferred (it downloads a browser). | Covers race and network cases with fakes (§19 last paragraph). |
| D31 | Lint / format / CI | `make check` = `cargo fmt --all --check` → `cargo clippy --workspace --all-targets -- -D warnings` → `cargo test --workspace` → `pnpm -C web lint` → `pnpm -C web typecheck` → `pnpm -C web check:rtl` → `pnpm -C web test --run` → `pnpm -C web build` → `make openapi-check`. Workspace lints: `unsafe_code = "forbid"`, `clippy::unwrap_used = "warn"` (allowed in tests). Hosted CI is deferred (open item). | Local gate equals future CI. |
| D32 | Config | clap with env fallbacks, prefix `ORQEN_`: `PORT=4280`, `MONGO_URI` (D6), `DB_NAME=orqen`, `DATA_DIR` (default `~/Library/Application Support/Orqen` via `dirs::data_dir()`), `CREDENTIAL_STORE=keychain`, `DEV=false`, `DEV_ORIGINS`, `LEASE_TTL_SECS=30`, `WORKER_CONCURRENCY=4`, `LOG_FORMAT=pretty\|json`, `ARTIFACT_MAX_BYTES=67108864`. The dev Makefile uses `DATA_DIR=.orqen-dev/data`. | One place for configuration. |
| D33 | Schema versioning | Collection `schema_meta {_id:"schema", version}`. `SCHEMA_VERSION = 1` in code. Startup: if DB version > code version, refuse to start (exit 3). Otherwise run migrations in order (v1 = create all indexes, idempotently). | §15: incompatible versions are detected at startup. |
| D34 | Redaction | `events::redact(Value)` replaces the value of any key matching `(?i)secret\|token\|password\|authorization\|cookie\|credential(?!_ref)` with `"[REDACTED]"`, recursively. It is applied to every event payload and artifact JSON report. | §15/§17. |
| D35 | Repo / VCS | `git init` in WP0 (local only) and a commit per WP **if the user approves** (open item). | Enables the drift check. |

---

## 2. Repo layout

```
orqen/
├─ Cargo.toml                    # [workspace] members=["crates/*"], resolver="3", workspace.dependencies, workspace.lints
├─ Cargo.lock
├─ rust-toolchain.toml
├─ rustfmt.toml                  # edition="2024", max_width=100
├─ Makefile
├─ docker-compose.yml            # mongo:8, 127.0.0.1:27018, replSet orqen-rs, volume orqen-mongo, init healthcheck
├─ .env.example
├─ .gitignore                    # target/, web/node_modules/, web/dist/, .orqen-dev/
├─ README.md                     # quickstart: install, dev-mongo, make dev-server, make dev-web, bootstrap
├─ docs/SPEC.md  docs/PLAN.md
├─ scripts/
│  ├─ dev-mongo.sh               # start|stop|status|reset
│  └─ check.sh                   # = make check (bash, set -euo pipefail)
├─ crates/orqen-server/
│  ├─ Cargo.toml
│  ├─ src/
│  │  ├─ main.rs                 # fn main() -> orqen_server::cli::run()
│  │  ├─ lib.rs
│  │  ├─ cli.rs                  # serve | openapi | login-link | migrate
│  │  ├─ config.rs
│  │  ├─ app.rs                  # AppState, build_router(), serve(), shutdown
│  │  ├─ openapi.rs
│  │  ├─ error.rs                # AppError, Problem, ProviderError(Kind)
│  │  ├─ ids.rs                  # prefixed ULID newtypes, IssueIdentity, RepoIdentity
│  │  ├─ db/{mod.rs, txn.rs, migrate.rs, scoped.rs, counters.rs}
│  │  ├─ http/
│  │  │  ├─ mod.rs, pagination.rs, ui.rs
│  │  │  ├─ middleware/{request_id.rs, host.rs, origin.rs, session.rs, csrf.rs}
│  │  │  └─ extract/{org_scope.rs, if_match.rs, idempotency_key.rs}
│  │  ├─ auth/{mod.rs, model.rs, service.rs, routes.rs, tokens.rs}
│  │  ├─ organizations/{mod.rs, model.rs, service.rs, routes.rs}
│  │  ├─ connections/{mod.rs, model.rs, service.rs, routes.rs, url.rs, probe.rs}
│  │  ├─ credentials/{mod.rs, keychain.rs, memory.rs}
│  │  ├─ idempotency/{mod.rs, service.rs}
│  │  ├─ queue/{mod.rs, model.rs, store.rs, runner.rs, backoff.rs}
│  │  ├─ operations/{mod.rs, model.rs, service.rs, routes.rs}
│  │  ├─ events/{mod.rs, model.rs, writer.rs, notify.rs, redact.rs, routes.rs}
│  │  └─ artifacts/{mod.rs, model.rs, fs_store.rs, service.rs, routes.rs}
│  └─ tests/
│     ├─ common/{mod.rs, fake_upstream.rs}
│     ├─ health_test.rs  auth_test.rs  orgs_test.rs  scope_isolation_test.rs
│     ├─ idempotency_test.rs  jobs_lease_test.rs  events_sse_test.rs
│     ├─ artifacts_test.rs  connection_test_flow_test.rs  restart_test.rs
│     └─ keychain_test.rs        # #[ignore], manual
└─ web/
   ├─ package.json  pnpm-lock.yaml  index.html  vite.config.ts  vitest.config.ts
   ├─ tsconfig.json  tsconfig.app.json  tsconfig.node.json  eslint.config.js  components.json
   ├─ openapi.json               # generated, committed
   ├─ scripts/check-rtl.mjs
   └─ src/
      ├─ main.tsx  router.tsx  index.css
      ├─ api/{schema.d.ts (generated), client.ts, problem.ts, idempotency.ts, sse.ts, keys.ts}
      ├─ api/hooks/{auth.ts, orgs.ts, connections.ts, operations.ts, events.ts}
      ├─ i18n/{index.ts, en.json, fa.json, direction.ts}
      ├─ lib/utils.ts
      ├─ components/ui/*         # shadcn generated
      ├─ components/{app-shell.tsx, sidebar-nav.tsx, org-switcher.tsx, locale-toggle.tsx,
      │              theme-provider.tsx, theme-toggle.tsx, ltr.tsx, status-badge.tsx, live-indicator.tsx}
      ├─ features/auth/{bootstrap-page.tsx, login-page.tsx, require-session.tsx}
      ├─ features/orgs/{orgs-page.tsx, create-org-dialog.tsx, org-settings-page.tsx}
      ├─ features/connections/{connections-page.tsx, connection-form-dialog.tsx,
      │                         credential-dialog.tsx, connection-test-panel.tsx}
      ├─ features/overview/overview-page.tsx
      └─ test/{setup.ts, i18n.test.ts, direction.test.tsx, problem.test.ts, sse.test.ts}
```

---

## 3. Work packages

Conventions for **all** WPs:
- Every Rust command starts with `. "$HOME/.cargo/env" &&`.
- Run integration tests only after `scripts/dev-mongo.sh start`.
- Each WP ends with `cargo fmt --all && cargo clippy --workspace --all-targets -- -D warnings` clean, plus its own acceptance check.
- No `unwrap()` outside tests.
- All handlers return `Result<_, AppError>`.

### Dependency graph and parallel lanes

```
WP0 → WP1 → WP2 → {WP3, WP4, WP9a} → WP5 → WP6 → WP7 → WP8 → WP10 → WP11 → WP12
WP13 (web scaffold) can start after WP1 (needs openapi cmd) and runs in parallel with WP2–WP11
WP14 needs WP5 + WP13; WP15 needs WP12 + WP14; WP16 last
```

Parallel lanes for the coder:
- **Lane A** (Rust): WP2 → WP12.
- **Lane B** (Web): WP13 any time after WP1, WP14 after WP5, WP15 after WP12.

---

### WP0: Toolchain and repo bootstrap ⚠ needs user approval for the rustup install

**Goal:** Rust installed and pinned, empty workspace that builds, dev Mongo replica set running.

**Steps:**
1. Ask the user to approve D1, then run it. Run `rustc --version` and record X.Y.Z.
2. `git init` (if approved). Create `.gitignore`, `rust-toolchain.toml` (D2), `rustfmt.toml`, and the root `Cargo.toml` with a `[workspace]` containing:
   ```toml
   [workspace.lints.rust]
   unsafe_code = "forbid"

   [workspace.lints.clippy]
   unwrap_used = "warn"
   ```
3. `cargo new --lib crates/orqen-server`. Add `[[bin]] name="orqen" path="src/main.rs"` and `[lints] workspace = true`. `main.rs` prints the version.
4. `scripts/dev-mongo.sh` (bash, `set -euo pipefail`, `ROOT=$(cd "$(dirname "$0")/.." && pwd)`, `DIR=$ROOT/.orqen-dev`, `PORT=${ORQEN_DEV_MONGO_PORT:-27018}`):
   - **start:** `mkdir -p $DIR/mongo`. If `mongosh --port $PORT --quiet --eval 1` succeeds, say "already running". Otherwise run `mongod --replSet orqen-rs --port $PORT --bind_ip 127.0.0.1 --dbpath $DIR/mongo --logpath $DIR/mongo.log --fork --pidfilepath $DIR/mongo.pid`. Then:
     ```
     mongosh --port $PORT --quiet --eval 'try{rs.status().ok}catch(e){rs.initiate({_id:"orqen-rs",members:[{_id:0,host:"127.0.0.1:'$PORT'"}]})}'
     ```
     Then loop up to 30 times with `sleep 1` until `db.hello().isWritablePrimary` is true.
   - **stop:** `mongosh --port $PORT admin --quiet --eval 'db.shutdownServer()' || true`.
   - **status:** print `rs.status().ok` and `setName`.
   - **reset:** stop, then `rm -rf $DIR/mongo`.
5. `docker-compose.yml`:
   - service `mongo`, image `mongo:8`
   - `command: ["--replSet","orqen-rs","--bind_ip_all","--port","27017"]`
   - `ports: ["127.0.0.1:27018:27017"]`, volume `orqen-mongo:/data/db`
   - healthcheck: `mongosh --quiet --eval "try{rs.status().ok}catch(e){rs.initiate({_id:'orqen-rs',members:[{_id:0,host:'127.0.0.1:27017'}]}).ok}"` with interval 5s
   - Comment: image digest gets pinned at release.
6. `Makefile` targets:
   - `dev-mongo`
   - `dev-server` (`ORQEN_DEV=true ORQEN_DATA_DIR=.orqen-dev/data ORQEN_CREDENTIAL_STORE=keychain cargo run -p orqen-server -- serve`)
   - `dev-web` (`pnpm -C web dev`)
   - `openapi` (`cargo run -q -p orqen-server -- openapi > web/openapi.json && pnpm -C web gen:api`)
   - `openapi-check` (`$(MAKE) openapi && git diff --exit-code -- web/openapi.json web/src/api/schema.d.ts`)
   - `web-build`
   - `build` (`web-build` then `cargo build --release -p orqen-server`)
   - `check` (D31 list)
7. Write `.env.example` with every D32 variable.

**Accept:**
- `cargo build` succeeds.
- `scripts/dev-mongo.sh start && mongosh --port 27018 --quiet --eval 'db.hello().setName'` prints `orqen-rs`.
- `mongosh --port 27017 --quiet --eval 'db.hello().setName'` still prints nothing (Homebrew instance untouched).

---

### WP1: Server skeleton, config, errors, ids, DB connect, migrations, health, OpenAPI

**Files:** `cli.rs`, `config.rs`, `app.rs`, `error.rs`, `ids.rs`, `db/mod.rs`, `db/migrate.rs`, `openapi.rs`, `http/mod.rs`, `http/middleware/request_id.rs`, `lib.rs`.

**Interfaces:**
```rust
pub struct Config { port:u16, mongo_uri:String, db_name:String, data_dir:PathBuf, credential_store:CredStoreKind,
  dev:bool, dev_origins:Vec<String>, lease_ttl:Duration, worker_concurrency:usize, log_format:LogFormat, artifact_max_bytes:u64 }
#[derive(Clone)] pub struct AppState(Arc<Inner>); // Inner{ config, db: Db, creds: Arc<dyn CredentialStore>, events: EventNotifier, ... } (fields added in later WPs)
pub struct Db { pub client: mongodb::Client, pub db: mongodb::Database }
impl Db { pub async fn connect(uri,&str name)->Result<Db,AppError>; pub async fn assert_replica_set(&self)->Result<(),AppError>; pub fn coll<T>(&self,name:&str)->Collection<T> }
pub async fn migrate(db:&Db)->Result<u32,AppError>  // returns schema version
```

**`error.rs`:**
```rust
#[derive(thiserror::Error, Debug)]
pub enum AppError {
  Validation(Vec<FieldError>),            // 422 validation_failed
  NotFound,                               // 404 not_found
  RevisionConflict{ current: serde_json::Value }, // 409 revision_conflict
  PreconditionRequired,                   // 428 precondition_required
  IdempotencyKeyReused,                   // 422 idempotency_key_reused
  IdempotencyInProgress,                  // 409 idempotency_in_progress (+Retry-After: 1)
  Unauthenticated,                        // 401 unauthenticated
  CsrfFailed,                             // 403 csrf_failed
  OriginRejected,                         // 403 origin_rejected
  HostRejected,                           // 421 host_rejected
  AlreadyBootstrapped,                    // 409 already_bootstrapped
  InvalidToken,                           // 401 invalid_token
  PayloadTooLarge,                        // 413 payload_too_large
  Provider(ProviderError),                // 502 provider_<kind snake>
  LeaseLost,                              // internal; 500 if it ever surfaces
  Db(#[from] mongodb::error::Error),      // 503 if network/selection error, else 500 internal
  Internal(String),                       // 500 internal
}
impl IntoResponse for AppError  // problem+json; detail for Internal/Db is "internal error, see logs request_id=…" (never leak)
```

**IDs:** a macro `define_id!(OrgId, "org")` generating `new()`, `as_str()`, `FromStr` (validated), `Display`, Serialize/Deserialize as string, `ToSchema` (type string, pattern).

**`migrate` v1:** creates all indexes listed in each WP (collect them in `migrate.rs` as WPs land; WP1 only creates `schema_meta`). Uses `create_index` with explicit names (idempotent).

**`cli`:**
- `serve` sequence: tracing init → connect → `assert_replica_set` → `migrate` → `create_dir_all(data_dir)` → bind `127.0.0.1:{port}` → router → graceful shutdown on ctrl_c/SIGTERM.
- `openapi` prints `ApiDoc::openapi().to_pretty_json()`.
- `migrate` runs migrations and exits.

**Routes:** `GET /api/v1/health` returns `{status:"ok", version, schema_version, mongo:{ok, replica_set}}` (no auth). Route under `/api/*` that matches nothing gives a 404 problem.

**Accept:**
- `cargo run -p orqen-server -- serve` (dev env), then `curl -s 127.0.0.1:4280/api/v1/health` gives `"status":"ok"` and `"replica_set":"orqen-rs"`.
- With `ORQEN_MONGO_URI=mongodb://127.0.0.1:27017` the process exits with code 2 and the replica-set message.
- `cargo run -q -- openapi | jq .openapi` gives `"3.1.0"`.
- Unit tests for ids pass (`cargo test --lib`).

---

### WP2: Transaction helper, ScopedCollection, events writer, test harness

**Files:** `db/txn.rs`, `db/scoped.rs`, `db/counters.rs`, `events/{model,writer,notify,redact}.rs`, `tests/common/mod.rs`.

**`with_txn`** (must be implemented exactly like this):
```rust
pub async fn with_txn<T, F>(db: &Db, mut f: F) -> Result<T, AppError>
where F: for<'s> FnMut(&'s mut ClientSession) -> BoxFuture<'s, Result<T, AppError>> {
  let mut s = db.client.start_session().await?;
  for attempt in 0..5 {
    s.start_transaction().await?;   // default read concern snapshot, write concern majority
    match f(&mut s).await {
      Err(e) => { let _ = s.abort_transaction().await;
                  if is_transient(&e) && attempt < 4 { continue } return Err(e) }
      Ok(v) => loop {
          match s.commit_transaction().await {
            Ok(()) => return Ok(v),
            Err(ce) if ce.contains_label("UnknownTransactionCommitResult") => continue, // retry commit only
            Err(ce) if ce.contains_label("TransientTransactionError") => break,         // retry whole txn
            Err(ce) => return Err(ce.into()),
          }
      },
    }
  }
  Err(AppError::Internal("transaction retries exhausted".into()))
}
// is_transient: AppError::Db(e) where e.contains_label("TransientTransactionError")
```
Set transaction options: `ReadConcern::snapshot()`, `WriteConcern::majority()`. Callers clone captured values inside the closure: `Box::pin(async move {...})` with clones made before `async move`.

**`ScopedCollection<T>`:**
```rust
pub struct ScopedCollection<T>{ coll: Collection<T>, org: OrgId }
impl<T> ScopedCollection<T> {
  fn scope(&self, filter: Document) -> Document   // panics in debug if filter already has "org_id" != self.org; returns doc!{"$and":[{"org_id":org},filter]}
  find_one(filter, session: Option<&mut ClientSession>), find_page(filter, sort, limit, cursor), insert_one(&T) (asserts t.org_id()==org via trait OrgOwned), find_one_and_update(filter, update, session), update_one, count
}
pub trait OrgOwned { fn org_id(&self) -> &OrgId; }
```
Constructor: `ScopedCollection::new(db, name, scope: &OrgScope)`. `OrgScope` is defined here as `pub struct OrgScope { pub org_id: OrgId }`; its extractor arrives in WP5.

**Events:**
- `EventDoc { _id: ObjectId, seq: i64, org_id: Option<OrgId>, r#type: String, actor: String, subject: Subject{kind:String,id:String}, revision: Option<i64>, operation_id: Option<OperationId>, payload: Document, created_at: DateTime }`.
- `EventWriter::append(session:&mut ClientSession, NewEvent) -> Result<i64>`: `$inc` on `counters {_id:"events"}` with upsert and return-after, inside the session, then insert with that seq. `payload = redact(payload)`.
- After the **caller's** `with_txn` returns Ok, the caller calls `state.events.notify()` (wrapper `Notify::notify_waiters`). Provide a helper `commit_and_notify` pattern: `let r = with_txn(...).await?; state.notifier.notify(); Ok(r)`.
- Indexes: `events {seq:1}` unique, `events {org_id:1, seq:1}`.

**Test harness** `tests/common/mod.rs`:
- `TestApp::spawn(opts) -> TestApp { base_url, client (reqwest, cookie_store, default header Origin=base_url), db_name, data_dir: TempDir, handle }`.
- `TestApp::restart(self)` keeps the same db_name and data_dir.
- Helper `test_mongo_uri()`.
- Options: `credential_store = Memory` (shared `Arc<MemoryStore>` across restart if passed in), `lease_ttl`, `worker_concurrency`, `start_runner: bool`.
- `app::serve` must accept a pre-bound `TcpListener` and a shutdown future (refactor WP1 if needed).

**Tests:**
- 20 concurrent tasks each append one event in its own txn; seqs are exactly 1..=20 with no gaps or duplicates.
- An aborted txn does not consume a seq.
- Redaction unit tests: nested `{"api_token":"x","credential_ref":"cred_…"}` gives the token redacted and `credential_ref` kept.

**Accept:** `cargo test -p orqen-server --test health_test` and the event tests pass.

---

### WP3: Auth: owner bootstrap, sessions, login-link, host/origin/session/CSRF middleware

**Collections and indexes:**
- `owners {_id:"owner", created_at}`
- `sessions {_id:SessionId, token_hash:String(hex sha256) unique, csrf_token:String, created_at, last_seen_at, expires_at}`, TTL index on `expires_at` (expireAfterSeconds 0)
- `one_time_tokens {_id: tok_, token_hash unique, purpose:"bootstrap"|"login", expires_at, used_at:Option}`, TTL on `expires_at`

**Endpoints:**
| Method + path | Auth | Request | Response |
|---|---|---|---|
| `GET /api/v1/auth/status` | none | none | `{bootstrapped:bool, authenticated:bool, csrf_token:Option<String>}` |
| `POST /api/v1/auth/bootstrap` | origin only | `{token}` | 200 `{csrf_token}` + Set-Cookie |
| `POST /api/v1/auth/redeem` | origin only | `{token}` | 200 `{csrf_token}` + Set-Cookie |
| `POST /api/v1/auth/logout` | session + csrf | none | 204, cookie cleared (Max-Age=0), session deleted |

**Bootstrap transaction (exact):**
1. If an owner exists, return `AlreadyBootstrapped`.
2. `find_one_and_update({token_hash: sha(token), purpose:"bootstrap", used_at:null, expires_at:{$gt:now}}, {$set:{used_at:now}})`. None means `InvalidToken`.
3. Insert owner `_id:"owner"` (a duplicate key means `AlreadyBootstrapped`).
4. Create a session (32 random bytes, base64url) and a csrf token (32 bytes).
5. Append event `auth.bootstrapped` (org_id None).

Redeem works the same way with purpose `login`, and owner existence is required.

**Middleware order** (outermost first):
`request_id` → `TraceLayer` → `host_check` (all routes) → `origin_check` (non-safe methods on `/api`) → `session` (loads session into request extensions; required on `/api/*` except health, auth/status, auth/bootstrap, auth/redeem; missing or expired gives 401) → `csrf` (non-safe methods with a session: header `X-CSRF-Token` constant-time equal, else 403).
- Session `last_seen_at` is updated only if older than 5 min.
- `RequestBodyLimitLayer` 1 MiB on `/api`.

**Startup:** if there is no owner, run `tokens::issue_bootstrap(db, now)`:
- `update_many` sets `used_at` on every earlier unused bootstrap token (invalidates them)
- insert a new token with a 1 h expiry
- print the URL with `println!` (not tracing) and write `bootstrap-url.txt` with 0o600 permissions

CLI `login-link`: connect, require an owner, issue a login token (15 min), print `http://127.0.0.1:{port}/login#token=…`.

**Tests (`auth_test.rs`):**
- status before bootstrap is `bootstrapped=false`
- a wrong token gives 401
- a correct token gives a cookie and `csrf_token`
- a second bootstrap gives 409
- `GET /api/v1/orgs` without a cookie gives 401
- a POST with a cookie but no CSRF header gives 403 `csrf_failed`
- a POST with `Origin: http://evil.example` gives 403 `origin_rejected`
- `Host: evil.example:4280` gives 421
- logout invalidates the cookie
- a login-link token redeems once; a second redeem gives 401

The test helper `TestApp::bootstrap_owner()` reads the token by issuing one through a test-only lib function `auth::tokens::issue_bootstrap` that returns the raw token, and stores the csrf token for later requests.

**Accept:** `cargo test --test auth_test` is green.

---

### WP4: Pagination, If-Match and Idempotency-Key extractors (shared HTTP infra)

**Files:** `http/pagination.rs`, `http/extract/if_match.rs`, `http/extract/idempotency_key.rs`.
- `Page<T>{ items: Vec<T>, next_cursor: Option<String> }` (ToSchema, generic via `#[aliases]` or concrete per type).
- Query `{limit: Option<u32> (1..=200, default 50), cursor: Option<String>}`. The cursor is the base64url of the last `_id`. Query: `_id > cursor`, sorted `_id` ascending, fetch limit+1.
- `IfMatch(i64)`: parses `"N"` or `N`; missing gives `PreconditionRequired`; malformed gives `Validation`.
- `IdempotencyKey(Option<String>)` (validates 1–255 visible ASCII) and `RequiredIdempotencyKey(String)` (missing gives a 422 with field `Idempotency-Key`).

**Accept:** unit tests for parsing and cursor round-trip pass.

---

### WP5: Organizations and the OrgScope extractor

**Model:**
```rust
OrganizationDoc { _id: OrgId, slug: String /*^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$, unique global*/, name: String (1..=120),
  locale: Locale /* "fa"|"en" */, timezone: String /* validated chrono_tz::Tz */, revision: i64, created_at, updated_at, archived_at: Option<DateTime> }
```
Index: `{slug:1}` unique.

**Endpoints** (all require a session):
| Method + path | Notes | Status |
|---|---|---|
| `GET /api/v1/orgs?limit&cursor` | excludes archived | 200 `Page<OrganizationDto>` |
| `POST /api/v1/orgs` | optional Idempotency-Key, body `{slug,name,locale,timezone}` | 201 + ETag; duplicate slug gives 422 field `slug` "already used" |
| `GET /api/v1/orgs/{orgId}` | | 200 + ETag |
| `PATCH /api/v1/orgs/{orgId}` | If-Match, body `{name?,locale?,timezone?}`; slug immutable | 200 + ETag |

Events: `org.created` and `org.updated` (with revision) are written in the same txn.

**`OrgScope` extractor** (`http/extract/org_scope.rs`): `FromRequestParts` reads the `orgId` path param via `Path<HashMap<String,String>>`, parses `OrgId` (a parse failure gives `NotFound`, not 422), and loads the org with `archived_at: null` (missing gives `NotFound`). Returns `OrgScope{org_id, org: OrganizationDoc}`. **Every** `/api/v1/orgs/{orgId}/…` handler must take `OrgScope` as its first extractor. Code review rule: grep `orgs/{orgId}` route handlers lacking `OrgScope` must return nothing.

**Revision update (generic helper in `db/scoped.rs`):**
```rust
async fn update_with_revision<T>(coll, filter_id_and_org: Document, expected: i64, set: Document, session) -> Result<T, AppError> {
  let mut set = set; set.insert("updated_at", now);
  match coll.find_one_and_update(doc!{..filter, "revision": expected}, doc!{"$set": set, "$inc": {"revision": 1}})
       .return_document(After).session(session).await? {
    Some(d) => Ok(d),
    None => match coll.find_one(filter).session(session).await? {
       Some(cur) => Err(RevisionConflict{ current: to_dto_json(cur) }),
       None => Err(NotFound) } }
}
```

**Tests (`orgs_test.rs`):**
- create, then get gives an ETag of `"1"`
- PATCH with If-Match 1 gives revision 2
- PATCH again with If-Match 1 gives 409 with `current.revision == 2`
- no If-Match gives 428
- an invalid timezone gives 422
- a bad locale gives 422
- list pagination with limit=1 walks all orgs

**Accept:** `cargo test --test orgs_test` is green.

---

### WP6: Idempotency service

**Collection** `idempotency_records`:
```
{_id: idem_, scope: String /* org_id or "global" */, route: String /* e.g. "POST /api/v1/orgs/{orgId}/connections/{conId}/test" */,
 key, request_hash, state:"in_progress"|"completed", claim_token: String, locked_until, response_status: Option<i32>,
 response_body: Option<Document>, response_headers: Option<Document> /* ETag, Location only */, created_at, expires_at}
```
Indexes: unique `{scope:1, route:1, key:1}`; TTL on `expires_at` (now + 24 h).

**`request_hash`** = hex sha256 of `METHOD \n route_template \n sorted "k=v" path params joined by & \n serde_json::to_string(&body_value)`. Body maps are sorted because `preserve_order` is off. An empty body is the string `null`.

**API (`idempotency/service.rs`):**
```rust
pub enum Claim { Fresh{ id: IdemId, claim_token: String }, Replay{ status: u16, body: Value, headers: Vec<(String,String)> } }
pub async fn claim(db, scope, route, key, hash, now) -> Result<Claim, AppError>
pub async fn complete_in(session, id, claim_token, status, body, headers) -> Result<(), AppError>
pub async fn complete_standalone(db, id, claim_token, status, body) -> Result<(), AppError>
pub async fn release(db, id, claim_token) -> Result<(), AppError>
pub async fn run_idempotent<F>(state, scope, route, key: Option<String>, hash, f: F) -> Result<Response, AppError>
   where F: for<'s> FnMut(&'s mut ClientSession) -> BoxFuture<'s, Result<(StatusCode, Value, Vec<(String,String)>), AppError>>
```

**`claim` algorithm (exact):**
1. `insert_one{state:"in_progress", claim_token:new_ulid, locked_until: now+30s, …}`. Ok means `Fresh`.
2. On duplicate key (code 11000): `rec = find_one(scope,route,key)`.
   - `rec.request_hash != hash` gives `IdempotencyKeyReused`.
   - `rec.state=="completed"` gives `Replay`.
   - `rec.state=="in_progress"` and `rec.locked_until > now` gives `IdempotencyInProgress`.
   - Otherwise (stale claim) take over: `update_one({_id, state:"in_progress", locked_until:{$lte:now}}, {$set:{claim_token:new, locked_until:now+30s}})`. Matched 1 means `Fresh` with the new token; matched 0 means `IdempotencyInProgress`.

**`run_idempotent` behaviour:**
- `key == None`: run `with_txn(f)` directly.
- `Fresh`: `with_txn(|s| { let (st,body,h) = f(s).await?; complete_in(s, id, token, st, body, h).await?; Ok(..) })`. `complete_in` uses filter `{_id, claim_token, state:"in_progress"}`; matched 0 gives `Err(IdempotencyInProgress)` and aborts the domain write.
  - On `Err(e)`: if `e.status()` is 4xx and not 409 `idempotency_in_progress`, call `complete_standalone(id, token, e.status, e.problem_json)` (deterministic errors replay). Else call `release(id, token)` (delete where `claim_token` matches). Then return `Err(e)`.
- `Replay`: build the response from the stored status, body and headers, plus `Idempotent-Replayed: true`.

Apply it to `POST /orgs` (scope `"global"`) now, and to `POST /connections` and `POST /connections/{id}/test` in WP7 and WP10.

**Tests (`idempotency_test.rs`):**
- same key and body twice gives an identical 201 body, the second response carries `Idempotent-Replayed: true`, and exactly 1 org exists
- same key with a different body gives 422 `idempotency_key_reused`
- 10 concurrent identical requests give exactly 1 org; every response is 201-replay or 409 in-progress, and a retry of the 409s then replays 201
- a request that fails with a 422 validation error replays the same 422
- a stale in-progress record (`locked_until` set in the past by direct DB edit) gets taken over

**Accept:** green.

---

### WP7: Connections, URL normalization, credentials

**`connections/url.rs`:** `pub fn normalize_base_url(input:&str) -> Result<NormalizedUrl, FieldError>`
- scheme must be http or https
- host is required; lowercase the host; drop the default port only if it equals the scheme default; **keep any non-default port**
- keep the path exactly (case preserved); strip trailing `/` except root
- reject userinfo, query and fragment
- `NormalizedUrl{ url:String, host:String, port:u16 /*effective*/, path:String }`

Unit cases:
- `https://Jira.Corp.local:8443/jira/` becomes `https://jira.corp.local:8443/jira`
- `https://git.x/gitlab` keeps `/gitlab`
- `http://a:80/` becomes `http://a`
- `https://u:p@x` is rejected
- `https://x/?a=1` is rejected
- `ftp://x` is rejected

**Model:**
```rust
ConnectionDoc { _id: ConnectionId, org_id: OrgId, name: String(1..=80), name_key: String /*lowercase trimmed*/,
  provider: Provider /* jira|gitlab|confluence|figma */, deployment: Deployment /* cloud|server|data_center|self_managed */,
  base_url: String, auth_method: AuthMethod /* api_token|personal_access_token|basic|oauth */,
  credential_ref: Option<CredentialRef>, credential_state: CredState /* none|set */, credential_updated_at: Option<DateTime>,
  proxy_url: Option<String>, custom_ca_pem: Option<String> /* validated by reqwest::Certificate::from_pem */,
  timeout_ms: u32 /* 1000..=120000, default 15000 */, max_concurrency: u16 /* 1..=32, default 4 */,
  allowed_hosts: Vec<String> /* default [host of base_url]; lowercased */,
  status: ConnStatus /* untested|reachable|degraded|unreachable */, last_test: Option<TestSummary{operation_id, finished_at, overall, stages:Vec<StageResult>}>,
  revision, created_at, updated_at, archived_at: Option<DateTime> }
```
Indexes: unique `{org_id:1, name_key:1}` with partial filter `{archived_at: null}`... Mongo partial filters cannot use `$eq: null`, so instead add `active: bool` and use partialFilter `{active: true}`. Also `{org_id:1, _id:1}`.

**`ConnectionDto`** never contains a secret. It includes:
- `credential: {state, updated_at}`
- a derived `readiness: {unattended_ready: bool, reasons: Vec<String>}`. Reasons: `no_api_credential` if `credential_state==none`; `not_tested` if `last_test` is none; `provider_adapter_unavailable` **always in this slice**. So `unattended_ready` is always false now (§19 "browser login without API credential", and §20 says "do not show non-ready as ready").

**Endpoints** (all under `/api/v1/orgs/{orgId}`, with `OrgScope`):
| Method + path | Notes | Status |
|---|---|---|
| `GET /connections?limit&cursor` | | 200 |
| `POST /connections` | optional idempotency | 201 |
| `GET /connections/{conId}` | | 200 + ETag |
| `PATCH /connections/{conId}` | If-Match; changing `base_url` resets `status=untested`, `last_test=None` | 200 |
| `DELETE /connections/{conId}` | If-Match; soft archive (`archived_at`, `active=false`), deletes the credential from the store after commit | 204 |
| `PUT /connections/{conId}/credential` | If-Match, body `{secret: String (1..=8192)}` | 200 ConnectionDto |
| `DELETE /connections/{conId}/credential` | If-Match | 200 |

**Credential replace sequence (exact):**
1. `new_ref = CredentialRef::new()`.
2. `creds.put(&new_ref, secret)`; an error gives 503 `credential_store_unavailable`.
3. txn: `update_with_revision` sets `credential_ref=new_ref, credential_state=set, credential_updated_at=now`, and appends event `connection.credential_set` (payload `{credential_ref}` only).
4. If the txn fails, `creds.delete(&new_ref)` (best effort) and return the error.
5. If it succeeds and there was an old ref, `creds.delete(&old)` (best effort; log WARN on failure).

`secret` is a `SecretString` newtype whose Debug prints `"[REDACTED]"` and which has no Serialize. The request DTO uses `#[schema(format = Password)]`.

**`credentials/keychain.rs`:** `keyring::Entry::new("dev.orqen.credential", ref.as_str())`. `set_password`, `get_password` (NoEntry gives Ok(None)), `delete_credential` (NoEntry gives Ok). Wrap blocking calls in `tokio::task::spawn_blocking`.

**Tests (`scope_isolation_test.rs`)** (the §19 row 1 analogue). Two orgs A and B. Each creates a connection named `"Jira Main"`, provider jira, same base URL `http://127.0.0.1:9/jira`, and the same Idempotency-Key `"k1"`. Assert:
- each succeeds with **different** ids (idempotency scope is per org)
- `GET /orgs/A/connections` lists only A's
- `GET /orgs/A/connections/{B's conId}` gives 404
- PATCH and credential PUT on B's id via A's path give 404
- setting the credential on both produces two distinct `credential_ref`s, and the MemoryStore holds both secrets independently
- events listed for A (WP9 list endpoint; if implemented later, add this assertion in WP9) contain no B subjects
- a second connection with name `"jira main"` in A gives 422 `name` taken
- the same name in B is fine

**Tests (`orgs_test` additions):**
- the connection DTO JSON never contains the secret string (search the raw body)
- `readiness.unattended_ready == false` with reason `no_api_credential`

**Accept:** green. Manual check: `ORQEN_CREDENTIAL_STORE=keychain`, run `keychain_test` with `--ignored`; it puts, gets and deletes `cred_TEST…` and leaves the Keychain clean.

---

### WP8: Queue: job store with lease and fencing, runner

**Model:**
```rust
JobDoc { _id: JobId, org_id: OrgId, kind: String, dedupe_key: String, payload: Document,
  state: JobState /* queued|leased|succeeded|failed|dead */, active: bool /* true iff queued|leased */,
  attempts: i32, max_attempts: i32, next_at: DateTime, lease_owner: Option<String>, lease_expires_at: Option<DateTime>,
  fence: i64, operation_id: Option<OperationId>, last_error: Option<Document /* ProviderError or {code,message} */>,
  created_at, updated_at }
```
Indexes:
- `{state:1, next_at:1}`
- `{state:1, lease_expires_at:1}`
- **unique partial** `{org_id:1, dedupe_key:1}` with partialFilterExpression `{active: true}`
- `{org_id:1, _id:1}`

**`queue/store.rs`** (all functions take an explicit `now`):
```rust
pub struct Lease { pub job_id: JobId, pub org_id: OrgId, pub fence: i64, pub owner: String }

// in caller's txn; returns Err(AppError::DuplicateActiveJob{existing_job_id}) on 11000
pub async fn enqueue(s:&mut ClientSession, NewJob{org_id, kind, dedupe_key, payload, operation_id, max_attempts}, now) -> Result<JobId>

pub async fn acquire(db, owner:&str, kinds:&[&str], ttl:Duration, now) -> Result<Option<(JobDoc, Lease)>> {
  find_one_and_update(
    filter: { kind:{$in:kinds}, $or:[ {state:"queued", next_at:{$lte:now}},
                                      {state:"leased", lease_expires_at:{$lte:now}} ] },
    update: { $set:{state:"leased", lease_owner:owner, lease_expires_at: now+ttl, updated_at:now},
              $inc:{fence:1, attempts:1} },
    sort: {next_at:1}, return: After)          // NOT in a txn; single-doc atomic
}
pub async fn heartbeat(db, &Lease, ttl, now) -> Result<(), LeaseLost>  // update_one({_id, fence, state:"leased"}, {$set:{lease_expires_at: now+ttl}}); matched 0 => LeaseLost
pub async fn fenced_touch(s:&mut ClientSession, &Lease, now) -> Result<(), AppError> // update_one({_id, fence, state:"leased"}, {$set:{updated_at:now}}).session(s); matched 0 => Err(LeaseLost)
pub async fn complete(s, &Lease, now) -> Result<()>        // fenced: {_id,fence,state:"leased"} -> {state:"succeeded", active:false, lease_owner:null, lease_expires_at:null}
pub async fn fail(s, &Lease, err:&JobFailure, attempts:i32, max:i32, now) -> Result<JobState>
   // retryable && attempts < max  => {state:"queued", next_at: now+backoff(attempts) (or retry_after if larger), lease_*:null, last_error}
   // else => {state: if retryable {"dead"} else {"failed"}, active:false, last_error}
```

**Invariants** (write as doc comments; tests enforce them):
- **I1.** Every write caused by a job (domain docs, events, operation status, artifact records) happens inside `with_txn`, and the **first** statement in that txn is `fenced_touch` (or `complete`/`fail`, which are themselves fenced). A stale holder therefore either fails the filter (matched 0, LeaseLost, txn aborted) or hits a write conflict on the job doc, retries, and then fails the filter.
- **I2.** `acquire` always increments `fence`. Nothing else changes `fence`.
- **I3.** Lease expiry never deletes a job (no TTL on `jobs`).
- **I4.** If `attempts > max_attempts` immediately after acquire, the runner calls `fail(..., retryable=false)` with code `max_attempts_exceeded` and runs nothing.
- **I5.** Filesystem writes (artifacts) are not transactional. Write the blob first (idempotent, content-addressed), then write its record inside the fenced txn.
- **I6.** A handler that sees `LeaseLost` stops immediately and writes nothing further.

**`queue/runner.rs`:**
```rust
#[async_trait] pub trait JobHandler: Send + Sync {
  fn kind(&self) -> &'static str;
  async fn run(&self, ctx: JobCtx) -> Result<JobOutcome, JobFailure>;
}
pub struct JobCtx { pub job: JobDoc, pub lease: Lease, pub cancel: CancellationToken /* fired on LeaseLost or shutdown */, pub state: AppState }
pub enum JobOutcome { Done }            // handler itself performs its final fenced txn including queue::store::complete
pub struct JobFailure { pub retryable: bool, pub code: String, pub message: String, pub retry_after: Option<Duration> }
pub struct Runner { owner: String /* "w-{hostname}-{pid}-{ulid}" */, handlers: HashMap<&'static str, Arc<dyn JobHandler>>, concurrency: Semaphore, ttl, wake: Arc<Notify> }
impl Runner { pub fn spawn(self, state, shutdown: CancellationToken) -> JoinHandle<()> }
```
**Loop:**
1. Acquire a semaphore permit.
2. `acquire(now)`. None: release the permit, then `select!(wake.notified(), sleep(500ms), shutdown)`.
3. Some: spawn a task that starts a heartbeat ticker (every ttl/3; on LeaseLost it cancels `ctx.cancel` and logs WARN) and calls `handler.run`.
4. On `Err(JobFailure)`: `with_txn(fail(...) + event job.retry_scheduled|job.failed|job.dead + operation update (failed only when terminal))`. A LeaseLost here is logged and ignored.
5. On panic (JoinError): treated as retryable failure `internal_panic`.

`enqueue` callers call `wake.notify_one()` after commit. On shutdown, stop acquiring and wait up to 10 s for in-flight tasks; unfinished leases simply expire.

**Tests (`jobs_lease_test.rs`)**, using store functions with explicit `now` and a test-only no-op handler:
- **T1 fencing.** Enqueue J. A = acquire(t0, ttl 30 s) gives fence 1. B = acquire(t0+31 s) gives the same J with fence 2. A's `with_txn(fenced_touch + insert effect{job_id, by:"A"})` gives `LeaseLost`, and no "A" effect exists. B's completes. Final state succeeded, attempts 2.
- **T2 heartbeat.** After B reclaimed, A's heartbeat gives LeaseLost.
- **T3 not before next_at.** acquire at `t < next_at` gives None.
- **T4 retry/backoff.** Fail retryable with attempts=1 gives queued, `next_at = now + ~2 s`. Non-retryable gives failed, active=false.
- **T5 max attempts.** With max_attempts=2, the third acquire gives dead with `max_attempts_exceeded`.
- **T6 dedupe.** Two enqueues with the same `(org, dedupe_key)` while active: the second gives `DuplicateActiveJob`. After completion, enqueue works again. The same dedupe_key in another org is fine.
- **T7 two real runners.** Two `Runner`s (different owners) on one DB with a handler that does `with_txn(fenced_touch; insert effects{_id: job_id}; complete)`. Enqueue 50 jobs and wait until all are succeeded. Exactly 50 effects, and no duplicate-key errors surface as job failures. Handler: on 11000 for effects, treat it as success-already-applied and complete.

**Accept:** `cargo test --test jobs_lease_test` is green 5 runs in a row (`for i in 1 2 3 4 5; do cargo test --test jobs_lease_test || exit 1; done`).

---

### WP9: Events list + SSE stream (WP9a list can be done right after WP2; SSE after WP5)

**Endpoints:**
- `GET /api/v1/orgs/{orgId}/events?after=<i64>&limit=<1..500, default 100>` returns `{items: EventDto[], last_seq}`, filtered by `org_id` and ascending seq.
- `GET /api/v1/orgs/{orgId}/events/stream?after=<i64>`: axum `Sse`.

**SSE stream algorithm:**
```
cursor = max(header Last-Event-ID parsed i64 (else 0), query after (else 0))
loop {
  let notified = notifier.subscribe();           // obtain Notified future BEFORE querying (avoid lost wakeup): use notify.notified() then enable()
  let batch = find({org_id, seq:{$gt:cursor}}).sort(seq:1).limit(500)
  for e in batch { yield Event::default().id(e.seq.to_string()).data(json(EventDto)); cursor = e.seq }
  if batch.len() == 500 { continue }
  select! { _ = notified => {}, _ = sleep(2s) => {}, _ = client_disconnect/shutdown => break }
}
```
Use `tokio::sync::Notify`: `let n = notify.notified(); tokio::pin!(n); n.as_mut().enable();` before the query. Add `Sse::keep_alive(KeepAlive::new().interval(15s))` and send `retry: 3000` in the first event (`Event::default().retry(Duration::from_secs(3))` as a comment-only first frame). Shutdown also cancels streams.

`EventDto { seq, type, actor, subject, revision?, operation_id?, payload, created_at }`.

**Tests (`events_sse_test.rs`):** parse SSE manually from `reqwest` `bytes_stream` (split on `\n\n`, read `id:` and `data:`).
- Open the stream after=0. Create an org connection, which yields an event with seq s1. Drop the stream.
- Create 2 more connections.
- Reopen with header `Last-Event-ID: s1`. Receive exactly the 2 later events, in order.
- An org-B stream never receives org-A events.
- Without a cookie the stream returns 401.

**Accept:** green.

---

### WP10: Operations + artifacts

**Operations** (`operations/*`): model per D20. `operations::service::create_in(s, org, kind, subject, job_id)` and `set_status_in(s, &Lease-checked caller, …)`. Endpoint `GET /api/v1/orgs/{orgId}/operations/{opId}` returns `OperationDto`.

**Artifacts:**
```rust
ArtifactDoc { _id: ArtifactId, org_id, sha256: String /*64 lowercase hex*/, size: i64, media_type: String, kind: String /* e.g. "connection_test_report" */,
  source: Document /* {kind:"operation", id} */, created_at }
```
Index: unique `{org_id:1, sha256:1}`.

`artifacts::fs_store`:
```rust
pub async fn write_stream(root:&Path, org:&OrgId, reader: impl AsyncRead + Unpin, max: u64) -> Result<(String /*hex*/, u64), AppError>
  // tmp = root/.tmp/{ulid}; create_dir_all; write with Sha256 incremental; if bytes > max => remove tmp, Err(PayloadTooLarge)
  // file.sync_all(); final = root/{org}/sha256/{hex[0..2]}/{hex[2..4]}/{hex}; create_dir_all(parent)
  // if final exists => remove tmp else rename(tmp, final); File::open(parent).sync_all()
pub fn path_for(root, org:&OrgId, hex:&str) -> Result<PathBuf, AppError>  // validate hex ^[0-9a-f]{64}$ else Internal
```
`artifacts::service::put_bytes(state, &Lease-or-None, org, bytes, media_type, kind, source)` writes the blob, then upserts the record (inside the given session). On 11000, return the existing record.

**Endpoints:**
- `GET /api/v1/orgs/{orgId}/artifacts/{artId}` returns metadata.
- `GET /api/v1/orgs/{orgId}/artifacts/{artId}/content` streams the file with `Content-Type` = media_type, `X-Content-Type-Options: nosniff`, `Content-Disposition: attachment` for non-JSON.

**Tests (`artifacts_test.rs`):**
- the same bytes in org A twice give one record and one file
- the same bytes in org B give a separate record and a separate file under B's dir
- fetching A's artifact via B's path gives 404
- over-size returns `PayloadTooLarge` and leaves no tmp file
- a crash simulation: a leftover `.tmp/x` file does not affect reads

**Accept:** green.

---

### WP11: Connection Test operation (end-to-end vertical)

**Endpoint:** `POST /api/v1/orgs/{orgId}/connections/{conId}/test` with a **required** Idempotency-Key and empty body.

Inside `run_idempotent` → txn:
1. Load the connection (scoped; 404).
2. `job_id = JobId::new(); op = OperationId::new()`.
3. `queue::enqueue(s, {kind:"connection_test", dedupe_key: format!("connection_test:{conId}"), payload:{connection_id, connection_revision}, operation_id: op, max_attempts: 3})`.
   - On `DuplicateActiveJob{existing}`: return `Err(AppError::DedupedToExisting{operation_id})` from the closure. `run_idempotent` treats this variant specially: it aborts the txn, looks up the existing job's `operation_id`, and returns **202** with the existing `OperationDto` plus body field `"deduplicated": true`. It records that response as completed for this key.
4. `operations::create_in(s, …, status: pending)`.
5. Event `operation.created`.
6. Return 202 with `OperationDto` and `Location: /api/v1/orgs/{orgId}/operations/{op}`.

After commit, wake the runner and notify events.

**Handler `connections::probe::ConnectionTestHandler`** (kind `"connection_test"`):
1. Fenced txn: operation becomes running; event `operation.started`.
2. Load the connection (by org + id). If it is archived, fail non-retryable `connection_archived`.
3. Run the stages, in order. Each stage gets `StageResult{stage, status: passed|failed|skipped|not_implemented, code: Option<String>, detail: String, duration_ms}`. After each stage, a fenced txn appends event `connection_test.stage` (payload = StageResult). If any fenced txn returns LeaseLost, return immediately.

| Stage | What it does |
|---|---|
| `network` | `tokio::net::lookup_host(host:port)` then `TcpStream::connect` with timeout `timeout_ms`. DNS failure gives failed `dns_failed`; refused gives failed `connection_refused`; timeout gives failed `timeout`. These map to ProviderErrorKind `NetworkUnavailable`, recorded inside detail. |
| `tls` | `http` gives skipped "plain http". `https`: build a reqwest client (rustls; plus `custom_ca_pem` if set; plus `proxy_url` if set; `redirect::Policy::none()`) and send HEAD to base_url. A TLS/certificate error gives failed `tls_error`; otherwise passed. |
| `http` | Client with a custom redirect policy: follow at most 5 hops; each hop's host must be in `allowed_hosts`, otherwise stop and record failed `redirect_out_of_scope` with the target host. GET `base_url`, record the final status. Passed if any HTTP response arrives (any status, since auth is not tested yet); `detail = "HTTP {status}"`. Network errors give failed with the matching code. |
| `credential` | `credential_state==none` gives skipped `no_credential`. Otherwise `creds.exists(ref)`: true gives passed, false gives failed `credential_missing`. |
| `auth`, `identity`, `capabilities`, `sample_access` | always `not_implemented`, code `provider_adapter_unavailable`. |

If a stage fails, the remaining network-dependent stages (`tls`, `http`) are `skipped` with `detail="previous stage failed"`.

4. `overall`:
   - `unreachable` if `network`, `tls` or `http` failed
   - otherwise `degraded` if `credential` failed
   - otherwise `reachable`

   This is never "healthy" or "passed" in this slice.
5. Report JSON `{connection_id, base_url, tested_revision, stages, overall, finished_at}` is passed through `redact` and stored via `artifacts::put_bytes` (blob first).
6. Final fenced txn:
   - insert the artifact record
   - update the connection (by `_id` + org) with `$set {status: overall, last_test: {...}}` **without** a revision bump. This is system-written status, separate from user-edited fields. Note it in a doc comment; `PATCH` If-Match compares only user revision.
   - operation becomes succeeded with `result {overall, artifact_id}`
   - event `connection.tested`
   - `queue::store::complete`

   Stage failures are **results, not job failures**. Only internal errors (Mongo, fs) return `JobFailure{retryable:true}`.

**`tests/common/fake_upstream.rs`:** an axum server on `127.0.0.1:0` with routes:
- `/jira` → 200 "ok"
- `/gitlab` → 401
- `/redir-out` → 302 to `http://other.invalid:1/`
- `/redir-in` → 302 to `/jira`
- `/slow` → sleeps 5 s

**Tests (`connection_test_flow_test.rs`):**
- base `http://127.0.0.1:{p}/jira`: the op ends succeeded; stages are network passed, tls skipped, http passed "HTTP 200", credential skipped, the rest not_implemented; overall `reachable`; the artifact content is JSON whose `base_url` equals the exact `http://127.0.0.1:{p}/jira` (custom port and path preserved, §19 row 2); the connection `status=reachable`
- `/gitlab` gives http passed "HTTP 401" (auth not assessed)
- `/redir-out` gives http failed `redirect_out_of_scope`
- `/redir-in` passes
- `http://127.0.0.1:1/x` gives network failed `connection_refused`, overall `unreachable`
- **duplicate Start:** two POSTs with the same key give an identical op id, the second has the `Idempotent-Replayed` header, and only one job exists. Two POSTs with **different** keys while the first is still active (use `/slow` and `timeout_ms` 10000) give the same op id with `deduplicated:true`. After completion, a new key creates a new op.
- credential set in MemoryStore then removed directly from the store gives credential failed `credential_missing`, overall `degraded`
- SSE: the stage events arrive in order with seqs ascending

**Accept:** green.

---

### WP12: Restart persistence, UI embedding, OpenAPI completeness

**Files:** `http/ui.rs`, `openapi.rs` (completed), `tests/restart_test.rs`.

1. **UI serving per D23.** Add a fallback route `GET /*` (non-`/api`).
2. **OpenAPI:**
   - every route is registered via `OpenApiRouter::routes(routes!(...))`
   - all DTOs derive `ToSchema`
   - header params `If-Match`, `Idempotency-Key`, `X-CSRF-Token` are declared on the relevant ops
   - a `problem` schema is referenced by every 4xx/5xx response
   - `info.version` = crate version
   - the SSE endpoint is documented as `text/event-stream`
3. **`restart_test.rs`:**
   - (a) Bootstrap, create 2 orgs and connections, set a credential (shared MemoryStore Arc), run a connection test to completion. Restart TestApp (same DB and data dir). The same cookie still works; orgs, connections, `credential_state=set`, op and artifact content are all intact.
   - (b) Kill mid-job. Start with `lease_ttl=2s`, handler target `/slow`. After the stage `network` event, abort the server task (no graceful shutdown). Restart with the runner. Within 10 s the op is succeeded, there is exactly one `connection.tested` event for that op, and job `attempts == 2`.
   - (c) An unused bootstrap token from before the restart is invalidated after restart if there is still no owner (start without bootstrap, capture token 1, restart, token 1 gives 401).

**Accept:**
- `cargo test --workspace` is green.
- `pnpm -C web build && cargo run -- serve`, then `curl -s 127.0.0.1:4280/ | grep -c '<div id="root">'` gives 1, and `curl -s 127.0.0.1:4280/orgs/x/connections` gives the same HTML.
- `make openapi` produces no unexpected diff after a second run.

---

### WP13: Web scaffold (parallel lane B; needs only WP1's `orqen openapi`)

**Steps:**
1. `pnpm create vite@latest web --template react-ts`, then `cd web && pnpm install`.
2. Add `tailwindcss @tailwindcss/vite`; configure `vite.config.ts` with plugins `react()` and `tailwindcss()`, `resolve.alias {'@': '/src'}`, `server: { port: 5173, proxy: { '/api': { target: 'http://127.0.0.1:4280', changeOrigin: false } } }`. With `changeOrigin:false`, `Host` stays `localhost:5173`. Therefore the dev server must allow host `localhost:5173`: in `--dev` mode, `ORQEN_DEV_ORIGINS` hosts are also accepted as Host values. Implement this in WP3's host check (dev only).
3. tsconfig `paths {"@/*":["./src/*"]}`, `strict: true`.
4. Run `pnpm dlx shadcn@latest init`. Read `--help` first and pass flags non-interactively: style radix-nova if offered (else new-york), base color neutral, CSS variables, **RTL on** if the flag exists. Then add components: `button card input label select dialog dropdown-menu table badge sonner separator skeleton tooltip alert textarea sheet`.
5. Deps: `react-router @tanstack/react-query i18next react-i18next openapi-fetch lucide-react @fontsource-variable/vazirmatn`. Dev deps: `openapi-typescript vitest @testing-library/react @testing-library/jest-dom jsdom`.
6. Scripts:
   - `"gen:api": "openapi-typescript openapi.json -o src/api/schema.d.ts"`
   - `"typecheck": "tsc -b --noEmit"`
   - `"lint": "eslint ."`
   - `"test": "vitest"`
   - `"check:rtl": "node scripts/check-rtl.mjs"`
7. `scripts/check-rtl.mjs`: recursively scan `src/**/*.tsx`. Fail (exit 1, print file:line) on regex `\b-?(ml|mr|pl|pr|left|right|rounded-l|rounded-r|border-l|border-r|text-left|text-right)-` and `\b(text-left|text-right)\b` inside string literals. If shadcn-generated files contain them, convert to logical equivalents.
8. `i18n/index.ts`: i18next init with resources en and fa, `fallbackLng: 'en'`. `direction.ts` exports `applyLocale(locale)` which sets `document.documentElement.lang/dir` and toggles `font-fa` on `<html>` for fa (CSS: `.font-fa { font-family: 'Vazirmatn Variable', system-ui }`).
9. `api/client.ts`:
   ```ts
   export const api = createClient<paths>({ baseUrl: "", credentials: "same-origin" });
   let csrf: string | null = null; export const setCsrf = (t: string|null) => { csrf = t };
   api.use({ onRequest({ request }) { if (!["GET","HEAD"].includes(request.method) && csrf) request.headers.set("X-CSRF-Token", csrf); return request; } });
   ```
   `api/problem.ts`: `class ApiProblem extends Error { status; code; detail; errors?; current? }` plus `unwrap(res)` that throws ApiProblem on `res.error`.
   `api/idempotency.ts`: `newIdempotencyKey() = crypto.randomUUID()`. Rule: the key is created **once per user intent** (when the dialog opens or the button is first clicked) and reused on retry.
10. Tests:
    - `i18n.test.ts`: the key sets of en.json and fa.json are deep-equal
    - `direction.test.tsx`: `applyLocale('fa')` sets `dir=rtl`, `lang=fa`; `'en'` sets `ltr`
    - `problem.test.ts`

**Accept:** `pnpm -C web gen:api && pnpm -C web typecheck && pnpm -C web lint && pnpm -C web check:rtl && pnpm -C web test --run && pnpm -C web build` all exit 0.

---

### WP14: Web auth, shell, orgs (needs WP5 server + WP13)

**Router** (`router.tsx`):
```
/bootstrap                     BootstrapPage
/login                         LoginPage
/                              RequireSession → redirect: no orgs → /orgs ; else → /orgs/:lastOrgId/overview
/orgs                          RequireSession → OrgsPage (list + create)
/orgs/:orgId                   RequireSession → AppShell (Outlet)
   overview                    OverviewPage
   connections                 ConnectionsPage
   settings                    OrgSettingsPage
*                              NotFound
```

**Pages and components:**
- **`RequireSession`:** `useQuery(['auth','status'])`.
  - `!bootstrapped` → `<Navigate to="/bootstrap">`.
  - `!authenticated` → `/login`, which shows instructions: "run `orqen login-link`" inside `<Ltr>`.
  - Otherwise `setCsrf(status.csrf_token)`.
- **`BootstrapPage`:** reads `location.hash` `token`, then calls `history.replaceState` to strip the hash. A "Claim this Orqen" button POSTs to `/auth/bootstrap`. Success navigates to `/orgs`. Errors use mapped i18n messages for `invalid_token` and `already_bootstrapped`. `LoginPage` is the same against `/auth/redeem`.
- **`AppShell`:**
  - sidebar (logical start side) listing the §13 nav: Overview, Projects, Tasks, Inbox, Merge Requests, Knowledge, Graph, Models & catherd, Integrations (= Connections route), Security, Settings
  - items not built in this slice are rendered **disabled** with tooltip `t('nav.notAvailable')` and `aria-disabled`
  - header: `OrgSwitcher` (DropdownMenu listing orgs plus "Create organization"; it stores `localStorage['orqen.lastOrg']`), `LocaleToggle` (fa/en; writes `orqen.locale`; calls `applyLocale`), `ThemeToggle`, `LiveIndicator`, logout
- **`OrgsPage` + `CreateOrgDialog`:** fields slug, name, locale (select fa/en), timezone (input, default `Intl.DateTimeFormat().resolvedOptions().timeZone`). The idempotency key is generated on dialog open. 422 `errors[]` map to field messages.
- **`OrgSettingsPage`:** edit name, locale and timezone with `If-Match: "${revision}"`. On 409 `revision_conflict`, show an Alert "Changed elsewhere" with buttons "Load latest" (replace form with `problem.current`) and keep the user's input visible until they choose. The user's text is never silently overwritten (§16).
- The effective locale follows D28: when the org loads and no localStorage override exists, `applyLocale(org.locale)`.

**Accept:**
- typecheck, lint, check:rtl and test pass.
- Manual run (WP16 checklist items 1–4).

---

### WP15: Web connections, credential, test panel, SSE (needs WP12 + WP14)

**`api/sse.ts`: `useOrgEvents(orgId)`**
- `const key = 'orqen.lastSeq.'+orgId; after = sessionStorage[key] ?? 0`
- `es = new EventSource(\`/api/v1/orgs/${orgId}/events/stream?after=${after}\`)`
- `onmessage`: parse the EventDto; `sessionStorage[key]=seq`; push to a ring buffer (last 200, in a zustand-free React context via `useSyncExternalStore`); invalidate queries by type:
  - `org.*` → `['orgs']`
  - `connection.*` → `['connections',orgId]`
  - `operation.*` and `connection_test.stage` → `['operation',orgId,operation_id]`
- `onerror`: status `reconnecting` (native EventSource reconnects and sends Last-Event-ID). **Never** mark data as failed. `onopen`: status `live`.
- Close on unmount or org change.

**`ConnectionsPage`:** a Table with columns name, provider, base URL (`<Ltr>`), credential badge, status badge, last tested, readiness badge. The readiness badge reads "Not ready for automation" plus reasons tooltip, mapped via i18n `readiness.reasons.*`. Actions: Edit, Set credential, Test, Archive.
- Empty state is the explanatory text plus a "Add connection" button.
- Loading uses Skeleton.
- Errors use an Alert with the problem detail and a Retry button.

**`ConnectionFormDialog`:** fields name, provider, deployment, base URL (dir=ltr input), auth method, timeout_ms, max_concurrency, allowed hosts (comma list, ltr), proxy URL, custom CA (textarea, ltr). Create uses an Idempotency-Key; edit uses If-Match and the same 409 UX as org settings. Labels are tied via `htmlFor`; focus is trapped by Dialog.

**`CredentialDialog`:** a password input with `autocomplete="off"`; PUT with If-Match. After success the input is cleared and the dialog shows only "Credential set · {date}". Remove credential is a confirm dialog, then DELETE.

**`ConnectionTestPanel`** (Sheet on the logical end side):
- On click, it generates an idempotency key **once** for that click and POSTs.
- It reads `operation_id` from the 202 (also handles the `deduplicated` flag with an info toast).
- It renders the 8 stages in fixed order, each with an icon per status: passed check, failed X, skipped dash, not_implemented "Not available yet" badge (neutral, never green).
- Stage detail comes from `connection_test.stage` events. The final overall comes from the operation query. A "Download report" link goes to `/api/v1/orgs/{orgId}/artifacts/{artifact_id}/content`.

**`OverviewPage`:** cards for connection count by status and "Ready for automation: 0 (provider adapters not installed)", plus a live event feed (latest 50: time via `Intl`, type, subject id in `<Ltr>`).

**Accept:** typecheck, lint, check:rtl and test pass. Add a `sse.test.ts` with a mocked EventSource class: it persists seq to sessionStorage, and a subsequent hook mount uses `?after=<seq>`.

---

### WP16: Integration verification and README

1. `make check` is fully green.
2. **Manual checklist** (run via `make dev-mongo`, `make build`, `./target/release/orqen serve` with `ORQEN_DATA_DIR=.orqen-dev/data ORQEN_CREDENTIAL_STORE=keychain`; open the printed bootstrap URL in the Browser pane):
   1. The bootstrap claim works, and reloading `/bootstrap` afterwards shows "already set up".
   2. Create org "Acme" (fa) and org "Beta" (en). Switching orgs flips `dir` rtl/ltr; nav sits on the right in fa. The URL and base URL cells remain LTR.
   3. In both orgs, create a connection "Jira Main" with base `http://127.0.0.1:4280/api/v1/health`. Run Test; stages stream live; overall "reachable". Download the report.
   4. Set a credential in Acme. The UI never shows it again. `security find-generic-password -s dev.orqen.credential` lists one item (account `cred_…`). Beta's connection shows `no_api_credential`.
   5. Edit the same connection in two tabs; the second save shows the 409 conflict UI.
   6. Kill the server mid-test (use base `http://10.255.255.1` for a slow connect with timeout 10000), restart it, and the operation completes once; the event feed resumes without a reload error.
   7. Keyboard-only: Tab through the shell, open and close dialogs with Esc, and focus returns to the trigger.
   8. Theme light/dark/system.
3. Check `lsof -nP -iTCP:4280 -sTCP:LISTEN` shows only `127.0.0.1`.
4. Write `README.md` quickstart: prerequisites, `scripts/dev-mongo.sh start`, `make dev-server` + `make dev-web`, the bootstrap link location, `orqen login-link`, and `make check`.

**Accept:** checklist 1–8 recorded as passing (screenshots optional) and `make check` exits 0.

---

## 4. Definition of Done (slice)

| SPEC ref | Demonstrated by |
|---|---|
| §18 Sprint 1: Rust executable serving UI + API on loopback 4280 | WP12 accept + WP16 step 3 |
| §18 Sprint 1 / §19 row 1: two orgs with same-named IDs stay isolated | `scope_isolation_test` (same connection names, base URLs, Idempotency-Keys, dedupe keys, artifact bytes; cross-org 404; distinct credential refs; per-org events and SSE) + IssueIdentity/RepoIdentity unit tests |
| §18 Sprint 1: restart persistence | `restart_test` (a), (b), (c) + checklist 6 |
| §18 Sprint 1: credential refs | WP7 (Mongo holds only refs; rotation; Keychain manual test; secrets absent from DTOs, events and artifacts) |
| §19 row 2: custom port/path | URL unit tests + flow test asserting the exact `http://127.0.0.1:{p}/jira` is probed and reported; redirect scope enforced |
| §19 row 6: duplicate Start / two runners give one active run and one effect | idempotency tests (replay, mismatch, concurrency, stale takeover), dedupe 202 `deduplicated`, `jobs_lease_test` T1/T6/T7 |
| §19 row 7 (infra part): restart does not double-execute | `restart_test` (b): one `connection.tested`, attempts == 2 |
| §16: revision conflicts give 409 without overwrite | `orgs_test` + UI conflict flow (checklist 5) |
| §19 "browser login without API credential" | readiness `unattended_ready=false` with `no_api_credential`; UI badge |
| §19 "UI fa/en" | RTL/LTR + Persian font, LTR islands, keyboard/focus, SSE reconnect (sse.test + checklist 2, 6, 7) |
| §17: loopback, HttpOnly/SameSite cookie, origin, CSRF, masked secrets | `auth_test` + checklist 3–4 |
| §15: content-addressed artifacts with atomic write, unique within scope | `artifacts_test` |
| §15: schema version check | migrate refuses a newer DB version (unit/integration test in WP1: set `schema_meta.version=999`, server exits 3) |
| Tooling | `make check` green; OpenAPI and TS client have no drift |

---

## 5. Risks, open items, deferred

### Needs the user's decision or action

1. **Approve the Rust install** (D1: download and execute `sh.rustup.rs`). Alternative: `brew install rustup` if you prefer Homebrew-managed installs.
2. **Approve `git init`** in the project and a local commit per WP. Also say where the repo will be hosted (GitLab or GitHub), so hosted CI can mirror `make check`.
3. **Keychain service name `dev.orqen.credential`** (and reverse-DNS id `dev.orqen`). Changing it later orphans stored secrets. Confirm or supply your own domain.
4. **Dev MongoDB:** the plan runs a second, project-local `mongod` on 27018 (replica set) and leaves your Homebrew 27017 instance untouched. If you prefer Docker, start OrbStack and use `docker compose up -d` instead (same port and URI).
5. **Keychain prompts:** unsigned dev builds may trigger Keychain access prompts after each rebuild when the credential stage reads a secret. Code-signing is deferred; click "Always Allow" during development, or use `ORQEN_CREDENTIAL_STORE=memory` (secrets are then lost on restart).

### Known risks (owned by the plan)

- **Clock:** leases use the local wall clock. That is correct for a single machine; multi-runner across machines needs DB time (team phase).
- **The events counter document serialises event writes.** That is fine at local scale; revisit in the team phase.
- **Cookies on loopback** are not port-isolated by browsers. Mitigations are the port-suffixed name, HttpOnly, SameSite=Strict, CSRF, and Host/Origin checks. Other local apps on 127.0.0.1 could still receive the cookie: an accepted MVP risk (§17 says single-user laptop, not tenant isolation).
- **shadcn RTL CLI flags and style names** may differ from expectations. WP13 tells the coder to inspect `--help` and enforce RTL through `check-rtl`.

### Deferred, with Spike 0 as user-run steps

**Spike 0** (run before Sprint 3; needs your accounts):
1. Record versions: `codex --version`, `claude --version`, `catherd --version`, `bun --version`, and `codex app-server --help` (confirms App Server availability).
2. Create isolated catherd state:
   ```
   export CATHERD_HOME="$HOME/Library/Application Support/Orqen/spike0/catherd"
   mkdir -p "$CATHERD_HOME"
   ```
   Record `shasum` of your personal catherd config and profile files first. Then run `catherd init --no-input`, `catherd doctor`, `catherd profile --help`, `catherd catalog --help`. Afterwards, verify the personal file hashes are unchanged.
3. Start the real Codex host with that environment, run a tiny role run (architect/worker/reviewer/verifier) on a scratch repo, and record the exact rung per role (`catherd runs`, `catherd status`).
4. Kill the controller or host mid-run, restart it, and confirm the result can be collected (`catherd runs show …`) without a second dispatch.
5. With API tokens: read one Jira issue and its parent via `sooperset/mcp-atlassian`; list GitLab projects via `zereight/gitlab-mcp` with a custom API URL; read one Confluence page with its version.
6. Figma: inside the same host, call the official Figma MCP on a company node and confirm the active account is not "Kebrin".
7. Jev: install it (not on PATH now), run one bounded decision with schema validation, then a timeout injection.
8. OpenClaw Memory Wiki: install it (not on PATH now) and run a dedicated-vault ingest and search.

**Deferred to Sprint 2+:**
- provider adapters (Jira/GitLab/Confluence/Figma via RMCP) and the auth/identity/capability/sample stages
- project catalog and mapping
- catherd profile editor
- org template export/import
- a separate `outbox` collection for external side effects (`external_operations` reconciliation)
- operation/run cancel
- launchd start-at-login
- backup/restore and retention cleanup, including orphan artifact GC
- Mongo change streams
- Playwright e2e (needs a browser download)
- hosted CI
- code signing
- `mongo:8` image digest pinning
- workflow, Inbox, knowledge, graph, Strix, GitNexus

### Critical Files for Implementation
- crates/orqen-server/src/queue/store.rs
- crates/orqen-server/src/idempotency/service.rs
- crates/orqen-server/src/db/txn.rs
- crates/orqen-server/src/http/middleware/session.rs (with host.rs, origin.rs, csrf.rs)
- crates/orqen-server/src/connections/probe.rs
- docs/SPEC.md