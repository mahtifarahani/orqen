# Sprint 7 (Go) verification status

Honest status. Only what was actually run is marked as run. All work packages WP111 to WP122 are committed; WP123 is docs and final checks only.

| WP | Scope | Status | Evidence |
|----|-------|--------|----------|
| 111 | pure auto-sweep decisions | done, committed 7f3a8e0 | package tests passed |
| 112 | pure roles-author core, rolebriefs s7.1 | done, committed 992ac4c | package tests passed |
| 113 | readiness K41 warnings | done, committed 8ef8264 | package tests passed |
| 114 | automatic code-index and scan sweeps | done, committed 33caef0 | server tests passed against fakes |
| 115 | security summary counts on tasks | done, committed 0eb029b | server tests passed |
| 116 | roles author sessions, apply, fallback, routes | done, committed d867229 | server tests passed |
| 117 | roles author host execution (fakes) | done, committed b45a20d | 3 new tests; S4/S5 suites passed (549s) |
| 118 | wiki/graph retention, sandbox, policy census | done, committed a235aa6 | 3 new tests plus the census |
| 119 | docs K41 wiring, scheduled source sync | done, committed 7e7e8bc | 2 new tests |
| 120 | server-code translations | done, committed 557d6bc | Go census and vitest golden check |
| 121 | web for S7 features | done, committed 0f7a765 | vitest 179/179, typecheck, check:rtl, build |
| 122 | fakes and walk fixtures | done, committed 9e5ef1d | testfakes tests; TestWalkSetup skips without ORQEN_WALK_URL |
| 123 | browser walk | NOT RUN | see below |
| 123 | docs | done | DEVIATIONS Sprint 7 table, STATUS known gaps, this file |

## Browser walk (coordinator, 2026-10-31, isolated instance, fakes only)

Environment: `make build` binary on 47601, database `orqen_verify_s7` (dropped afterwards), memory credential store, `orqen-fakes` on 47600
with the fakehost wrappers, setup through `TestWalkSetup` with `ORQEN_WALK_S7=1` and live writes to the fake GitLab project only. No
credential was typed. `make check` was green twice in a row on `79c52cb` before the walk.

| Step | Result |
|---|---|
| Run ORQ-1 to publish | Reached the publish approval on its own (intake, triage, plan, coding, verification against fakes). |
| Inbox publish approval | **Bug found and fixed (b4fd359).** The Inbox offered "Approve all", the server answers 422 `answer_invalid` (the exact payload is approved on the task), and the form showed nothing. It now links to the task Publishing tab and shows a `choice` error. vitest case added. |
| Publishing dialog | Pass: exact payload, loopback host, one operation at a time. Both pushes and the MR went only to the fake (`/_fakes/gitlab/writes`: 1 push, 1 MR). A summary note needed its own approval. A recorded approval still reads "Waiting for approval" until every operation is approved; unchanged S4 behaviour. |
| Merge on the fake, roles author | Pass: session `applied` (writer-K1 and kverify-K1 ok), proposal `merge_roles` waiting for review, record `draft`, nothing verified. Knowledge page shows the session, the proposal and the "a person must verify it" text. |
| Persian at 375 px, Knowledge | Pass: rtl, no horizontal overflow, Jalali dates, texts translated. |
| Raw-code scan of ten pages (fa) | No snake_case tokens found; coarse check (innerText regex), lists were empty on most pages. |

Not walked: automatic code index and automatic security scan in the browser (no GitNexus or Strix extension was registered; no credential was typed), the
licence flows, Graph code panel, Skills tabs, Roles-tab skills manifest, Maintenance, restore in the browser. They stay covered by server and vitest tests.
The personal-state hash guard before and after was not run; no personal path was touched by any command of this walk.

## Second browser walk (2026-10-31, fresh isolated instance, fakes only)

| Step | Result |
|---|---|
| Extensions, GitNexus licence flow | Pass: Detect is off until the licence is accepted; a wrong confirmation phrase keeps "Accept license" off; the right phrase plus a note accepts it; register, Detect, Test, Enable, bind proj-1 all work. The Note is required but not marked as required (UX nit, not changed). The three basis radios have the accessible name "on" (label not associated; not changed). |
| **Bug found and fixed (3c15eaa)** | After Test the server reports `usable{ok:false, reason:"disabled"}`; the sheet only allowed Enable when `usable.ok !== false`, so a tested extension could never be enabled from the UI. Now `reason === "disabled"` is allowed; other reasons still block. Two vitest cases. |
| **Fake fixed (6d45bf4, d2d0fd5)** | The fake GitLab branch API returned a synthetic commit that does not exist in the fake git repository, so every code index failed with `index_failed`. With the repositories linked it now returns the real sha of main, and the seed files no longer move it. |
| Graph code panel | Pass after the fake fix: Build index, Ready (6 files), impact query answers, a failed index shows its code and Remove. The panel does not show a stale banner after the fake head moves; staleness is evaluated for a run's context packet, not for the panel. |
| Policy help texts | Translated for all 77 keys in en and fa (756f20e), with a census and a vitest. |

Not walked: Skills tabs, Maintenance, restore in the browser, automatic index and scan in the browser, Strix.

## Third browser walk (2026-10-31, fresh isolated instance, fakes only)

| Step | Result |
|---|---|
| Models, Skills tab | Pass: built-in skills approved, the poisoned fixture is a draft with its lint flags and line numbers; Approve stays off until all three flags are acknowledged one by one, then the version is Approved. "Bind the recommended set" fills Worker, Verifier and Reviewer. |
| Models, catherd Maintenance | Pass: version, binary, isolated home, catalog count, profiles, doctor result. |
| Settings, Maintenance | Pass: support bundle and the restore instructions. The downloaded bundle (816 bytes) holds none of the fixture secrets and no home folder name. |

Still not walked in a browser: restore of a real backup, automatic index and scan, Strix, project Skills override, budget bar of the bindings editor.
`orqen restore` stays tested by Go tests and the S6 CLI walk on a throwaway database.

## Final checks

See the final report for the results of `make check`, `make openapi-check`, the S4 no-diff check and the company-hostname grep.
