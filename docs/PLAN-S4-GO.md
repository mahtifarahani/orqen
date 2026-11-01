# PLAN-S4-GO: Sprint 4. Coding, verification, Draft MR, independent review, fixes and CI readiness. Test-only, with no writes to anything real.

This plan is based on HEAD `4004088`. I checked it read-only against these sources:
- `internal/server/{queue,jobs,run_advance_job,run_stages,workspace_stage,project_validate,automation,readiness,app,artifacts}.go`
- `internal/{runstate,catherd,providers,workspace,testfakes,readiness,policy}` and `cmd/orqen-fakes`
- catherd 1.5.0 source: `infra/{host-context,codex-queue,delivery,dispatch-dir,paths}.ts`, `domain/{record,role-prompts}.ts`, `entry/{runs-command,role-cli-command,profile-command}.ts`, `entry/mcp/{server,handshake}.ts`, `services/profile-store.ts`, `plugin/skills/catherd/SKILL.md`, `plugin/.mcp-codex.json`
- reference-project `docs/architecture/orchestrator.md` and ADR 0009
- `codex app-server --help`, `codex queue --help` (Codex CLI 0.160.0)

Nothing else was run. I did not start any app-server, thread, turn, catherd run, or network write. I could not generate the app-server JSON schema, because the generator only writes to a directory and this session was read-only. WP54 generates it, which is a local read and spends nothing.

**Verified facts that drive the design:**
- **How catherd knows its host.** catherd identifies the host from the MCP client name `codex-mcp-client`, or from `CATHERD_ORCHESTRATION_HOST=codex`. It learns the thread from `_meta.threadId` on every Codex tool call.
- **Push is only a delivery notice.** catherd tells a thread a role finished with `codex queue --remote unix:// --thread <uuid>`. The receipt only proves the queue accepted the message. Its own fallback is "use peek/result".
- **What marks completion.** A dispatch is finished when its record exists in `R/runs.jsonl` (`RunRecordSchema`: `status` ok/failed/limit/timeout/cancelled, `rung`, `backend`, `failoverFrom`, `thread`, `tokens`, `costUsd|null`, `changedOwned`, `violations`, `replyStatus`, `replyPath`).
- **Admission is visible on disk.** Admission creates `R/roles/<name>/<dispatchId>/` atomically. A `collect` file stays there until the orchestrator calls `result`.
- **Read-only CLI.** `catherd runs show <id> --json --debug` reads records, dispatches and delivery receipts, redacted. `catherd runs cancel <id> <name>` interrupts the role, then sends SIGTERM and SIGKILL, and records it cancelled. Neither needs a model.
- **Workers do not commit; the orchestrator does.** `land` is refused until the milestone has both a reviewer record and a verifier verdict.
- **Profile resolution.** catherd uses the profile bound to the repo's git toplevel (`profile use <name> --repo`, run from that cwd), else the active one. `run_start(repo, …)` pins that profile, role access and isolation for the run.
- **Reply files can exceed what `result` returns.** The reviewer and verifier replies sit in `replyPath`; `result` returns a capped copy.

---

## 1. Key decisions

| # | Topic | Decision |
|---|---|---|
| H1 | Host architecture | **The Codex App Server is the real host. Orqen is the orchestrator's brain; the Codex thread is its hands ("directed turns").** Per run, Orqen starts one real thread and sends turns that each instruct exactly one catherd MCP tool call with exact arguments. The authoritative result is the app-server's `mcpToolCall` item (server `catherd`, tool, arguments, result). The model's prose is never trusted. The catherd skill is **not** loaded; Orqen's directive replaces it. No thread ID or client name is ever fabricated: catherd sees Codex's own client and `_meta.threadId`. |
| H2 | Transport and process | One `codex app-server --listen stdio:// --strict-config` child process **per organization**, spawned and supervised by Orqen. It speaks newline-delimited JSON-RPC over stdin and stdout. It stops after `host.idle_stop_secs` with no run in a host state.<br>**Push is not used** (`delivery: poll`): completion comes from catherd records polled by the `role_watch` job. With stdio there is no unix endpoint, so catherd's queue attempt answers "not-submitted" and no inbound turn can start an unsupervised orchestrator.<br>**Spike check (user, HS3):** catherd's `codex queue --remote unix://` must not reach the user's personal daemon. If it could, it would only be refused (unknown thread UUID), but the spike must confirm this. |
| H3 | Isolation of the host tree | Every process under the app-server gets an **explicit** environment, nothing inherited:<br>- `HOME=<data>/host-home/<org>` (0700)<br>- `CODEX_HOME=<data>/codex/<org>/home` (0700, Orqen-owned; it holds only the user's `auth.json` from their own `codex login`)<br>- `CATHERD_HOME`, `CATHERD_CLAUDE_AGENTS_DIR` from `OrgEnv`<br>- `CATHERD_ORCHESTRATION_HOST=codex`<br>- `PATH=rt.ChildPath`, `LANG`, `TMPDIR=<org>/tmp`<br>- `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=<data>/host-home/<org>/gitconfig`<br>- `GIT_SSH_COMMAND=false`, `GIT_TERMINAL_PROMPT=0`<br>- the K3 matrix secrets for host processes (`TYPESAFE_API_KEY`, plus isolation keys of backends the pinned profile uses)<br>**Never passed:** `SSH_AUTH_SOCK`, any `CODEX_*` or `CLAUDE_*` from Orqen's own env, proxies to write hosts, or any GitLab or Jira credential.<br>The gitconfig sets `credential.helper=` (empty), and `url."orqen-blocked://".pushInsteadOf` for `https://`, `http://`, `ssh://` and `git@`. So **any push a worker attempts fails**, and the osxkeychain helper and the user's `~/.gitconfig`, `~/.netrc` and glab or gh tokens are not reachable (HOME is isolated).<br>App-server flags: `-c shell_environment_policy.inherit=all`, `-c shell_environment_policy.set.GIT_CONFIG_GLOBAL=…`, `-c approval_policy="never"`, `-c sandbox_mode="read-only"` (orchestrator thread), `-c mcp_servers.catherd.command=<abs catherd>`, `args=["mcp"]`, `env={CATHERD_HOME…}`, `-c analytics.enabled=false`.<br>The mirror config (shared by all worktrees) also gets `remote.origin.pushurl=orqen-blocked://disabled`.<br>**Consequence: Sprint 4 supports only `codex:` rungs.** Other backends need logins in HOME, so readiness reports blocker `host_backend_unsupported:<backend>`. |
| H4 | Protocol pinning | WP54 runs `codex app-server generate-json-schema --out internal/codexhost/testdata/schema-0.160.0`. It spends nothing and writes only that directory.<br>`internal/codexhost/methods.go` lists every method Orqen sends (`initialize`, `thread/start`, `thread/resume`, `turn/start`, `turn/interrupt`, `account/read`, `model/list`, `mcpServerStatus/list`) and every notification and server request it handles (`turn/started`, `turn/completed`, `item/started`, `item/completed`, `thread/tokenUsage/updated`, `error`, and the `*requestApproval` / request-user-input family).<br>A test asserts each name and the required params exist in the schema. If a name differs, the coder uses the schema's name and records it in DEVIATIONS.<br>`MinCodexVersion = "0.159.2"` (reference-project ADR 0009). |
| H5 | Directive and validation | Turn input is a single text item:<br>`ORQEN-DIRECTIVE v1 id=<dir_…>` / `Call exactly one tool: catherd.<tool>` / `ARGS <canonical JSON>` / `Call no other tool and run no command. Afterwards reply exactly: DONE <dir_id>`.<br>The thread's base instructions repeat this rule and add: "if a catherd message arrives, reply `ACK` and call nothing".<br>**Validation:**<br>- exactly one `mcpToolCall` item with server `catherd`, the right tool, and `canonical(args) == canonical(want)`<br>- any `commandExecution`, `fileChange`, `webSearch` or second tool item → `host_deviation`: interrupt, the directive fails, event `host.deviation`<br>- approval requests: accept only for the matching catherd call, decline everything else<br>- the result is `structuredContent`, else the JSON parsed from `content[0].text`; a catherd error is `{code, message, fix}` → `catherd.ToolError`<br>- turn timeout `host.turn_timeout_secs` (120) → `turn/interrupt` → `ErrOutcomeUnknown` → reconcile |
| H6 | Orchestrator model | Org-only policy `host.orchestrator_model` (string; `""` = Codex default) and `host.orchestrator_effort` (low/medium/high, default low). The actual model comes from the `thread/start` response and is stored on `host_sessions`. Orchestrator token usage comes from `thread/tokenUsage/updated`; when absent it is `unknown`, never 0. |
| H7 | catherd run per Orqen run | Host-session stage, in order:<br>1. `catherd profile use <pinned o-<slug>-v<n>> --repo` with cwd = worktree, under `OrgEnv` (org namespace only)<br>2. directed `run_start(repo=<worktree>, title="<KEY>: <summary>", a_lines=<acceptance items>)`<br>3. store `catherd_run_id` and the run dir `dir` (must resolve inside `<org>/home/data` after EvalSymlinks)<br>4. directed `profile_get(repo)`: the profile name must equal the pinned one, else fail `profile_pin_mismatch` |
| H8 | Roles and names | Single milestone `M1`. Name scheme:<br>- `architect-M1`: `route(role)`, then `dispatch` with that rung<br>- worker per lane: `worker-<lane>` (`dispatch(..., lane)`; catherd routes); fix rounds reuse the same name with `thread: <record.thread>` (catherd requires the same name for its own thread)<br>- `verifier-M1`: fresh thread each round<br>- `reviewer-M1-r<n>`: a new name and fresh thread every round<br>- `writer`: unused in S4<br>The architect brief requires exactly one milestone M1 with 1–3 lanes. Orqen reads `plan.md` and `lanes/*.md` directly from the validated run dir (read-only, 256 KiB cap). Lanes run **sequentially** in `After:` order. A lane whose `Owns` matches a deny protected path → Inbox `protected_change` before dispatch. |
| H9 | Completion and collection | A role is **finished** when `runs show --json --debug` has a record for its `dispatchId`. It is **collected** after a directed `result(run, name)`, which marks it read; Orqen stores the capped reply. Orqen reads the full `replyPath` file (path validated inside the org home, ≤ 256 KiB) as the artifact `role_reply`. A worker's `STATUS: complete` and its "tests pass" claims are **never** evidence. |
| H10 | Dispatch without double dispatch | `role_executions` move through `planned → dispatching → dispatched → finished → collected` (plus `cancel_requested`, `cancelled`, `failed`, `ambiguous`). The `dispatching` mark is committed **before** the turn. **Reconcile rule** (on recovery, or after any unknown turn outcome): list the dispatches of that name with `admittedAt ≥ rex.dispatching_at` that no other rex has claimed.<br>- found → adopt its dispatchId (event `role.reconciled{adopted:true}`)<br>- none, and the host's turn is known ended (`turn/completed` seen, or `thread/resume` history has no completed catherd `dispatch` item for this directive) → dispatch again with the same rex<br>- none, and the outcome is unknown (the thread cannot be read) → rex `ambiguous` → Inbox `dependency_unavailable` code `role_dispatch_uncertain` (options `retry_dispatch`: re-reconcile first; `cancel_run`)<br>Admission is atomic in catherd, and `E_ADMIT_DUPLICATE` protects a live name. |
| H11 | Lifecycle, budget, timeouts | catherd enforces each role's idle and wall timeouts (record `timeout`, exit reason). Orqen adds three guards:<br>- **Wall ceiling:** `roles.max_wall_secs` (7200) → role cancel<br>- **Dispatch cap:** `roles.max_dispatches_per_run` (12) → Inbox `budget_exceeded` (code `dispatch_cap`) before the next dispatch<br>- **Budget refusal:** catherd's `E_RUN_BUDGET` → Inbox `budget_exceeded`<br>**Outcomes:**<br>- record `limit` without a stand-in (catherd pauses) → Inbox `model_unavailable` (`usage_limit`)<br>- `failed` or `timeout` → one directed `climb(run, lane, reason)` when the ladder allows, then dispatch fresh; `top:true` or a second failure → Inbox `model_unavailable` (`role_failed`)<br>- `E_ADMIT_RUNG` / `E_BACKEND_*` → Inbox `model_unavailable` with catherd's `fix` text<br>**Silent fallback is forbidden:** there is no direct `codex exec` path anywhere. |
| H12 | Cancel and interrupt | `cancel_run` keeps its Sprint 3 transaction and adds: live rexes → `cancel_requested`, plus job `role_cancel`. For each live name the job runs `catherd runs cancel <run> <name>` (CLI, no model) and `turn/interrupt` for any active turn. `cancellation.confirmed` is set only when every rex record is terminal. The workspace is kept (preserved when dirty). Orqen never closes an MR (closing is a write, left to the user). |
| H13 | Concurrency | Global coding slots `Config.CodingSlots` (env `ORQEN_CODING_SLOTS`, default 1, system-wide across orgs) and a per-org cap from `concurrency.coding_runs`. Slot docs live in `coding_slots` (`_id slot:<n>`).<br>**Acquired** on queued → coding by the scheduler's dispatcher (every tick, FIFO by `queued_at` across orgs).<br>**Held** through coding, verifying, publishing_draft, reviewing and fixing.<br>**Released** in the same transaction that moves the run to needs_input, awaiting_ci, a hold, or a terminal state. A queued run without a slot shows hold `awaiting_coding_slot`. A sweep releases slots whose run is not in a slot state.<br>**Host lock:** `flock(<codex home>/orqen-host.lock)` per org; a second runner gets retryable `host_busy`. |
| H14 | Host unavailable | `HostAdapterAvailable` (readiness) = host probe ok within 24 h **and** (fake host **or** user spike attestation). Probe job `host_probe`: spawn the app-server, `initialize`, `account/read`, `model/list`, `mcpServerStatus/list`; catherd must be listed with tools `run_start`, `dispatch`, `result`, `route`, `cancel`, `land` and `profile_get`. No thread or turn is created, so nothing is spent. Specific blockers: `host_codex_missing`, `host_codex_too_old`, `host_login_missing`, `host_catherd_mcp_missing`, `host_not_probed`, `host_spike_unverified` (real codex only; cleared by `POST /host/spike-attest` after the user runs the gated spike). Without a host, queued runs stay `project_not_ready` and nothing is dispatched. |
| H15 | Verification | **Two layers; both must pass.**<br>**(a) Orqen runs the approved project commands itself** in a separate **detached verification checkout** (`git worktree add --detach <ws>/<org>/verify/<run>-<sha12> <sha>` from the mirror), never the coder's worktree:<br>- argv exec (no shell), cwd validated inside the checkout<br>- env = minimal base plus resolved env refs; no credentials<br>- `timeout_secs`; process group kill (Setpgid, SIGTERM, then SIGKILL after 5 s)<br>- stdout and stderr capped at `verification.max_output_bytes` (4 MiB), scrubbed (contextpkt scrubber plus known connection secrets), stored as artifacts, sha256 recorded<br>- macOS `sandbox-exec` profile: deny `network-outbound` unless `needs_network`; deny `file-write*` outside the checkout and its tmp dir. `verification.sandbox=enforce` (default) makes a missing `sandbox-exec` give `incomplete` (`sandbox_unavailable`); `best_effort` records `network_unenforced`<br>- `needs_docker` with a failed preflight docker check → `incomplete`<br>**(b) The catherd verifier role** (`verifier-M1`, fresh) gets acceptance lines, the commands and the head SHA. Its `VERDICT: PASS` is required; FAIL means a fix round; `BLOCKED: environment` → Inbox `dependency_unavailable`.<br>Layer (a) runs first; a fail skips (b) and saves model spend. |
| H16 | Baseline vs new failure | **Baseline:** the same commands run at `base_sha`, once per (repo, base_sha, command fingerprints), cached as `verification_evidence kind=baseline`.<br>**Per command:**<br>- head pass → `pass`<br>- head fail and base pass → `new_failure` (gate fail)<br>- head fail and base fail → `baseline_failure` (gate fail unless `verification.allow_baseline_failures=true`, in which case it passes with a note)<br>- timeout or spawn error → `incomplete`<br>The gate is `pass` only when every command is pass (or allowed baseline) **and** the verifier says PASS. Anything partial is `incomplete`, never pass. |
| H17 | Test-weakening detection | Pure `verify.Weakening(diff, testGlobs)` over `base..head`. Signals:<br>- a deleted test file<br>- a net decrease of test cases per file (regexes: `func Test`, `it(`, `test(`, `describe(`, `def test_`, `@Test`)<br>- added skip or only markers (`t.Skip`, `.skip(`, `xit(`, `it.only`, `@pytest.mark.skip`, `@Disabled`)<br>- changed test-command config (`package.json` `scripts`, `Makefile`, `vitest`/`jest`/`pytest` config, `.gitlab-ci.yml`)<br>- a changed file that an approved command's argv references<br>Default test globs: `**/*_test.go`, `**/*.{test,spec}.*`, `**/test/**`, `**/tests/**`, `**/__tests__/**`.<br>Any signal → gate `incomplete` → Inbox `protected_change` code `test_weakening_suspected`. Options: `send_back` (fix round with the signals), `accept(text)` (audited waiver, recorded on the evidence), `cancel_run`. |
| H18 | Commit | After the workers are collected:<br>1. `git status`; empty → treat as refusal (H11 climb)<br>2. changed paths matching a deny protected path → Inbox `protected_change`<br>3. `git add -A` then `git commit` with `-c core.hooksPath=/dev/null`, author and committer from org policy `publish.commit_author_name` / `publish.commit_author_email` (defaults `Orqen`, `orqen@localhost.invalid`), message `<KEY>: <summary>\n\nOrqen-Run: <run_id>`<br>4. record `heads.head`<br>A commit made by the worker is accepted but flagged `worker_committed` (warning). The commit is local only. |
| H19 | Independence | **Before dispatch:** the profile `ComputeIndependence` must be `independent` (readiness already checks this).<br>**After review, from the actual records:** the coder families are the `Family(rung)` of every worker record in the run, failover stand-ins included (`record.rung` is the actual rung). The reviewer family is that of the reviewer record.<br>- any unknown family → gate `independence_unknown`<br>- shared family → `same_model` → Inbox `model_unavailable` code `reviewer_not_independent`<br>- also required: `reviewer.thread ∉ worker threads` and the reviewer dispatched without `thread`<br>`review.require_different_model` stays Locked true. |
| H20 | Review | A fresh reviewer per round. The brief contains:<br>- acceptance items<br>- `base_sha..head_sha` and the changed files: in live mode, the MR's `diff_refs` must equal (base, head), otherwise Orqen fetches the MR diff and uses it; in dry-run, the local diff, which is exactly the push candidate<br>- test evidence (commands, outcomes, output hashes)<br>- critical rules from the packet<br>It contains no worker trace or reply.<br>The reply must end with `ORQEN-REVIEW v1` followed by JSON `{verdict: approved\|changes_requested\|approved_with_scope, scope, findings:[{id, severity: blocker\|major\|minor\|nit, file, line, side:new\|old, title, body, acceptance_ref}]}`.<br>**Validation:** the file must exist at head and the line must be in range, else `location_valid=false` (kept, never inline). Blocking = blocker or major. Approved with blocking findings counts as changes_requested. Unparseable → one fresh retry, then Inbox `review_disagreement` (`review_unparseable`). |
| H21 | Fix rounds | When verification fails, weakening is sent back, or review has blocking findings: round++. With round ≤ `review.max_fix_rounds` (2), the worker is dispatched on its own thread with a brief listing the failing commands (output tail ≤ 2 KiB each) and the findings. Then commit → verify → publish (push update) → a fresh review.<br>Above the cap → Inbox `review_disagreement` with options:<br>- `another_round` (raises the cap for this run by 1, audited)<br>- `waive(text)` (marks the open blocking findings `waived`; needs text)<br>- `cancel_run` |
| H22 | Comments | After each review, Publisher operations follow, under `automation.publish_comments`:<br>- one summary note (`note:<round>:summary`)<br>- per finding: an **inline discussion only when** (file, new-side line) is an added or context line of a hunk in `git diff base..head` **of that head** (`position_type=text`, `base_sha`, `start_sha`, `head_sha`, `new_path`, `new_line`); otherwise a general discussion quoting `file:line`<br>Every body carries a marker `<!-- orqen:run=<run> round=<n> finding=<id> head=<sha12> -->`. Before posting, Orqen lists notes and discussions and adopts an existing marker. |
| H23 | Write gate (defense in depth) | Every external write is one `external_operations` doc and goes only through `extwrite.Gate`.<br>`Decide(op)` checks in order; the **first match wins**:<br>1. global `ORQEN_EXTERNAL_WRITES=off` or org `paused` → `dry_run` (reason `kill_switch` / `paused`)<br>2. action not supported (`jira_comment`, `jira_transition`: reserved, refused in S4) → `refused`<br>3. env guard: scope `loopback_only` and target host not loopback → `refused_env` (hard; recorded artifact)<br>4. `mode != live` → `dry_run`<br>5. (connection_id, provider_project_id) not in the allowlist → `dry_run` (`not_allowlisted`)<br>6. no write credential for that connection → `dry_run` (`write_credential_missing`)<br>7. action policy manual and no approval with this exact `intent_hash` → `awaiting_approval`<br>8. otherwise → `execute`<br>`Execute` takes a `Decision` that only `Decide` can construct (unexported fields). It **re-runs Decide inside itself** immediately before the transport call. |
| H24 | Env guard | `extwrite.BuildChannel` (string, default `"dev"`) is set to `"release"` only by `make release` (`-ldflags -X orqen/internal/extwrite.BuildChannel=release`). The runtime `ORQEN_ENV` ∈ {dev, test, prod}, default dev.<br>**Scope is `any_host` only when** BuildChannel is release **and** `ORQEN_ENV=prod` **and** `ORQEN_DEV` is not true; otherwise `loopback_only`. `go test` binaries and `make build` are always loopback_only.<br>**Loopback rules:**<br>- **HTTP writes:** the URL host must be `127.0.0.1`, `::1` or `localhost`, **and** the write client's dialer `Control` refuses any non-loopback IP after resolution<br>- **git push:** an IP literal (`127.0.0.1`/`::1`) only<br>- in loopback_only scope the write transport ignores proxies (`Proxy: nil`, `-c http.proxy=`, `NO_PROXY=*`)<br>- redirects are never followed on writes |
| H25 | Live mode | Not a policy key (it must not be overridable). It lives in a separate doc `external_write_settings` per org: `mode` dry_run (default) or live, plus `allowlist[]` and `paused`.<br>Setting live requires `PUT /external-writes` with If-Match and `confirm_phrase == "ENABLE LIVE WRITES"`, plus a non-empty allowlist. Events `external_writes.mode_changed` and `external_writes.allowlist_changed` record it.<br>Allowlist entries must reference an active GitLab connection and an Orqen project's primary repo. |
| H26 | Write credential | A separate Keychain secret per GitLab connection, stored as a ref in `publisher_credentials`. The UI is write-only (K4 pattern). Orqen never creates tokens.<br>On PUT, Orqen compares sha256 against the read credential and refuses equality: 422 `write_credential_same_as_read`.<br>Only `internal/extwrite` reads it, inside `Execute` and the check job. Workers, reviewers, the host env and readers never receive it; the leak test asserts this.<br>**Check job `write_credential_check`** (GET only): `/user`, `/personal_access_tokens/self` (needs `api` or `write_repository`+`api`), and `/projects/:id` for each allowlisted project (`permissions.project_access` or `group_access` access_level ≥ 30). This is the Sprint 2 deferred "push permission check". When the read credential carries `api` scope → warning `read_credential_has_write_scope`. |
| H27 | Idempotency and reconciliation | **Keys and markers:**<br>- `intent_key` (unique per org): `push:<branch>:<head>`, `mr_create:<run>`, `mr_update:<run>:<head>`, `note:<run>:<round>:summary`, `discussion:<run>:<round>:<finding>`<br>- `intent_hash = sha256(canonical exact payload)`<br>- marker `<!-- orqen:run=<run> intent=<intent8> -->`<br>**Execution:**<br>1. fenced: state `executing`, attempts++<br>2. if attempts > 1 or recovering: **reconcile first** (MR by `source_branch` plus marker; notes and discussions by marker; push by `git ls-remote` sha == head)<br>3. write with a 30 s timeout<br>**Outcomes:**<br>- success → `succeeded` with remote ids<br>- definite 4xx → `failed` (401/403 → Inbox `credential_expired`; 409 or lease rejection → H29)<br>- timeout, conn reset or 5xx after send → `outcome_unknown` → reconcile ×3 (2 s, 8 s, 30 s): found → `reconciled`; not found → Inbox `external_write_uncertain` (options `retry_write`, which reconciles again first; `cancel_run`)<br>**Never a blind retry.** |
| H28 | Dry-run continue | With an outcome of `dry_run`, the run **continues as a simulation**:<br>- the op is stored with its exact payload artifact (`extwrite_payload`)<br>- the MR doc gets `mode=dry_run` (no iid; `current_head` = local head)<br>- review runs on the local diff (the push candidate)<br>- comment ops become dry_run artifacts<br>- the run ends at `awaiting_ci` with hold `dry_run`<br>Readiness can never be green in dry-run (check `remote_mr` fails).<br>"Approve & publish" (`POST /runs/{id}/external-operations/approve`) works only when settings are live, allowlisted and env-allowed. It publishes the current head's ops with the user-confirmed intent hashes. Ops of an older head become `superseded`. |
| H29 | Push | Orqen's own git env (Sprint 3 runner: `GIT_CONFIG_GLOBAL=/dev/null`, `-c` flags; **not** the host gitconfig).<br>Command: `git push <explicit https url, no userinfo> <head_sha>:refs/heads/<branch> --force-with-lease=refs/heads/<branch>:<last_pushed_sha or "">` with `GIT_ASKPASS` and the write credential.<br>A lease rejection or non-fast-forward means **someone else pushed**: `mr.head_changed{foreign:true}`. If the remote descends from `last_pushed_sha` and the worktree is clean, the run adopts it (`fetch`, `reset --hard <remote>`, round++, back to verify; `review.on_foreign_push=reverify`, the default). Otherwise → Inbox `policy_conflict` code `branch_rewritten` (options `adopt_remote`, `cancel_run`). |
| H30 | MR create and update | **Create:** `POST /projects/:id/merge_requests` with:<br>- title `Draft: <KEY>: <summary≤200>`, `source_branch`, `target_branch`, `remove_source_branch=false`<br>- description: Jira key and link, change summary from the architect plan goal, an evidence table (command key, outcome, exit, duration, output sha12, `verified_head`), review status, marker<br>The description is scrubbed and holds no local filesystem paths (a test checks this).<br>**Pre-create lookup:** `GET merge_requests?source_branch=<b>&state=all` filtered by the marker.<br>**Update:** `PUT :iid` with description only. Orqen never un-drafts, merges, closes or approves. |
| H31 | CI and readiness | Job `mr_watch` (dedupe `mr:<run>`, every `ci.poll_interval_secs`, default 60) reads:<br>- the MR (`sha`, `state`, `has_conflicts`, `blocking_discussions_resolved`, `diff_refs`)<br>- `GET :iid/pipelines`, then the newest pipeline with `sha == current_head`<br>- that pipeline and its jobs<br>**Pure `mrready.Evaluate`.** Ready iff all hold:<br>- `current_head == verified_head == reviewed_head == pipeline_head`<br>- pipeline `success` and every job with `allow_failure=false` is `success` (pending, running, manual, skipped, canceled, created and unknown are **not** success)<br>- no open blocking finding (waived counts as resolved)<br>- `blocking_discussions_resolved`<br>- no conflicts; MR opened<br>- verification gate pass at that head<br>- review verdict approved or approved_with_scope at that head<br>- independence `independent`<br>- run not stale and not held; mode live<br>**No CI:** `ci.require_pipeline=false` **and** no pipeline exists for the head after `ci.no_pipeline_grace_secs` (300) → the pipeline check passes with detail `no_ci_policy`. A pipeline that does exist must still pass.<br>**Merged-results pipelines** (sha ≠ head) are unsupported in S4 (check fails with `merged_results_pipeline_unsupported`; DEVIATIONS).<br>**State changes:**<br>- ready → `ready_for_merge` plus event `mr.ready{head}` (an announcement only)<br>- any check lost → back to `awaiting_ci` plus `mr.readiness_lost{codes}`<br>- a new head → H29<br>- MR merged → run `merged` (terminal), event `mr.merged{merge_commit_sha}`, workspace cleanup<br>- MR closed → `cancelled` (`mr_closed_by_user`)<br>- waiting longer than `ci.max_wait_secs` (86400) → Inbox `dependency_unavailable` (`ci_timeout`) |
| H32 | Architect gate | When the pinned profile enables the architect, the Sprint 3 `plan_gate` passes through and approval moves to the new `architect_gate`, which shows the real plan: `automation.plan_approval=manual` or risk sensitive → Inbox approval `plan_approval` (code `architect_plan_approval`; approve / request_changes(text) → the architect again on a fresh thread / cancel). With the architect disabled, the Sprint 3 behaviour is unchanged and Orqen writes a single lane `M1.L1` from the plan brief. |
| H33 | Stale mid-run | Sprint 3 `stale` on a run at or after coding → Inbox `ambiguous_behavior` code `acceptance_changed_mid_run`. Options:<br>- `restart_from_context`: cancel live roles, keep the branch and MR, re-triage the same run; coding resumes on the same worktree as round+1<br>- `continue_as_is`: audited<br>- `cancel_run` |
| H34 | Skills | **No download and no global install.** `internal/rolebriefs` embeds Orqen-authored, role-scoped briefs: `architect.md`, `worker.md` (with a systematic-debugging section for fix rounds), `verifier.md` (verification-before-completion: evidence comes from fresh commands), `reviewer.md` (requesting-code-review: acceptance, base/head, scope, no coder history). The version is pinned as `rolebriefs.Version = "s4.1"`, and every role_execution stores the version and the brief sha256. They only shape the brief text; catherd appends its own reply contract. Vendoring obra/superpowers verbatim is deferred to the Sprint 6 extension catalog. |
| H35 | Jira | No Jira write in S4. The action kinds exist but are refused. Merge only records `mr.merged`; knowledge promotion and the Jira reference are Sprint 5. |
| H36 | Census guard | A test (`internal/extwrite/census_test.go`) parses every non-test Go file under `internal/` and `cmd/`. It fails when:<br>- a `"POST"`/`"PUT"`/`"PATCH"`/`"DELETE"` or `http.Method{Post,Put,Patch,Delete}` literal appears outside the allowlisted files: `internal/extwrite/*`, `internal/decision/jev.go` (Jev inference), `internal/codexhost/*` (none expected), server route definitions in `internal/server/app.go`, and `cmd/orqen-fakes`/`internal/testfakes` (fakes)<br>- the string `"push"` is used as a git argument outside `internal/extwrite`<br>Adding a write path therefore forces a review of this list. |

**Recovery rules per stage** (all stage side effects are reconcilable):

| Stage | Committed before external effect | Recovery |
|---|---|---|
| host_session | `host_sessions{state: starting}` | thread exists in doc → `thread/resume`; resume fails → new thread (catherd run unchanged; `run_start` only if `catherd_run_id` is empty, otherwise **never again**) |
| architect, implement, verify (role part), review | rex `dispatching` | H10 reconcile |
| collect | rex `finished` | `result` again (idempotent), reply file re-read |
| commit | none (local) | `git log` contains `Orqen-Run: <run>` at HEAD with the same tree → reuse |
| verify (Orqen part) | none | evidence unique per (run, kind, head, commands_fp) → reuse |
| publish, comment | op `executing` | H27 reconcile-first |
| land | none (catherd refuses a duplicate land on the same commit) | `runs show` ledger has M1 at head → skip |
| ci | none | read-only |

---

## 2. Data model, API, jobs, state machine, events, errors, policy, i18n

**Schema:** `SchemaVersion` stays 2 (additive; K12 precedent). `policy.ProductDefaultsVersion` 2 → 3. New ID prefixes in `ids.go` and the `apiMiddleware` path params: `rex`, `vev`, `fnd`, `xop`, `mrq`, `hss`, `pcr`, plus singleton `xws`.

### 2.1 Collections

| Collection | Key fields | Indexes |
|---|---|---|
| `role_executions` (`rex_`) | org_id, run_id, attempt, stage, round, role, name, lane, brief_artifact_id, brief_sha, rolebrief_version, requested_rung, state (H10), dispatching_at, catherd{run_id, dispatch_id, thread, rung, backend, family, failover_from, attempt, status, reply_status, reply_why, environment, secs, tokens{input,cached,output}, cost_usd\|null, changed_owned[], violations[], exit_reason}, host{session_id, turn_id, directive_id}, host_tokens\|null, reply_artifact_id, result_hints[], created_at, updated_at, revision | unique (org_id, run_id, name, round); (org_id, run_id, created_at); (state, updated_at) |
| `host_sessions` (`hss_`) | org_id, run_id, thread_id, codex_version, model, effort, state starting\|active\|lost\|closed, runner_owner, last_directive{id, tool, args_hash, state sent\|completed\|unknown, turn_id}, catherd_run_id, catherd_run_dir_rel, started_at | unique partial (org_id, run_id){state ∈ starting, active} |
| `coding_slots` (`slot:<n>`) | holder{org_id, run_id, acquired_at} \| null | `_id` only |
| `verification_evidence` (`vev_`) | org_id, run_id, round, kind baseline\|head, base_sha, head_sha, commands_fp, commands[{key, fingerprint, argv, cwd, exit_code, duration_ms, timed_out, sandbox enforced\|best_effort\|none, outcome pass\|fail\|new_failure\|baseline_failure\|incomplete, stdout_artifact_id, stderr_artifact_id, output_sha256}], diff_artifact_id, diff_sha, weakening{suspected, signals[{code, path, detail}]}, verifier{rex_id, verdict, artifact_id}\|null, gate{status pass\|fail\|incomplete, reasons[]}, waiver{text, at}\|null, created_at | unique (org_id, run_id, kind, head_sha, commands_fp); (org_id, run_id, created_at) |
| `review_findings` (`fnd_`) | org_id, run_id, round, review_rex_id, head_sha, finding_key, severity, blocking, file, line, side, location_valid, inline_eligible, title≤200, body≤4000, acceptance_ref, status open\|fixed\|waived\|superseded, waiver{text, at}, publication{op_id, remote_id}, created_at | unique (org_id, run_id, round, finding_key); (org_id, run_id, status) |
| `external_operations` (`xop_`) | org_id, run_id, project_id, connection_id, provider_project_id, action git_push\|mr_create\|mr_update\|mr_note\|mr_discussion\|jira_comment\|jira_transition, intent_key, intent_hash, marker, payload_artifact_id, target{host, project_path, branch, mr_iid}, decision{outcome execute\|dry_run\|awaiting_approval\|refused\|refused_env, reasons[], decided_at, settings_revision, policy_version_id, env_scope}, approval{intent_hash, at, inbox_id, source: dialog\|inbox}\|null, state planned\|dry_run\|awaiting_approval\|executing\|succeeded\|failed\|outcome_unknown\|reconciled\|superseded\|cancelled\|refused, attempts, remote{mr_iid, note_id, discussion_id, pushed_sha, web_url}, error{code, message}, created_at, updated_at, revision | unique (org_id, intent_key); (org_id, run_id, created_at); (org_id, state, updated_at) |
| `merge_requests` (`mrq_`) | org_id, run_id, project_id, connection_id, provider_project_id, mode dry_run\|live, iid, web_url, title, source_branch, target_branch, draft, state opened\|merged\|closed, current_head, last_pushed_sha, verified_head, reviewed_head, pipeline{id, sha, status, source, jobs[{name, status, allow_failure}], fetched_at}\|null, discussions_resolved, has_conflicts, readiness{ready, checks[{code, status pass\|fail\|pending\|stale, detail}], evaluated_at, head}, merged{at, merge_commit_sha}, active, revision | unique partial (org_id, run_id){active:true}; (org_id, readiness.ready, updated_at); (org_id, project_id, updated_at) |
| `external_write_settings` (`"xws_"+org[4:]`) | org_id, mode, allowlist[{connection_id, provider_project_id, host_snapshot, path_snapshot, added_at}], paused, paused_reason, revision, mode_changed_at | unique (org_id) (lazy, If-Match "0") |
| `publisher_credentials` (`pcr_`) | org_id, connection_id, credential_ref, state set\|none, updated_at, last_check{status ok\|refused\|insufficient\|unchecked, username, user_id, scopes[], projects[{provider_project_id, access_level}], checked_at, operation_id}, active, revision | unique partial (org_id, connection_id){active:true} |
| `host_states` (`"hst_"+org[4:]`) | org_id, last_probe{status, codex_version, login, catherd_tools[], fake, checked_at, reasons[]}, spike_attested{at, codex_version, catherd_version, note}\|null, revision | unique (org_id) |

**RunDoc additions:**
- `catherd_run_id`, `host_session_id`
- `heads{base, head, verified, reviewed, pushed, remote}`
- `round`, `fix_cap_extra`
- `verification{evidence_id, head, gate}`
- `review{round, verdict, head, blocking_open, independence{status, coder_families[], reviewer_family}}`
- `merge_request_id`, `publish_mode`, `slot{id, acquired_at}`

### 2.2 State machine (`internal/runstate`)

**New stages:** `host_session`, `architect`, `architect_gate`, `implement`, `verify`, `publish`, `review`, `comment`, `land`, `ci`.

**`StateOfRound(stage, round)`:**

| Stage(s) | State |
|---|---|
| host_session, architect, architect_gate, implement with round 0 | `coding` |
| implement with round > 0 | `fixing` |
| verify | `verifying` |
| publish | `publishing_draft` |
| review, comment, land | `reviewing` |
| ci | `awaiting_ci` (`ready_for_merge` is set by mr_watch) |

**New holds:** `awaiting_coding_slot`, `dry_run`, `external_writes_paused`.

**New transitions** (actor system unless marked):
- queued → coding (dispatcher; guard: eligible, not paused, not stale, slot acquired, host available)
- coding → coding, reviewing → reviewing, awaiting_ci → awaiting_ci (stage changes)
- coding → verifying
- verifying → fixing, verifying → publishing_draft
- publishing_draft → reviewing
- reviewing → fixing, reviewing → awaiting_ci
- fixing → verifying
- awaiting_ci → ready_for_merge, ready_for_merge → awaiting_ci
- awaiting_ci or ready_for_merge → verifying (adopted foreign push)
- awaiting_ci or ready_for_merge → merged
- awaiting_ci or ready_for_merge → cancelled (MR closed)
- needs_input → {coding, verifying, publishing_draft, reviewing, fixing, awaiting_ci} (actor user)
- waiting_network → same set (system)

`Allowed` permits a same-state move for planned, coding, reviewing and awaiting_ci (system).

### 2.3 Jobs (registered in `registerBuiltinJobs`)

| Kind | Dedupe | Max attempts | Notes |
|---|---|---|---|
| `role_watch` | `rolewatch:<run>` | 100000 | polls `runs show` every `roles.poll_interval_secs` (5); re-enqueues itself in the completing transaction while any rex is `dispatched`; marks rexes finished and enqueues `run_advance` |
| `role_cancel` | `rolecancel:<run>` | 20 | H12 |
| `mr_watch` | `mr:<run>` | 100000 | H31; self-re-enqueue with `next_at` |
| `host_probe` | `host_probe:<org>` | 1 | H14 |
| `write_credential_check` | `write_credential_check:<con>` | 1 | H26 |

The scheduler tick (5 s) gains `dispatchCodingSlots()` and the slot sweep.

### 2.4 API routes (hand-authored per PLAN-S2-GO §A.5)

All under `/api/v1/orgs/{orgId}`. Idem = required `Idempotency-Key` (added to `idemRequired`); mutations carry `X-CSRF-Token`.

| Method + path | operationId | Headers | Body → Response / errors |
|---|---|---|---|
| GET `/runs/{runId}/roles` | `list_run_roles` | — | → `RoleExecutionDto[]` |
| GET `/runs/{runId}/verification` | `list_run_verification` | — | → `VerificationEvidenceDto[]` |
| GET `/runs/{runId}/diff` | `get_run_diff` | — | `head?` → `RunDiffDto{base_sha, head_sha, files[{path, status, additions, deletions}], artifact_id, weakening}`; 404 before the first commit |
| GET `/runs/{runId}/findings` | `list_run_findings` | — | `round?` → `ReviewFindingDto[]` |
| POST `/runs/{runId}/findings/{fndId}/waive` | `waive_finding` | If-Match, Idem | `{reason≤2000}` → `ReviewFindingDto`; 409 `finding_not_waivable` (not open or not blocking) |
| GET `/runs/{runId}/external-operations` | `list_run_external_operations` | — | → `ExternalOperationDto[]` |
| GET `/external-operations/{xopId}` | `get_external_operation` | — | → `ExternalOperationDetailDto` (+ `payload` exact JSON, `preview{title, body, diffstat, commits[], refspec, url}`) |
| POST `/runs/{runId}/external-operations/approve` | `approve_external_operations` | If-Match (run), Idem | `{operations:[{id, intent_hash}]}` → `ApproveResultDto{run, operations}`; 409 `external_writes_not_live`, 422 `write_target_not_allowlisted`, 409 `env_guard_refused`, 409 `intent_changed` (+current), 409 `external_writes_paused`, 422 `write_credential_missing`, 409 `run_state_conflict` |
| GET `/merge-requests` | `list_merge_requests` | — | `project_id?, ready?, mode?, limit, cursor` → `Page_MergeRequestDto` |
| GET `/merge-requests/{mrqId}` | `get_merge_request` | — | → `MergeRequestDto` |
| POST `/merge-requests/{mrqId}/refresh` | `refresh_merge_request` | Idem | → 202 `OperationDto` |
| GET `/external-writes` | `get_external_write_settings` | — | → `ExternalWriteSettingsDto{mode, allowlist[], paused, paused_reason, env_guard{build_channel, orqen_env, scope, kill_switch}, revision, mode_changed_at}` |
| PUT `/external-writes` | `put_external_write_settings` | If-Match | `{mode, allowlist[{connection_id, provider_project_id}], confirm_phrase?}` → DTO; 422 `confirm_phrase_mismatch`, 422 `allowlist_invalid` |
| PUT `/external-writes/pause` | `put_external_writes_pause` | If-Match | `{paused, reason?≤500}` → DTO |
| GET `/connections/{conId}/write-credential` | `get_write_credential` | — | → `WriteCredentialDto{state, updated_at, last_check, revision}` |
| PUT `/connections/{conId}/write-credential` | `put_write_credential` | If-Match ("0" lazy) | `{secret}` (writeOnly) → DTO; 422 `secret_invalid`, 422 `write_credential_same_as_read`, 422 `connection_kind_unsupported` |
| DELETE `/connections/{conId}/write-credential` | `delete_write_credential` | If-Match | → DTO |
| POST `/connections/{conId}/write-credential/check` | `check_write_credential` | Idem | → 202 `OperationDto` |
| GET `/host` | `get_host_status` | — | → `HostStatusDto{host, codex_bin, codex_version, min_version, codex_home, login_command, app_server{state, threads}, adapter{available, reasons[]}, last_probe, spike_attested, fake}` |
| POST `/host/probe` | `probe_host` | Idem | → 202 `OperationDto` |
| POST `/host/spike-attest` | `attest_host_spike` | If-Match | `{codex_version, catherd_version, note≤1000}` → DTO; 409 `host_not_probed` |

- `RunDto` gains the RunDoc additions plus `slot_queue_position`.
- `TaskSummaryCountsDto` gains `merge_requests{ready, awaiting_ci, dry_run}`, `external_operations{awaiting_approval, outcome_unknown}`, `coding_slot{run_id|null, queued}`.

**New error codes (`problem()`):** `external_writes_not_live` 409, `write_target_not_allowlisted` 422, `env_guard_refused` 409, `intent_changed` 409, `external_writes_paused` 409, `write_credential_missing` 422, `write_credential_same_as_read` 422, `confirm_phrase_mismatch` 422, `allowlist_invalid` 422, `finding_not_waivable` 409, `host_not_probed` 409, `connection_kind_unsupported` 422.

### 2.5 Events

Payloads hold ids, codes and SHAs only; never text or secrets.

- `role.dispatched{run_id, rex_id, role, name, rung}`, `role.finished{status}`, `role.collected`, `role.reconciled{adopted}`, `role.cancel_requested`, `role.cancelled`
- `host.session_started{thread_id}`, `host.session_lost`, `host.deviation{directive_id}`, `host.probed{status}`, `host.spike_attested`
- `coding_slot.acquired|released{run_id}`
- `run.committed{head}`
- `verification.recorded{head, gate}`
- `review.recorded{round, verdict, blocking}`, `finding.waived`
- `extwrite.decided{op_id, action, outcome, reasons}`, `extwrite.approved`, `extwrite.executing`, `extwrite.succeeded`, `extwrite.outcome_unknown`, `extwrite.reconciled`, `extwrite.failed{code}`
- `external_writes.mode_changed{from, to}`, `external_writes.allowlist_changed{count}`, `external_writes.paused|resumed`
- `write_credential.set|removed|checked{status}`
- `mr.created{iid}`, `mr.head_changed{from, to, foreign}`, `mr.pipeline_updated{status}`, `mr.ready{head}`, `mr.readiness_lost{codes}`, `mr.merged`, `mr.closed`

### 2.6 Policy keys (registry)

Project-overridable unless marked org-only:

| Key | Type | Default |
|---|---|---|
| `ci.poll_interval_secs` | int 15..900 | 60 |
| `ci.no_pipeline_grace_secs` | int 0..3600 | 300 |
| `ci.max_wait_secs` | int 600..172800 | 86400 |
| `verification.allow_baseline_failures` | bool | false |
| `verification.sandbox` | enum enforce, best_effort | enforce |
| `verification.max_output_bytes` | int 1048576..16777216, org-only | 4194304 |
| `roles.max_wall_secs` | int 600..86400 | 7200 |
| `roles.max_dispatches_per_run` | int 4..50 | 12 |
| `roles.poll_interval_secs` | int 2..60, org-only | 5 |
| `host.orchestrator_model` | string ≤128, org-only | "" |
| `host.orchestrator_effort` | enum low, medium, high, org-only | low |
| `host.idle_stop_secs` | int 60..7200, org-only | 600 |
| `host.turn_timeout_secs` | int 30..600, org-only | 120 |
| `publish.commit_author_name` | string ≤100, org-only | Orqen |
| `publish.commit_author_email` | string ≤254, org-only | orqen@localhost.invalid |
| `review.on_foreign_push` | enum reverify, inbox | reverify |

Existing keys used as gates: `automation.publish_draft_mr` (push, mr_create, mr_update), `automation.publish_comments` (notes, discussions), `automation.plan_approval` (architect_gate), `automation.jira_write` (unused; Jira refused), `review.max_fix_rounds`, `review.require_different_model`, `ci.require_pipeline`, `concurrency.coding_runs`.

**Readiness and wizard.**
- **New blockers:** `host_codex_missing`, `host_codex_too_old`, `host_login_missing`, `host_catherd_mcp_missing`, `host_not_probed`, `host_spike_unverified`, `host_backend_unsupported:<b>`.
- **New warnings:** `publishing_dry_run` (shown while mode ≠ live, informational), `write_credential_missing` and `write_credential_unchecked` (only when mode = live and the project is allowlisted), `read_credential_has_write_scope`.
- **Enable automation never requires a write credential or live mode.** Dry-run is fully supported. The wizard is unchanged.

**i18n (fa and en, equal key sets):**
- `taskDetail.tabs.{roles, verification, diff, review, mergeRequest, publishing}`
- `roles.{role.<8>, state.<9>, status.<6>, replyStatus.<5>, rung, actualModel, effort, failover, thread, tokens, cost, unknown, reply, hints}`
- `verification.{gate.<3>, outcome.<5>, sandbox.<3>, baseline, head, weakening.<codes>, verifier.<verdicts>, output, waived}`
- `diff.{files, additions, deletions, open}`
- `review.{verdict.<3>, severity.<4>, blocking, inline, general, locationInvalid, waive, waiveDialog.*, independence.<4>}`
- `mr.{title, simulated, draftNote, heads.{current, verified, reviewed, pipeline}, stale, readiness.check.<codes>, pipeline.status.<9>, ready, notReady, refresh, mergeIsYours}`
- `publish.{mode.{dry_run, live}, banner.*, approveDialog.{title, exactPayload, target, intentHash, confirmCheckbox, approve, dryRunExplain, envGuardExplain}, op.action.<7>, op.state.<11>, decision.reason.<codes>}`
- `externalWrites.{title, warning, modeLive, confirmPhrase, allowlist.*, pause.*, envGuard.*, killSwitch}`
- `writeCredential.{title, separateNote, state.*, check.*, sameAsRead}`
- `host.{title, version, codexHome, login, loginCommand, probe, available, unavailable, reasons.<codes>, spike.*, fake}`
- `overviewPage.{mergeRequests, publishing, codingSlot}`, `run.hold.{awaiting_coding_slot, dry_run, external_writes_paused}`, `run.stage.<10 new>`
- `inbox.question.{role_dispatch_uncertain, usage_limit, role_failed, dispatch_cap, reviewer_not_independent, review_unparseable, test_weakening_suspected, branch_rewritten, ci_timeout, acceptance_changed_mid_run, architect_plan_approval, publish_approval, catherd_land_refused}`, `inbox.reason.publish_approval`, `inbox.option.{retry_dispatch, send_back, accept, another_round, waive, adopt_remote, restart_from_context, continue_as_is, retry_write, approve_all, reject}`
- `readiness.blockers.<7 new>`, `readiness.warnings.<4 new>`, `policy.keys.<16 new>.{label, help}`, `problems.<12 new>`

---

## 3. Frontend

Rules: orval hooks only, wrapped in `web/src/api/hooks/{roles,verification,mergeRequests,externalWrites,host,writeCredential}.ts` (`ifMatch` plus one Idempotency-Key per intent). Every panel has loading, empty, blocked, degraded and failed states. Logical utilities only; `LocaleDirection`/`DirectionProvider` untouched. SHAs, branches, URLs, paths, diffs, argv and payload JSON go in `<Ltr>`/`dir="ltr"` pre; MR body text is `dir="auto"`.

- **`components/write-mode-badge.tsx`**, in the app-shell header on every org page. `DRY-RUN` (neutral, eye-off icon) or `LIVE` (destructive variant, radio icon, plus the target count). Paused shows "Writes paused". The env guard shows "Loopback only". Clicking opens Settings → Publishing. Text and icon always, never colour alone.
- **Task detail new tabs** (`features/tasks/`):
  - `roles-tab.tsx`: a timeline row per rex with role, name, state badge, **actual rung (backend:model#effort from the catherd record)**, family, failover-from, thread (short), secs, tokens (or "unknown"), cost (or "unknown"), reply status and why, hints, and a reply viewer (artifact, monospace, lazy). Host session card: thread id, actual orchestrator model/effort, host tokens.
  - `verification-tab.tsx`: per round, baseline vs head table (key, outcome badge, exit, duration, sandbox badge, output sha12, open output), weakening signals, verifier verdict, gate badge, waiver.
  - `diff-tab.tsx`: file list with +/−; the unified diff from the artifact in a virtualized monospace `dir="ltr"` view (lazy, 2 MiB cap, download link).
  - `review-tab.tsx`: verdict per round, findings list (severity, blocking, file:line LTR, inline or general, location-invalid badge, status), Waive dialog (text required), independence badge (reuse `independence-badge.tsx`).
  - `merge-request-tab.tsx`: MR panel (link or "Simulated (dry-run)"); HEAD badges current/verified/reviewed/pipeline, each with sha7, match tick or **"stale"** badge; readiness checklist per check code with pass/fail/pending/stale and detail; pipeline jobs table; "Merge is yours: Orqen never merges" note; Refresh.
  - `publishing-tab.tsx`: ops list (action, target host/path/branch LTR, state, decision reasons, intent hash short, remote link). The **"Approve & publish"** button opens `approve-publish-dialog.tsx`, which shows:
    - the DRY-RUN or LIVE banner
    - per op: exact payload JSON (`get_external_operation`), a rendered preview (title, body raw pre, diffstat, commits, refspec, URL)
    - a checkbox "I reviewed the exact payload"
    - Approve disabled until checked and the settings allow it
    - in dry-run or env-refused: the reason text and a link to Settings; no approve
    It sends the ids plus `intent_hash`. A 409 `intent_changed` reloads and keeps the dialog.
  - Plan tab: shows the architect plan (plan.md and lanes) when present.
- **Merge Requests page** (`features/merge-requests/merge-requests-page.tsx` and `mr-detail-sheet.tsx`, route `/orgs/:orgId/merge-requests`, nav `to`): infinite table with task key, MR or Simulated, four HEAD badges, readiness, pipeline, blocking count and updated. Filters: project, ready, mode. The sheet opens on the end side.
- **Settings → Publishing tab** (`features/orgs/publishing-tab.tsx`):
  - mode radio with a strong warning card ("Live mode sends pushes, merge requests and comments to the hosts on the allowlist. Development builds can only reach loopback hosts.")
  - selecting live opens a dialog requiring the typed phrase `ENABLE LIVE WRITES`
  - allowlist editor (connection select, then project select from org projects)
  - Pause writes switch
  - env guard read-only panel (build channel, ORQEN_ENV, scope, kill switch)
- **Connections page:** per GitLab connection a "Publishing credential (separate token)" card (`write-credential-card.tsx`) with Set/Replace (reusing `secret-dialog.tsx`), Check, Remove, last check (username, scopes, per-project access) and the note "Create this token yourself in GitLab; Orqen never creates tokens. It is used only by the Publisher."
- **Models → Orchestrator & keys tab:** `host-card.tsx` shows codex version against the minimum, CODEX_HOME path (LTR), login state with a copyable `CODEX_HOME=<path> codex login` (the user runs it), Probe button, adapter available/unavailable with reasons, and spike attest (form; shown only for a real codex).
- **Overview:** `merge-requests-card.tsx` (ready / awaiting CI / simulated), `publishing-card.tsx` (mode, awaiting approval, outcome unknown → Inbox link), `coding-slot-card.tsx` (current run, queue length).
- **SSE `invalidateFor`:**

  | Event prefix | Invalidates |
  |---|---|
  | `role.*`, `host.*`, `verification.*`, `review.*`, `finding.*`, `run.committed`, `coding_slot.*` | `${org}/runs*`, `${org}/tasks*` |
  | `extwrite.*` | runs, `${org}/external-operations*`, `${org}/tasks/summary` |
  | `mr.*` | `${org}/merge-requests*`, runs, summary |
  | `external_writes.*` | `${org}/external-writes`, readiness |
  | `write_credential.*` | `${org}/connections*`, readiness |
  | `host.probed`, `host.spike_attested` | `${org}/host`, readiness |

  Each mapping gets a test in `sse.test.ts`.

---

## 4. Work packages (WP54 to WP72, one committer, strictly in order)

Every WP ends with `make fmt && make check`. Route WPs also run `make openapi` and commit `api/openapi.json`, `web/openapi.json` and `web/src/api/generated/` together. Mongo tests need `scripts/dev-mongo.sh start`. Commit after each numbered step that leaves `make check` green when a WP is large (rate-limit cutoffs).

### WP54: Codex App Server protocol client (no server changes)
- **Files:** `internal/codexhost/{conn.go, methods.go, types.go, directive.go, client.go}`, `internal/codexhost/testdata/schema-0.160.0/` (generated), `internal/codexhost/{schema_test.go, client_test.go}`.
- **Step 1:** `codex app-server generate-json-schema --out internal/codexhost/testdata/schema-0.160.0` (local, no network, no session). Record the codex version in `testdata/VERSION`.
- **Signatures:**
  - `type Conn struct` (JSONL over `io.ReadWriteCloser`; `Call(ctx, method string, params, result any) error`; `Notify`; `Events() <-chan Event`; `ServerRequests() <-chan ServerRequest` with `Respond(id, result)`)
  - `func Dial(ctx, rwc io.ReadWriteCloser, info ClientInfo) (*Client, error)` (`initialize` + `initialized`)
  - `(*Client) StartThread(ctx, ThreadParams) (Thread, error)`, `ResumeThread(ctx, id) (Thread, error)`
  - `RunDirective(ctx, threadID string, d Directive, timeout time.Duration) (ToolCall, error)` (H5 validation; errors `ErrDeviation`, `ErrNoToolCall`, `ErrArgsMismatch`, `ErrOutcomeUnknown`, `*ToolError{Code, Message, Fix}`)
  - `Interrupt(ctx, threadID, turnID)`, `Probe(ctx) (ProbeResult, error)`
  - `directive.go`: `NewDirective(tool string, args map[string]any) Directive{ID, Tool, Args, Text}`, canonical JSON via `internal/canon`
- **Tests:**
  - `schema_test`: every name in `methods.go` exists in the schema's client-request and notification unions; required params of `thread/start` and `turn/start` match the fields Orqen sends
  - `client_test` uses an in-test JSONL peer covering: happy directive; wrong args; extra `commandExecution` item → deviation and interrupt sent; no tool call; tool error object; turn hang → interrupt and `ErrOutcomeUnknown`; approval request for a non-matching tool declined; a stray notification ignored; peer EOF mid-turn → `ErrOutcomeUnknown`
- **Accept:** `go test -short ./internal/codexhost/...`

### WP55: Fake catherd and fake app-server, and catherd port extensions
- **Files:**
  - `internal/catherd/{runs.go (RunRecord, Dispatch, RunsShow), port.go, cli.go, fake.go}`
  - `internal/testfakes/hostfake/{catherd.go, appserver.go, scenario.go, roles.go}`
  - tests `internal/catherd/runs_test.go`, `internal/testfakes/hostfake/hostfake_test.go`
- **Port additions** (only `*OrgEnv`, no secrets):
  - `RunsShow(ctx, env *OrgEnv, runID string) (RunView{Records []RunRecord; Dispatches []Dispatch{Name, DispatchID, Rung, AdmittedAt, Record *RunRecord}}, error)` (`runs show <id> --json --debug`)
  - `RunsCancel(ctx, env, runID, name) error`
  - `ProfileUseRepo(ctx, env, host, name, repoDir string) error` (cwd = repoDir; `profile use <name> --repo --host <h>`)
- `RunRecord` mirrors `RunRecordSchema` exactly (required fields strict, unknown kept in `Extra`). The fixture `testdata/runs-show.json` is hand-written to the schema; risk R2.
- **`hostfake.Catherd`:** in-memory runs and dispatches, with a file-backed variant for the binary (`StateDir`, flock). It implements the run_start, profile_get, route, dispatch, result, peek, cancel, climb, gate_check/gate_pass, land and write_run_file/read_run_file tools, plus `Port.RunsShow/RunsCancel/ProfileUseRepo`.
- **Role behaviour** comes from `Scenario{Architect, Worker func(round int, worktree string) (reply string, edits map[string]string), Verifier, Reviewer func(round int) string}`. Records are written after `RecordDelay`, with actual rung, family and failover per the scenario.
- **`hostfake.AppServer`** speaks the WP54 protocol subset over `io.Pipe`. On `turn/start` it parses the directive, executes it against `Catherd`, and emits `item/started`, `item/completed` (mcpToolCall with `_meta.threadId` recorded) and `turn/completed`.
- **Injection flags:** `NoToolCall`, `WrongArgs`, `ExtraCommandItem`, `HangTurn`, `CrashAfterAdmit` (closes the pipe right after admission), `ToolErr{tool, code}`, `RoleStatus{name, status}`, `FailoverTo{rung}`, `ReviewerFamily`, `MalformedReview`, `WorkerLies` (claims pass, writes a failing change), `WorkerPushAttempt` (runs `git push origin` with the host env in the worktree).
- **Accept:** `go test -short ./internal/catherd/... ./internal/testfakes/hostfake/...`

### WP56: Execution host adapter (supervisor, env, session, reconcile)
- **Files:** `internal/execution/{supervisor.go, env.go, session.go, reconcile.go, errors.go}`, `internal/execution/execution_test.go`.
- **Signatures:**
  - `type Supervisor struct{ Spawn func(ctx, org string, env []string, args []string) (io.ReadWriteCloser, Proc, error); ... }`
  - `(*Supervisor) Client(ctx, org string, cfg HostConfig) (*codexhost.Client, error)` (lazy spawn, flock, idle stop)
  - `BuildHostEnv(rt *catherd.Runtime, org *catherd.OrgEnv, dataDir string, sec catherd.SecretEnv) ([]string, error)`, writing the H3 gitconfig and dirs (0700)
  - `HostArgs(catherdBin string, org *catherd.OrgEnv, cfg HostConfig) []string`
  - `ReconcileDispatch(view catherd.RunView, name string, since time.Time, claimed map[string]bool) (dispatchID string, found bool)`
  - `DispatchSafeToRetry(threadHistory []codexhost.Turn, directiveID string) (safe, known bool)`
- **Tests:**
  - env has exactly the H3 names; no `SSH_AUTH_SOCK`, `CODEX_THREAD_ID` or connection secrets (sentinels set in the test process env); the gitconfig has the pushInsteadOf rules
  - **`WorkerPushAttempt` against `testfakes.GitRepos`:** the push fails and the fake receives **zero** receive-pack requests
  - idle stop kills the process group
  - a second supervisor on the same org home gets `ErrHostBusy`
  - reconcile table: adopt, retry-safe, ambiguous
- **Accept:** `go test -short ./internal/execution/...`

### WP57: Policy keys, runstate extension, rolebriefs, actual independence
- **Files:** `internal/policy/registry.go` (§2.6, `ProductDefaultsVersion=3`, fix `policy_test.go`), `internal/runstate/{states.go, table.go, runstate_test.go}` (§2.2), `internal/rolebriefs/{briefs.go, architect.md, worker.md, verifier.md, reviewer.md, briefs_test.go}`, `internal/catherd/independence.go` (`ActualIndependence(workers, reviewer []RunRecord, cat *Catalog) Independence`).
- **Tests:**
  - every new transition row; no move out of a terminal state except retry
  - `StateOfRound`
  - briefs render deterministically (golden), embed no secret, and pin `Version`
  - `ActualIndependence`: failover onto the reviewer's family → `same_model`; unknown rung → `unknown`; same thread → `unknown` with reason `same_thread`
- **Accept:** `go test -short ./internal/policy/... ./internal/runstate/... ./internal/rolebriefs/... ./internal/catherd/...`

### WP58: Server data model, coding slots, host session, architect stages, role_watch, cancel hook, roles API
- **Files:** `internal/server/{s4_docs.go (Docs + DTOs), db.go (indexes), ids.go, app.go (routes, prefixes, HostFactory/HostSupervisor seam, Config.CodingSlots), coding_slots.go, host_stage.go, role_stages.go (shared dispatch/collect helpers), role_watch_job.go, role_cancel_job.go, roles.go, run_advance_job.go (new stage cases), run_actions.go (cancel hook), intake_scheduler.go (dispatcher + slot sweep), jobs.go}`, `api/openapi.json`, tests `s4_host_test.go`.
- **Shared role helper** (subtle; implement literally):
  ```
  func (s *stageCtx) roleStep(ctx, run, spec RoleSpec) (parked bool, rex RoleExec, err error)
    rex := upsert role_executions{org, run, name, round} $setOnInsert{state: planned, brief…}   // fenced
    switch rex.state:
    case planned, (dispatching && recovering):
      if rex.state == dispatching:               // crash or unknown outcome
        view := Catherd.RunsShow(run.catherd_run_id)
        if id, ok := ReconcileDispatch(view, name, rex.dispatching_at, claimed): adopt → fenced dispatched; return parked
        if !safe(history): fenced rex ambiguous + stop Inbox role_dispatch_uncertain; return
      check paused / dispatch cap / max wall → hold or Inbox
      fenced: rex dispatching (dispatching_at = now) ; host_sessions.last_directive sent
      call, err := client.RunDirective(thread, Directive("dispatch", args), turnTimeout)
      if ErrOutcomeUnknown → return (job retries; next pass reconciles)
      if ToolError → map (H11)
      fenced: rex dispatched{dispatch_id} + enqueueRoleWatch ; event role.dispatched
      return parked
    case dispatched: return parked                // role_watch wakes us
    case finished:
      directive result(run, name) → read replyPath (validated) → store artifact
      fenced: rex collected (+catherd fields) ; event role.collected
      return not parked, rex
    case collected: return not parked, rex
  ```
- **Dispatcher (scheduler tick):**
  ```
  for run in runs{state:queued, active, hold:nil|awaiting_coding_slot, eligibility:eligible, stale:false} sort queued_at:
    if org paused or !hostAvailable(org) → skip (sweep sets project_not_ready)
    txn: free := slot doc with holder nil (count < Config.CodingSlots) and orgHolders < concurrency.coding_runs
         if none → set hold awaiting_coding_slot (once); continue
         slot.holder = run ; casRun queued→coding, checkpoint host_session, slot{} ; enqueueRunAdvance ; event coding_slot.acquired
  ```
- `releaseSlot(tx, run)` is called from `casRun` whenever the new state is not in {coding, verifying, publishing_draft, reviewing, fixing} or `active=false` (one central place).
- **host_session stage:** ensure host (probe status ok), `ProfileUseRepo`, start or resume the thread, directed `run_start` only when `catherd_run_id` is empty, `profile_get` check, then → `architect` (or `implement` when the architect is disabled; Orqen writes lane `M1.L1` from the plan brief via directed `write_run_file`).
- **Architect stage:** directed `route(role:architect)`, then `roleStep`. Validate `plan.md` and lanes (1–3 lanes, single M1, owns vs deny paths), store artifacts `architect_plan` and `architect_lanes` → `architect_gate`.
- **Route:** `list_run_roles`.
- **Tests (fake host and fake catherd via the seam):**
  1. Two queued runs in two orgs with `CodingSlots=1` → exactly one in coding; the other shows `awaiting_coding_slot`; releasing at needs_input lets the second go.
  2. **Crash mid-role:** `CrashAfterAdmit` on the architect dispatch → after restart **one** catherd dispatch, the rex adopted (`role.reconciled{adopted:true}`), the fake records `dispatch` called once.
  3. **Lease loss:** `runHook("architect")` → recovered, one dispatch.
  4. **Host disconnect** while the role runs: the fake app-server is killed → role_watch still sees the record via `RunsShow` → the next collect resumes the thread (`thread/resume` count 1), no re-dispatch.
  5. **Ambiguous receipt:** `HangTurn` on dispatch with no admission and no history → re-dispatched once; with an unreadable history → Inbox `role_dispatch_uncertain`.
  6. `NoToolCall` / `ExtraCommandItem` → `host.deviation`, no dispatch, Inbox after 2 tries (`role_dispatch_uncertain`).
  7. **Cancel** during a live role → `RunsCancel` called, rex cancelled, `cancellation.confirmed` only after the record is terminal; the workspace is kept.
  8. **Host unavailable** (probe failing) → queued run never dispatched; zero `turn/start` calls.
  9. **Pause** → no new dispatch; the live role finishes and is collected; then hold.
- **Accept:** `go test ./internal/server -run 'TestS4Host|TestCodingSlot|TestRole'`

### WP59: Implement stage, commit, failures (climb, budget, timeout), architect gate
- **Files:** `internal/server/{implement_stage.go, architect_gate.go, inbox.go (new reasons/codes, resume stages)}`, tests `s4_implement_test.go`.
- **Steps:**
  - architect_gate per H32
  - per lane in order, `roleStep(worker-<lane>, lane)`
  - after all lanes: H18 commit → `verify`
  - H11 mapping: climb via directed `climb(run, lane, reason)` then a fresh dispatch (round unchanged, attempt++)
  - `E_RUN_BUDGET` → `budget_exceeded`; the dispatch cap; record `limit` → `model_unavailable`
- **Tests:**
  - happy path to commit (HEAD message has `Orqen-Run:`, author from policy)
  - empty change → climb once, then Inbox
  - timeout record → climb
  - budget error → Inbox `budget_exceeded`, slot released
  - a lane owning a deny path → `protected_change` before any worker dispatch
  - commit recovery (crash after commit → reuse; one commit)
  - manual plan approval: Inbox item shows plan artifact refs; request_changes → architect on a fresh thread
- **Accept:** `go test ./internal/server -run 'TestS4Implement|TestArchitectGate'`

### WP60: Verification runner and verify stage
- **Files:** `internal/gitdiff/{parse.go, hunks.go, gitdiff_test.go}`, `internal/verify/{runner.go, sandbox.go, classify.go, weakening.go, verify_test.go}`, `internal/workspace/manager.go` (`AddDetached(ctx, mirror, dir, sha)`, `RemoveDetached`), `internal/server/{verify_stage.go, verification.go (routes list_run_verification, get_run_diff)}`, `api/openapi.json`, tests `s4_verify_test.go`.
- **Signatures:**
  - `verify.Run(ctx, Spec{Dir, Commands []Command, BaseEnv []string, Sandbox Mode, MaxOutput int64, Scrub func([]byte) []byte}) ([]Result, error)`
  - `verify.SandboxProfile(checkout, tmp string, network bool) string`
  - `verify.Classify(head, baseline []Result, allowBaseline bool) Gate`
  - `verify.Weakening(d gitdiff.Diff, globs []string, commands []Command) []Signal`
- **Verify stage order** (H15–H17):
  1. `git diff base..head` artifact
  2. weakening
  3. baseline (cached)
  4. head run
  5. classify
  6. on pass, the verifier role
  7. evidence doc; next stage `publish`, or fix round → `implement`, or Inbox
- **Tests:**
  - **Short:** a timeout kills the process group (a child `sleep` is gone); output cap and scrub (sentinel absent); sandbox denies a write outside the checkout and an outbound connection when `needs_network=false` (skips with a reason when `/usr/bin/sandbox-exec` is absent); classify table; weakening fixtures (deleted test, skip marker, script change, count decrease); hunks: added and context line membership.
  - **Server:**
    - **worker lies** (`WorkerLies`): head command fails → gate fail → fix round 1 dispatched on the same worker thread with the failing output tail; the reply claiming "tests passed" is stored but has no effect
    - baseline fail plus head fail → `baseline_failure` gate fail; with `allow_baseline_failures` it passes
    - `needs_docker` with docker failed → incomplete
    - weakening → Inbox `test_weakening_suspected`; `accept(text)` resumes with a waiver
    - verifier FAIL → fix round; verifier BLOCKED → Inbox
    - the verification checkout is never the coder worktree (path assertion) and is removed afterwards
- **Accept:** `go test -short ./internal/gitdiff/... ./internal/verify/...` and `go test ./internal/server -run TestS4Verify`

### WP61: ExternalWriteGate core, settings, write credential, census
- **Files:** `internal/extwrite/{guard.go (BuildChannel, Scope, LoopbackDialer), decide.go, intent.go, permit.go, census_test.go, extwrite_test.go}`, `internal/server/{extwrite_settings.go, write_credentials.go, write_credential_check_job.go}`, `Makefile` (`release` target with ldflags; `build` unchanged), `api/openapi.json`, tests `s4_extwrite_settings_test.go`.
- **Signatures:**
  - `extwrite.CurrentScope(getenv func(string) string) Scope`
  - `extwrite.Decide(in DecideInput{Op OpView; Settings Settings; Scope Scope; KillSwitch bool; HasCredential bool; PolicyMode string; Approval *Approval}) Decision` (pure, H23 order)
  - `type Decision struct{ outcome string; reasons []string; op string; hash string }` with accessors `Outcome()` and `Reasons()`
  - `extwrite.IntentHash(payload any) string`, `Marker(run, intentKey string) string`
- **Tests:**
  - `Decide` table: every row and its precedence (kill switch beats everything; env guard beats live)
  - `CurrentScope`: dev default; `ORQEN_ENV=prod` with BuildChannel dev → loopback_only; under the test binary, always loopback_only
  - `LoopbackDialer` refuses a hostname resolving to a non-loopback IP (custom resolver hook) and **records zero dials**
  - census test (H36)
  - **Server:** settings PUT live without the phrase → 422; with the phrase and allowlist → events; the allowlist rejects unknown connection or project; write credential equal to the read credential → 422; check job against the fake GitLab (WP62 adds endpoints; here use the existing `/user` and `/personal_access_tokens/self`); the leak test is extended with a write-credential sentinel (absent everywhere except MemoryStore; **absent from the host env** recorded by the WP58 fake).
- **Accept:** `go test -short ./internal/extwrite/...` and `go test ./internal/server -run 'TestS4ExtWrite|TestSecretLeak'`

### WP62: GitLab write adapter, git push, fake GitLab MR/CI/permissions
- **Files:** `internal/extwrite/{gitlab.go (CreateMR, FindMRByMarker, UpdateMRDescription, ListNotes, CreateNote, ListDiscussions, CreateDiscussion), push.go (Push, LsRemote), execute.go (Execute with reconcile-first)}`, `internal/providers/{gitlab.go, iface.go}` (read side: `GetMR`, `ListMRPipelines`, `GetPipeline`, `ListPipelineJobs`, `GetMRDiffs`, `ListDiscussions`), `internal/testfakes/{gitlab_mr.go, gitlab.go, gitrepo.go}`, tests `internal/extwrite/execute_test.go`, `internal/testfakes/gitlab_mr_test.go`.
- **Fake GitLab additions:**
  - `WriteToken` (writes need it; the read token on a write → 403)
  - MR store with marker lookup
  - notes and discussions with position validation against the stored diff refs
  - pipelines (`AutoPipeline` on push → running; `SetPipeline(sha, status, jobs)`)
  - `/projects/:id` with `permissions.project_access.access_level` for the write token
  - `Writes() []RecordedWrite{Method, Path, TokenKind, BodySHA}`
  - `HangAfterWrite map[pathPrefix]int` (performs the write, then holds the connection until the client times out)
- **GitRepos additions:** `http.receivepack=true`, `PushToken` (the read token → 403 on receive-pack), `PushCount()`, `PushAsOther(branch)` (creates a foreign commit), `Merge(iid)` (via the GitLab fake state).
- **Tests:**
  - **Timeout after MR create on the server side** (`HangAfterWrite`) → `outcome_unknown` → reconcile finds it by marker → `reconciled`, **one** MR in the fake
  - push lease: a first push with an absent lease; a second push after `PushAsOther` → rejected → `ErrForeignPush`
  - inline-ineligible finding → discussion without position
  - any write with `Scope=loopback_only` to `https://gitlab.example.invalid` → `refused_env` and zero dials and no git process spawned (runner hook count 0)
  - the push argv holds no secret; `.git/config` unchanged
- **Accept:** `go test -short ./internal/extwrite/... ./internal/testfakes/... ./internal/providers/...`

### WP63: Publish stage, dry-run simulation, approval, external ops API
- **Files:** `internal/server/{publish_stage.go, external_ops.go (routes list/get/approve), merge_requests.go (doc and create), inbox.go (publish_approval)}`, `api/openapi.json`, tests `s4_publish_test.go`.
- **Publish stage pseudo-code:**
  ```
  ops := plan(run): push:<branch>:<head>, then mr_create:<run> (first time) or mr_update:<run>:<head>
  for op in ops (in order):
    upsert external_operations (payload artifact first via storeBlob; intent_hash)   // fenced
    d := Decide(...)
    switch d.Outcome():
      dry_run      → fenced op dry_run; MR doc mode=dry_run (current_head=head); continue
      refused_env, refused → fenced op refused; run Inbox policy_conflict code env_guard_refused; stop
      awaiting_approval → fenced op awaiting_approval; open Inbox approval publish_approval (params: op ids + intent hashes); park
      execute → Execute (H27); succeeded → MR doc live, iid, last_pushed_sha=head, current_head=head
  then → review
  ```
- **`approve_external_operations`** (in `idempotent`):
  - check the run revision
  - each op state ∈ {awaiting_approval, dry_run} with `head == run.heads.head` and the hash matching (else 409 `intent_changed`)
  - re-run Decide with an approval → it must be execute (else the specific 409/422)
  - in one transaction: set approvals, resolve the open `publish_approval` item, clear hold `dry_run`, set checkpoint `publish` (or `comment` for comment ops), `publish_mode=live`, enqueue run_advance
- The generic Inbox answer refuses `approve_all` for `publish_approval` (422 `answer_invalid`, "use the approval dialog"); `reject` → hold `dry_run`; `cancel_run`.
- **Tests:**
  1. **Default settings:** ORQ-1 runs through publish → ops `dry_run`, MR doc `mode=dry_run`, **fake `Writes()` empty and `PushCount()==0`**.
  2. Live plus allowlist (fake 127.0.0.1) plus credential plus manual policy → `awaiting_approval`; Inbox item; approve with a stale hash → 409 `intent_changed`; approve with the correct hash → exactly one push and one MR in the fake; replay of the Idempotency-Key → identical response, no second write.
  3. Live, but the allowlist lacks the project → `dry_run not_allowlisted`.
  4. Kill switch env → `dry_run kill_switch` even with an approval.
  5. Org paused mid-flight (pause between Decide and Execute via a hook) → Execute re-decides → no write.
  6. 401 with the write token → Inbox `credential_expired`.
  7. Restart while `executing` → reconcile first, no duplicate.
- **Accept:** `go test ./internal/server -run TestS4Publish`

### WP64: Review stage, findings, comments, fix rounds, waive
- **Files:** `internal/review/{parse.go, validate.go, review_test.go}`, `internal/server/{review_stage.go, comment_stage.go, findings.go (routes list_run_findings, waive_finding), land_stage.go}`, `api/openapi.json`, tests `s4_review_test.go`.
- **Steps:**
  - review stage: live MR `diff_refs` check (H20) → reviewer roleStep (fresh name `reviewer-M1-r<n>`, no thread) → parse and validate → `ActualIndependence` → findings docs → `comment` (ops per H22 through Decide) → if blocking or verdict changes_requested: round++ → `implement` (or Inbox above the cap) → else `land`
  - land stage: directed `land(run, "M1", what, commit=head, evidence=<evidence summary>, next="awaiting user merge")`; `E_LAND_GATE` → Inbox `review_disagreement` (`catherd_land_refused`) → `ci`
- **Tests:**
  - happy approve
  - blocking finding → fix round 1 (worker thread reused, `thread` arg equals the record thread) → re-verify → re-review with a **new** reviewer name and thread
  - after 2 rounds still blocking → Inbox; `waive(text)` → land
  - `ReviewerFamily` equal to the worker family → Inbox `reviewer_not_independent`
  - `FailoverTo` the reviewer's family → same
  - unknown rung → gate fails, no ready
  - `MalformedReview` → one retry, then Inbox
  - inline vs general eligibility (finding on an unchanged line → general with `file:line`)
  - marker adopt on repost after a crash (one discussion)
- **Accept:** `go test -short ./internal/review/...` and `go test ./internal/server -run TestS4Review`

### WP65: mr_watch, readiness formula, invalidation, foreign push, merge
- **Files:** `internal/mrready/{evaluate.go, evaluate_test.go}`, `internal/server/{mr_watch_job.go, merge_requests.go (routes list, get, refresh)}`, `api/openapi.json`, tests `s4_mrwatch_test.go`.
- **Signature:** `mrready.Evaluate(in Inputs{CurrentHead, VerifiedHead, ReviewedHead string; Pipeline *Pipeline; RequirePipeline bool; NoPipelineSince *time.Time; Grace time.Duration; Now time.Time; BlockingOpen int; DiscussionsResolved, HasConflicts, Opened bool; VerifyGate, ReviewVerdict, Independence string; Stale, Held bool; Mode string}) Result{Ready bool; Checks []Check}`. Check codes: `remote_mr`, `head_match`, `pipeline_head`, `pipeline_success`, `required_jobs`, `blocking_findings`, `discussions`, `conflicts`, `mr_open`, `verification`, `review`, `independence`, `run_state`.
- **Invalidation:** `current_head` ≠ `last_pushed_sha` → H29 path. The new head blanks `verified_head` and `reviewed_head` on the MR doc and sets readiness `stale`. State → verifying (adopt) or Inbox.
- **Tests:**
  - **Pure table:** skipped, manual, pending or canceled pipeline → not ready; a pipeline for an older sha → `pipeline_head` fail; an unrelated sha → fail; no CI with `require_pipeline=true` → fail; `false` plus grace elapsed → pass; `false` but a failing pipeline exists → fail; dry_run → `remote_mr` fail.
  - **Server:**
    - full live flow → pipeline success via `SetPipeline` → `ready_for_merge` and `mr.ready`
    - **second pusher** (`PushAsOther`) → `mr.head_changed{foreign:true}`, readiness lost immediately (the stale badge data), state verifying, re-verify and re-review of the new head, ready again only after a new pipeline for that sha
    - non-descendant rewrite → Inbox `branch_rewritten`
    - merge in the fake → run `merged`, terminal, cleanup enqueued, no write by Orqen
    - MR closed → cancelled
    - `ci_timeout` with a fake clock
- **Accept:** `go test -short ./internal/mrready/...` and `go test ./internal/server -run TestS4MRWatch`

### WP66: Host probe, readiness inputs, summary, docs of contract
- **Files:** `internal/server/{host_routes.go, host_probe_job.go, readiness.go, task_summary.go, server_test.go (TestContract maps; TestContractRefs for new Idem routes)}`, `internal/readiness/evaluate.go` (+ host inputs and warnings), `internal/sysprobe` (`bin.codex` version check uses `MinCodexVersion`), `README.md` (env `ORQEN_ENV`, `ORQEN_CODING_SLOTS`, `ORQEN_EXTERNAL_WRITES`, `ORQEN_CODEX_BIN`, make release, the safety model), `docs/DEVIATIONS.md` (Sprint 4 section).
- **Tests:**
  - probe against the fake app-server → available with `fake:true` and no spike needed
  - real-looking serverInfo without attestation → `host_spike_unverified`
  - login missing → blocker
  - non-codex rung → `host_backend_unsupported:<b>`
  - `publishing_dry_run` warning present by default
  - **enable automation succeeds with no write credential and mode dry_run**
- **Accept:** `go test ./internal/server -run 'TestS4HostProbe|TestReadiness|TestContract|TestTaskSummary'` and `make openapi-check`

### WP67 (web): Write-mode badge, Settings Publishing, write credential card, host card
- **Files:** `web/src/components/write-mode-badge.tsx`, `app-shell.tsx`, `features/orgs/{publishing-tab.tsx, live-mode-dialog.tsx, allowlist-editor.tsx}`, `org-settings-page.tsx`, `features/connections/write-credential-card.tsx`, `features/models/host-card.tsx`, `orchestrator-keys-tab.tsx`, `api/hooks/{externalWrites.ts, writeCredential.ts, host.ts}`, i18n, tests `test/publishing-settings.test.tsx`.
- **Tests:**
  - badge shows DRY-RUN by default and LIVE with text and icon
  - live is impossible without the exact phrase (button disabled; a wrong phrase gives no request)
  - the secret is cleared after save and absent from the DOM
  - host card shows the login command in LTR
  - fa `dir=rtl`
- **Accept:** `pnpm -C web lint && pnpm -C web typecheck && pnpm -C web check:rtl && pnpm -C web test --run && pnpm -C web build`

### WP68 (web): Task detail tabs and Approve & publish
- **Files:** `features/tasks/{roles-tab.tsx, verification-tab.tsx, diff-tab.tsx, review-tab.tsx, merge-request-tab.tsx, publishing-tab.tsx, approve-publish-dialog.tsx, head-badges.tsx}`, `task-detail-page.tsx`, `plan-tab.tsx`, `run-state-badge.tsx` (new holds), i18n, tests `test/task-s4.test.tsx`.
- **Tests:**
  - the roles tab renders the **actual** rung from the record, plus "unknown" for null cost and tokens
  - head badges show `stale` when the heads differ
  - the approve dialog renders the exact payload JSON and sends `{id, intent_hash}` with If-Match and Idempotency-Key
  - approve is disabled until the checkbox is ticked, and in dry-run (an explanation shown)
  - 409 `intent_changed` reloads and keeps the dialog open
  - double click → one fetch
  - SHAs and diff are inside LTR
- **Accept:** as WP67.

### WP69 (web): Merge Requests page, overview cards, SSE, nav
- **Files:** `features/merge-requests/{merge-requests-page.tsx, mr-detail-sheet.tsx}`, `router.tsx`, `sidebar-nav.tsx`, `features/overview/{merge-requests-card.tsx, publishing-card.tsx, coding-slot-card.tsx}`, `overview-page.tsx`, `api/hooks/mergeRequests.ts`, `api/sse.ts`, tests `test/merge-requests.test.tsx`, `sse.test.ts`.
- **Accept:** as WP67.

### WP70: cmd/orqen-fakes and cmd/orqen-fakehost
- **Files:** `cmd/orqen-fakes/main.go`, `cmd/orqen-fakehost/main.go`, `README.md` (fakes section).
- **orqen-fakes:** sets `WriteToken`, `PushToken` and `AutoPipeline`, and prints them. New loopback admin endpoints:
  - `POST /_fakes/gitlab/pipeline {project, sha?, status, jobs?}`
  - `POST /_fakes/gitlab/push-as-other {project, branch}`
  - `POST /_fakes/gitlab/merge {project, iid}`
  - `GET /_fakes/gitlab/writes`
  - `POST /_fakes/gitlab/hang-after-write {path_prefix, count}`
  - `PUT /_fakes/host/scenario` (writes `<fakehost state>/scenario.json`)
- The fake repo gains `scripts/check.sh` (`grep -q orqen-feature src/feature.txt`) and `scripts/lint.sh` (`exit 0`).
- **orqen-fakehost:** `orqen-fakehost app-server --listen stdio:// …` serves the WP55 fake app-server (state dir `$ORQEN_FAKEHOST_DIR`, file-backed catherd). `orqen-fakehost catherd <args>` emulates the catherd CLI subset (`--version` 1.5.0, profile list/show/new/set/validate/use, doctor, catalog list, runs show/cancel, `mcp` unused). It **replaces** the out-of-repo stub catherd for Sprint 4. Orqen points `ORQEN_CODEX_BIN` and `ORQEN_CATHERD_BIN` at it via two tiny wrapper scripts the WP writes under `<data>/bin` at walk time (documented).
- **Accept:** `go vet ./cmd/...`; manual smoke: `go run ./cmd/orqen-fakes`, then curl each admin endpoint; `orqen-fakehost catherd --version`.

### WP71: Verification walk and docs
1. `make check`. Run none of the gated real tests.
2. **Personal-state guard** before, during and after, all identical:
   - `find ~/.config/catherd ~/.local/share/catherd -type f -exec shasum {} + | sort | shasum`
   - `shasum ~/.codex/config.toml ~/.codex/auth.json ~/.codex/hooks.json`
   - `ls ~/.codex/sessions | wc -l`
   - `ls -la ~/.claude/agents ~/Library/LaunchAgents`
   - `security find-generic-password -s dev.orqen.credential`: only `cred_*` accounts
   - `security find-internet-password -s 127.0.0.1`: no new item
3. **Setup:** `make build` (dev channel); `bin/orqen serve` with data dir `orqen_verify_s4`, `ORQEN_CREDENTIAL_STORE=memory`, `ORQEN_ENV` unset; `go run ./cmd/orqen-fakes`; fakehost wrappers. Setup by HTTP script: org, connections, project ORQ, commands `["/bin/sh","scripts/check.sh"]` and `["/bin/sh","scripts/lint.sh"]` approved, profile bound, host probe ok (fake), auto plan and start overrides, enable.
4. **Zero-writes proof with default settings:** ORQ-1 runs to `awaiting_ci` with hold `dry_run`. Then `GET /_fakes/gitlab/writes` → `[]`, the git push count is 0, every `external_operations.state ∈ {dry_run}`, and `lsof -nP -a -p <orqen pid> -i` shows only 127.0.0.1 peers. In the Browser:
   - DRY-RUN badge visible
   - Publishing tab: ops with dry-run reasons
   - MR tab: "Simulated"
   - readiness `remote_mr` failing
5. **Dry-run → approve → recorded push to the FAKE only:**
   - set the write credential (fake `WriteToken`)
   - Settings → Publishing → live (typed phrase) → allowlist fake project 1
   - Task → Publishing → Approve & publish (check payload, checkbox, approve)
   - `writes` shows exactly 1 push, 1 MR create and the comment ops, all with TokenKind `write`
   - replay → no new writes
6. **CI:** `POST /_fakes/gitlab/pipeline` running → MR tab pending → success → `ready_for_merge`, the Ready badge beside the head, and "Merge is yours".
7. **Second pusher:** `push-as-other` → the stale badge appears on the next poll, readiness lost, a re-verify and re-review round in the Roles tab, a new pipeline needed → ready again after success.
8. **Env-guard proof:** add a second GitLab connection `https://gitlab.example.invalid` and allowlist it (API) → its approve answers 409 `env_guard_refused`; no DNS or connect happens (Go test covers the counter; the walk checks `lsof`).
9. **Failure injection via the scenario:**
   - `WorkerLies` → fix round visible
   - `CrashAfterAdmit` plus an orqen restart → one dispatch in `runs show`
   - `HangAfterWrite` on MR create → `outcome_unknown`, then `reconciled`; the MR count is 1
   - `ReviewerFamily` same → Inbox
   - Cancel during a role → cancellation confirmed
10. **Merge** via `/_fakes/gitlab/merge` → run `merged`, no Orqen write.
11. fa and en RTL on Task detail tabs, the MR page and Settings Publishing; keyboard check: dialog focus trap and Escape.
12. Write `docs/VERIFICATION-S4.md`; update DEVIATIONS and README.

### WP72: Gated real tests (written and committed, never run by the coder)
- **Files:** `internal/execution/codex_real_test.go`, `docs/LIVE-TRIAL.md`.
- `TestCodexHostProbeReal` (`ORQEN_TEST_CODEX_HOST=1`, `ORQEN_TEST_CODEX_HOME=<logged-in isolated home>`): initialize, model/list and mcpServerStatus/list only. No thread, no tokens.
- `TestCodexCatherdSpikeReal` (`ORQEN_TEST_CODEX_SPIKE=1`; **spends tokens**): runs in a temp git repo, temp CATHERD_HOME and the isolated CODEX_HOME with a cheap user-chosen rung (`ORQEN_TEST_SPIKE_RUNG`). Steps:
  - one directed `status`
  - `run_start`
  - one worker dispatch writing a file
  - kill the app-server mid-role, restart, reconcile → assert one dispatch, record collected
  - assert the personal-state guard is unchanged
  - assert a worker `git push` to a loopback bare repo is blocked
  - assert catherd's queue attempt reached no personal daemon (delivery receipts `not-submitted`)
- `LIVE-TRIAL.md` is the user's procedure for an optional live trial: `make release`, `ORQEN_ENV=prod`, a sandbox GitLab project the user chooses (**never** git.digikala.com production repos unless the user decides), a self-created write token, typed phrase, allowlist one project, manual approval. It says explicitly that the coder never runs it.
- **Accept:** `go vet ./internal/execution/...`; the tests skip without their env vars.

---

## 5. Definition of Done, risks, needs the user

| Requirement (SPEC §18 Sprint 4 exit / §19, adapted to fake-verified) | Shown by |
|---|---|
| "A real MR in the pilot project" → **a real Draft MR in the fake GitLab**, created only after dry-run → explicit approval; zero writes by default | WP63 tests 1–2, WP71 steps 4–5 |
| "Independent comment" → reviewer on a different actual model family, fresh thread, findings posted inline or as a general discussion with markers | WP64 tests, WP71 step 5 |
| "Latest-head gating" → readiness formula, stale on a new head | WP65 pure table plus second-pusher test, WP71 steps 6–7 |
| "Merge stays with the user" → no merge, close, un-draft or approve code path (census plus fake write log) | WP61 census, WP65 merge test, WP71 step 10 |
| §19 restart mid-coding → existing role found, no second worker | WP58 tests 2–5 |
| §19 timeout after MR create → reconcile, no duplicate | WP62 and WP63 tests |
| §19 someone else pushes after review → Ready invalid; new test, review and CI | WP65 |
| §19 pipeline skipped, unrelated or old → not ready | WP65 pure table |
| §19 worker claims tests passed but did not run them → gate fails | WP60 `WorkerLies` |
| §19 reviewer not independent → refused; no fake badge | WP57, WP64 |
| §19 model unavailable or wrong name → no silent substitution | WP59 (`E_ADMIT_RUNG`/limit → Inbox), H11 |
| §19 profile change during a run → snapshot fixed | WP58: profile bound per worktree at host_session, `profile_get` check |
| §19 catherd outage → coding stops, ingestion continues | WP58 test 8 (host or catherd unavailable) |
| §19 Cancel with a slow process → cancellation separate from termination | WP58 test 7 |
| §16 WriteOutcomeUnknown, 401/403 on write, 429 | WP62 and WP63 |
| §17 write credential separate, never given to roles; secrets never leaked | WP61 leak test, WP56 env test |
| Safety: dev and test builds cannot write to non-loopback hosts | WP61 and WP62 env-guard tests, WP71 step 8 |
| UI fa/en, RTL, DRY-RUN/LIVE indicator, exact-payload approval | WP67–WP69, WP71 step 11 |
| Spike 0 (real host → catherd → roles → recovery) | **Not claimed.** WP72 gated test exists; readiness blocks a real host until it is attested |

**Risks**
1. **App-server protocol drift.** Method names in 0.160.0 may differ from the names used here. WP54's schema test forces a correction and a DEVIATIONS entry; Codex is pinned to ≥ 0.159.2.
2. **catherd record and `runs show` shapes are taken from source, not captured from a real run.** The spike test validates them; the parser keeps unknown fields.
3. **Directed turns depend on the orchestrator model obeying.** Deviations are detected and refused, never acted on. Every directive costs orchestrator tokens (cheap model, low effort; shown as unknown when not reported).
4. **The isolated HOME and CODEX_HOME make roles slower and drop the user's skills, plugins and AGENTS.md from the host tree.** Intentional for safety; only `codex:` rungs work in S4.
5. **The macOS keychain is per user, not per HOME.** A worker deliberately running `security find-internet-password` could reach stored credentials (a GUI prompt is likely). This is mitigated, not eliminated; the spike checks Codex's seatbelt behaviour. Real roles run only in the user's gated tests.
6. **catherd's `codex queue --remote unix://` path resolution** relative to CODEX_HOME is unverified (H2). If it reaches a personal daemon, the push is refused for an unknown thread; the spike confirms this.
7. **`sandbox-exec` is deprecated.** It still works on current macOS; `enforce` fails closed.
8. Merged-results pipelines, multi-milestone plans and parallel lanes are out of S4.
9. A Draft MR prefix only; GitLab versions before 13 are unsupported.
10. `internal/server` keeps growing; decisions stay in pure packages (`extwrite`, `mrready`, `verify`, `review`, `gitdiff`, `runstate`).

**Needs the user**
- **Real Codex host spike:** create the isolated home by running `CODEX_HOME=<data>/codex/<org>/home codex login` yourself, run `TestCodexHostProbeReal`, then (spends tokens) `TestCodexCatherdSpikeReal` with a cheap rung you choose, and attest via the Host card. Until then a real host stays `host_spike_unverified`.
- **Write token:** create one only if and when you decide to; Orqen never asks for or creates one, and dry-run needs none.
- **Live trial:** pick a sandbox or test GitLab project and follow `docs/LIVE-TRIAL.md` with a `make release` build and `ORQEN_ENV=prod`. Never use git.digikala.com or dkjira.digikala.com production unless you decide so explicitly.
- **Approvals:** the commit author identity, the branch scheme, and the defaults (fix rounds 2, CI poll 60 s, `roles.max_wall_secs` 7200, dispatch cap 12, coding slots 1).
- **Merges:** every merge and un-draft is yours.

**Deferred**
- **Sprint 5:** knowledge promotion from merged runs; Jira comment and transition (gate kinds exist but are refused); Confluence and Figma content.
- **Sprint 6:** vendored superpowers skills in the extension catalog; Strix and GitNexus.
- **Later:** a Claude Code host adapter, multi-repo, merged-results pipelines, parallel lanes, auto-merge (never in MVP).

### Critical Files for Implementation
- internal/server/run_advance_job.go
- internal/server/queue.go
- internal/runstate/table.go
- internal/catherd/port.go
- internal/testfakes/gitlab.go
- catherd reference (read-only): /Users/you/node_modules/catherd-cli/src/domain/record.ts, src/infra/dispatch-dir.ts, src/infra/host-context.ts, src/entry/runs-command.ts