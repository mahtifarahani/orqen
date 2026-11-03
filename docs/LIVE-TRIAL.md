# Live trial procedure (optional, for the user only)

**The coder (Claude) never runs any of this.** Sprint 4 was built and verified against in-repo fakes only. Nothing here was run
against a real Codex host, a real GitLab or a real model. Do this yourself, deliberately, when you want to see Orqen work
for real. Every step that spends tokens or writes to a service says so.

## 0. What is checked in CI-style (no tokens, no network)

`make check` runs everything that needs no real service. The two real tests below are **skipped** unless their environment
variables are set:

| Test | Variables | Spends tokens |
|---|---|---|
| `TestCodexHostProbeReal` | `ORQEN_TEST_CODEX_HOST=1`, `ORQEN_TEST_CODEX_HOME`, `ORQEN_TEST_CODEX_BIN` | No (initialize, `model/list`, `mcpServerStatus/list`) |
| `TestCodexCatherdSpikeReal` | `ORQEN_TEST_CODEX_SPIKE=1`, `ORQEN_TEST_SPIKE_RUNG`, `ORQEN_TEST_CODEX_HOME`, `ORQEN_TEST_CODEX_BIN`, `ORQEN_TEST_CATHERD_BIN` | **Yes**, on the cheap rung you choose |

| `TestClaudeCatherdSpikeReal` | `ORQEN_TEST_CLAUDE_SPIKE=1`, `ORQEN_TEST_SPIKE_RUNG`, `ORQEN_TEST_CLAUDE_BIN`, `ORQEN_TEST_CATHERD_BIN`, `ORQEN_TEST_CLAUDE_DATA_DIR`, `ORQEN_TEST_CLAUDE_ORG` | **Yes**, on the cheap `claude-code:` rung you choose |

The Claude spike is the same Spike 0 for the Claude Code host (`internal/claudehost`). The Claude login lives in the keychain under the
name of the organization's `CLAUDE_CONFIG_DIR`, so `ORQEN_TEST_CLAUDE_DATA_DIR`/`_ORG` point at the data directory and organization
that ran `claude auth login`; the catherd home, the repository and the run are in a temporary directory. Instead of killing an
app-server it cuts one `dispatch` turn short and checks that the session history and catherd agree on whether the dispatch happened.
Example rung: `claude-code:claude-haiku-4-5-20251001#default`.

`ORQEN_TEST_CODEX_HOME` is an **isolated** `CODEX_HOME` that you logged in yourself (`CODEX_HOME=<dir> codex login`); never your
everyday `~/.codex`. Run: `go test ./internal/execution -run Real -v -count=1`.

The spike (Spike 0) uses a temporary git repo, a temporary `CATHERD_HOME` and the isolated Codex home. It does one directed
`status`, `run_start`, one worker dispatch that writes a file, kills the app-server mid-role, restarts and reconciles (one
dispatch, one collected record), checks that a worker `git push` to a loopback bare repository is blocked, that catherd's
queue attempt reached no personal daemon, and that your personal state (`~/.config/catherd`, `~/.local/share/catherd`,
`~/.claude/agents`, LaunchAgents, `~/.codex/config.toml`) is byte-identical afterwards.

## 1. Attest the spike in Orqen

After the spike passes, Settings, Publishing, Codex host: **Probe host**, then enter the Codex and catherd versions you used and
**Attest spike**. Until then a real host reports `host_spike_unverified` and no coding run starts. (The fake host used in
tests needs no attestation.)

## 2. A live write trial (writes to a GitLab project you choose)

1. Build the release channel: `make release`. Only the release build may write to a non-loopback host, and only with
   `ORQEN_ENV=prod`. Development and test builds refuse every non-loopback write (`env_guard_refused`).
2. Choose a **sandbox GitLab project** that you own. Do **not** use a production repository (for example git.digikala.com)
   unless you decide that yourself, knowing that Orqen then pushes a branch and creates a Draft merge request there.
3. Create a write token yourself in GitLab (scope `api` is enough; a project access token is best). Orqen never creates tokens.
   It must differ from the read token.
4. Settings, Publishing: enter it as the connection's write credential, **Check** it (GET only), allowlist exactly that one
   project, then **Enable live writes** and type `ENABLE LIVE WRITES`.
5. Let one small task run. At the Publishing tab, read the exact payload (host, project, branch, refspec, commits, MR text)
   and approve each operation yourself. Nothing is written before you approve.
6. Merging stays yours: Orqen never merges, closes, un-drafts or approves a merge request.

To stop writing at any time: Settings, Publishing, **Pause all external writes**, or `ORQEN_EXTERNAL_WRITES=off`.

## Roles knowledge author

The roles author (`knowledge.author=roles`) sends the writer and the knowledge verifier through catherd and a Codex thread of
its own. It is covered here only by your own real-tool checks (`TestCatherdReal` and the Codex spike): the automated tests
drive the in-process fakes, so nobody has watched a real model write the drafts. Everything the roles write is a draft that a
person must verify, and a failed or cancelled session falls back to the deterministic author when learn-on-merge is on.
