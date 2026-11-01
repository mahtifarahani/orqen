# PLAN-S2-GO-ADDENDUM-2: orchestrator host setting and catherd/backend keys

Extends `docs/PLAN-S2.md` and `docs/PLAN-S2-GO.md`. Grounded in the Go tree at commit `71424f9`
(`internal/catherd/{env.go,schema.go}` with `Host = "codex"`, `internal/server/{catherd.go,catherd_jobs.go,credentials*.go,policy.go,readiness.go,preflight.go,wizard.go}`, `internal/policy/registry.go`, `internal/wizard/steps.go`, `internal/sysprobe/check.go`), catherd 1.5.0 source (`src/services/credentials.ts`, `jev-service.ts`, `infra/jev-client.ts`, `infra/sources/artificial-analysis.ts`, `infra/env.ts` (`SECRET_ENV`), `domain/secrets.ts`, `adapters/*/index.ts`, `entry/host-arg.ts`, `infra/host-context.ts`) and reference-project (`packages/extension/src/setup/service.ts` `saveKeys`, `setup/detect.ts`, `gateway/env.ts`, `docs/research/catherd-doctor-setup.md` §3).

## 0. Verified facts (catherd 1.5.0)

| Topic | Fact |
|---|---|
| Hosts | `--host` accepts `auto \| codex \| claude-code`. `auto` is decided from session evidence (`CATHERD_ORCHESTRATION_HOST`, `CODEX_THREAD_ID`/`CODEX_SESSION_ID`, `CLAUDE_CODE_*`); conflicting evidence gives `unknown`. The host affects how omitted architect/verifier rungs resolve, and native `claude:` rungs are valid only on `claude-code`. |
| catherd's own keys | Jev = `TYPESAFE_API_KEY` env, else `<config>/credentials.json` field `typesafeApiKey`. Artificial Analysis = `ARTIFICIAL_ANALYSIS_API_KEY` env, else `artificialAnalysisApiKey`. **Env wins over file.** The file is mode 600, written only by `catherd init` (interactive); reference-project writes it by piping keys into `init`. |
| Leak control inside catherd | Both env names are in `SECRET_ENV`: scrubbed from every process catherd starts (workers, doctor's MCP handshake). catherd's logger redacts values of env vars matching `/KEY\|TOKEN\|SECRET\|PASSWORD\|CREDENTIAL/i`. |
| Cheap validity checks | Jev: `GET https://api.typesafe.ai/v1/models` with `Authorization: Bearer <key>` (no inference; catherd's `testJevKey`, also run by `doctor`). AA: `GET https://artificialanalysis.ai/api/v2/language/models/free?page=1` with `x-api-key`; 200 ok, 401 refused, else unchecked; counts against AA rate limit. |
| Backend keys catherd reads | Only for an **isolated** harness (`isolationKey`): antigravity `GEMINI_API_KEY`, cursor `CURSOR_API_KEY`, grok `XAI_API_KEY`. `profile validate`/save errors when the harness is isolated and the key is absent from catherd's env (`isolationKeyErrors`). |
| Native logins | codex (`codex login`), claude-code (`claude auth login`), opencode (`opencode auth`) use the user's own login in `HOME`; no API key. Isolated codex/claude-code/opencode use catherd-made homes; their login prerequisites are **not verified**. |

## 1. Decisions

| # | Decision | Reason |
|---|---|---|
| K1 | **Storage.** Each secret is a Keychain item through the existing `CredentialStore` (`dev.orqen.credential`, account `cred_<ULID>`). Mongo holds refs only, in new collection `catherd_secrets` (§3). Scope per org. | Same model as connection credentials (D12). Nothing plaintext on disk. |
| K2 | **Delivery to catherd is env injection only.** Secret is read from Keychain right before an `exec`, added to that one command's `cmd.Env`, dropped. **Orqen never writes `credentials.json`** (org or personal home) and never runs `catherd init`. If `<org>/home/config/credentials.json` exists, preflight warns `unexpected_credentials_file`. catherd's env-wins rule means Orqen's value is used. | Env is supported and scrubbed by catherd itself; the file is plaintext at rest. Personal home never touched (`PersonalEnv` has no secret injection API at all). |
| K3 | **Least exposure per call**: `catherd.SecretEnv` passed explicitly; default none (matrix below). | Each call gets only what it needs. |
| K4 | **Write-only UI.** PUT accepts the secret; no route ever returns it, a fragment, or a hash. DTO shows `state set\|none`, `updated_at`, last check. Rotation follows the connection-credential sequence: put new ref → transaction swaps ref → best-effort delete old. Remove = transaction clears ref → delete item. | §17 masking. Reuses `replaceCredential` logic. |
| K5 | **Verification** is a `catherd_secret_check` job: one HTTP call from Go, outside any transaction, 20 s, no retry. Jev and AA: `ok \| refused \| unchecked`. `XAI_API_KEY`: `GET https://api.x.ai/v1/models` Bearer. `GEMINI_API_KEY`: `GET https://generativelanguage.googleapis.com/v1beta/models` with header `x-goog-api-key` (never a query param). `CURSOR_API_KEY`: `unverifiable`. No call runs a model. | Cheap authenticated check spends no tokens, or UI says explicitly it cannot verify. |
| K6 | **Orchestrator host** is a new org-only policy key `orchestrator.host`, enum `codex \| claude-code`, default `codex`, `ProjectOverridable: false`. Versioned and audited through existing policy draft/publish (`policy.published` events, immutable `policy_versions`). `catherd.Host` becomes a per-call argument resolved from the org's effective policy. `auto` is never offered. | Policy already gives versioning, audit, effective-with-source. `auto` would depend on Orqen's own process env. |
| K7 | **Host-bound versions.** `model_profile_versions` and the `profile_validate` result gain `host`. Validation/publish run with the effective host at job start. New readiness **blocker** `profile_host_mismatch` when bound version's host differs from effective host. Fix: "Republish for current host" = existing rollback to same version (new number, revalidated under new host). | Same document can be valid on one host and invalid on the other. |
| K8 | **claude-code host is allowed but truthful.** Selecting it gives readiness blocker `host_adapter_unavailable` (Orqen's execution host is Codex App Server per SPEC §3/§6; no Claude host adapter planned). Orqen also keeps refusing native `claude:` rungs on every host (`claude_native_unsupported`, replaces `claude_native_on_codex`): their agent links land in the org's `claude-agents` dir which a real Claude Code session never reads. `claude-code:` (headless) rungs are fine. | No fake "ready". |
| K9 | **Doctor per host.** `POST /catherd/doctor` takes optional `{host}` (default effective). `catherd_states.doctors.<host>` replaces `last_doctor`, with read fallback: legacy `last_doctor` = `doctors.codex`. Readiness uses effective host's doctor. | Compare hosts without switching. |
| K10 | **Running work.** Sprint 2 has no runs. From Sprint 3 a run pins `{policy_version_id, profile_version_id, host}` at start, so changing host never alters a pinned run. | Explicit audited change only. |
| K11 | **Readiness effect of keys.** Jev missing/refused = **warning** `jev_key_missing`/`jev_key_refused`, only if bound version has `jev.use=auto` (SPEC §9 fallback: never a blocker). Missing AA key needs nothing. A backend isolation key **missing** while the bound version has `harness.<b>.isolated=true` and a role/failover on `<b>` = **blocker** `backend_key_missing:<b>`; `refused` is a blocker too; `unverifiable`/`unchecked` = warning. | SPEC §9 fallback vs hard execution failure. |
| K12 | **No schema bump** (stays `SchemaVersion = 2`). New collection and fields are additive; `migrate` adds the index idempotently. | Additive only. |
| K13 | **The Jev key the user pasted in chat** is not used, stored or referenced anywhere. The user enters a key through the new UI. Recommend rotating it at TypeSafe. | Hygiene. |

**Injection matrix (K3):**

| Call | Injected env |
|---|---|
| `Doctor` | `TYPESAFE_API_KEY` (jev row), plus isolation keys for every backend with a key set |
| `CatalogSync` | `ARTIFICIAL_ANALYSIS_API_KEY` |
| `profile_validate` staging and `profile_publish` (Create/SetKey/Validate) | only isolation keys of backends whose harness is `isolated=true` in that document |
| `CatalogRefresh` | the isolation keys |
| `ListProfiles`/`ShowRaw`/`ShowResolved`/`CatalogList`, every `PersonalEnv` call | none |
| Sprint 4 host processes | decided then; same store |

## 2. Code shape (Go)

**`internal/catherd/env.go`:**
- `type SecretEnv map[string]string`; keys constrained to `AllowedSecretEnv = {"TYPESAFE_API_KEY","ARTIFICIAL_ANALYSIS_API_KEY","GEMINI_API_KEY","CURSOR_API_KEY","XAI_API_KEY"}`; any other name panics.
- `func (e *OrgEnv) VarsWith(rt *Runtime, s SecretEnv) []string` and `SyncVarsWith`.
- `PersonalEnv` gets nothing. `Port` write methods and `Doctor`/`CatalogSync`/`CatalogRefresh` gain a final `SecretEnv` parameter (nil allowed).
- `cli.go` scrubs stdout and stderr: every injected value replaced with `[REDACTED]` before parsing, storing or wrapping into errors. Never format `cmd.Env` or `cmd.String()` into logs/errors. `Fake` records env **names** plus `sha256(value)[:8]` for assertions, never values.
- `Host` stops being a constant. `CLI` methods take `host string`, validated against `{"codex","claude-code"}`.

**`internal/server/catherd_secrets.go`** (new):
- `secretKinds` table: `kind → {env, label_key, verifier, used_by}`. Kinds: `jev`, `artificial_analysis`, `gemini`, `cursor`, `xai`.
- `func (a *App) secretEnvFor(ctx, org string, need []string) (catherd.SecretEnv, error)` reads `catherd_secrets` refs then `Creds.Get`. If the store is unavailable, the job ends with result `credential_store_unavailable`.
- Each job builds its own `need` list per K3.

**`internal/policy/registry.go`:** add `{Key: "orchestrator.host", Type: "enum", Enum: []string{"codex","claude-code"}, Default: "codex"}` (org-only). Add `func (a *App) orgHost(ctx, org) string` from the latest published org policy, falling back to default.

## 3. Data model and indexes

```
catherd_secrets {
  _id: csec_<ULID>, org_id, kind,
  credential_ref?: cred_, state: "set"|"none",
  updated_at?, active: bool, revision, created_at,
  last_check?: { status: ok|refused|unchecked|unverifiable, checked_at, detail /* redacted, <=300 chars */, operation_id }
}
```
- Index: unique `(org_id, kind)` partial `{active: true}`. Docs created lazily on first PUT (If-Match `"0"`), like the wizard.
- `model_profile_versions` + `host` (string; missing read as `codex`). `last_validation` + `host`. `catherd_states.doctors: {<host>: DoctorSummary}`.
- ID prefix `csec` added to `ids.go` validation and `apiMiddleware` prefix list. Path param `{kind}` validated in handler (unknown kind → 404).

## 4. API (hand-authored `api/openapi.json` per PLAN-S2-GO §A.5; tag `catherd`; snake_case operationIds; `TestContract` maps extended)

| Method + path | operationId | Headers | Body → Response |
|---|---|---|---|
| GET `/api/v1/orgs/{orgId}/catherd/secrets` | `list_catherd_secrets` | — | `[CatherdSecretDto{kind, env_name, state, updated_at?, revision, last_check?, verifiable, used_by[], required_reason?}]` (all 5 kinds, synthesized when absent) |
| PUT `/catherd/secrets/{kind}` | `put_catherd_secret` | If-Match, CSRF | `PutCatherdSecretRequest{secret: string (format password, 1..2048, single line, no control chars)}` → CatherdSecretDto |
| DELETE `/catherd/secrets/{kind}` | `delete_catherd_secret` | If-Match, CSRF | → CatherdSecretDto (`state: none`) |
| POST `/catherd/secrets/{kind}/check` | `check_catherd_secret` | Idempotency-Key (required), CSRF | → 202 OperationDto (dedupe `catherd_secret_check:{kind}`) |
| GET `/catherd/hosts` | `list_orchestrator_hosts` | — | `[{host, effective: bool, catherd_supported: true, orqen_host_adapter: "available"\|"unavailable", last_doctor?}]` |
| POST `/catherd/doctor` (changed) | `run_catherd_doctor` | as before | optional `RunDoctorRequest{host?}` |

- `orchestrator.host` is edited through existing `/policy/draft` and `/policy/publish` routes.
- `PutCatherdSecretRequest.secret` is `writeOnly: true`.

**Events** (no secret, no ref hash, no env value in payloads): `catherd_secret.set {kind}`, `catherd_secret.removed {kind}`, `catherd_secret.checked {kind, status}`; `policy.published` covers host change; `catherd.doctor_completed` gains `host`.

**Errors:** `secret_invalid` 422 (field `secret`); `credential_store_unavailable` 503 (exists); 404 unknown `kind`; new readiness codes `profile_host_mismatch`, `host_adapter_unavailable`, `backend_key_missing`, `backend_key_refused`, `jev_key_missing`, `jev_key_refused`, `claude_native_unsupported` (issue).

## 5. UI, wizard, preflight

**Models & catherd → new tab "Orchestrator & keys"** (`web/src/features/models/orchestrator-keys-tab.tsx`):
1. **Host card.** Radio `codex`/`claude-code` with effective value, source badge and version. Changing opens the org policy draft with the value prefilled; Publish uses the existing flow, with an explanation that bound versions become `profile_host_mismatch`. "Run doctor for this host" button and per-host doctor summaries from `/catherd/hosts`.
2. **Keys table.** One row per kind: name, env name in `<Ltr>`, "Used by" text, state badge (Not set / Set · date / Valid / Refused / Could not check / Cannot verify). Actions: Set or Replace (dialog with password input, `autocomplete="off"`, cleared on success, stored value never shown), Check (an operation), Remove (confirm). Note: "Orqen keeps keys in macOS Keychain and passes them only to the catherd process that needs them; your personal catherd keys are not read or changed."

**Wizard:** the `profile` step embeds these two cards above the profile quick start. Its consequences gain `jev_fallback_rules` ("without a Jev key, routing uses the lanes' kind and difficulty"). Completion criteria unchanged: keys optional. `backend_key_missing` already blocks through the bound profile's readiness.

**Preflight** (`internal/sysprobe` new checks; org secret states passed as `Deps.Secrets`, no Keychain read of values):

| Check | Required | Rule |
|---|---|---|
| `catherd.jev_key` | no | pass if set and ok; warn if missing/refused while any published version uses `jev.use=auto`; else skipped |
| `catherd.backend_keys` | no | warn per isolated backend without a key |
| `catherd.credentials_file` | no | warn if `<org>/home/config/credentials.json` exists |
| `bin.opencode_login` | no | `opencode auth list --format json`, read-only |

**i18n (fa + en):** `models.orchestrator.{title,host,hostHelp,claudeHostUnavailable,republishHint,runDoctor}`, `models.keys.{title,note,columns.*,state.{none,set,ok,refused,unchecked,unverifiable},actions.{set,replace,check,remove},dialog.*,usedBy.*}`, `secrets.kind.{jev,artificial_analysis,gemini,cursor,xai}`, `readiness.blockers.{profile_host_mismatch,host_adapter_unavailable,backend_key_missing,backend_key_refused}`, `readiness.warnings.{jev_key_missing,jev_key_refused}`, `profile.issues.claude_native_unsupported`, `wizard.consequence.jev_fallback_rules`, `preflight.check.{catherd.jev_key,catherd.backend_keys,catherd.credentials_file,bin.opencode_login}`, `problems.secret_invalid`.

## 6. Work packages (after WP30; one committer; order WP35 → WP36 → WP37 → WP38 → WP39 → WP40)

**WP35: catherd secrets backend (store, injection, check)**
- Files: `internal/catherd/{env.go,cli.go,fake.go,port.go}`, `internal/server/{catherd_secrets.go,catherd_secret_check_job.go,catherd_jobs.go,profile_validate_job.go,profile_publish_job.go,jobs.go,ids.go,app.go,db.go}`, `api/openapi.json`, tests.
- Tests: `go test -short ./internal/catherd/...`: `VarsWith` includes only allowlisted names; `PersonalEnv` has no injection path (compile-time); stdout/stderr scrubbing. `go test ./internal/server -run 'TestCatherdSecrets|TestSecretLeak'`:
  - put/replace/remove: old Keychain ref deleted; stale If-Match gives 409.
  - Check against httptest Jev/AA (base URLs injectable via unexported test hooks): ok, refused, unchecked.
  - Injection matrix: Fake records env names per call. Doctor gets `TYPESAFE_API_KEY`; list/show/personal calls none; validate gets `XAI_API_KEY` only when `harness.grok.isolated=true`.
  - **Leak test.** Set sentinel `orqen-SENTINEL-<ulid>` for all 5 kinds, run doctor, sync, validate, publish and check jobs, assert the sentinel is absent from: every API response body and header; every Mongo collection (bson dump of all docs); every file under `DataDir` (artifacts, catherd org home, incl. absence of `credentials.json`); captured `slog` output (test handler); Fake-recorded argv; operation results and events. Assert it is present **only** in the MemoryStore.
- Real (gated): `ORQEN_TEST_CATHERD_BIN=$HOME/.bun/bin/catherd go test ./internal/server -run TestCatherdRealSecrets -count=1 -v`: runs doctor in a temp org home with `TYPESAFE_API_KEY=<sentinel>`; asserts doctor JSON has a `jev` row; sentinel absent from stdout/stderr after scrub and from every file in the temp home; no `credentials.json`; `~/.config/catherd` manifest byte-identical before/after; temp "personal" home manifest unchanged. Then `make check`.

**WP36: Orchestrator host setting**
- Files: `internal/policy/registry.go`, `internal/catherd/schema.go` (remove `Host` constant), `internal/server/{catherd.go,catherd_jobs.go,model_profiles.go,profile_*_job.go,readiness.go}`, `internal/readiness/evaluate.go` (+ inputs `EffectiveHost`, `BoundHost`, `HostAdapterAvailable`), `internal/catherd/issues.go` (`claude_native_unsupported`), `api/openapi.json`.
- Tests: `go test -short ./internal/policy/... ./internal/readiness/... ./internal/catherd/...` and `go test ./internal/server -run 'TestOrchestratorHost|TestReadiness|TestModelProfiles'`: publish org policy with `claude-code`; bound codex v1 gives `profile_host_mismatch` + `host_adapter_unavailable`; "Republish" gives v2 with `host: claude-code`, v1 byte-identical; doctor with `{host:"claude-code"}` stored under `doctors["claude-code"]` without changing `doctors.codex`; legacy `last_doctor` read as codex; Fake records `--host claude-code` on the right calls. Then `make check`.

**WP37: Readiness key effects, preflight checks, wizard consequence**
- Files: `internal/readiness/evaluate.go` (key inputs per K11), `internal/sysprobe/{catherd_keys.go,binaries.go}`, `internal/server/{readiness.go,preflight.go}`, `internal/wizard/steps.go`.
- Tests: `go test -short ./internal/readiness/... ./internal/sysprobe/... ./internal/wizard/...` and `go test ./internal/server -run 'TestReadiness|TestPreflight|TestWizard'`: missing Jev is warning only and absent when `jev.use=off`; isolated grok without key gives blocker `backend_key_missing:grok`; planted `credentials.json` gives preflight warn; skipping profile step lists `jev_fallback_rules`; readiness-unaffected-by-wizard invariant still holds.

**WP38 (web): "Orchestrator & keys" tab**
- Files: `web/src/features/models/{orchestrator-keys-tab.tsx,secret-dialog.tsx}`, hooks via orval-generated client only, `models-page.tsx` (tab), `i18n/{en,fa}.json`.
- Tests (vitest): secret input cleared after success and no rendered DOM contains the typed value after close; request body carries the secret only on PUT; host change routes to policy draft with value prefilled; claude-code option shows adapter-unavailable notice.
- Accept: `pnpm -C web lint`, `typecheck`, `check:rtl`, `test --run`, `build`.

**WP39 (web): wizard, preflight and readiness strings**
- Files: `features/setup/*` (embed host and keys cards in profile step), `features/setup/system-check-panel.tsx` (new checks), readiness blocker/warning texts, i18n.
- Tests: embedded cards render in profile step; skip dialog shows `jev_fallback_rules`; fa RTL. Accept as WP38.

**WP40: Verification**
1. `make check`.
2. Gated real tests: WP35 real and existing `TestReal|TestCatherdReal`.
3. Personal-hash guard (`find ~/.config/catherd -type f -exec shasum {} + | sort | shasum`, plus `ls -la ~/.claude/agents`) before and after.
4. Browser checklist: set a placeholder Jev key → Check shows Refused (expected for a fake value); remove it; switch host to claude-code → see the two blockers → republish → switch back; doctor per host; fa/en keys tab; `security find-generic-password -s dev.orqen.credential` lists only `cred_*` accounts (manual, Keychain store).
5. Update `DEVIATIONS.md` and `VERIFICATION.md`.

## 7. DoD additions

| Requirement | Shown by |
|---|---|
| Orchestrator host editable, versioned, auditable; per-host doctor; no stale ready | WP36 tests, WP40 checklist |
| Jev/AA/backend isolation keys entered in UI, write-only, rotate/remove/check without spending model tokens or with explicit "cannot verify" | WP35 and WP38 tests |
| Secrets never in logs, events, DTOs, argv, files or Mongo; never in any personal catherd state; no `credentials.json` written | WP35 leak test and real test, WP40 hash guard |
| Jev missing gives SPEC §9 fallback warning, not a blocker; missing isolated-backend key blocks | WP37 tests |

## 8. Risks

1. A child's env is readable by the same OS user (`ps eww`). Accepted under the single-user local model (§17); still better than a plaintext file.
2. The AA check consumes one rate-limited request: runs only on explicit click.
3. Gemini/xAI verification endpoints are vendor APIs outside catherd's contract: a failure gives `unchecked`, never `refused`, unless the response is 401/403.
4. Isolated codex/claude-code/opencode login prerequisites are unverified; the editor warns when enabling those isolations; the Sprint 4 spike decides.
5. Changing the host invalidates all bound versions at once, intentionally, with a one-click republish per profile.

## 9. Needs the USER

- Which host(s) you actually use. Recommendation: `codex` only for now (Orqen's execution host adapter is Codex).
- Enter the Jev key (and AA key, if wanted) through the new UI after WP38. **Rotate the Jev key that was pasted in chat.**
- Whether you use isolated cursor/grok/antigravity harnesses at all; if not, those rows stay "Not set" with no effect.
- Approve that the AA "Check" button spends one AA API request.
- Keep native logins current (`codex login`, `claude auth login`, `opencode auth login`); Orqen only detects them.
