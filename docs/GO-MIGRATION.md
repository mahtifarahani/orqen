# Go migration

The Go implementation supersedes the Rust Sprint 1 implementation. The React/TypeScript frontend
is retained, including local fonts and the generated orval client. The original planning and UI
verification documents remain historical records; their Rust paths and commands are superseded
by this document and the README.

## Layout and commands

- `cmd/orqen`: CLI (`serve`, `login-link`, `migrate`, `openapi`) and signal handling.
- `internal/server`: HTTP middleware, auth, organizations, connections, credentials, transaction
  helpers and migrations, idempotency, events/SSE, durable jobs, probes and artifacts. Files split
  these concerns within one package; no external service is needed for the worker.
- `api/openapi.json`: authoritative OpenAPI 3.1 contract embedded in the executable. Contract tests
  compare routes, request fields and response fields with the Go implementation. `make openapi`
  exports it and regenerates the existing client; `make openapi-check` checks both for drift.
- `web/assets.go`: embedded release assets; development reads `web/dist` from disk.
- `internal/server/swagger`: vendored Swagger UI assets and license, served without a CDN.
- `go.mod` and `go.sum`: pinned Go dependencies. `make build` produces `bin/orqen`.

`make check` runs formatting checks, `go vet`, Go tests with the race detector, web lint/typechecks,
RTL checks, web tests/build and the OpenAPI drift check. MongoDB integration tests fail if the
replica set is unavailable; `go test -short ./...` runs unit tests without MongoDB.

## Compatibility and invariants

No data conversion or schema reset is required. The migration kept schema version 1; Sprint 2 raised it to 2 (see DEVIATIONS), which makes the old Rust binary refuse the database. Collection names,
BSON field names/types, indexes and ID prefixes match the old server. In particular, the events
counter continues from `counters._id=events, seq`, and BSON timestamps remain MongoDB dates.
Existing sessions and token hashes use the same SHA-256 format.
An opt-in acceptance test (`ORQEN_TEST_LEGACY_BINARY`) was run against the previous Rust release
binary: its organizations, connections, sessions, idempotency responses, event counter and artifact
bytes were successfully reopened/replayed by Go. Idempotency uses the same canonical
JSON/method/route/path hash, stored response fields, lock takeover and expiry semantics.

On macOS, credentials continue to use `dev.orqen.credential` and the existing `cred_*` accounts.
The native Security framework reads and writes raw password bytes, preserving legacy credentials,
Unicode and the 8192-byte API limit. This requires cgo and Apple's developer tools. The Makefile selects the SDK of the active Xcode
toolchain through `xcrun`, avoiding mismatched compiler/SDK installations. For direct `go` commands
on a machine with multiple SDKs, set `SDKROOT=$(xcrun --sdk macosx --show-sdk-path)` if needed. Without cgo,
the memory store works and attempts to use Keychain return `credential_store_unavailable`.
The native Keychain test is opt-in because it can trigger an OS access prompt.

All organization-owned lookups include `org_id`; malformed IDs, archived organizations and foreign
records return 404. User updates still require `If-Match`, while test-result updates do not advance
user revisions. Host, Origin, session, CSRF, body limits, error redaction and request IDs are retained.

MongoDB transactions use snapshot read concern and majority write concern with the official driver's
transaction retry handling. Every job-caused transaction first touches the fenced job document;
acquisition alone advances the fence. Heartbeats detect lease loss, cancellation aborts in-flight
requests, and expired leases can be recovered. Retry exhaustion fails the linked operation. Jobs
have no TTL index. The blob is durably written before its artifact record enters the fenced
transaction. Reports are content-addressed per organization.

SSE uses committed event sequence numbers, supports `after` and `Last-Event-ID`, emits retry/keepalive
frames and polls MongoDB as a fallback. Streams subscribe before querying to avoid lost wakeups.
Connection probes retain exact URL paths, proxy and custom CA settings, redirect host restrictions
and the eight stages. Provider adapter stages remain `not_implemented`, matching Sprint 1.

## Verification scope

Go tests cover public/protected routes, bootstrap and login redemption, origin/CSRF/host checks,
revision conflicts, pagination, credential cleanup, organization isolation, idempotency replay,
concurrent writes and stale claims, event rollback/sequence ordering, SSE resume, artifact isolation,
probe HTTP/TLS behavior, multiple runners, lease fencing, backoff, exhaustion and restart/recovery.
The Keychain roundtrip is a separate opt-in test. No new provider adapter or Sprint 2 feature is
introduced by this migration.

## Web dependency resolution

The existing lockfile was rejected by pnpm's minimum-release-age policy. The minimum compatible
versions of `radix-ui`, `lucide-react` and `@testing-library/user-event` were moved back one release
to 1.8.0, 1.55.0 and 14.6.7 respectively, and the lockfile was regenerated under the same age
policy, without adding exclusions. Fonts, frontend features and styling are retained.
