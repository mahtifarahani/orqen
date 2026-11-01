# PLAN-S7-GO: Sprint 7. Close the known gaps (automatic sweeps, roles author, small not-built items, never-walked screens). Test-only.

Based on HEAD `381237a`, with a clean working tree. I checked it read-only against:
- `docs/{STATUS, DEVIATIONS, PLAN-S5-GO (K40/K41, WP84), PLAN-S6-GO, VERIFICATION-S5, VERIFICATION-S6}.md`
- `internal/server/{intake_scheduler, knowledge_sweeps, codegraph, security, tasks, knowledge_author, knowledge_store (verifyVersion), wiki, sources, run_sources, run_stages (packet), readiness, role_stages, role_watch_job, host_session, host_stage, mr_watch_job (merge txn), jobs, jobs_tx, jobs_ops, queue (fenced), automation_state, extension_jobs, walk_setup*_test, s4_env_test}.go`
- `internal/{policy/registry.go, rolebriefs, knowledge/{roles_reply, neverauto, bundle}.go, readiness/evaluate.go, runstate/states.go, orggraph/freshness.go, kindex/sandbox.go, testfakes/hostfake/roles.go}`
- `web/src/{i18n/*, i18n/s6/*, features/{tasks,extensions,graph,security,knowledge,policy}}`

## 0. Facts that drive the design (several correct stale DEVIATIONS rows)

1. **Branch point for every sweep.** `sweep()` in `intake_scheduler.go` is the one place every scheduler sweep is called from: `sweepExtensionHealth`, `sweepBackupDue`, `rollupUsage`, `sweepCompaction`. Every sweep only enqueues, using `txnRetryJob` + `ensureJobTx` (dedupe on active jobs). Jobs fence their writes with `a.fenced(ctx, lease, …)`. `transitionOperation` is a no-op when a job has no operation, so system jobs (no operation) can reuse the route job handlers (`runCodeIndex`, `runSecurityScan`) unchanged.
2. **Validation reuse.** The manual routes do all their validation inline:
   - `startCodeIndex`: usable gate, then project binding.
   - `startSecurityScan`: reviewed head, strix usable, docker, model/budget, then the scan doc and the job with dedupe `secscan:<run>:<head>`.

   The auto sweeps must reuse exactly this logic. So the scan validation moves into one shared function (see WP114).
3. **Head the packet item uses.** `rolePacket` uses the run's `RepoHeadSHA` for the code-impact item. So the auto index has to target `run.RepoHeadSHA`.
4. **Passing review.** Defined by `run.Review{Verdict, Head, BlockingOpen}` plus `run.Heads.Reviewed`. The verdicts are `review.VerdictApproved` / `VerdictApprovedScope`.
5. **Roles author entry point.** The roles author can start from the existing `knowledge_author` job (S5 code), not from the merge transaction. `mr_watch_job.go` already enqueues `knowledge_author` (dedupe `kauthor:<run>`) inside the merge txn, and `runKnowledgeAuthor` reads the pinned policy. **`knowledge.author` (enum deterministic|roles) already exists in the registry** but nothing reads it.
6. **The S4 dispatch path cannot be reused for the author.** `roleStep`/`dispatchRole`/`collectRole` and `hostRun` depend on a `stageCtx` (run lease, `s.hold`, `stopFromError`, `casRun`, `host_sessions.run_id`, `run.CatherdRunID`). After a merge, that catherd run has ended and the worktree may already be cleaned up.
7. **Host and catherd primitives a detached author can call read-only, without editing them:**
   - `a.HostSupervisor.Client/Pin/Unpin` (per org)
   - `a.hostAvailable`
   - `a.hostConfig(ctx, originRun)` (takes the RunDoc by value)
   - `a.Catherd.{ProfileUseRepo, RunsShow, RunsCancel}`
   - `execution.{ReconcileDispatch, DispatchSafeToRetry, ReadReply, ValidateRunDir}`
   - `catherdFromRecord`, `a.familyOf`
8. **catherd roles.** catherd's role list is `architect, verifier, worker, reviewer, ui-reviewer, artist, writer, researcher`. There is no `kverifier`, so the kverifier brief is dispatched as catherd role `verifier`. The in-process `hostfake.Catherd.finalize` answers any role it does not know with a default reply, so it needs writer and verifier-for-knowledge reply scenarios.
9. **Pure parts already exist** (S6, X37): `rolebriefs` `writer.md`/`kverifier.md` (marked `NotDispatched`), `knowledge.ParseWriterReply`/`ParseKVerifyReply`/`MergeVerdicts`. `verifyVersion` runs `knowledge.CheckClaims` and then `NeverAuto`. Merging role verdicts there is a one-line additive change in S5 code.
10. **WP81 (DEVIATIONS) is mostly stale.**
    - `maintainWikiFenced` already has the vault `flock` and a lease fence before every phase.
    - `sweepWiki` is already the scheduler sweep and already purges applied rows.
    - Real gaps:
      - (a) `purge_at` is hard-coded to 7 days; `retention.outbox_days` (default 30) is never read.
      - (b) In fallback mode (the production default), pending outbox rows are never applied or purged, so the collection grows without bound.
11. **WP80 (DEVIATIONS) is partly stale.**
    - Already built: `design_refs` (run_stages.go section 4), the on-demand linked-page fetch (`run_sources.go linkedPages`), and the readiness warnings `docs_not_synced` / `docs_evidence_stale`.
    - Missing K41 parts:
      - the `knowledge_index_fallback` warning
      - the `confluence_ref_unreachable` warning (sync failures are silently skipped by `syncProjectSources`)
      - the rule that clears `confluence_refs_unverified` (today it fires whenever there is at least one ref)
      - a scheduled source sync (`docs.sync_interval_secs` is never read)
      - the `docs.linked_pages_per_run` cap
12. **Policy keys that are never read** (outside the registry): `security.timeout_secs` (the Strix CLI uses a fixed 1 h), `knowledge.wiki.sandbox`, `extensions.sandbox`, `graph.inferred_retention_days`, `retention.outbox_days`, `retention.derived_cache_days`, `docs.sync_interval_secs`, `docs.linked_pages_per_run`, `knowledge.retrieval.cache_ttl_secs`, `knowledge.compaction.threshold`. Specific problems:
    - **Safety bug:** `indexFor` (wiki.go) hard-codes `kindex.SandboxBestEffort` while the policy default is `enforce`.
    - `graph.go:173` passes a literal `30` to `orggraph.Merge` instead of the policy value.
13. **Root cause of the raw codes in the web UI:**
    - `extensions.unusable.*` in i18n has the keys `changed, license_required, not_enabled, not_registered, not_tested, unhealthy`. The server reasons are `not_registered, license_acceptance_required, license_revoked, not_detected, not_tested, disabled, binary_changed, credential_missing, config_invalid, project_not_bound`. So `license_revoked` and most other reasons fall back to the raw code.
    - 37 policy keys have no `policy.keys.*` label: all S4 keys after `ci.require_pipeline`, all S5 keys, plus docs/graph/retention.
    - 6 enum values have no `policy.values.*`: `deterministic, roles, scoped_text, pinned_only, reverify, inbox`.
    - 5 problem codes are untranslated: `conflict_not_open, knowledge_key_taken, knowledge_not_verified, knowledge_pin_type_invalid, rollback_target_invalid`.
    - `context.excluded` lacks `index_stale, index_missing, license_revoked, expired, below_rank, not_for_role, not_synced, not_verified`.
    - The never-auto codes have no i18n at all.
14. **The user's previously uncommitted web files** (app-shell, ui/*, connections-page, tasks-page, …) were committed in `381237a`. They may now be edited. **If `git status` shows new uncommitted user edits when you start, do not edit, stage or revert those files.**
15. **Scheduler sweep interval.** It is fixed at 60 s in a built binary (`SchedSweep` is test-only). Walk steps that wait for a sweep allow 2 minutes.
16. **Strix needs a credential.** The manifest requires the credential `llm_api_key`, and `walkSetupS6` does not set it (the S6 walk typed a dummy value into the UI). Under the S7 rule "never type credentials", an auto-scan walk step needs a decision from the user (section 6).

---

## 1. Hard constraints (restate them to the coder at the start of every WP)

- **Network and services:**
  - Tests and fakes run on loopback only.
  - Never contact or write to real GitLab, Jira or Confluence.
  - Company hostnames never appear in code or fixtures. `git grep -il digikala -- ':!docs'` must stay empty (check it in every WP).
- **No real tools:**
  - No real Codex, catherd, Jev, Strix, GitNexus or Wiki runs.
  - Gated real tests stay skipped.
  - No model spend.
- **Credentials:** never create, type or paste credentials. Tests use `MemoryStore` with the existing obviously fake fixture constants only.
- **Never touch these paths** (read, write or hash only in the walk guard):
  - `~/.config/catherd`
  - `~/.local/share/catherd`
  - `~/.claude/agents`
  - `~/.codex`
  - `~/.openclaw`
  - `~/Library/LaunchAgents`
  - `/Users/you/Projects/reference-project`
- **Git and processes:**
  - Never `git push`.
  - Never `pkill` or `killall`; stop processes by their own pid.
  - Stage with `git add <explicit paths>` only. Never `-A`, `.` or `commit -a`.
  - Commit after every green WP.
- **Environment:**
  - Dev Mongo: `scripts/dev-mongo.sh start`.
  - Build env: `SDKROOT=$(xcrun --sdk macosx --show-sdk-path) MACOSX_DEPLOYMENT_TARGET=$(xcrun --sdk macosx --show-sdk-version)`.
- **Every WP ends with** `make fmt && make check` green: gofmt, vet, `go test -race ./...`, web lint/typecheck/check:rtl/vitest/build, openapi-check.
- **OpenAPI** is hand-authored with `scripts/oas_s7_<area>.py` (reusing the `scripts/oas.py` helpers, as `oas_s6_*.py` do), then `make openapi` regenerates the orval client. Commit the script, `api/openapi.json`, `web/openapi.json` and `web/src/api/generated/` together.
- **S4 state machine untouched.** These files must show **no diff** against `381237a` at the end of the sprint (acceptance check in WP123):
  `internal/server/{role_stages,host_session,host_stage,run_stages,implement_stage,verify_stage,review_stage,publish_stage,comment_stage,mr_watch_job,role_watch_job,run_advance_job,run_actions,runs,runs_create,coding_slots,workspace_jobs,workspace_stage,plan_stages,triage_stage}.go`
- **Test budget:**
  - At most 3 Mongo tests per server WP, in a new `internal/server/s7_<area>_test.go`.
  - Matrices go in pure packages.
  - `t.TempDir()` for every directory; no test reads or writes `$HOME`.
- **`findMany` must always get a non-nil sort.** A nil sort breaks the sweep (WP99 trap).

---

## 2. Key decisions (do not re-litigate; deviations go to DEVIATIONS "Sprint 7")

| # | Topic | Decision |
|---|---|---|
| Y1 | Sweep placement | Both new sweeps live in a **new file** `internal/server/auto_sweeps.go`. `sweep()` gets two call lines after `sweepExtensionHealth`; that is the only edit to `intake_scheduler.go`. Both sweeps only enqueue, never execute. Each does at most one enqueue per org per sweep and reads at most 200 runs. |
| Y2 | `sweepCodeIndexAuto` | **Candidate runs:**<br>- `active`, `repo_head_sha` set<br>- state ∈ {planned, queued, coding, fixing, reviewing}<br>- `hold` nil or `hold.code` ∈ {awaiting_coding_slot, dry_run}<br>- never `restored` or `automation_paused`, and the org not paused<br>**Pinned** `codegraph.auto_index == on_run`, `gitnexusUsable(org, project)` ok (license gate and project binding), no ready, queued or indexing index for (repo_key, sha), and no active `code_index` job in the org.<br>**Not retried automatically:** `failed` and `license_revoked` indexes. The person uses Index now.<br>**Enqueue:** `upsertCodeIndex` + `ensureJobTx(org, "code_index", "code_index:"+cix.ID, 2, {cix_id, trigger:"auto"})`. This is the same dedupe key as the route, so route and sweep never double-run. No operation is created.<br>`CodeIndexDoc.Trigger` (manual\|auto) is added and exposed as `trigger` in `CodeIndexDto`. |
| Y3 | `sweepSecurityAuto` (X25) | **Candidate runs:**<br>- `active`, state ∈ {awaiting_ci, ready_for_merge}<br>- hold nil or `dry_run` (the scan is local and read-only, so the external-write gate does not apply; never `restored` or `automation_paused`; org not paused)<br>- `heads.reviewed != ""`, `review.head == heads.reviewed`, `review.verdict` ∈ {approved, approved_with_scope}, `review.blocking_open == 0`<br>**Policy:** pinned `automation.security_scan == auto` (opt-in, model spend). Model and budget come from the same effective policy the route reads.<br>**Also required:** strix usable, docker usable, no non-cancelled scan for (run, reviewed head) whatever its completion (an incomplete auto scan is never repeated automatically), and no queued or running scan in the org.<br>**Enqueue:** through the shared `a.queueScan(sc, org, run, trigger)`, extracted from `startSecurityScan`; dedupe `secscan:<run>:<head>`. `SecurityScanDoc.Trigger` is added and exposed as `trigger` in `SecurityScanDto`. |
| Y4 | Pure decisions | `codegraph.AutoIndexDecision(AutoIndexInput) (bool, string)` and `secscan.AutoScanDecision(AutoScanInput) (bool, string)` hold the whole decision table. The server only gathers facts. Skip reasons are an enumerated list tested in the pure packages and not stored, so a sweep writes nothing when it skips. |
| Y5 | Task security summary | **Org summary:** `TaskSummaryCountsDto.security{open_blocking, scans_running}` (the original WP102 plan). It shares one helper `a.securityCounts(ctx, org)` with `getSecurityStatus`.<br>**Per task:** `TaskSummaryDto.security`, nullable, `{scan_id, head_sha, state, completion, open_blocking, stale}` for the latest non-cancelled scan of the task's latest run. It is computed in one aggregate per page (no N+1), and `stale` = scan head ≠ the run's reviewed head. |
| Y6 | Roles author: no S4 code changes | A **detached author session** with its own collection, job, lease and catherd run. It never writes `runs`, `role_executions`, `host_sessions` or the origin run's catherd run, and it never calls `s.hold`/`stageStop`/`casRun`/`stopFromError`/`roleStep`. **Existing S4 code that changes: none.**<br>**Additive edits outside S4:**<br>- `knowledge_author.go`: a branch on `knowledge.author`<br>- `knowledge_store.go`: the `RoleVerdicts` field plus one `MergeVerdicts` call in `verifyVersion`<br>- `restore.go`: one post-restore mark<br>- `db.go`, `ids.go`, `jobs.go`, `app.go`: registration only<br>- `internal/testfakes/hostfake`: reply scenarios<br>The detached host adapter **duplicates** about 100 lines of `hostRun.call/callWith` in a new file instead of generalising `hostRun`. This is deliberate and recorded in DEVIATIONS. |
| Y7 | Session model | `knowledge_author_sessions` (`kas_`).<br>**Created by** `runKnowledgeAuthor` when trigger=merge, the pinned `knowledge.learn_on_merge` is true and the pinned `knowledge.author == roles`. Created in one transaction with `ensureJobTx(org, "knowledge_author_roles", "kauthor-roles:"+org, 1000, {})`. Unique dedupe `kas:merge:<run>`.<br>**One job per org** serialises the sessions: it advances the oldest non-terminal session; when a role is running it re-enqueues itself with delay `roles.poll_interval_secs`; when a session ends it re-enqueues immediately if another session is waiting.<br>**No coding slot** (no repository writes). |
| Y8 | Detached catherd run | A scratch directory `<data>/kauthor/<org>/<kas>/repo` (0700, inside the data dir, never a personal checkout), initialised with `git init -q` through `workspace.Git`. Then `ProfileUseRepo(env, HostCodex, <origin pinned profile>.CatherdName, repo)`, then `run_start{repo, title:"knowledge: <KEY>", a_lines:["draft knowledge records from the evidence bundle"]}`. The run dir is validated with `execution.ValidateRunDir(<catherd home>/data, dir)`.<br>**Assumed contract** (fake-verified only): catherd accepts an empty initialised repo. The gated `TestCatherdReal` stays the user's check. |
| Y9 | Dispatch | Same Codex app-server client (supervisor, per org), a **fresh thread per session** (`StartThread{Cwd: env.Tmp()}`), `route{run, role}` for the rung, then `dispatch{run, role, name, rung, brief}`.<br>**Writer:** catherd role `writer`, name `writer-K<attempt>`, brief `rolebriefs.Writer` with `Context = knowledge.RenderBundle(bundle)`.<br>**Knowledge verifier:** catherd role `verifier`, name `kverify-K<attempt>`, brief `rolebriefs.KVerifier` with `Context = knowledge.RenderDraftForVerify(records)`. Claim ids are `<key>/c<n>`.<br>**Watch and collect:** `RunsShow` and `RecordFor(dispatchID)`. Past `roles.max_wall_secs` → `RunsCancel` and `failed{timeout}`. `result{run, name}` plus `ReadReply` → artifacts `knowledge_writer_reply` / `knowledge_kverify_reply`.<br>**Crash recovery:** `ReconcileDispatch` adopts a dispatch found by name; when it is unknown and `DispatchSafeToRetry` is not true → `failed{dispatch_uncertain}`. **Never a blind second dispatch.** |
| Y10 | Preconditions (all from the origin run's pinned profile and policy) | - pinned profile enables catherd roles `writer` **and** `verifier` (`catherd.RoleEnabled`); otherwise `failed{writer_role_disabled\|verifier_role_disabled}` with zero dispatches<br>- host available; otherwise retry with backoff while `host_failures < 5`, then `failed{host_unavailable}`<br>- org paused → the job re-enqueues with a 60 s delay without dispatching<br>- origin run missing → `failed{origin_missing}` |
| Y11 | Never auto verify | `knowledge.NeverAuto` gets the new code **`roles_authored_needs_review`**, set whenever `Origin.Kind == "roles"`. This is strictly more conservative: no role-written text ever becomes verified without a person. The existing codes and the deterministic path are unchanged.<br>The writer's unknown or missing refs stay `unsupported` (ParseWriterReply). Knowledge-verifier verdicts are stored as `KnowledgeVersionDoc.RoleVerdicts` and merged with `knowledge.MergeVerdicts` (downgrade only) right after `CheckClaims` in `verifyVersion`, so they can only lower a gate verdict. The injection lint runs as for any draft. |
| Y12 | Deterministic fallback | Any terminal failure, cancel included, leaves the run's knowledge to the deterministic author: `authorRun(org, run, "merge")` (dedupe `merge:<run>`, so it runs at most once). For `failed` this happens automatically when learn_on_merge is true; otherwise through the session action "Use deterministic draft". `kas.fallback_proposal_id` is recorded. |
| Y13 | Result | A `knowledge_proposals` doc with trigger `merge_roles` and dedupe `roles:<kas>`. Items are drafts only (Y11); a key collision adds a draft version (same rule as `authorRun`). Origin `{kind:"roles", proposal_id, actor:"writer"}`. |
| Y14 | Brief version | Writer/kverifier texts drop "(not dispatched)" and gain a fixed "no tools, no files, no other agents" line, so `rolebriefs.Version` becomes **"s7.1"**. The four dispatched briefs must be byte-identical except the version string (golden diff check). `NotDispatched` is renamed `DetachedOnly`, and a census test checks that no S4 stage file references `rolebriefs.Writer`/`KVerifier`. |
| Y15 | Policy | **No new policy keys**: count stays **77**, `ProductDefaultsVersion` stays **5**, `SchemaVersion` stays **2**. The existing keys are wired instead (Y16). If a WP seems to need a new key, stop and record it; that would change the pins (78, PDV 6, labels, backup manifest). |
| Y16 | Wire dead keys | - `knowledge.wiki.sandbox` → `indexFor` (enforce by default; `a.noSandbox` → best effort in tests)<br>- `extensions.sandbox` → `codeGraphIndex`/`secScanner` Mode<br>- `security.timeout_secs` → secscan `CLI.Timeout`<br>- `graph.inferred_retention_days` → `orggraph.Merge`<br>- `retention.outbox_days` → wiki purge and fallback retention<br>- `docs.sync_interval_secs` → `sweepSourceSync`<br>- `knowledge.compaction.threshold` → compaction group size, if the job has a fixed constant today; otherwise listed as deferred<br>**Census test** `TestEveryPolicyKeyIsRead` (internal/server): every registry key appears as a string literal in a non-test Go file under `internal/server` or `internal/providers`, except an explicit `deferredPolicyKeys` map with a reason per key. Expected deferred: `docs.linked_pages_per_run`, `retention.derived_cache_days`, `knowledge.retrieval.cache_ttl_secs` (unless trivially wired), each with a DEVIATIONS line. |
| Y17 | Wiki retention | `purge_at = now + retention.outbox_days` (org live policy). In `sweepWiki`, for orgs where `indexFor == nil` (fallback): delete pending rows with `created_at < now − retention.outbox_days` and set `rebuild_requested=true` in the same txn, so a wiki configured later rebuilds from MongoDB (canonical). Knowledge itself is never touched. |
| Y18 | Docs K41 | **New derived collection** `source_sync_states` (`_id` = project id; classified **exclude** in backups): `org_id`, `refs[{connection_id, page_id, ok, code, at}]`, `last_sync_at`. `syncProjectSources` records every ref result (it skips failures exactly as today).<br>**Readiness inputs:** `ConfluenceVerified` (refs whose last sync succeeded), `ConfluenceSyncFailed`, `KnowledgeIndex` (`""` \| `degraded`).<br>**Warnings (never blockers):**<br>- `confluence_refs_unverified` only when verified < refs<br>- `confluence_ref_unreachable` when some ref failed<br>- `knowledge_index_fallback` only when the openclaw-wiki extension is usable for the org **and** the vault is degraded or behind. Plain "not configured" is the designed default and gives no warning.<br>`sweepSourceSync` enqueues `source_sync` (route dedupe `source_sync:<prj>`) when `docs.sync_interval_secs > 0` (live project policy), the project is active with page refs, the org is not paused and `last_sync_at` is older than the interval. |
| Y19 | i18n code census | **Go test** `TestI18nCodeCensus` builds `web/src/test/fixtures/server-codes.json` (golden; `-update` rewrites it) from:<br>- problem codes (go/ast over `errors.go` switch cases and `problem("…")` / `&AppError{Code:"…"}` literals in non-test server files) → `problems.*`<br>- `extensions.Reason*` → `extensions.unusable.*`<br>- policy registry → `policy.keys.<slug>.label` and `policy.values.<enum>`<br>- `runstate.Holds` → `run.hold.*`<br>- contextpkt reasons plus codegraph item reasons → `context.excluded.*`<br>- `skills` reasons → `skills.excluded.*`<br>- `knowledge.AllNeverAuto` → `knowledgePage.neverAuto.*`<br>- readiness `block(…)`/`warn(…)` codes (go/ast over `internal/readiness/evaluate.go`) → `readiness.reasons.*`<br>- secscan completions → `security.completion.*`<br>- code-index states → `codeGraph.state.*`<br>- kauthor states and fail codes → `knowledgePage.author.*`<br>**Vitest** `server-codes.test.ts` checks every listed key in the merged en **and** fa trees. New strings go in `web/src/i18n/s6/s7-codes.{en,fa}.json`. The existing merge loader picks up `*.en.json` / `*.fa.json` in that folder, so no loader change is needed; its key-parity test still applies. |
| Y20 | What stays deferred | See section 5. |

---

## 3. Data model, API, jobs, events, errors

**ID prefixes:** `kas` (`kasId` path param validated in `apiMiddleware`).

**Collections** (`Migrate`, comment "Sprint 7 collections (additive; schema version stays 2)"):

| Collection | Fields | Indexes | Backup |
|---|---|---|---|
| `knowledge_author_sessions` (`kas_`) | org_id, run_id, project_id, dedupe_key, attempt, trigger (merge\|retry), state, code, host_failures, host{thread_id, last_directive{id, tool, args_hash, state, turn_id}}, catherd{run_id, run_dir, repo_rel}, writer{name, rung, family, dispatch_id, directive_id, dispatching_at, dispatched_at, status, reply_artifact_id, tries}, kverifier{same}, bundle_artifact_id, proposal_id, fallback_proposal_id, created_at, updated_at, revision | unique (org_id, dedupe_key); (org_id, state, created_at); (org_id, run_id) | **include**; post-restore: non-terminal → `failed{restored_incomplete}` |
| `source_sync_states` | Y18 | unique `_id`; (org_id) | **exclude** (derived) |
| `code_indexes` (existing) | + `trigger` | — | unchanged (excluded) |
| `security_scans` (existing) | + `trigger` | — | unchanged |
| `knowledge_versions` (existing) | + `role_verdicts[{claim_id, verdict, reason}]` (omitempty) | — | unchanged |

**Routes (org)**, script `scripts/oas_s7_kauthor.py`:

| Method + path | operationId | Headers | Response / errors |
|---|---|---|---|
| GET `/knowledge-author-sessions` | `list_knowledge_author_sessions` | — | `run_id?` → `{items: KnowledgeAuthorSessionDto[]}` |
| GET `/knowledge-author-sessions/{kasId}` | `get_knowledge_author_session` | — | DTO with ETag |
| POST `/knowledge-author-sessions/{kasId}/cancel` | `cancel_knowledge_author_session` | If-Match | DTO; 409 `kas_terminal` |
| POST `/knowledge-author-sessions/{kasId}/retry` | `retry_knowledge_author_session` | Idem | 201 new DTO (attempt+1); 409 `kas_not_failed` |
| POST `/knowledge-author-sessions/{kasId}/fallback` | `fallback_knowledge_author_session` | Idem | `KnowledgeProposalDto`; 409 `kas_active` |

**DTO:** `KnowledgeAuthorSessionDto{id, run_id, attempt, trigger, state, code, writer{name, rung, status}, kverifier{…}, proposal_id, fallback_proposal_id, created_at, updated_at, revision}`. It never contains paths, the brief text or reply text; artifact ids only.

**Changed DTOs:**
- `TaskSummaryCountsDto.security`
- `TaskSummaryDto.security` (nullable)
- `SecurityScanDto.trigger`
- `CodeIndexDto.trigger`
- `KnowledgeVersionDto.role_verdicts` (optional)

Scripts: `oas_s7_tasks.py`, `oas_s7_sweeps.py`.

**Jobs:** `knowledge_author_roles` (dedupe `kauthor-roles:<org>`, max attempts 1000, self re-enqueue). Existing `code_index`, `security_scan` and `source_sync` jobs are reused by the sweeps.

**Events** (ids and codes only):
- `knowledge.author_session{kas_id, run_id, state, code}`
- `code_index.auto_queued{cix_id, commit_sha}`
- `security.scan_auto_queued{scan_id, run_id}`
- `source.sync_failed{project_id, count}` (counts only)

**Errors:** `kas_terminal`, `kas_not_failed`, `kas_active` (409). All added to `problem()` and covered by the i18n census.

**kauthor fail codes:** `host_unavailable`, `catherd_unavailable`, `writer_role_disabled`, `verifier_role_disabled`, `dispatch_uncertain`, `reply_unparseable`, `role_failed`, `timeout`, `restored_incomplete`, `origin_missing`, `cancelled`.

---

## 4. Work packages (WP111 to WP123; one committer, strictly in order)

Every WP ends with `make fmt && make check`, then `git add <explicit paths>`, then a commit. Run the digikala grep each time.

### Pure packages first

#### WP111: Pure auto-sweep decisions (codegraph, secscan)
**Files:**
- `internal/codegraph/auto.go`: `AutoIndexInput{Mode, UsableReason, RunState, HoldCode, OrgPaused, HeadSHA, IndexState (""\|queued\|indexing\|ready\|failed\|license_revoked\|removed), ActiveIndexJobs int}` and `AutoIndexDecision`. Skip reasons: `mode_off, not_usable, run_state, hold, org_paused, no_head, index_exists, index_failed, busy`.
- `internal/codegraph/auto_test.go`
- `internal/secscan/auto.go`: `AutoScanInput{Mode, UsableReason, DockerOK, ModelSet, BudgetOK, RunState, HoldCode, OrgPaused, ReviewVerdict, ReviewHead, ReviewedHead, BlockingOpen, ScanExistsForHead, ScansRunningInOrg}` and `AutoScanDecision`. Skip reasons: `mode_not_auto, not_usable, docker_unavailable, model_unset, budget_unset, run_state, hold, org_paused, unreviewed, review_not_passing, head_mismatch, scan_exists, busy`.
- `internal/secscan/auto_test.go`

**Tests (table):**
- every skip reason is reachable
- `restored` and `automation_paused` holds always skip; `dry_run` and `awaiting_coding_slot` holds do not
- `failed` / `license_revoked` indexes never auto-retry
- an incomplete existing scan blocks a repeat
- `changes_requested` never scans
- `manual` and `off` never scan

**Acceptance:** `go test -race ./internal/codegraph ./internal/secscan`.

#### WP112: Pure roles-author core (kauthor state machine, knowledge helpers, rolebriefs s7.1)
**Files:**
- `internal/kauthor/{state.go, next.go, kauthor_test.go}` (new pure package; no server import):
  - States: `queued, starting, writer_dispatching, writer_running, writer_done, kverify_dispatching, kverify_running, kverify_done, applied, failed, cancel_requested, cancelled`
  - `Terminal(state)`
  - `Next(Facts) Step` with Steps `StartHost, StartCatherdRun, DispatchWriter, Reconcile, Wait, CollectWriter, RetryWriter, DispatchKVerify, CollectKVerify, Apply, Fail(code), Cancel, Pause`
  - Facts: host up, roles enabled, org paused, dispatch known/uncertain, record present, wall exceeded, parse ok, tries, cancel requested
- `internal/knowledge/{bundle_render.go (BundleRefs(Bundle) map[string]string; RenderBundle(Bundle) string, deterministic, ≤ 16 KiB, ref_id on every line), draft_render.go (RenderDraftForVerify([]WriterRecord) string with claim ids "<key>/c<n>"; SplitVerdicts([]KVerdict) map[key][]KVerdict with ids mapped back to c<n>)}`
- `internal/knowledge/neverauto.go`: `NARolesAuthored = "roles_authored_needs_review"`, added to `AllNeverAuto`; `NeverAuto` sets it when `v.Origin.Kind == "roles"`. Check that `Version` carries Origin in the domain model; if it does not, add it.
- `internal/rolebriefs/{briefs.go (Version "s7.1"; DetachedOnly replaces NotDispatched), writer.md, kverifier.md}`, `testdata/*.golden.md` (header line only), `briefs_test.go`

**Tests:**
- `Next` matrix:
  - kverify never before the writer reply is parsed
  - an uncertain dispatch → `Fail(dispatch_uncertain)`, never a second dispatch
  - one writer retry after an unparseable reply, then `Fail(reply_unparseable)`
  - cancel from every non-terminal state
  - paused → `Pause`
- `RenderBundle` is deterministic and every ref of `BundleRefs` appears in it
- `SplitVerdicts` round trip
- `NeverAuto` with origin roles always contains the new code, and the deterministic origin is unchanged (existing tests stay green)
- golden diff: only line 1 differs between s6.1 and s7.1 (assert by stripping line 1)
- census: `internal/server/{role_stages,host_stage,implement_stage,verify_stage,review_stage}.go` never contain `rolebriefs.Writer` or `rolebriefs.KVerifier`

**Pin changes:** `briefs_test.go` L66/L70 (`s6.1` → `s7.1`); 5 goldens; `TestWriterAndKVerifierTemplatesExistButAreNotDispatched` → `TestWriterAndKVerifierAreDetachedOnly`; `internal/server/s4_host_test.go:37` `"s6.1"` → `"s7.1"` (a test file only; the S4 production files are untouched).

**Acceptance:** `go test -race ./internal/kauthor ./internal/knowledge/... ./internal/rolebriefs`, plus `go test ./internal/server -run 'TestS4Host'` with the updated pin.

#### WP113: Pure readiness K41
**Files:** `internal/readiness/evaluate.go`, `evaluate_test.go`.
- New inputs `ConfluenceVerified int`, `ConfluenceSyncFailed int`, `KnowledgeIndex string`.
- Warnings per Y18; `docs_not_synced` and `docs_evidence_stale` are unchanged.

**Tests:**
- `confluence_refs_unverified` is cleared once verified == refs
- `confluence_ref_unreachable` with a failed ref
- `knowledge_index_fallback` only for `degraded`
- none of these is ever a blocker (`Ready` unchanged)

**Pin change:** any existing test expecting `confluence_refs_unverified` merely because refs > 0 now passes `ConfluenceVerified: 0` explicitly. List each edited assertion in the commit message.

**Acceptance:** `go test -race ./internal/readiness`.

### Server wiring

#### WP114: `sweepCodeIndexAuto` and `sweepSecurityAuto`
**Files:**
- `internal/server/auto_sweeps.go` (new; Y1 to Y4)
- `intake_scheduler.go` (two call lines in `sweep()`)
- `codegraph.go`: `Trigger` field and DTO; the route sets `manual`; the job payload's `trigger` is copied
- `security.go`: extract `a.queueScan(sc, org, run, trigger) (SecurityScanDoc, Output, error)` and `a.scanPolicy(ctx, org, project) (ScanConfigRef, *AppError)` from `startSecurityScan` (behaviour-preserving; the existing `s6_security_test.go` must stay green); `Trigger` field and DTO
- `scripts/oas_s7_sweeps.py`, `s7_sweeps_test.go`

**Rule:** fail closed. Any error reading usability or policy skips that run (logged with `slog.Warn` with ids only) and never aborts the whole `sweep()`. A database error is returned, as the other sweeps do.

**Tests (3):**
1. **Code index:**
   - Setup: gitnexus usable with the `a.CodeGraph` fake, bound, pinned `on_run`; a planned run with `repo_head_sha` A.
   - Two sweeps → exactly one `code_index` job and one `cix` for A (trigger auto); the job runs → ready.
   - Each of these leaves no job: policy `off`; license revoked; org paused; hold `restored`; an existing `failed` index for A.
2. **Security:**
   - Setup: a run in `awaiting_ci` with an approved review on head H (= reviewed); pinned `auto`; model and capped budget set; `a.Scanner` fake (`findings`); docker seam ok.
   - Two sweeps → exactly one scan (trigger auto); it finishes; a third sweep → none (also when the first completion was `stopped_budget`).
   - None for: `manual`; `changes_requested`; review head ≠ reviewed head; model unset; another scan running in the org.
3. **Safety:**
   - Dry-run hold → a scan is queued.
   - `/_fakes/gitlab/writes` is unchanged and the extwrite census passes.
   - The run docs' `revision` is unchanged by both sweeps (sweeps never write `runs`).
   - The LLM fixture value is absent from every Mongo document.

**Acceptance:** `go test -race ./internal/server -run 'TestS7Sweep|TestSecurityScan|TestCodeIndex'`; openapi-check clean.

#### WP115: Security summary counts in tasks.go
**Files:**
- `tasks.go`: `securityCounts`; `taskSecurity(ctx, org, runIDs) map[runID]bson.M` (one `$match/$sort/$group` aggregate on `security_scans` plus one count aggregate on `security_findings` grouped by scan_id); `listTasks`, `getTask` (through the `SummaryDTO` callers) and `getTaskSummary` add it
- `security.go`: `getSecurityStatus` reuses `securityCounts`
- `scripts/oas_s7_tasks.py`, `s7_tasks_test.go`

**Tests (2):**
1. A running scan plus a blocking open finding → summary `{open_blocking:1, scans_running:1}`. The list item of that task has `security{completion, open_blocking:1, stale:false}`. A waive → 0. A new reviewed head → `stale:true`. A task without scans → `security:null`.
2. Org isolation: a second org's scans never count.

**Acceptance:** those tests plus `TestContract`; openapi-check clean.

#### WP116: Roles author, part 1 (sessions, apply, fallback; no host yet)
**Files:**
- `internal/server/knowledge_roles.go` (new): `KnowledgeAuthorSessionDoc`, store, DTO, the 5 routes, `createAuthorSessionTx`, `applyAuthorReplies(ctx, kas, writerReply, kverifyReply string) (KnowledgeProposalDoc, error)` (Y11, Y13), `fallbackAuthor(ctx, kas)` (Y12)
- `knowledge_author.go`: in `runKnowledgeAuthor`, when trigger=merge, learn_on_merge and `knowledge.author == "roles"` → `createAuthorSessionTx` + `ensureJobTx` in one fenced txn, then complete the job. Otherwise unchanged.
- `knowledge_store.go`: `RoleVerdicts` field and domain mapping; one line after `CheckClaims`:
  `if len(v.RoleVerdicts) > 0 { out.Gate.Claims = knowledge.MergeVerdicts(out.Gate.Claims, v.roleKVerdicts()) }`
- `restore.go`: post-restore mark (non-terminal kas → `failed{restored_incomplete}`)
- `db.go`, `ids.go`, `app.go` (routes, idemRequired)
- `internal/backup/classes.go` (+ `knowledge_author_sessions` include, + `source_sync_states` exclude; restore order)
- `jobs.go`: register `knowledge_author_roles` with a stub handler that marks the session `failed{host_unavailable}` and falls back. WP117 replaces the stub.
- `scripts/oas_s7_kauthor.py`, `s7_kauthor_test.go`

**Tests (3):**
1. Policy `roles`: the merged run's `knowledge_author` job creates exactly one session (twice-run job → still one, dedupe) and **no** deterministic proposal. Policy `deterministic` → unchanged behaviour (the existing `s5_proposals_test` stays green).
2. `applyAuthorReplies` with fixture replies:
   - a writer reply with one supported ref, one unknown ref and one injection-suspect body
   - a kverify reply that downgrades a claim to `contradicted`
   - Result: drafts with origin `roles`; the unknown ref is unsupported (`unknown_ref`); the downgraded claim is merged in verify.
   - With `knowledge.promotion.*=auto`, nothing is verified (never-auto contains `roles_authored_needs_review`).
   - A user verify with attestations works and the role verdict still downgrades (it cannot be upgraded).
3. Fallback and routes:
   - stub job → `failed{host_unavailable}` → the deterministic proposal `merge:<run>` exists exactly once (calling fallback twice → no second one)
   - `retry` on a failed session → attempt 2
   - `cancel` on a terminal session → 409 `kas_terminal`
   - the restore mark sets `restored_incomplete`

**Acceptance:** these tests, `s5_proposals_test`, `TestEveryCollectionIsClassified`, `TestRestoreOrderCoversExactlyTheIncludedCollections` and `TestContract` green.

#### WP117: Roles author, part 2 (detached host execution against the in-process fakes)
**Files:**
- `internal/server/kauthor_host.go` (new):
  - `detachedHost{a, kas, lease, env, cfg, client, thread, timeout, unpin}`
  - `a.openDetachedHost(ctx, origin, kas, lease)`, which reuses the read-only calls of fact 7
  - `call/callWith`: a copy of the `hostRun.callWith` semantics, but `last_directive` is written to `kas.host` and the events subject is `{"knowledge_author_session", id}`, all fenced with the kas job lease
  - `ensureCatherdRun` (Y8)
  - `dispatchRole(spec)` / `collectRole` per Y9
- `internal/server/kauthor_job.go` (new): `runKnowledgeAuthorRoles` drives `kauthor.Next` with CAS on `kas.revision` + state in every fenced txn, applies through WP116, and uses `fallbackAuthor` on failure. It replaces the WP116 stub.
- `internal/testfakes/hostfake/{scenario.go, roles.go}`: `Scenario.WriterReply`, `Scenario.KVerifyReply` (strings, defaults are valid fixture replies citing the run's refs) and `RoleStatuses` for `writer-K1`/`kverify-K1`. The verifier case uses the knowledge reply when the dispatch name starts with `kverify-`.

**Tests (3), in `s7_kauthor_host_test.go`, using `newS4Env` with a profile that enables writer and verifier with different families:**
1. **Happy path:**
   - Run to merge, policy `roles`.
   - The session goes queued → applied.
   - The fake catherd saw exactly 2 dispatches (`writer-K1`, `kverify-K1`) in a run whose repo is under `<data>/kauthor/<org>/`, with role `verifier` for the second.
   - Drafts exist and none is verified.
   - The origin run document is byte-equal before and after the session (canonical JSON); no new `role_executions` or `host_sessions` rows for the origin run.
   - `/_fakes/gitlab/writes` is unchanged and `GET /_fakes/jira/writes` is empty.
2. **Failures:**
   - writer reply unparseable twice → `failed{reply_unparseable}` with exactly 2 writer dispatches, then the deterministic proposal
   - writer disabled in the profile → `failed{writer_role_disabled}`, zero dispatches, fallback
   - host stopped → `host_unavailable` after 5 failures, fallback
3. **Crash and cancel:**
   - a lease lost right after the dispatch call (test hook) → the next job pass adopts the dispatch by name and never dispatches twice
   - cancel while running → `RunsCancel` was called, state `cancelled`, no drafts
   - org paused → no dispatch until it is unpaused

**Acceptance:**
- these tests and every `TestS4*` / `TestS5*` test green
- `git diff --exit-code 381237a -- <S4 file list from section 1>` empty
- the extwrite census green

#### WP118: Wiki and graph retention, sandbox and timeout wiring, policy census
**Files:**
- `wiki.go`: Y17, and `indexFor` sandbox from `knowledge.wiki.sandbox` (org live policy; `a.noSandbox` → best effort)
- `graph.go`: retention from `graph.inferred_retention_days`
- `codegraph.go` and `security.go`: Mode from `extensions.sandbox`; `secScanner` Timeout from `security.timeout_secs`
- knowledge compaction threshold, if it is a constant today
- `policy_wiring_test.go` (the census, Y16)
- `s7_retention_test.go`

**Tests (2):**
1. Wiki:
   - fallback org with an old pending row → deleted and `rebuild_requested` set; a recent row is kept
   - an applied row's `purge_at` uses the policy days
   - with the `a.Index` fake, a rebuild projects every verified version (existing `TestWiki*` green)
2. Graph and secscan:
   - a stale inferred edge older than the policy days is removed; a younger one is kept
   - the secscan CLI built by `secScanner` has `Timeout == security.timeout_secs`
   - `indexFor` returns `SandboxEnforce` under the default policy

Plus the census test.

**Acceptance:** these tests, `TestWiki*`, `TestGraph*` and `TestExtensionWikiThroughCatalog` green. The `deferredPolicyKeys` map has a DEVIATIONS line per entry.

#### WP119: Docs K41 wiring and scheduled source sync
**Files:**
- `sources.go`: `syncProjectSources` records per-ref results into `source_sync_states` (fenced in the job)
- `readiness.go`: fills the WP113 inputs
- `auto_sweeps.go`: `sweepSourceSync` (Y18), plus a call line in `sweep()`
- `db.go`, `s7_docs_test.go`

**Tests (2), with the fake Confluence:**
1. Page 1001 ok and a page that answers 404 → readiness warnings `confluence_ref_unreachable` + `confluence_refs_unverified`, never a blocker. Fix the ref, sync → both cleared.
2. `docs.sync_interval_secs=60` with a clock seam (`last_sync_at` set in the past) → one `source_sync` job across two sweeps; `0` → none; org paused → none.

**Acceptance:** these tests and `s5_sources_test` green.

### Web

#### WP120: Raw codes translated (i18n census)
**Files:**
- `internal/server/i18n_codes_test.go` and `web/src/test/fixtures/server-codes.json` (Y19)
- `web/src/test/server-codes.test.ts`
- `web/src/i18n/s6/s7-codes.{en,fa}.json`
- In `extensions-page.tsx`, `extension-sheet.tsx`, `graph/code-panel.tsx` and `security/security-page.tsx`, keep the `extensions.unusable.<server reason>` lookups; the keys now exist. Delete the stale unusable keys only when grep shows no reader.
- Wherever never-auto codes are rendered on the knowledge page, use `knowledgePage.neverAuto.*`.

**Strings to add (en and fa):**
- 10 extension reasons
- 37 policy labels + help
- 6 enum values
- 5 + 3 new problems
- 8 context exclusions
- 14 never-auto codes
- the new readiness warnings
- kauthor states and codes

**Tests:**
- the census vitest passes; the existing `i18n.test.ts` and `s6-i18n.test.ts` stay green
- a vitest renders an extension card with reason `license_revoked` in fa and en and asserts no raw code is visible

**Acceptance:** `pnpm -C web vitest run`, `check:rtl`; the Go census test is green and the golden file is up to date.

#### WP121: Web for the S7 features
**Files:**
- `web/src/api/hooks/{knowledge.ts (useAuthorSessions, useProposals, cancel/retry/fallback with ifMatch + one Idempotency-Key per intent), tasks.ts}`
- `features/tasks/tasks-page.tsx`: Security badge column, shown only when `security` is non-null; only `complete` with 0 open blocking is green; a stale chip; `<Ltr>` for sha
- `features/overview/work-cards.tsx`: security counts
- `features/knowledge/{proposals-section.tsx, author-sessions.tsx}` added to `knowledge-page.tsx`: proposals list, session state badge, translated code, Retry / Use deterministic draft / Cancel, and a note that role-written drafts always need review
- `features/security/scan-list.tsx`: auto chip
- `features/graph/code-panel.tsx`: auto chip
- `api/sse.ts`: `knowledge.author_session*` → `${org}/knowledge-author-sessions*` and `${org}/knowledge-proposals*`; `security.*` additionally → `${org}/tasks*`; check that `code_index.*` (the actual event names) invalidates the code-index queries, and add it if missing
- `sse-sprint7.test.ts`

**Tests:**
- tasks badge states
- session actions disabled per state with the reason
- fallback confirms
- fa/en parity
- RTL snapshot of the knowledge page section

**Acceptance:** `make check`.

### Fakes and walk

#### WP122: Fakes and walk fixtures
**Files:**
- `internal/testfakes/gitlab.go`: the branch API returns the head commit id and moves it on `PUT /_fakes/gitlab/file`. This lifts the WP110 limitation so graph staleness can be walked. Existing fake tests stay green.
- `cmd/orqen-fakes`: check that `PUT /_fakes/host/scenario` passes `writer_reply` / `kverify_reply` through (no code change expected).
- `walk_setup_s7_test.go`: active only with `ORQEN_WALK_S7=1`. `walkPolicyS7()` merges `codegraph.auto_index=on_run`, `automation.security_scan=auto` and `knowledge.author=roles` into the single publish before automation is enabled (WP109 ordering rule), and the walk profile enables writer and verifier. **It sets no credential**: it only reuses the existing fixture constants.
- `docs/LIVE-TRIAL.md`: one paragraph saying the roles author is covered by the user's `TestCatherdReal`/Codex spike only.

**Acceptance:** `go test ./internal/testfakes/...`; `TestWalkSetup` still skips without `ORQEN_WALK_URL`.

#### WP123: Browser walk and docs
**Environment:**
- `make build` binary on 47601, database `orqen_verify_s7`, throwaway `ORQEN_DATA_DIR`, `ORQEN_CREDENTIAL_STORE=memory`
- `cmd/orqen-fakes` on 47600; fakehost wrappers (codex, catherd, openclaw, gitnexus, strix) as in S6
- `TestWalkSetup` with `ORQEN_WALK_S7=1`
- Browser pane at 800x600, and 375x812 for Persian
- Loopback only
- **The walker never types a credential into any field**

**Steps** (en and fa, Persian checks at 375 px):
1. **Personal-state guard before:** hash the section 1 paths (catherd logs excluded, as in S5/S6); `test -e ~/.openclaw` checks existence only.
2. **Licence flows:** Extensions → GitNexus:
   - Detect is disabled and the reason is translated
   - the licence dialog with a wrong phrase shows an error; then the right phrase
   - register the wrapper path; Detect, Test, Enable, bind the project
   - Revoke → the card shows the translated "licence revoked" (no raw code); accept again
3. **Graph code panel:** Index now → ready at commit A; impact query; `PUT /_fakes/gitlab/file` → the stale banner names A; with `on_run`, start a new run → an **auto** index for the new head appears within 2 minutes; the Context tab shows `index_stale` translated before it.
4. **Skills tabs (Models & catherd):**
   - in a fresh org slug, "Use recommended set" on a project that has no bindings
   - import the poisoned fixture through the dialog (paste); flags show their lines
   - Approve stays disabled until every flag is acknowledged; approve
   - org bindings editor: budget bar
   - project Skills tab: override disables one skill
5. **Roles tab skills manifest:** start a new run after binding → the Roles tab lists the included and excluded skills; the brief artifact contains the preamble.
6. **Tasks page:**
   - the security badge after a manual scan (only if the Strix credential already exists, see section 6; otherwise mark "not walked, test-covered")
   - overview security counts
7. **Roles author:**
   - merge ORQ-1 in the fakes (live fake writes as in S5 fixtures)
   - Knowledge page shows a session that goes to `applied`, drafts marked as needing review, and none verified
   - set the scenario `writer_reply` to garbage and retry → `failed{reply_unparseable}` translated → "Use deterministic draft" creates the deterministic proposal once
8. **Maintenance tab:** support bundle download (search it for the fixture secret values → absent); extension health summary; code-index retention preview, then delete one index with its confirm.
9. **Restore in the browser:**
   - CLI `orqen restore --from <backup> --dry-run --db-name orqen_verify_s7_r --data-dir <tmp2>`, then the real restore
   - second server on 47602 and `orqen login-link`
   - the UI shows the last restore report card (missing credentials with links, republish list, held runs, automation paused); a kas that was mid-flight is `restored_incomplete`
   - press Resume automation; held runs clear only through Resume
   - **do not set any credential**; the card must keep showing "missing"
10. **Persian at 375 px** on Extensions, Graph, Knowledge, Tasks, Settings → Maintenance: `dir=rtl`, no horizontal overflow, sha/paths LTR, no raw codes.
11. **Personal-state guard after:** hashes equal.
12. **Cleanup:** drop both databases, stop processes by their pids, remove the temp dirs.

**Docs:**
- `docs/VERIFICATION-S7.md`: an honest table, each step run / not run.
- DEVIATIONS "Sprint 7 (Go)" table, and corrections to the stale WP80 and WP81 rows (point to the S7 rows rather than rewriting history).
- `docs/STATUS.md` known gaps updated.

**Acceptance:**
- `make check` green twice in a row
- the S4 no-diff check
- the digikala grep is empty

---

## 5. Deferred (and why)

| Item | Why |
|---|---|
| `docs.linked_pages_per_run` cap | The cap needs the run's pinned policy at the `linkedPages` call site in `run_stages.go`, which is on the no-diff list. Listed in the census allowlist. |
| `retention.derived_cache_days` (codegraph trees, scan dirs, search cache) | Needs a reference census of derived dirs. Low value. |
| `knowledge.retrieval.cache_ttl_secs` | Unless the search cache TTL is a one-line wiring (the coder decides and records it). |
| Knowledge create form, pin editor (WP87 rest) | Not a known gap from STATUS; budget. |
| URL scan targets, orphan artifact GC, notifications, Figma MCP, user manifests, signed backups | As in S6. |
| A gated real test of the roles author | It would spend model tokens; the user's catherd/Codex spike covers the contract. |

---

## 6. Definition of done, pins, risks, needs the user

**DoD:**
- STATUS "Known gaps" bullets 1 to 3 closed (roles author, automatic index and scan, security counts, raw codes, never-walked screens walked or explicitly marked)
- `make check` green
- openapi drift clean
- extwrite and deps censuses unchanged
- S4 no-diff check passes
- personal-state guard identical

**Expected pin changes (each deliberate, listed in its WP commit):**
1. `rolebriefs.Version` s6.1 → **s7.1**: `briefs_test.go` (2 assertions), 5 golden headers, `s4_host_test.go:37`, and the not-dispatched test renamed (WP112).
2. `knowledge.AllNeverAuto` +1 (`roles_authored_needs_review`); the `knowledge_test.go:135` loop and the i18n census cover it (WP112).
3. Readiness tests that expected `confluence_refs_unverified` from refs > 0 alone (WP113).
4. `backup.Classes`: +`knowledge_author_sessions` (include), +`source_sync_states` (exclude), and the restore order list (WP116).
5. OpenAPI schemas: TaskSummaryCountsDto, TaskSummaryDto, SecurityScanDto, CodeIndexDto, KnowledgeVersionDto, new KnowledgeAuthorSessionDto and 5 routes (WP114 to WP116).
6. hostfake `Scenario` gains optional fields (WP117).
7. New golden `web/src/test/fixtures/server-codes.json` (WP120).

**Explicitly unchanged** (assert, do not edit):
- policy key count **77** (`internal/policy/policy_test.go:17`, `internal/server/policy_test.go:47`)
- `ProductDefaultsVersion` **5**
- `SchemaVersion` **2**
- `testdata/deps.sha256` (no new Go module or npm package)

**Risks:**
1. **The detached catherd run is an assumed contract:** an empty scratch repo and `ProfileUseRepo` on it. Mitigations: fake-verified only, labelled assumed, and the deterministic fallback always produces knowledge.
2. **Duplicated host-call code** (Y6) can drift from `hostRun`. Mitigation: a test asserts that both write the same `last_directive` shape; recorded as a deliberate deviation.
3. **Two threads on one org Codex app-server** (a coding run and an author session) at the same time. The in-process fake allows it; the real app-server is unverified. Mitigation: the author job retries on `host_busy`; the user's spike decides.
4. **Model spend** from `knowledge.author=roles` and `automation.security_scan=auto`. Both are opt-in, off by default, and stated in the policy help text. An auto scan never repeats per (run, head) and runs one at a time per org.
5. **Enforcing the wiki sandbox by default** (Y16) can break a user's working wiki setup that relied on the silent best effort. It is documented, and the policy can choose `best_effort`.
6. **The i18n census parses Go with go/ast.** Unusual code construction (codes built by concatenation, such as `"wiki_"+kind`) must be listed by hand in the test with a comment.
7. **Server test time** grows by about 15 Mongo tests; the matrices stay pure.
8. **Sweeps read up to 200 runs per minute** per kind. They are bounded and indexed on existing (org_id, state) indexes; check that the index exists, and add it to `Migrate` if not.

**Needs the user:**
- Decide whether the Strix walk steps may use a dummy key that **you** type yourself. The coder and walker never type credentials, so without it the scan and tasks-badge browser steps are marked "not walked, test-covered".
- Approve the stricter never-auto rule for role-written drafts (Y11) and the "warning only when the wiki extension is enabled" reading of K41 (Y18).
- Run `TestCatherdReal` / the Codex spike to confirm the detached-run contract (Y8) before using `knowledge.author=roles` for real.

**Order of execution:** WP111 → 112 → 113 (pure) → 114 → 115 → 116 → 117 → 118 → 119 (server) → 120 → 121 (web) → 122 (fakes/fixtures; the hostfake reply scenarios already landed in WP117 because its tests need them) → 123 (walk and docs). Commit after each green WP. Each WP can be checked on its own with the acceptance commands listed.

### Critical files for implementation
- internal/server/intake_scheduler.go
- internal/server/security.go
- internal/server/codegraph.go
- internal/server/knowledge_author.go
- internal/server/host_session.go (read-only reference for the detached copy; must not change)
- internal/server/knowledge_store.go
- internal/server/tasks.go
- internal/knowledge/roles_reply.go
- internal/rolebriefs/briefs.go
- internal/readiness/evaluate.go
- internal/server/wiki.go
- internal/testfakes/hostfake/roles.go
- web/src/i18n/s6-resources.ts
