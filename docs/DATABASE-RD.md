# Database R&D for Orqen

Date: 2026-10-13. Recommendation: keep MongoDB for the current local product and MVP; do not add Redis now. This is an architecture assessment, not a throughput benchmark or a full correctness audit.

## Basis

Reviewed SPEC sections on packaging, workflow, knowledge, graph, storage, recovery and the team roadmap; PLAN decisions D6–D21 and D26; PLAN-S2 storage/index designs, provider limits and policy/profile publishing. Inspected Rust transactions, indexes, event counters/SSE and job acquisition/fencing, plus the in-progress Go database and event implementations currently present in the workspace. Existing uncommitted work was not modified.

The planned load is one coding run globally, one Wiki writer per vault, separately limited discovery/polling, and a local modular monolith. Four background worker slots are not a promise of four concurrent coding runs. No measured query latency, event volume, database resource usage or team workload was available to establish a capacity limit.

## Fit by workload

| Workload | Assessment | Action |
|---|---|---|
| Provider metadata, catherd profiles, immutable configuration snapshots | Good document fit | Keep bounded documents and explicit schema versions |
| Workflow state, operations, idempotency, durable stage jobs | Suitable with transactions and conditional writes | Keep state and job/outbox intent in the same database transaction |
| Audit events and reconnectable SSE | Suitable at the planned local scale | Persist events; notifications only wake readers |
| Knowledge versions/provenance | Suitable for metadata and bounded claims | Keep large content in the existing artifact store |
| Project graph | Suitable for scoped adjacency and bounded exploration | Measure traversal workloads before choosing another store |
| Reporting across org/project/run/evidence/usage | More natural in a relational model as requirements grow | Reassess PostgreSQL when this becomes a dominant requirement |

MongoDB supports multi-document transactions on replica sets. It also offers embedding and references; the correct choice depends on access patterns. Independent versions/events/jobs should remain separate records, while bounded configuration snapshots can be embedded. [Transactions](https://www.mongodb.com/docs/manual/core/transactions/), [data modeling](https://www.mongodb.com/docs/manual/data-modeling/).

MongoDB is viable, but it is not uniquely necessary. For a fresh team control plane dominated by relationships, integrity constraints and cross-entity reports, I would lean toward PostgreSQL with relational columns plus JSONB for provider/configuration payloads. PostgreSQL has foreign-key constraints and indexed JSONB. That is a design preference, not evidence that MongoDB cannot implement Orqen. The current implementation and local scope do not justify migration now. [Constraints](https://www.postgresql.org/docs/current/ddl-constraints.html), [JSON types](https://www.postgresql.org/docs/current/datatype-json.html).

## Concrete issues and recommendations

1. **Global event counter serializes event-writing transactions.** Rust `events/writer.rs` calls `counters::next(..., "events")` inside each transaction; Go `AppendEvent` uses the same shared record. This provides a simple commit-ordered cursor when all writes participate in the transaction, but creates contention across organizations. Keep it for MVP, batch related domain changes into short transactions and measure conflict/retry rates. If contention becomes material, consider per-org sequences with a corresponding index/cursor migration. Do not replace sequences with wall-clock timestamps or ordinary preallocated IDs without proving replay cannot miss late commits. Redis would not remove this MongoDB write bottleneck.

2. **Policy numbering has an incorrect retry assumption.** PLAN-S2 WP21 says `max+1` with a unique index retries through `with_txn`. Rust `db/txn.rs` retries transaction-body errors only when they carry `TransientTransactionError`; an ordinary duplicate-key error is not explicitly handled. Allocate a per-scope number using an atomic counter increment inside the transaction, or implement bounded retry of this specific version-number conflict outside the aborted transaction. Keep the unique index. Test concurrent publishing and stale draft revisions separately. Profile publishing already plans a `next_number` field; ensure increment and version insertion share a transaction. Driver retry semantics distinguish whole-transaction transient retries from uncertain-commit retries. [Transaction error handling](https://www.mongodb.com/docs/manual/core/transactions-in-applications/).

3. **Keep external work outside database transactions.** Jira/GitLab requests, model calls, catherd processes, Wiki work and file writes can take seconds or minutes. Transactions should only claim/finalize stages and commit bounded state, events and intent records. The default transaction lifetime is less than a minute and conflicting document writes can abort a transaction. Existing blob-first/fenced-record-finalization is the right direction. [Production considerations](https://www.mongodb.com/docs/manual/core/transactions-production-consideration/).

4. **Bound uncertain-commit handling without blindly rerunning effects.** Rust `with_txn` has a limit on whole-transaction attempts but an unbounded `UnknownTransactionCommitResult` commit loop. Give it an operational deadline/backoff and surface an uncertain outcome for reconciliation. A deadline must not be interpreted as proof of rollback. Keep remote intent/receipt reconciliation before retrying MR/comment creation; database fencing alone cannot prevent a stale process from making an external API call.

5. **TTL is cleanup, not an exact timer.** Preserve explicit expiry checks for sessions/tokens and lease queries. Session reads already check `expires_at`; jobs correctly have no TTL deletion. Apply the same rule to future caches and retention. MongoDB TTL cleanup runs in the background and can lag expiry. [TTL indexes](https://www.mongodb.com/docs/manual/core/index-ttl/).

6. **Use query evidence to tune queue/catalog indexes.** Current queue indexes cover `(state,next_at)` and `(state,lease_expires_at)`; acquisition additionally filters kinds and sorts by `next_at`. Run execution-stat explains on both due jobs and expired leases with a realistic backlog before changing indexes. Candidate indexes including `kind` must be evaluated against that actual query, not added by guesswork. Measure catalog search separately from its listing cursor index.

7. **Bound document growth and complete recovery work.** Never append all run events/logs/evidence into a single growing run document. BSON documents have a 16 MiB maximum; large bodies belong in the artifact store already specified. Backup/restore must coordinate database references and artifact manifests; orphan GC requires a grace period and reference checks. A one-member replica set provides transaction capability but no redundant database member or failover. It fits the accepted local failure model; team availability requires a separately designed deployment. [Document limits](https://www.mongodb.com/docs/manual/reference/limits/), [replica-set development deployment](https://www.mongodb.com/docs/manual/tutorial/deploy-replica-set-for-testing/).

## Redis and other services

| Service | Needed now? | Add only when |
|---|---|---|
| Redis cache | No | Repeated expensive reads remain a measured bottleneck after query/index tuning; cached data is rebuildable |
| Redis shared rate limits | No | Multiple API/worker processes need a shared per-connection concurrency/rate budget; current in-process limiter cannot coordinate them |
| Redis Pub/Sub wakeups | No | Multiple processes need faster wakeups than database polling; MongoDB replay remains authoritative |
| Redis Streams job transport | No | Measured queue throughput/latency or delivery requirements justify a separate transport and its recovery machinery |
| Vector/search service | No | Retrieval evaluation shows the planned metadata/Wiki fallback misses required results or latency goals |
| Graph database | No | Deep traversal workloads exceed the bounded graph queries and code-index adapter's capabilities |
| Object storage | No | Multiple hosts need shared artifact access; local content-addressed files stop fitting deployment needs |

Redis Pub/Sub uses at-most-once delivery: disconnected consumers can lose messages. Streams support persisted history and consumer groups with at-least-once delivery, but still need idempotent consumers and suitable persistence configuration. If a separate transport is introduced, publish to it from a durable MongoDB outbox; do not independently write domain state to MongoDB and enqueue to Redis as if those two writes were atomic. [Pub/Sub](https://redis.io/docs/latest/develop/pubsub/), [Streams](https://redis.io/docs/latest/develop/use-cases/streaming/).

## Verification before revisiting the decision

Measure transaction latency/retries, unknown commit outcomes, queue acquisition latency/backlog age, documents examined per returned result, event replay delay, and MongoDB memory/disk growth. Exercise two orgs, concurrent policy publishes, expired-lease takeover, process restart and remote-write uncertainty. Test at expected workload plus an explicitly chosen growth margin; agree on acceptable product latency first. These are proposed checks, not checks executed by this research.

The immediate work is transaction/recovery correctness, the WP21 numbering correction, query measurement, and backup/restore. Adding Redis or migrating databases before these steps would increase operational work without resolving the identified problems.
