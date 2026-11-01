# PLAN-S5-GO: Sprint 5. Versioned knowledge, Wiki projection, compaction, manual/auto promotion, organization graph (test-only)

Based on HEAD `2343290`. I checked it read-only against: SPEC §10, §11, §14–§19, §21; GO-MIGRATION, DEVIATIONS, PLAN-S3-GO, PLAN-S4-GO; `internal/{contextpkt,policy,providers,rolebriefs,catherd,testfakes/hostfake}`; `internal/server/{db,queue,jobs,run_stages,context_routes,host_stage,host_session,role_stages,mr_watch_job,projects,readiness}.go`; `web/{package.json,assets.go,src/components/sidebar-nav.tsx,src/router.tsx,src/api/sse.ts}`.

**Facts that drive the design:**
- **No outbox collection exists.** Jobs inserted in the same transaction are the outbox today, and S5 keeps that pattern.
- **Duplicate keys.** A duplicate-key error inside a transaction aborts it (DEVIATIONS WP8/WP11).
- **Inbox uniqueness.** `inbox_items` has a partial unique index on (org_id, run_id){status: open}. Inbox items without a run would collide, so knowledge review must not use the Inbox.
- **Briefs ignore the packet.** `rolebriefs.Data.Context` is never filled by the server (only `architect.md` references it). Roles do not receive the packet today; S5 fixes this.
- **Placeholders already in the packet.** `buildSections` adds `knowledge`/`experience` excluded with `module_unavailable`, and Confluence/Figma links excluded with `adapter_unavailable`.
- **Confluence already exists in config.** It is a connection provider and a project mapping field (`Mapping.Confluence[]`). mapping_check reports `not_implemented`, readiness warns `confluence_refs_unverified`.
- **Merge observation exists.** `mr_watch` already observes the merge (`case "merged"` transaction), so S5 hooks there and stays read-only.
- **catherd roles.** catherd has a `writer` role. `stageCtx` is only `{a, lease, job}`, so a non-run job can build one.
- **CSP.** `web/assets.go` allows `style-src 'unsafe-inline'`, so React Flow inline styles work.
- **Go dependencies.** go.mod has no `x/net/html`, so the Confluence storage→text conversion uses `encoding/xml` (HTMLAutoClose/HTMLEntity, non-strict). No new Go module.
- **OpenClaw Wiki CLI contract: NOT verifiable.** The binary is not installed and there are no offline docs (only the SPEC link `docs.openclaw.ai/cli/wiki`; fetching it is out of bounds). The adapter is therefore thin and schema-pinned, labelled "assumed contract v1". The deterministic fake is the executable spec, and the gated real test reports the differences.

---

## 1. Key decisions

| # | Topic | Decision |
|---|---|---|
| K1 | Package seam | **Pure packages** (no `internal/server` import; fast tests):<br>- `internal/knowledge`: model, transitions, hash, claim gate, never-auto, promotion, conflicts, diff, injection lint, deterministic author, compaction<br>- `internal/knowledge/retrieve`: ranking, budget, reasons<br>- `internal/kindex`: KnowledgeIndex port, Fake, CLI adapter, queue planner, schemas<br>- `internal/orggraph`: manifest parsers, edge rules, bounded BFS<br>- `internal/learnbench`<br>- `internal/providers/confluence*.go` (DocsSource)<br>- `contextpkt` extensions<br>`internal/server` holds only docs, transactions, routes and jobs. |
| K2 | Schema | `SchemaVersion` stays **2** (additive, K12/S2 precedent). `policy.ProductDefaultsVersion` goes 3 → **4**.<br>The `Migrate` index struct gains `text bool` + `weights`, so it can create one compound text index. |
| K3 | Canonical vs projection | **MongoDB is canonical.** `source_versions` (raw provenance, hash, link to a content-addressed artifact) and `knowledge_records` + `knowledge_versions` (claims, evidence, status) are canonical.<br>The Wiki vault, the search cache and the graph's inferred edges are **derived and rebuildable**. A summary never overwrites source text: source text lives only in the `source_versions` artifact; a knowledge body is a separate, claim-backed text. |
| K4 | Record identity | `knowledge_records` (`kno_`) is the identity: `(org_id, scope_key, key)` is unique.<br>- `scope_key` ∈ `org` \| `project:<prj>` \| `sprint:<prj>:<sprintId>` \| `task:<run>`<br>- `key` is a slug `^[a-z0-9][a-z0-9._-]{1,99}$`<br>- type ∈ `business` (scope org or project), `project` (project), `sprint` (sprint), `task` (task), `experience` (project or org)<br>- a type/scope mismatch → 422 `knowledge_scope_invalid` |
| K5 | Versions are immutable | `knowledge_versions` (`kvr_`) has number N per record. Content fields never change after insert. Only these change: `status`, `superseded_by`, `promotion`, `claims[].verdict*` (while draft) and `evidence_stale`.<br>Index guarantees: **at most one `verified`** version per record and **at most one `draft`** per record (both partial unique indexes). An edit creates a new draft. |
| K6 | Statuses | `draft` → `verified` (user, or policy auto) \| `conflicted` (detector) \| `archived` (reject).<br>`conflicted` → `verified` (user resolution) \| `archived`.<br>`verified` → `superseded` (a newer version of the record is verified) \| `archived` (record archived).<br>`superseded` and `archived` are terminal for that version.<br>Reject = `archived` with `archive_reason: rejected`. The five SPEC statuses are kept exactly. |
| K7 | Supersede and rollback | **Supersede:** verifying version N of record R sets R's current verified version → `superseded{superseded_by:N}` **first**, then N → `verified`, in one transaction (partial unique safe).<br>**Rollback** to version V is a new version N+1: content copied from V, `origin.kind=rollback`, `rollback_of=V`, `supersedes=current`. The gate re-runs against **current** evidence: pass and no conflict → verified (user action); otherwise it stays draft with the reasons.<br>History is never deleted or rewritten. |
| K8 | Claims and evidence | `claims[]` = `{id cl_<n>, text≤500, template{code, params}?, fact{subject, predicate, value}?, critical bool, evidence[]}`. Each evidence entry is `{ref_kind, ref_id, locator?, excerpt≤300?, excerpt_sha?}`.<br>`ref_kind` ∈ `source_version`, `artifact`, `verification_evidence`, `review_finding`, `merge_request`, `run`, `user_attestation`.<br>Verdict ∈ `supported` \| `unsupported` \| `contradicted` \| `unchecked`, with `verdict_by` ∈ `deterministic` \| `verifier_role` \| `user`. |
| K9 | Claim gate (pure) | `knowledge.CheckClaims(type, claims, Resolver) GateResult`. Per claim, in order:<br>1. no evidence → `claim_without_evidence`<br>2. every ref resolves **in the same org** and in an allowed scope (Resolver filters by org_id) → else `evidence_ref_invalid`<br>3. ref kind admissible for the type: `business` needs at least one `source_version` with authority `canonical_external` (Confluence or Jira) or `user_attestation`. Run, MR or merge evidence alone → `business_claim_without_canonical_source` ("merge alone is not proof")<br>4. a template claim: params must equal the resolved fields (for example outcome=pass at head H)<br>5. a free-text claim: the normalized `excerpt` must occur verbatim in the referenced normalized text (`excerpt_sha` recomputed) → else `excerpt_not_found`<br>6. a referenced source_version that is no longer the latest for its source → `evidence_stale`<br>A claim is `supported` only if 1–6 pass. |
| K10 | Never auto-verify | Pure `knowledge.NeverAuto(v, rec, gate, conflicts, flags) []string`. Any of these blocks auto promotion:<br>- `conflict_open` or `conflict_detected`<br>- `critical_or_pinned` (the record is pinned or critical, or any claim is `critical`)<br>- any claim not `supported` (`claim_without_evidence`, `claim_unsupported`, `claim_contradicted`)<br>- `fact_missing` (a free-text claim without `fact` cannot be conflict-checked)<br>- `injection_suspect`<br>- `evidence_stale`<br>- `supersedes_user_verified` (it would supersede a version a user verified)<br>- `business_claim_without_canonical_source`<br>- `policy_not_auto`<br>**Manual verify by a user:** allowed when the gate passes or every failing claim gets a `user_attestation` (text≤1000, audited). It is refused (422 `knowledge_claims_unsupported`) for zero-evidence claims without attestation, and 409 while a conflict is open. |
| K11 | Promotion policy | Keys `knowledge.promotion.{business,project,sprint,task,experience}` ∈ off\|manual\|auto, default `manual` (SPEC §21), project-overridable.<br>- `off`: no proposal is created for that type<br>- `manual`: the draft waits in the proposals queue<br>- `auto`: verified only if `NeverAuto` is empty<br>The policy used is the **pinned** policy of the origin run (or the live project policy for manual proposals) and is recorded in `promotion.policy_version_id`. |
| K12 | Experience cycle | **Proposal lifecycle:** queued → authoring → verifying → awaiting_review \| promoted \| conflicted \| rejected \| failed \| superseded. In order:<br>(1) **Evidence bundle** (deterministic JSON artifact `knowledge_evidence_bundle`) from run, snapshot, acceptance, verification evidence, review findings, diff file list, MR merge observation and fix rounds; every element has a `ref_id`.<br>(2) **Author** per `knowledge.author`:<br>- `deterministic` (default): template claims, no model<br>- `roles`: catherd `writer` then a fresh catherd `verifier` via the S4 host (directed turns, `role_executions`); fake in tests<br>(3) the pure gate always runs; **the verifier role may only downgrade a verdict, never upgrade one**<br>(4) policy<br>(5) on verified: Wiki outbox in the same transaction<br>(6) use measured in `knowledge_usage`<br>**No Inbox for knowledge:** the proposals queue on the Knowledge page plus nav, Overview and summary counts. |
| K13 | Merge trigger | In the existing `mr_watch` merged transaction: when the run's pinned `knowledge.learn_on_merge` (default true) is set, insert `knowledge_proposals{origin:run_merged, dedupe_key: merged:<run>}` and `ensureJobTx(knowledge_author, kauthor:<kpr>)`.<br>Manual "Propose from run" works for any terminal run (`merge_observed=false`; no merge claims). The merge is observed read-only (existing GET).<br>**Jira writes stay refused in S5.** No knowledge reference is written to Jira; the extwrite census is unchanged. |
| K14 | Deterministic author templates | Each template carries `fact` and evidence refs, and renders via i18n `knowledge.template.<code>`:<br>- `task.acceptance_verified{item_idx, head, commands[]}`: verification_evidence gate pass at head + review verdict<br>- `task.merged{iid, merge_commit}`: merge_request<br>- `experience.fix_after_failure{command, failed_round, outcome, fixed_head, files[]}`: two verification_evidence records<br>- `experience.finding_fixed{severity, title_sha8, round}`: review_finding status fixed<br>- `project.command_passes{command, merge_commit}`: head evidence pass<br>**Business templates do not exist.** Facts: subject `repo:<con>:<pid>` / `command:<key>` / `issue:<key>`. |
| K15 | Conflicts | Pure `knowledge.DetectConflicts(cand, existing)`. Kinds:<br>- `value_mismatch`: same normalized (subject, predicate), different normalized value, overlapping validity, scope equal or ancestor/descendant in the chain org ⊃ project ⊃ sprint ⊃ task<br>- `negation`: `fact.predicate` `not:<p>`<br>- `source_changed`: a cited source has a newer content_hash<br>`knowledge_conflicts` (`kcf_`) is unique per open fingerprint `sha(sorted version ids, kinds)`. The candidate becomes `conflicted`; the existing verified version **stays verified** (no silent demotion).<br>**Resolution** (user, If-Match):<br>- `keep_existing`: candidate → archived<br>- `accept_new`: candidate verified via K7, old version superseded (needs a passing gate or attestations)<br>- `archive_both`<br>A note ≤ 2000 is required for `accept_new`. |
| K16 | Source drift | On `source.synced` with a changed hash:<br>- verified versions citing an older source_version of the same source get `evidence_stale=true`<br>- a `source_changed` proposal is created (dedupe `srcchg:<record>:<new srv>`) whose draft copies the claims; the gate then re-evaluates the excerpts<br>Stale verified knowledge is excluded from retrieval (reason `stale`) unless it is pinned critical: then it is **included and flagged**, never dropped. |
| K17 | Pinned and critical | `PUT /knowledge/records/{id}/pin {pinned, critical, applies_to_roles[]}`. Only a record whose current version is verified (409 `knowledge_not_verified`); `critical` only for type business or project (422 `knowledge_pin_type_invalid`).<br>The S3 `pinned_rules` collection stays as is (user-authored rules) and gains `applies_to_roles[]` (empty = all). Critical pinned knowledge and critical rules both go to `critical_rules`. |
| K18 | Critical never truncated | **Split = per-role partition.** Critical items carry `AppliesTo`. A role packet drops non-applicable critical items with the visible reason `not_for_role`.<br>If one role's critical set still exceeds the cap → the existing Inbox `policy_conflict`/`packet_oversize` with `params.role` (options raise_limit, cancel). A single item larger than the cap is never cut. |
| K19 | Per-role packets | The run packet stays (S3). At each dispatch, `contextpkt.BuildRole(in, role)` uses `RoleProfiles`:<br>- architect, worker: all 8 sections<br>- verifier: critical, goal, repo_rules, code, knowledge<br>- reviewer: critical, goal, repo_rules, design, code, knowledge, experience (never task_discussion and never any worker trace; results come via the S4 evidence fields)<br>- writer: critical, goal, plus the evidence bundle as a section `evidence`<br>Stored as artifacts `context_packet`/`context_manifest` (subject rex). `role_executions.context = {packet_artifact_id, manifest_artifact_id, bytes, cap, critical_bytes, est_tokens}`. `rolebriefs.Version` "s4.1" → **"s5.1"**: worker, reviewer and verifier templates get `{{if .Context}}`, same as architect. |
| K20 | Context consumption | Manifest v2 adds `consumption{packet_bytes, cap, critical_bytes, est_tokens, est_method:"bytes_div_4", tokenizer:null, actual_input_tokens, host_overhead:"unknown"}`.<br>`actual_input_tokens` comes from the catherd record `tokens.input` of that rex when present, else null and shown as "unknown". It is the whole model input, labelled as such, never compared with the packet as if they were equal.<br>No tokenizer is shipped in S5: when a valid tokenizer is absent there is no token budget, only bytes. Shown in the task Context tab, per role. |
| K21 | Retrieval | `retrieve.Select(In) Out`, pure. Candidates come from Mongo, filtered by `{org_id: run.org, status: verified, scope_key ∈ chain(run)}`. `chain` = `org`, `project:<run.project>`, `sprint:<run.project>:<active sprint ids from the snapshot>`, `task:<run>`; never another project.<br>**Exclusion reasons:** `expired` (valid_until < now), `stale` (evidence_stale and not include_stale, or older than max_age_days), `below_rank` (beyond top_k per section), then `contextpkt` budget/ItemLimit.<br>**Score:** search score (wiki or Mongo `textScore`) + scope bonus (task 3, sprint 2, project 1, org 0); ties by verified_at desc, then id.<br>Experience → `experience` section; others → `knowledge`.<br>**Item text** = rendered version (title + supported claims, body ≤ `max_source_bytes`, visible truncation note). `trust=external` when any evidence is external or the author was a role; `user` only for user-authored manual versions.<br>**Strategy:** `scoped_text` \| `pinned_only` \| `off`. **No vector search.** |
| K22 | Cross-org leak prevention | Four layers:<br>(a) the compound text index has the prefix `org_id, status`, so a query without org cannot use it<br>(b) one vault per org; the adapter takes a `Vault{OrgID, Dir}` and refuses a Dir outside `<data>/wiki/<org>`<br>(c) wiki hits are re-resolved with `{_id ∈ ids, org_id, status: verified}`; foreign or unknown ids are dropped with event `knowledge.scope_violation{count}` (no ids of the other org in the payload)<br>(d) `contextpkt.inScope` OrgID check on every item (existing)<br>Test: two orgs, same key, and a fake index returning the other org's id. |
| K23 | KnowledgeIndex port | `kindex.Index`, matching the SPEC adapter list (ingest_projection, compile, lint, search, read_slice). Context-aware methods; typed errors map to providers-style kinds:<br>- `Health(ctx) (Health{Version, Schema}, error)`<br>- `Ingest(ctx, Vault, dir string) (IngestResult{Accepted, Rejected []Doc}, error)`<br>- `RegisterSynthesis(ctx, Vault, file string) error`<br>- `Compile(ctx, Vault) (CompileResult, error)`<br>- `Lint(ctx, Vault) (LintResult{Errors, Warnings []Issue}, error)`<br>- `Search(ctx, Vault, q string, limit int) ([]Hit{ID string; Score float64; Snippet string}, error)`<br>- `Get(ctx, Vault, id string, maxBytes int) (Slice{ID, ContentHash, Text}, error)`<br>**Implementations:** `Fake` (in-memory and file-backed; failure injection per verb; idempotent on (id, hash)) and `CLI`. The Mongo fallback is a separate `Searcher`, not an Index. |
| K24 | CLI contract (assumed v1, schema-pinned) | **Env:** `ORQEN_WIKI_BIN` (absolute; empty = not configured; **no PATH lookup**).<br>**argv** (no shell) `<bin> wiki <verb> --vault <abs> --json [...]`, with verbs:<br>- `version`<br>- `ingest --input <dir>`<br>- `synthesis register --input <file>`<br>- `compile`<br>- `lint`<br>- `search --query <q> --limit <n>`<br>- `get --id <id> --max-bytes <n>`<br>**Never called:** cron, dream*, dispatch, serve, daemon, setup or any other verb (argv allowlist test).<br>**Explicit env:** `HOME=<data>/wiki/<org>/home`, `OPENCLAW_HOME=<data>/wiki/<org>/state`, `TMPDIR`, `PATH=rt.ChildPath`, `LANG=C.UTF-8`; nothing inherited, no credentials.<br>**Sandbox:** macOS `sandbox-exec` denies `network-outbound` and `file-write*` outside `<data>/wiki/<org>` (policy `knowledge.wiki.sandbox=enforce`; a missing sandbox → unavailable).<br>**Limits:** process group, 120 s per verb, stdout cap 4 MiB.<br>**Output:** strict JSON validated against `internal/kindex/schema/v1/<verb>.json` (embedded); anything else → `InvalidOutput` → degraded.<br>The fake CLI (`orqen-fakehost openclaw`) implements exactly these schemas. |
| K25 | Outbox → Wiki | **Writes:** every status change that adds or removes a verified version inserts `wiki_outbox` (`wob_`) entries `{org_id, seq (counters wob:<org> $inc), op upsert\|delete, version_id, record_id, content_hash, state pending\|applied\|failed\|deferred, attempts}` **in the same transaction**, plus `ensureJobTx(wiki_maintain, wiki:<org>)`.<br>**One job kind per org dedupe = one writer per vault** (SPEC §16). Rebuild is a flag on `wiki_vault_states` (`"wvs_"+org[4:]`), not a second job.<br>**Index failure never touches canonical:** the version stays verified, the outbox entry stays pending/failed, and the vault becomes degraded. |
| K26 | Vault maintenance | Sequential phases per batch: write projection files → `ingest` → `synthesis register` (Orqen-authored deterministic scope index pages, no model) → `compile` → `lint` → `get` smoke on one upserted id (content_hash must match) → commit `applied_seq`.<br>**Guards:** cross-process `flock(<data>/wiki/<org>/vault.lock)` (busy → retryable `vault_busy`, 5 s), plus a fenced write of `phase` before each CLI verb (a lost lease stops before the next verb).<br>**Lint errors → degraded `lint_failed`** and applied_seq does not advance (failed docs are listed). |
| K27 | Degraded mode | Index status `mode`:<br>- `wiki`: configured, healthy, no pending outbox<br>- `fallback`: not configured, degraded, or `index_lagging` (pending > 0)<br>- `unavailable`: neither works (Mongo down; nothing is served)<br>Retrieval and search in fallback use the Mongo compound text index (`default_language: "none"`, so fa/en tokenize on whitespace and punctuation) and return `degraded{reason}`. The UI shows a degraded badge on Knowledge search, the task Context tab, Overview and Settings. |
| K28 | Rebuild from canonical | `POST /knowledge/index/rebuild {confirm:true}` sets `rebuild_requested`. The maintain job validates the vault path (EvalSymlinks inside `<data>/wiki/<org>/vault`), removes only that directory, increments `generation` and re-projects every verified version (pages of 200). Outbox entries with seq ≤ the snapshot seq are marked applied. |
| K29 | Confluence DocsSource | `providers.DocsSource`: `Identity`, `Capabilities`, `GetPage(ctx, id)`, `GetPageVersion(ctx, id, n)`, `Search(ctx, cql, limit)`. All GET; same client, limiter, auth matrix and errors.<br>**Server/DC** (base may include a context path, kept exactly):<br>- `/rest/api/user/current`<br>- `/rest/api/content/{id}?expand=body.storage,version,space,history.lastUpdated`<br>- `/rest/api/content/{id}?status=historical&version={n}&expand=body.storage,version`<br>- `/rest/api/content/search?cql=…&limit=`<br>**Cloud** (`{base}` + `/wiki` unless the base already ends in `/wiki`):<br>- `/wiki/rest/api/user/current`<br>- `/wiki/api/v2/pages/{id}?body-format=storage`<br>- `…?version={n}&body-format=storage`<br>- `/wiki/rest/api/search?cql=…`<br>**Conversion:** storage → text with `ConfluenceStorageToText` (encoding/xml; keeps headings, lists, tables as text and `ac:structured-macro name=code` bodies; drops other macros; collects `ri:page`/URL links).<br>**Hashes:** `content_hash = sha256(S11 normalize(text))`, plus `raw_hash`. Page cap `docs.max_page_bytes`.<br>**Optional:** with no Confluence connection everything works; the Sources tab shows the empty state "No Confluence connection". The Cloud and Server endpoints are fake-verified only (user verifies on real access). |
| K30 | Other sources | Source kinds and authority:<br>- `jira_issue`: a source_versions row referencing an existing `issue_snapshots` id; same content_hash, no new external read; authority `canonical_external`<br>- `repo_document`: reviewed contract docs<br>- `run_evidence`, `merge_request`: authority `orqen_record`<br>- `figma_link`: **link-only and truthful**: `link_only=true`, hash of the URL, no content; packet reason `adapter_unavailable` with detail `design_mcp_not_attached`. S5 attaches no Figma MCP.<br>- `user_note`: authority `user` |
| K31 | Context-stage docs | Mapped Confluence pages use their latest source_version. Issue-linked Confluence URLs are resolved to a page id (`/pages/<id>`, `pageId=<id>`); the connection must be in the project mapping and its base host must match. Up to `docs.linked_pages_per_run` (5) are fetched on demand, with 10 s each.<br>- failure → excluded `adapter_unavailable` (network) or `out_of_scope` (host or connection)<br>- a page never synced → `not_synced`<br>Contents are external data (wrapped and scrubbed). |
| K32 | Org graph | `graph_nodes` (`gno_`, unique (org_id, kind, natural_key)) and `graph_edges` (`ged_`, unique (org_id, from, to, relation)).<br>**Node kinds:** project, repo, service, api, domain, owner, doc, task, test, jira_project.<br>**Relations:** maps_to, depends_on, exposes_api, owned_by, documents, belongs_to_domain, implements, tested_by, related.<br>**Edge fields:** `status verified\|inferred\|stale\|rejected`, `confidence 0..1`, `evidence[{kind, source_version_id?, artifact_id?, path?, commit_sha?, excerpt≤300?}]`, `observed_at`, `commit_sha`, `last_seen_build`.<br>**Rules (pure `orggraph`):**<br>- Orqen config edges are verified 1.0: maps_to (project→repo/jira_project), implements (task→repo), tested_by (repo→approved command)<br>- `depends_on` A→B is inferred 0.9, **only** when A's manifest declares a dependency whose exact identity equals B's declared identity (go.mod `module`/`require`, package.json `name`/deps, pom `groupId:artifactId`) and that identity is unique in the org. **No fuzzy or filename matching** (test: `payment-api` vs `payments-api` → no edge)<br>- `exposes_api` inferred 0.8 from an openapi file at fixed paths<br>- `owned_by` inferred 0.7 from the CODEOWNERS `*` rule<br>- `documents` inferred 0.6, doc→repo, from a mapped Confluence page that links a catalog `web_url` exactly<br>- user-created or confirmed → verified 1.0 (`user` evidence)<br>**Freshness:** an inferred edge not re-observed at a build whose HEAD differs → `stale`; removed after `graph.inferred_retention_days`. Verified edges are never auto-removed.<br>**Code graph: not in S5.** `code_graph{available:false, reason:"code_graph_sprint6"}` (GitNexus is license-gated, S6). |
| K33 | Graph query | Bounded drill-down: `neighborhood(node, depth 1..2, relations[], status, limit ≤ 200)` returns `truncated:true` past the limit.<br>The run packet adds one `code_context` item `graph_neighbors`: depth 1 of the primary repo, ≤ 10 edges, ≤ 1 KiB, each edge tagged verified or inferred; truncatable, never critical. |
| K34 | Graph UI | `@xyflow/react` (React Flow 12, MIT). Transitive deps: `@xyflow/system` (MIT), `zustand` (MIT), `classcat` (MIT), `d3-drag/selection/zoom` (ISC).<br>**Lazy route chunk only** (`React.lazy` for `graph-page`); the main chunk must not grow by more than 2 KB.<br>The coder records the chunk sizes before and after plus every license field in DEVIATIONS, and picks the newest version allowed by pnpm `minimumReleaseAge`.<br>Own deterministic radial layout (`layout.ts`, no dagre/elk). The canvas wrapper is `dir="ltr"` (geometry); labels are `dir="auto"`. An accessible **List** view shows the same data in a table. |
| K35 | Compaction | Daily scheduler scan: when ≥ `knowledge.compaction.threshold` (5) verified **experience** versions in one scope share the `fact.subject` and predicate family, create a `compaction` proposal. Its draft = deduplicated claims (same fact and the same evidence set merged), evidence union, `supersedes_many[]`.<br>**Always manual** (`NeverAuto` adds `compaction_requires_review`). Verifying it archives the merged records (`archive_reason: compacted_into:<kno>`).<br>Packet compaction = knowledge items use the version rendering, not source text. Wiki compile is the index-side compaction. |
| K36 | Retention | **Permanent until explicit user action (archive):** `source_versions` + artifacts, `knowledge_*`, conflicts, proposals, verified and user graph edges, benchmark runs, usage.<br>**Derived:**<br>- `knowledge_search_cache` (TTL index `expires_at`, `knowledge.retrieval.cache_ttl_secs`)<br>- applied `wiki_outbox` (TTL on `purge_at` = applied_at + `retention.outbox_days`)<br>- inferred edges as in K32<br>- vault files (rebuildable)<br>`GET /knowledge/retention/preview` returns counts only. S5 has no delete API for canonical records. |
| K37 | Injection and redaction | **External text is data.** Source text, role writer output and claim text are wrapped and scrubbed (`contextpkt.Scrub` plus known connection secrets) when rendered into packets, briefs or Wiki projection files.<br>`knowledge.InjectionSuspect(text) []string` (en+fa phrases: ignore previous/above instructions, system prompt, you are now, tool or credential names, `policy`/`token`/`approve` imperatives, `<<<SOURCE` forgery) → claim flag → never auto, UI badge.<br>**Claims never grant anything:** no code path reads knowledge into policy, tools, extwrite, allowlists or commands (census-style test on imports and calls).<br>**Events** carry ids, codes and counts only. |
| K38 | Usage measurement | `knowledge_usage` (`kus_`): one row per (run, rex\|null, role, version) per packet, with included, reason and bytes. A rollup on the terminal transition writes `outcome{fix_rounds, first_review_verdict, merged}`.<br>`GET /knowledge/metrics` shows "runs with included verified knowledge vs without" for avg fix rounds and first-pass approval, labelled **observational, not causal**. |
| K39 | Learning benchmark | `internal/learnbench` holds the embedded suite `fixture-v1`: 6 sequential tasks; each needs facts that appear only after an earlier task's learning step.<br>The fake `Engine` is deterministic: a task reworks once per needed fact missing from its packet. Arms knowledge on/off.<br>**Metrics:** rework_rate, repeated_issue_count, sources_needed, est_tokens, reviewer_first_pass_rate. Stored in `knowledge_benchmark_runs` (`kbr_`) with `engine:"fake"`, `claim_level:"harness_only"`. The UI banner reads "validates the measurement harness, not the product".<br>A real engine is S6+ and user-run. |
| K40 | Roles author (optional) | `knowledge.author=roles` dispatches inside the origin run's catherd run: `writer-K<n>`, then `kverifier-K<n>` (role verifier, fresh thread), with `role_executions.proposal_id` set.<br>- needs the host available and the pinned profile enabling writer and verifier; otherwise the proposal is `failed{host_unavailable\|writer_role_disabled}` with actions Retry or "Use deterministic draft"<br>- no coding slot; one author job per org at a time (dedupe `kauthor_org:<org>` held by a gate doc, see WP84)<br>- workspace cleanup skips the run while a roles proposal is queued or authoring<br>**Reply contracts:** writer ends with `ORQEN-KNOWLEDGE v1` + JSON `{records:[{type, scope_kind, key, title≤200, body≤4000, claims[{text≤500, fact?, critical?, evidence[{ref_id, excerpt?}]}]}]}`, where ref_ids must exist in the bundle; verifier ends with `ORQEN-KVERIFY v1` `{claims:[{claim_id, verdict, reason≤500}]}`. Unparseable → one fresh retry, then `failed{reply_unparseable}`. |
| K41 | Wizard and readiness | **No wizard step**; Skip semantics are unchanged.<br>Readiness (pure) gains input `KnowledgeIndexMode` and these warnings (never blockers):<br>- `knowledge_index_fallback`<br>- `confluence_ref_unreachable` (a sync failed)<br>`confluence_refs_unverified` is cleared once every mapped ref has a successful sync. mapping_check verifies Confluence refs via `GetPage`. |

---

## 2. Data model, API, jobs, events, errors, policy, i18n

**ID prefixes** (`ids.go`; path params in `apiMiddleware`):
- `srv` (srvId), `kno` (knoId), `kvr` (kvrId), `kpr` (kprId), `kcf` (kcfId), `wob`, `gno` (gnoId), `ged` (gedId), `kbr` (kbrId), `kus`
- add path param `rexId:rex`
- singleton `wvs_`

### 2.1 Collections and indexes (added to `Migrate`)

| Collection | Key fields | Indexes |
|---|---|---|
| `source_versions` | org_id, project_id\|null, kind, connection_id\|null, external_id, url, title, remote_version, remote_updated_at, content_hash, raw_hash, artifact_id, bytes, truncated, link_only, authority, snapshot_id?, fetched_at, prev_id, latest bool | unique (org_id, kind, connection_id, external_id, content_hash); unique partial (org_id, kind, connection_id, external_id){latest:true}; (org_id, project_id, kind, fetched_at) |
| `knowledge_records` | org_id, scope_key, scope{kind, project_id, sprint_id, run_id}, type, key, title, current_version_id, verified_version_id\|null, status, pinned, critical, applies_to_roles[], next_number, active, archive_reason, revision, created_at, updated_at | unique (org_id, scope_key, key); (org_id, type, status, updated_at); (org_id, pinned, critical) |
| `knowledge_versions` | org_id, record_id, number, type, scope_key, project_id, title, body, claims[] (K8 + verdict, verdict_by, flags[]), valid_from, valid_until, status, supersedes, superseded_by, rollback_of, supersedes_many[], origin{kind manual\|proposal\|rollback\|resolution, proposal_id, actor}, promotion{mode, decided_by, policy_version_id, at, note}, evidence_stale, injection_flags[], content_hash, verified_at, created_at | unique (org_id, record_id, number); unique partial (org_id, record_id){status:"verified"}; unique partial (org_id, record_id){status:"draft"}; (org_id, status, type, scope_key); **text** (org_id, status, title:text, claims.text:text, body:text) weights 5/2/1, default_language none |
| `knowledge_proposals` | org_id, project_id, origin{kind run_merged\|run_manual\|source_changed\|compaction\|manual, run_id, mr_id, source_version_id}, dedupe_key, state, author{mode, rex_ids[], reply_artifact_id}, bundle_artifact_id, bundle_hash, draft_version_ids[], gate{per version: result, reasons[], never_auto[]}, decision{mode, outcome, by, at}, failure{code}, revision | unique (org_id, dedupe_key); (org_id, state, created_at) |
| `knowledge_conflicts` | org_id, fingerprint, version_ids[], record_ids[], pairs[{a_version, a_claim, b_version, b_claim, kind}], state open\|resolved, resolution{choice, note, by, at, result_version_id}, revision | unique partial (org_id, fingerprint){state:"open"}; (org_id, state, created_at); (org_id, version_ids) |
| `wiki_outbox` | org_id, seq, op, version_id, record_id, content_hash, state, attempts, last_error, applied_at, purge_at | unique (org_id, seq); (org_id, state, seq); TTL purge_at |
| `wiki_vault_states` | org_id, mode, bin_version, schema, applied_seq, generation, phase, phase_fence, rebuild_requested, degraded{reason, phase, at}\|null, last_run_at, revision | unique (org_id) |
| `knowledge_search_cache` | org_id, key, version_ids[], mode, expires_at | unique (org_id, key); TTL expires_at |
| `knowledge_usage` | org_id, run_id, rex_id, role, version_id, record_id, included, reason, bytes, outcome{…}, created_at | (org_id, run_id); (org_id, version_id, created_at) |
| `graph_nodes` | org_id, kind, natural_key, label, ref{project_id, connection_id, provider_project_id, source_version_id, tracked_issue_id}, props, last_seen_build, updated_at | unique (org_id, kind, natural_key); text (org_id, label:text) |
| `graph_edges` | org_id, from, to, relation, status, confidence, evidence[], commit_sha, observed_at, last_seen_build, created_by system\|user, revision | unique (org_id, from, to, relation); (org_id, from, status); (org_id, to, status) |
| `knowledge_benchmark_runs` | org_id, suite_id, suite_hash, engine, claim_level, arms{on, off}{metrics}, per_task[], created_at | (org_id, created_at) |
| `pinned_rules` (existing) | + `applies_to_roles[]` (optional) | — |
| `role_executions` (existing) | + `context{…K19}`, `proposal_id?` | (org_id, proposal_id) |

### 2.2 API routes (hand-authored; all under `/api/v1/orgs/{orgId}`; mutations carry X-CSRF-Token; Idem = required Idempotency-Key, listed in `idemRequired`)

| Method + path | operationId | Headers | Body → response / errors |
|---|---|---|---|
| GET `/knowledge/records` | `list_knowledge_records` | — | `type?, scope_kind?, project_id?, status?, pinned?, q?, limit, cursor` → `Page_KnowledgeRecordDto` |
| POST `/knowledge/records` | `create_knowledge_record` | Idem | `{type, scope{kind, project_id?, sprint_id?, run_id?}, key, title, body, claims[], valid_from?, valid_until?}` → 201 `KnowledgeRecordDetailDto` (v1 draft); 409 `knowledge_key_taken`, 422 `knowledge_scope_invalid`, 422 `evidence_ref_invalid` |
| GET `/knowledge/records/{knoId}` | `get_knowledge_record` | — | → `KnowledgeRecordDetailDto{record, current_version, verified_version, draft_version, open_conflicts[]}` with ETag |
| GET `/knowledge/records/{knoId}/versions` | `list_knowledge_versions` | — | → `KnowledgeVersionDto[]` (≤ 200, newest first) |
| POST `/knowledge/records/{knoId}/versions` | `create_knowledge_version` | If-Match, Idem | `{base_version_id, title, body, claims[], valid_from?, valid_until?}` → 201 `KnowledgeVersionDto`; 409 `knowledge_draft_exists` (+current) |
| GET `/knowledge/versions/{kvrId}` | `get_knowledge_version` | — | → `KnowledgeVersionDto` (claims with resolved evidence labels and gate preview) |
| GET `/knowledge/versions/{kvrId}/diff` | `diff_knowledge_versions` | — | `against` (kvr) → `KnowledgeDiffDto{title{a,b}, body_hunks[], claims{added[], removed[], changed[]}, validity{a,b}}` |
| POST `/knowledge/versions/{kvrId}/verify` | `verify_knowledge_version` | If-Match (record), Idem | `{note?, attestations?[{claim_id, text≤1000}]}` → `KnowledgeRecordDetailDto`; 409 `knowledge_version_not_draft`, 409 `knowledge_conflict_open`, 422 `knowledge_claims_unsupported` (+claims) |
| POST `/knowledge/versions/{kvrId}/reject` | `reject_knowledge_version` | If-Match, Idem | `{reason≤2000}` → detail |
| POST `/knowledge/records/{knoId}/rollback` | `rollback_knowledge_record` | If-Match, Idem | `{to_version_id, note≤2000}` → detail (new version verified or draft + `gate`); 422 `rollback_target_invalid` |
| PUT `/knowledge/records/{knoId}/pin` | `pin_knowledge_record` | If-Match | `{pinned, critical, applies_to_roles[]}` → `KnowledgeRecordDto`; 409 `knowledge_not_verified`, 422 `knowledge_pin_type_invalid` |
| POST `/knowledge/records/{knoId}/archive` | `archive_knowledge_record` | If-Match, Idem | `{reason≤2000}` → `KnowledgeRecordDto` |
| GET `/knowledge/search` | `search_knowledge` | — | `q, type?, project_id?, limit≤50` → `KnowledgeSearchResultDto{mode, degraded, hits[{version_id, record_id, title, type, scope_key, score, snippet}]}` |
| GET `/knowledge/proposals` | `list_knowledge_proposals` | — | `state?, origin?, project_id?, limit, cursor` → `Page_KnowledgeProposalDto` |
| GET `/knowledge/proposals/{kprId}` | `get_knowledge_proposal` | — | → `KnowledgeProposalDetailDto` (bundle summary with ref ids/labels, draft versions, gate, never_auto, rex refs) |
| POST `/knowledge/proposals` | `create_knowledge_proposal` | Idem | `{run_id}` → 202 `OperationDto` or 200 `{deduplicated:true, proposal}`; 409 `run_state_conflict` (not terminal) |
| POST `/knowledge/proposals/{kprId}/promote` | `promote_knowledge_proposal` | If-Match, Idem | `{version_ids[], note?, attestations?}` → detail; same errors as verify, plus 409 `proposal_not_open` |
| POST `/knowledge/proposals/{kprId}/reject` | `reject_knowledge_proposal` | If-Match, Idem | `{reason}` → detail |
| POST `/knowledge/proposals/{kprId}/retry` | `retry_knowledge_proposal` | If-Match, Idem | `{author: deterministic\|roles}` → 202 Operation |
| GET `/knowledge/conflicts` | `list_knowledge_conflicts` | — | `state?` → `KnowledgeConflictDto[]` |
| POST `/knowledge/conflicts/{kcfId}/resolve` | `resolve_knowledge_conflict` | If-Match, Idem | `{choice, note?, attestations?}` → `KnowledgeConflictDto`; 409 `conflict_not_open` |
| GET `/knowledge/sources` | `list_sources` | — | `kind?, project_id?, connection_id?, limit, cursor` → `Page_SourceVersionDto` (latest only) |
| GET `/knowledge/sources/{srvId}` | `get_source_version` | — | → `SourceVersionDetailDto{…, text (scrubbed, ≤ 256 KiB, truncated), history[]}` |
| POST `/projects/{prjId}/sources/sync` | `sync_project_sources` | Idem | → 202 `OperationDto`; 422 `confluence_mapping_missing` |
| GET `/knowledge/index` | `get_knowledge_index` | — | → `KnowledgeIndexStatusDto{mode, configured, bin_version, schema:"assumed-v1", applied_seq, pending, failed, phase, generation, degraded, last_run_at}` |
| POST `/knowledge/index/maintain` | `maintain_knowledge_index` | Idem | → 202 Operation; 409 `index_not_configured` |
| POST `/knowledge/index/rebuild` | `rebuild_knowledge_index` | Idem | `{confirm:true}` → 202 Operation; 409 `index_not_configured`, 422 `confirm_required` |
| GET `/knowledge/retention/preview` | `preview_knowledge_retention` | — | → counts per derived category plus permanent totals |
| GET `/knowledge/metrics` | `get_knowledge_metrics` | — | → versions by status/type, proposals by state, promotions auto/manual, usage counts, observational outcome table |
| GET `/knowledge/benchmarks` | `list_knowledge_benchmarks` | — | → `KnowledgeBenchmarkRunDto[]` |
| POST `/knowledge/benchmarks` | `run_knowledge_benchmark` | Idem | `{suite:"fixture-v1"}` → 202 Operation |
| GET `/runs/{runId}/context/roles` | `list_run_role_contexts` | — | → `RoleContextDto[]{rex_id, role, name, round, bytes, cap, critical_bytes, est_tokens, actual_input_tokens, manifest_artifact_id, packet_artifact_id}` |
| GET `/runs/{runId}/context/roles/{rexId}` | `get_run_role_context` | — | → `ContextManifestDto` (v2) |
| GET `/graph/status` | `get_graph_status` | — | → `{last_build{at, operation_id}, nodes, edges{verified, inferred, stale, rejected}, code_graph{available:false, reason}}` |
| GET `/graph/nodes` | `list_graph_nodes` | — | `q?, kind?, limit, cursor` → `Page_GraphNodeDto` |
| GET `/graph/nodes/{gnoId}` | `get_graph_node` | — | → `GraphNodeDto` |
| GET `/graph/nodes/{gnoId}/neighborhood` | `get_graph_neighborhood` | — | `depth 1..2, relation[]?, status? (default verified+inferred), limit≤200` → `GraphSubgraphDto{nodes, edges, truncated, depth}` |
| GET `/graph/edges/{gedId}` | `get_graph_edge` | — | → `GraphEdgeDto` (+evidence) |
| POST `/graph/edges` | `create_graph_edge` | Idem | `{from, to, relation, note≤1000}` → 201; 422 `graph_edge_invalid` |
| POST `/graph/edges/{gedId}/confirm` | `confirm_graph_edge` | If-Match | → `GraphEdgeDto` |
| POST `/graph/edges/{gedId}/reject` | `reject_graph_edge` | If-Match | → `GraphEdgeDto` |
| POST `/graph/build` | `build_graph` | Idem | → 202 Operation |

**Changed DTOs:**
- `ContextManifestDto` gets `version:2`, `role`, `consumption`, `knowledge{mode, degraded}`, and per-item `trust, record_id?, version_id?, freshness?, applies_to?`. Reasons gain `expired`, `below_rank`, `not_for_role`, `not_synced`, `not_verified`.
- `TaskSummaryCountsDto` gets `knowledge{proposals_awaiting, conflicts_open, index_mode, degraded}`.
- `PinnedRuleDto` and its requests get `applies_to_roles`.
- `RoleExecutionDto` gets `context`.

**Error codes** (`problem()`):

| Code | Status |
|---|---|
| `knowledge_key_taken` | 409 |
| `knowledge_scope_invalid` | 422 |
| `evidence_ref_invalid` | 422 |
| `knowledge_draft_exists` | 409 |
| `knowledge_version_not_draft` | 409 |
| `knowledge_conflict_open` | 409 |
| `knowledge_claims_unsupported` | 422 |
| `rollback_target_invalid` | 422 |
| `knowledge_not_verified` | 409 |
| `knowledge_pin_type_invalid` | 422 |
| `proposal_not_open` | 409 |
| `conflict_not_open` | 409 |
| `index_not_configured` | 409 |
| `confirm_required` | 422 |
| `confluence_mapping_missing` | 422 |
| `graph_edge_invalid` | 422 |

### 2.3 Jobs

| Kind | Dedupe | Max attempts | Notes |
|---|---|---|---|
| `source_sync` | `source_sync:<prj>` | 20 | mapped Confluence pages; network → retryable |
| `knowledge_author` | `kauthor:<kpr>` | 50 | deterministic or roles |
| `wiki_maintain` | `wiki:<org>` | 1000 | K25–K28; self-re-enqueue while pending |
| `graph_build` | `graph_build:<org>` | 10 | — |
| `knowledge_benchmark` | `kbench:<org>` | 1 | — |

**Scheduler additions** (in the existing sweep):
- `sweepWiki`: an org with pending outbox and no active job → enqueue
- `sweepSourceSync`: per `docs.sync_interval_secs`
- daily: `sweepCompaction`, plus `graph_build` for orgs with enabled projects

**`ensureJobTx(sc, job) error`:** CountDocuments active by dedupe → present → nil; else insert. A duplicate-key error returns `errJobRace`; the caller re-runs its whole transaction once (it then sees the job).

### 2.4 Events (ids, codes and counts only)

| Family | Events |
|---|---|
| Sources | `source.synced{source_version_id, kind, changed}`, `source.sync_failed{project_id, code}` |
| Versions | `knowledge.version_created{record_id, version_id, status, origin}`, `knowledge.verified{…, mode}`, `knowledge.rejected`, `knowledge.superseded{old, new}`, `knowledge.rolled_back{to, new}`, `knowledge.evidence_stale{count}` |
| Records | `knowledge.pinned`, `knowledge.unpinned`, `knowledge.archived` |
| Conflicts | `knowledge.conflict_opened{conflict_id}`, `knowledge.conflict_resolved{choice}` |
| Proposals | `knowledge.proposal_created{proposal_id, origin}`, `knowledge.proposal_state{state}`, `knowledge.auto_blocked{codes}` |
| Leak guard | `knowledge.scope_violation{count}` |
| Wiki | `wiki.maintained{applied_seq}`, `wiki.degraded{reason, phase}`, `wiki.recovered`, `wiki.rebuilt{generation}` |
| Context | `context.role_packet_built{run_id, rex_id, role, bytes, cap}` |
| Graph | `graph.built{nodes, edges, stale}`, `graph.edge_created`, `graph.edge_confirmed`, `graph.edge_rejected` |
| Benchmark | `knowledge.benchmark_recorded{id}` |

### 2.5 Policy keys (ProductDefaultsVersion 4; project-overridable unless org-only)

| Key | Type | Default |
|---|---|---|
| `knowledge.promotion.business` / `project` / `sprint` / `task` / `experience` | enum off, manual, auto | manual |
| `knowledge.learn_on_merge` | bool | true |
| `knowledge.author` | enum deterministic, roles | deterministic |
| `knowledge.retrieval.strategy` | enum scoped_text, pinned_only, off | scoped_text |
| `knowledge.retrieval.top_k` | int 1..50 | 8 |
| `knowledge.retrieval.max_source_bytes` | int 256..8192 | 2048 |
| `knowledge.retrieval.max_age_days` | int 0..3650 (0 = none) | 0 |
| `knowledge.retrieval.include_stale` | bool | false |
| `knowledge.retrieval.cache_ttl_secs` | int 0..86400, org-only | 600 |
| `knowledge.compaction.threshold` | int 3..50 | 5 |
| `knowledge.wiki.sandbox` | enum enforce, best_effort, org-only | enforce |
| `docs.sync_interval_secs` | int 0..86400 | 3600 |
| `docs.max_page_bytes` | int 65536..4194304, org-only | 1048576 |
| `docs.linked_pages_per_run` | int 0..20 | 5 |
| `graph.inferred_retention_days` | int 1..365, org-only | 30 |
| `retention.derived_cache_days` | int 1..365, org-only | 30 |
| `retention.outbox_days` | int 1..365, org-only | 30 |

Config: `ORQEN_WIKI_BIN` (env and flag; empty = not configured).

### 2.6 i18n (fa and en, equal key sets)

- `nav.knowledge`, `nav.graph` (they become links)
- `knowledge.*`:
  - `tabs.{records, proposals, conflicts, sources, pinned}`
  - `type.<5>`, `status.<5>`, `scope.<4>`
  - `claim.{verdict.<4>, by.<3>, evidence, attest, flags.injection_suspect, stale}`
  - `template.<5 codes>`
  - `actions.{verify, reject, edit, supersede, rollback, pin, unpin, archive, propose, promote, retry, useDeterministic}`
  - `diff.*`, `versions.*`
  - `neverAuto.<10 codes>`
  - `proposal.{state.<9>, origin.<5>, failure.<codes>}`
  - `conflict.{kind.<3>, choice.<3>}`
  - `source.{kind.<6>, authority.<3>, linkOnly, truncated, history}`
  - `index.{mode.<3>, degraded.<reasons>, maintain, rebuild, rebuildConfirm, schemaAssumed, notConfigured}`
  - `benchmark.{harnessOnly, arm.on, arm.off, metric.<5>}`
  - `metrics.observational`
  - `retention.*`
- `context.{role.<5>, consumption.{packetBytes, cap, critical, estTokens, estimated, actualInput, unknown, hostOverheadUnknown}, excluded.<new 5 reasons>, trust.<2>, degraded}`
- `graph.{kind.<10>, relation.<9>, status.<4>, confidence, evidence, freshness, commit, depth, list, canvas, codeGraphUnavailable, build, confirm, reject, addEdge}`
- `overviewPage.knowledge`
- `settings.tabs.knowledge`
- `policy.keys.<21 new>.{label, help}`
- `problems.<16 new>`
- `readiness.warnings.{knowledge_index_fallback, confluence_ref_unreachable}`
- `inbox.question.packet_oversize` gets a role param variant

---

## 3. Frontend

Rules (unchanged):
- orval hooks only, wrapped in `web/src/api/hooks/{knowledge,sources,knowledgeIndex,graph,roleContext}.ts` (ifMatch plus one Idempotency-Key per intent)
- every panel has loading / empty / blocked / degraded / failed states
- logical utilities only; `LocaleDirection`/`DirectionProvider` untouched
- ids, keys, SHAs, URLs and CQL in `<Ltr>`; knowledge text and source text `dir="auto"`

**Pages:**
- **Knowledge** (`features/knowledge/knowledge-page.tsx`, route `/orgs/:orgId/knowledge`, `?record=`/`?proposal=` open sheets on the end side):
  - **Records tab:** type, scope (project select), status and pinned filters; search box (`search_knowledge`) with the mode badge (`index-status-badge.tsx`, degraded with reason); infinite table.
  - **`record-detail-sheet.tsx`:**
    - current verified version, claims via `claim-list.tsx` (verdict chip, evidence chips that open the source viewer or artifact, injection and stale badges)
    - versions list with a status badge and origin (rollback_of)
    - `version-diff.tsx` (claims added/removed/changed plus body hunks)
    - actions: Edit → new draft dialog; Verify (attestation fields for failing claims); Reject; Rollback (`rollback-dialog.tsx` with a target select and the gate preview); Pin/Unpin (`pin-dialog.tsx`: critical, applies-to roles); Archive
    - a 409 shows `ConflictAlert`
  - **Proposals tab:** a queue by state; `proposal-sheet.tsx` shows the evidence bundle rows, draft claims, gate result, never-auto reasons (i18n), author mode/rex links, and Promote / Reject / Retry / "Use deterministic draft".
  - **Conflicts tab:** pairs side by side (claim a vs b, scope, validity, evidence); `conflict-resolve-dialog.tsx`.
  - **Sources tab:** latest per source (kind, authority, version, fetched, hash short, link-only badge); `source-viewer-sheet.tsx` (scrubbed text pre `dir="auto"`, history chain, "Sync now" per project). Empty and blocked state: "No Confluence connection / mapping".
  - **Pinned tab:** critical knowledge plus S3 rules (links to Settings rules).
- **Graph** (`features/graph/graph-page.tsx`, lazy, route `/orgs/:orgId/graph`):
  - node search (`list_graph_nodes`), relation filter chips, status filter (verified / inferred / stale), depth 1|2, Canvas or List toggle
  - `graph-canvas.tsx`: React Flow, `layout.ts` radial by depth; edges styled by status with a text label (verified solid, inferred dashed, stale dotted, plus an icon; never colour alone)
  - `evidence-drawer.tsx` (end side): evidence rows, confidence, commit sha, observed_at and freshness, Confirm / Reject; "Add relation" dialog
  - `code-graph-notice.tsx`: "Code graph (symbols / impact) is not available: GitNexus is an optional Sprint 6 adapter pending license approval"
  - "truncated" banner from the API
- **Task detail Context tab** (rewrite `features/tasks/context-tab.tsx`, plus `budget-bar.tsx`):
  - selector: Run packet + one entry per role execution (`list_run_role_contexts`)
  - stacked budget bar (critical / other / free against the cap), est tokens with an "estimated" label, actual input tokens or "unknown", "Host overhead: unknown"
  - knowledge mode badge (degraded)
  - sections with included / truncated / excluded and the reason (i18n), knowledge items linking to the version
  - open the packet artifact
- **Overview:** `features/overview/knowledge-card.tsx` (proposals awaiting, open conflicts, index mode, degraded link to Settings). The nav badge on Knowledge = awaiting + conflicts, from `useTaskSummary`.
- **Settings → Knowledge tab** (`features/orgs/knowledge-settings-tab.tsx`):
  - the policy panel filtered to the `knowledge.*`, `docs.*`, `graph.*` and `retention.*` keys (reuse `policy-panel.tsx` with a `keyPrefix` filter prop)
  - Wiki status card: mode, bin version, "schema: assumed v1", applied/pending/failed, phase, generation, Maintain, Rebuild (typed confirm)
  - retention preview
  - benchmark card with the harness-only banner
  - the Settings Rules tab gains applies-to roles
- **SSE `invalidateFor`** (with tests in `sse.test.ts`):

  | Event prefix | Invalidates |
  |---|---|
  | `knowledge.*` | `${org}/knowledge*`, `${org}/tasks/summary` |
  | `source.*` | `${org}/knowledge/sources*`, readiness |
  | `wiki.*` | `${org}/knowledge/index`, `${org}/knowledge/search*`, summary, readiness |
  | `context.role_packet_built` | `${org}/runs*` |
  | `graph.*` | `${org}/graph*` |

---

## 4. Work packages (WP73 to WP92; one committer, strictly in order)

Every WP ends with `make fmt && make check`. Route WPs run `make openapi` and commit `api/openapi.json`, `web/openapi.json` and `web/src/api/generated/` together. Commit after every green step (rate-limit cutoffs).

**Test budget:** each server WP adds **at most 3 Mongo tests** in a new `internal/server/s5_<area>_test.go` and reuses fixtures from `walk_setup_test.go` / `testutil_test.go`. All matrices (gates, conflicts, retrieval, parsers, planners, schemas) live in the pure packages.

### WP73: Pure knowledge core (no server)
**Files:** `internal/knowledge/{model.go, transitions.go, hash.go, gate.go, neverauto.go, promote.go, conflicts.go, diff.go, inject.go, author_det.go, bundle.go, compact.go}` plus `_test.go` files.

**Signatures:**
- `type Resolver interface{ Resolve(ref Evidence) (Resolved{Kind, OrgID, Authority, Text, Fields map[string]string, Latest bool}, error) }`
- `CheckClaims(t Type, cs []Claim, r Resolver, org string) GateResult{Claims []ClaimVerdict; Pass bool}`
- `NeverAuto(v Version, rec Record, g GateResult, conflicts []Pair, userVerifiedSuperseded bool) []string`
- `Decide(policy string, never []string) (Outcome auto_verify|await_review|skip)`
- `DetectConflicts(cand Version, existing []Version) []Pair`
- `ContentHash(v) string` via `internal/canon`
- `Diff(a, b Version) DiffResult` (line LCS ≤ 2000 lines, else "too large")
- `InjectionSuspect(text string) []string`
- `DraftFromBundle(b Bundle) []DraftRecord`
- `Consolidate(vs []Version) (Draft, []string)`
- `Transition(from, to Status, actor) error`

**Invariants (tested):**
- zero-evidence → unsupported
- foreign-org ref → invalid
- a business claim with only run evidence → blocked
- an excerpt that is not verbatim → unsupported
- template param mismatch → unsupported
- every NeverAuto code is reachable
- conflicting values with overlapping validity → a pair; non-overlapping → none
- a negation pair
- poisoned text fixtures (en+fa) are flagged
- `DraftFromBundle` never emits business
- table-driven transitions (superseded and archived are terminal)

**Acceptance:** `go test ./internal/knowledge/...`

### WP74: Policy keys, contextpkt extensions, retrieval
**Files:**
- `internal/policy/registry.go` (21 keys, PDV 4) and `policy_test.go`
- `internal/contextpkt/{build.go (Item.AppliesTo, Item.Trust override, Item.RecordID/VersionID/Freshness; In.Role; new reasons), roles.go (RoleProfiles, BuildRole), consumption.go (EstimateTokens(bytes) = ceil(bytes/4), method name)}`
- `internal/knowledge/retrieve/{select.go, select_test.go}`

**Invariants:**
- `BuildRole` never truncates a critical item
- a critical item not for the role → manifest reason `not_for_role`
- Oversize carries Role
- `Select` drops OrgID ≠ in.Org (and reports it in a `Violations` count)
- top_k per section, then `below_rank`
- pinned critical with stale evidence → included with `freshness: evidence_stale`
- deterministic order

**Failure tests:**
- huge pinned rule (cap+1 bytes) → Oversize
- a 40 KiB critical set split across roles fits per role
- poisoned knowledge text cannot break the `<<<SOURCE` frame (`neutralize`)

**Acceptance:** `go test ./internal/policy ./internal/contextpkt ./internal/knowledge/...`

### WP75: KnowledgeIndex port, fake, CLI adapter, queue planner
**Files:**
- `internal/kindex/{port.go, errors.go, fake.go, cli.go, env.go, sandbox.go, schema/v1/{version,ingest,synthesis,compile,lint,search,get}.json, schema.go, projection.go (RenderDoc(v) frontmatter+body, scrubbed), plan.go (Coalesce(entries) last-op-wins per version; Batches), fakecli.go (RunFakeCLI(args, stdin, stdout, stateDir) int: the executable spec)}`
- tests using the helper-process pattern (`TestHelperProcess` re-execs the test binary as a fake `openclaw`)
- `cmd/orqen-fakehost`: new `openclaw` subcommand → `kindex.RunFakeCLI`, state `$ORQEN_FAKEHOST_DIR/wiki/`, scenario `wiki.{down, fail_verb, lint_errors, slow_ms, wrong_schema}`

**Tests:**
- argv allowlist (only the 7 verbs; a test fails if any other verb string appears in `cli.go`)
- explicit env (no inherited variables; `HOME`/`OPENCLAW_HOME` inside the org dir)
- a vault dir outside `<data>/wiki/<org>` → refused
- schema violation → `InvalidOutput`
- timeout → process group killed
- fake idempotent on (id, hash)
- `Coalesce` upsert→delete→upsert
- schema files equal the fake's emitted shapes (round-trip)

**Acceptance:** `go test ./internal/kindex ./cmd/orqen-fakehost`

### WP76: Confluence DocsSource and fake
**Files:**
- `internal/providers/{confluence.go, confluence_storage.go, confluence_test.go}`
- `internal/testfakes/confluence.go`: Server/DC under a custom context path `/confluence` and Cloud under `/cloud/wiki` (v2 pages + v1 search), versions, 401/403/429/5xx injection, an injection-poisoned page fixture, a page linking a catalog `web_url`
- `cmd/orqen-fakes` mounts both and prints the URLs and a token
- admin endpoint `POST /_fakes/confluence/page {id, body}` creates a new version

**Tests:**
- exact path preservation with a context path
- Cloud `/wiki` suffix rule
- historical version read
- storage→text (headings, code macro, tables, links)
- normalize hash stable across CRLF
- 429 `Retry-After`
- census unchanged (GET only)

**Acceptance:** `go test ./internal/providers ./internal/testfakes ./internal/extwrite`

### WP77: orggraph (pure)
**Files:** `internal/orggraph/{model.go, manifests.go (go.mod, package.json, pom.xml, CODEOWNERS, openapi detection), derive.go (Derive(in BuildIn) (nodes, edges)), freshness.go, query.go (Neighborhood(adj, start, depth, filter, limit))}` plus tests.

**Invariants:**
- no edge from name similarity (fixtures `payment-api`/`payments-api`, `foo`/`foo-client`)
- a duplicate identity in two repos → no depends_on edge (ambiguous)
- config edges are verified
- BFS bounded and truncated flagged
- stale marking only when HEAD changed and the edge was not re-observed

**Acceptance:** `go test ./internal/orggraph`

### WP78: learnbench (pure)
**Files:** `internal/learnbench/{suite.go (embedded fixture-v1.json), engine.go (Engine interface; FakeEngine), run.go (Run(suite, arm, packetFn) Result), metrics.go}` plus tests.

**Tests:**
- deterministic results
- the off arm reworks exactly once per needed fact
- `claim_level` is always `harness_only` for the fake engine
- the metrics arithmetic

### WP79: Server knowledge store and routes
**Files:**
- `internal/server/{db.go (indexes incl. text option), ids.go, app.go (path params, idemRequired, routes), knowledge_store.go, knowledge_routes.go, knowledge_dto.go, jobs_tx.go (ensureJobTx)}`
- `api/openapi.json`
- `internal/server/s5_knowledge_test.go`

**Verify/supersede transaction** (also used by promote, conflict accept_new and rollback):
```
pre (outside the txn): v, rec := load(org); gate := CheckClaims(v, resolverFor(org)); conflicts := DetectConflicts(v, verifiedInChain(org, v.scope_key))
if actor == user: failing claims without attestation → 422 knowledge_claims_unsupported
if actor == policy: never := NeverAuto(...); if Decide(...) != auto_verify → record the decision; return
txn:
  rec' := find {_id, org_id, revision: ifMatch}             → 409 revision_conflict (+current)
  v'   := find {_id, org_id, record_id, status: draft}      → 409 knowledge_version_not_draft
  if open conflicts reference v' → 409 knowledge_conflict_open
  if len(conflicts) > 0 → insert kcf (unique open fingerprint); v'.status = conflicted; events; commit; return 409 knowledge_conflict_open
  if rec'.verified_version_id != nil: UpdateOne {_id: old, status: verified} → {status: superseded, superseded_by: v}; MatchedCount must be 1
  UpdateOne v' → {status: verified, verified_at, promotion, claims verdicts (+attestation evidence appended)}
  rec' → {verified_version_id: v, current_version_id: v, status: verified, $inc revision}
  outbox upsert(v) [+ delete(old)]: seq via counters "wob:<org>" $inc; ensureJobTx(wiki_maintain, "wiki:<org>")
  events
```
**Rollback:** target ∈ record, `verified_at != nil`, target ≠ current → insert version N+1 (rec.next_number $inc in the same txn), then the path above with actor user; if the gate fails, leave it a draft (200 + gate).

**Search route:** in WP79 it uses only the Mongo fallback (WP81 adds wiki). The `$text` query always includes `org_id` and `status`.

**Tests (3):**
1. create → edit draft → verify → second version → supersede → rollback creates v3 verified and v2 superseded, history intact
2. two orgs with the same key: search, get and verify across orgs → 404, no leak
3. a conflicting draft → conflicted with the existing one still verified; resolve accept_new with attestation; a zero-evidence claim → 422

### WP80: Sources, sync job, mapping check, context docs
**Files:**
- `internal/server/{sources.go, source_sync_job.go, probe_adapter.go (Confluence identity/capabilities), mapping_check_job.go (GetPage per ref), readiness.go + internal/readiness/evaluate.go (warnings K41), run_stages.go (design_refs from source_versions; on-demand linked pages K31; jira_issue source_versions written at ingest)}`
- `provider_clients.go` (DocsSource factory)
- `s5_sources_test.go`

**Rules:**
- the source_version insert is idempotent on (org, kind, con, ext, hash)
- `latest` flips in the same txn
- a changed hash → K16 stale marking + `source_changed` proposals (enqueue)

**Tests (3):**
- a Server/DC sync with a custom path creates v1, then a fake edit → v2 + evidence_stale on the citing verified version
- the poisoned page enters the packet wrapped, scrubbed and flagged, and changes no policy or command (assert the run's pinned policy and commands are unchanged)
- a Cloud linked-page fetch in the context stage; a foreign host → `out_of_scope`

### WP81: Wiki projection
**Files:**
- `internal/server/{wiki.go (vault paths, status), wiki_maintain_job.go, knowledge_search.go (searcher selection, cache), intake_scheduler.go (sweepWiki, retention sweep)}`
- routes index / maintain / rebuild
- `s5_wiki_test.go`

**Job** (pseudo, per K26):
```
st := lazyState(org)
if bin == "": set mode=fallback, degraded{wiki_not_configured}; mark pending → deferred; complete; return
lock := flock(vault.lock) or JobFailure{vault_busy, Retryable, 5s}
if st.rebuild_requested: plan := allVerified(org) (paged); wipeValidated(vaultDir); gen++
else plan := Coalesce(outbox{org, pending, seq > applied} ≤ 200)
write files atomically (tmp → rename)
for phase in [ingest, synthesis, compile, lint]:
  fenced(lease, set phase, phase_fence)   // lost lease → return errLeaseLost before the CLI runs
  err := idx.<phase>(ctx, vault)
  on err: fenced(set degraded{code, phase}, attempts++ (failed after 10), event wiki.degraded); return retryable backoff
smoke Get(one upsert id) hash == expected, else degraded{smoke_mismatch}
fenced(applied_seq = max; entries applied, purge_at; degraded = nil (event wiki.recovered); phase idle; rebuild_requested = false; if more pending → complete + insert the next job in the same txn)
```
**Search:** mode wiki only when configured, not degraded and pending == 0. Hits are re-resolved with the K22c filter; otherwise the fallback `degraded{reason}`. Cache key = sha(org, q, chain, generation, applied_seq).

**Tests (3):**
1. **Wiki down:** fake `down` → verify still commits, the version stays verified, the outbox stays pending, status `fallback/degraded`, search returns fallback hits; fake recovers → maintain → `wiki` mode; rebuild → generation+1 and the same hits
2. **Lease conflict:** two Apps (two runners) on one DB; a hook between ingest and compile expires runner A's lease; runner B acquires; A's fenced phase write fails and A runs no further CLI verb; B waits on flock (`vault_busy` retry) then completes; every outbox entry is applied once; applied_seq is monotonic
3. **Cross-org fake hit:** the fake search returns org B's version id → dropped, `knowledge.scope_violation` event

### WP82: Context integration
**Files:**
- `internal/server/{run_stages.go (knowledge/experience sections via retrieve.Select; pinned critical knowledge into critical_rules; graph_neighbors item after WP85 behind a nil check), role_context.go (BuildRole at dispatch inside dispatchRole before the brief is rendered; artifacts; rex.context; usage rows), context_routes.go (roles routes; manifest v2)}`
- `rolebriefs/{worker,reviewer,verifier}.md` + Version "s5.1"
- `rules.go` (applies_to_roles)
- `s5_context_test.go`

**Invariant:** the role packet is built from the same stored sources as the run packet plus the knowledge retrieval at dispatch time. The reviewer packet never contains a `task_discussion` or worker reply item (test asserts the item kinds).

**Tests (3):**
- a run with verified, stale, expired and foreign-project knowledge → the manifest reasons are exact; consumption fields are present and actual tokens come from the fake catherd record
- a huge pinned critical rule for `worker` only → the reviewer packet builds, the worker gets Inbox `packet_oversize{role: worker}`, and the rule text is not truncated anywhere
- retrieval in fallback → the manifest shows `knowledge.degraded`

### WP83: Proposals, deterministic author, merge hook
**Files:**
- `internal/server/{knowledge_bundle.go, knowledge_author_job.go, knowledge_proposals.go (routes), mr_watch_job.go (merged txn: insert the proposal + ensureJobTx when the pinned learn_on_merge is set and the type policy ≠ off), runs terminal txn usage rollup}`
- `s5_proposals_test.go`

**Job:**
1. bundle → artifact
2. `DraftFromBundle`
3. per draft record: upsert the record by (scope_key, key) and insert a draft version (if a draft already exists → mark the proposal `superseded` for that record and attach the new draft id)
4. gate + NeverAuto + Decide per the type policy
5. auto → the verify path with actor policy; else `awaiting_review`

**Tests (3):**
- a fake GitLab merge → proposal → deterministic drafts (task, experience), policy manual → awaiting; promote → verified + outbox; policy auto for experience → auto-verified; the task.merged claim passes; **no Jira request recorded** (fake Jira write log empty)
- with auto on: a conflicting experience fact → conflicted, not verified
- manual "propose from run" on a dry-run terminal run → no merge claim

### WP84: Roles author (fake-tested)
**Files:**
- `internal/rolebriefs/{writer.md, kverifier.md}` (+briefs.go roles `Writer`, `KVerifier` mapped to catherd role `verifier`)
- `internal/server/knowledge_roles.go`
- `role_stages.go`: add `roleSpec.Detached bool`. When set, a pause returns parked without `s.hold`, and a tool error or stop returns an error instead of opening an Inbox item. The coder audits every `s.hold`, `stageStop` and `casRun` reachable from `roleStep`/`dispatchRole`/`collectRole`, gates them on `!Detached`, and lists them in DEVIATIONS.
- `workspace_jobs.go` (cleanup skip guard)
- `internal/testfakes/hostfake/scenario.go` (`Replies` keyed by name prefix `writer-`, `kverifier-`)
- `s5_roles_test.go`

**Rules:**
- org serialization: `knowledge_author_gates` doc `kag_<org>` with a holder, CAS acquire and release in the job's fenced txns
- a held gate → retryable `author_busy`
- the verifier verdict can only downgrade (`min(det, role)`)

**Tests (2):**
- a fake writer and verifier happy path: rex docs with proposal_id, 2 dispatches, no coding slot taken
- the writer cites an unknown ref_id or the verifier says contradicted → claim unsupported → awaiting_review with never_auto; an unparseable reply twice → failed{reply_unparseable}

### WP85: Graph server
**Files:**
- `internal/server/{graph.go (routes), graph_build_job.go (inputs: enabled projects' mapping, CodeHost.ResolveCommit default branch, ReadFileRaw of manifests at that SHA (≤ 256 KiB each), CODEOWNERS (`CODEOWNERS`, `.gitlab/CODEOWNERS`, `docs/CODEOWNERS`), openapi paths, Confluence source_versions links, tracked issues/runs/MRs; → orggraph.Derive; upsert nodes/edges; stale/retention)}`
- `run_stages.go` graph_neighbors
- `s5_graph_test.go`

**Rules:**
- user edges (POST) need both nodes in the org, and a relation from the enum
- confirm → verified with user evidence appended
- reject keeps the edge hidden (status rejected), and a later build never resurrects it

**Tests (2):**
- build over two fake repos with exact module identity → depends_on inferred; similar names → none; neighborhood depth/limit truncation; confirm → verified
- a HEAD change without re-observation → stale; the inferred edge is pruned after retention, the verified one is kept

### WP86: Compaction, retention, benchmark, metrics, summary
**Files:** `internal/server/{knowledge_compaction.go (daily sweep), knowledge_retention.go (preview), knowledge_benchmark_job.go, knowledge_metrics.go, tasks.go summary}`.

**Tests (1):** compaction proposal from 5 experience versions (manual only); benchmark job stores `harness_only`; preview counts.

### WP87 (web): Knowledge page
**Files:**
- `web/src/api/hooks/{knowledge,sources,knowledgeIndex}.ts`
- `web/src/features/knowledge/*` (§3)
- router + sidebar `to: 'knowledge'` + badge
- i18n
- tests: claim list verdicts, verify with attestation, a 409 conflict alert, rollback dialog, degraded badge, RTL snapshot with `DirectionProvider`

**Acceptance:** `pnpm -C web test && pnpm -C web check:rtl && pnpm -C web typecheck`

### WP88 (web): Context tab, Overview, Settings, SSE
**Files:**
- `features/tasks/{context-tab.tsx, budget-bar.tsx}`
- `features/overview/knowledge-card.tsx`
- `features/orgs/knowledge-settings-tab.tsx`
- `policy-panel.tsx` (keyPrefix filter)
- `features/rules/rules-panel.tsx` (applies-to)
- `api/sse.ts` + `sse.test.ts`
- `api/hooks/roleContext.ts`

**Tests:** budget bar math and labels ("estimated", "unknown"), not_for_role reason text in fa and en.

### WP89 (web): Graph page
**Steps:**
1. `pnpm -C web add @xyflow/react@<newest allowed>`
2. record the licenses (`node_modules/@xyflow/*/package.json` license, zustand, classcat, d3-*) and the `vite build` chunk sizes before and after in DEVIATIONS; main chunk growth ≤ 2 KB

**Files:** `features/graph/*` (§3), lazy route, `api/hooks/graph.ts`.

**Tests:** `layout.ts` deterministic; list view keyboard navigation; the code-graph notice; the evidence drawer opens on the end side in RTL.

### WP90: Fakes and walk fixtures
- `cmd/orqen-fakes`: Confluence (both flavours), admin endpoints, and a `/_fakes/jira/writes` log (must stay empty)
- `orqen-fakehost openclaw` scenario via `PUT /_fakes/host/scenario`, keys `wiki.*`
- `walk_setup_test.go` gains a Confluence connection, a mapping with an architecture page, two fake repos with manifests and a policy fixture (experience auto)
- README "Sprint 5 fakes" section

### WP91: Verification walk and docs
Built binary (`make build`), `orqen-fakes`, fakehost wrappers for codex, catherd and openclaw (`ORQEN_WIKI_BIN=<wrapper>`), throwaway `ORQEN_DATA_DIR`/`ORQEN_DB_NAME`, `ORQEN_CREDENTIAL_STORE=memory`.

**Browser walk** (Browser tools, fa and en):
1. Confluence sync → source viewer; edit the page → v2 + stale badge
2. a task to a fake live-loopback MR → `/_fakes/gitlab/merge` → proposal → promote → verified; experience auto-verified; Wiki mode `wiki`
3. a conflicting manual record → Conflicts tab → resolve
4. rollback → v3
5. pin a huge critical rule for the worker → Inbox packet_oversize{worker}; the reviewer packet is fine
6. Wiki scenario `down` → degraded badges on Knowledge, Context, Overview and Settings; canonical unchanged; recover + Rebuild
7. Graph page: canvas and list, evidence drawer, confirm edge, code-graph notice
8. Context tab per role: bytes, estimated tokens, unknown host overhead
9. a second org with the same key sees nothing from the first
10. the poisoned page leaves policy unchanged
11. `/_fakes/jira/writes` is empty; `/_fakes/gitlab/writes` contains only the S4 MR flow

**Personal-state guard:** hash before and after as in S4, plus only `test -e ~/.openclaw` (existence, no read) and it must be unchanged.

**Docs:** write `docs/VERIFICATION-S5.md`; update the DEVIATIONS S5 table and README (Sprint 5 section, `ORQEN_WIKI_BIN`, "schema assumed v1").

### WP92: Gated real tests (written and committed, never run by the coder)
- **`ORQEN_TEST_WIKI_BIN=<abs openclaw>`:** `go test ./internal/kindex -run TestWikiReal -v`. Temporary vault under `t.TempDir()`, explicit env, sandbox enforce. It runs `version`, each verb on two fixture docs and `search`/`get`, validates every output against `schema/v1`, and on mismatch prints a structured diff (no auto-fix). It asserts no process remains in the group and no files exist outside the temp dir.
- **`ORQEN_TEST_CONFLUENCE_URL` + `ORQEN_TEST_CONFLUENCE_TOKEN_CMD` + `ORQEN_TEST_ALLOW_REMOTE_READ=1`:** GET-only identity and one page read with version. The token is obtained from the user's command at run time, never printed. It refuses `*.digikala.com` unless `ORQEN_TEST_ALLOW_COMPANY_HOST=1` is also set by the user.
- **`ORQEN_TEST_KNOWLEDGE_ROLES=1`** (+ the S4 codex and catherd vars): one writer + kverifier dispatch on a fixture bundle. **Spends model tokens; user-run only.**

---

## 5. Definition of Done, risks, needs the user

**DoD, mapped to the SPEC §18 Sprint 5 exit:**

| Exit criterion | Where it is shown |
|---|---|
| "Claim with evidence" | Every verified version's claims pass `CheckClaims` or carry an audited attestation; evidence chips resolve to the source, artifact or MR (WP73/79/83 tests, walk steps 1–2). |
| "Conflict/rollback" | Conflicts open, never auto-verify, are resolved in the UI; rollback creates a new version with history intact (WP79 tests, walk steps 3–4). |
| "Critical rules preserved" | Never truncated; per-role partition or Inbox; the huge-rule test; pinned critical with stale evidence is still included (WP74/82, walk step 5). |
| "Context consumption visible" | Manifest v2 per run and per role, with bytes, estimated tokens, actual input tokens or unknown, and unknown host overhead, in the Context tab (WP82/88, walk step 8). |

**§19 rows:**
- **Two orgs with the same key share no knowledge:** WP79 test 2, WP81 test 3, walk step 9
- **Conflicting knowledge or a claim without proof is never auto-verified:** WP73, WP83 test 2, WP84 test 2
- **Large context and long critical rules are split or referred, never removed:** WP74, WP82 test 2
- **A task doc with policy or token instructions is read as data:** WP80 test 2, walk step 10
- **fa/en RTL:** WP87–89 tests and walk
- **Wiki unavailable keeps canonical, with degraded retrieval** (§16): WP81 test 1, walk step 6

**Also required:** `make check` green; openapi drift clean; census unchanged (no new write paths; Jira writes still refused); personal-state guard identical.

**Risks:**
1. The Wiki CLI contract is unverified. The adapter is isolated to `cli.go` + `schema/v1`, and the gated test produces the diff.
2. The Confluence Cloud v2 and Server endpoints are fake-verified only.
3. The `roleStep` Detached refactor touches S4 code. It is audited in WP84 with S4 tests kept green; if it is risky, `roles` mode ships disabled in the UI (deterministic only) and is recorded as a deviation.
4. The Mongo `default_language: none` text search is weak for Persian morphology. It is acceptable as a fallback and labelled degraded.
5. Server test time grows: at most ~17 new Mongo tests, the rest in pure packages.
6. React Flow bundle and license: lazy chunk; licenses checked at install.
7. Template-only deterministic drafts give modest knowledge value. They are honest; the roles author is optional.
8. A transaction duplicate-key on `ensureJobTx` is mitigated by retry-once plus the scheduler backstop.
9. The learning benchmark is a harness only; no improvement is claimed.

**Needs the user:**
- install OpenClaw Memory Wiki, set `ORQEN_WIKI_BIN`, run `TestWikiReal` and report the schema diffs
- real Confluence access (if it exists at the company) and the gated read test
- writer and verifier model and rung choices in the model profile, and the decision to enable `knowledge.author=roles` (model spend)
- policy decisions on auto promotion per type and scope (defaults are manual)
- the React Flow dependency approval if your policy requires it
- a later decision on any Jira knowledge reference write (refused in S5)

**Deferred to Sprint 6+:**
- the GitNexus code graph (license-gated)
- a Figma MCP design source
- vector or local embeddings search
- a real-model learning benchmark and causal evaluation
- pyproject/gradle manifest parsers
- Jira Server epic links
- a tokenizer-backed token budget
- shared or team vaults
- canonical delete/export

### Critical files for implementation
- internal/contextpkt/build.go
- internal/server/run_stages.go
- internal/server/mr_watch_job.go
- internal/server/db.go
- internal/server/role_stages.go
- internal/policy/registry.go
- internal/providers/iface.go
