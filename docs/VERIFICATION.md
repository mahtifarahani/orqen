# WP16 manual UI verification

> Historical Rust implementation record. The Go migration supersedes its backend paths, commands and dependency choices; see [GO-MIGRATION.md](GO-MIGRATION.md) and [README](../README.md).

Run on 2026-10-13 in the Claude Browser pane (Chromium, 1024x768, dark system theme) against the
built UI served by the Rust server on `127.0.0.1:4280`, fresh database `orqen_ui2`, project-local
Mongo replica set on 27018, `ORQEN_CREDENTIAL_STORE=memory` (no Keychain prompts).

Binary used: item 1 and the listen-address check were run with `target/release/orqen` (built with
`cargo build --release`); the remaining items were run with `target/debug/orqen`, which reads
`web/dist` from disk (same code, same embedded-assets route). Test data was created partly through
the UI and partly with `fetch` calls from the page (same session and CSRF token) to save time;
each item says which.

| # | Item | Result | Notes |
|---|---|---|---|
| 1 | Bootstrap claim works; reloading `/bootstrap` afterwards shows "already set up" | PASS | Claimed through the UI (release binary). The `#token=` fragment is stripped from the address bar. Reload shows "Orqen is already set up". |
| 2 | Orgs Acme (fa) and Beta (en); switching flips `dir`; nav on the right in fa; URL cells LTR | PASS | Acme created through the dialog (language select set to fa); Beta via `fetch`. Acme: `dir=rtl lang=fa`, `font-fa`, sidebar on the right, base URL cell `dir=ltr`. Switching to Beta: `dir=ltr lang=en`, sidebar on the left. Test panel sheet opens on the logical end side (left in fa). |
| 3 | "Jira Main" in both orgs, base `http://127.0.0.1:4280/api/v1/health`; Test streams; overall reachable; download report | PASS | Acme through the UI (stages network/http passed, tls and credential skipped, four stages "Not available yet", overall reachable); Beta via API (status reachable). The report link returns JSON with the exact base URL (`application/json`, 200). The stages arrive in under a second on loopback, so intermediate streaming was only seen as the "running" first stage; ordering and the live feed are covered by `sse.test.ts` and the Overview feed (updated without reload). |
| 4 | Credential set, never shown again; Keychain item; Beta shows `no_api_credential` | PARTIAL | Set through the dialog with a fake value: the input is cleared, the dialog shows "Credential set · date" (Persian calendar), the secret is absent from the DOM and from the connections API. Beta: `readiness.reasons = [no_api_credential, provider_adapter_unavailable]`. NOT VERIFIED: `security find-generic-password -s dev.orqen.credential` (needs the Keychain store; memory store used on purpose). Left as a manual step: `cargo test -p orqen-server --test keychain_test -- --ignored`. |
| 5 | Edit the same connection in two tabs; second save shows the 409 UI | PASS | Edit dialog opened, a second "tab" changed the name via `fetch` with the current `If-Match`, then saving in the dialog gave the "Changed elsewhere" alert listing your value vs latest value; the typed text stayed. "Keep my changes" rebased onto the latest revision and the next save succeeded. |
| 6 | Kill the server mid-test (base `http://10.255.255.1`, timeout 10000), restart; operation completes once; feed resumes | PASS | `kill -9` about 2 s into the test, restarted immediately. After the 30 s lease expired the operation ran again: `succeeded` (overall `unreachable`), exactly one `connection.tested`, two `operation.started`. The open page's indicator returned to "Live" and the feed showed the new events with no reload and no error. |
| 7 | Keyboard: Tab through the shell; dialogs open/close with Esc; focus returns to trigger | PASS | Tab moves through skip link, nav, header controls and table actions with a visible focus ring. Esc closes the test sheet and the edit dialog; `document.activeElement` is the same row action button that opened them (explicit `returnFocusRef`). Not exhaustively tested: every dialog (archive, credential, create-org) uses the same mechanism. |
| 8 | Theme light/dark/system | PASS | Each choice toggles the `dark` class on `<html>` and persists to `orqen.theme`; system follows `prefers-color-scheme`. |
| - | `lsof -nP -iTCP:4280 -sTCP:LISTEN` only `127.0.0.1` | PASS | `TCP 127.0.0.1:4280 (LISTEN)` with the release binary. |
| - | Swagger UI `/api/docs` renders; spec at `/api/v1/openapi.json` | PASS | Rendered in the pane; also covered by `tests/docs_test.rs`. |
| - | UI works under `Content-Security-Policy: default-src 'self'` | PASS | The page, bundled fonts, SSE and all flows above ran under the header the server sends; the pages rendered and worked; the console was not reviewed exhaustively. |

Not covered: mobile widths (the layout stacks the sidebar above the content below `md` but was
not exercised), Safari/Firefox, screen-reader pass, dark-mode contrast audit.

The browser viewport was never emulated (no resize), so there is nothing to reset. All servers
started for this check were stopped; the project Mongo on 27018 was already running and was left
running.
