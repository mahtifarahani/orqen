<p align="center">
  <img src="web/public/orqen-mark.png" alt="Orqen" width="96" />
</p>

<h1 align="center">Orqen</h1>

<p align="center"><strong>From issue to reviewed change. With you in control.</strong></p>

<p align="center">
  A local workspace for orchestrating AI-assisted software delivery.<br />
  Bring your issues, code, context and approvals into one workflow.
</p>

<p align="center">
  <a href="#what-is-orqen">What is Orqen?</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#get-started">Get started</a> ·
  <a href="docs/DEVELOPMENT.md">Developer guide</a>
</p>

> **Early development:** The delivery workflow is implemented and tested against local simulated services. Real-service and real-tool validation is still pending. See [project status](docs/STATUS.md) and the [live trial guide](docs/LIVE-TRIAL.md) before connecting a real project.

## What is Orqen?

AI can write code. Delivering a useful change also takes context, a plan, review, verification and a decision about what to publish.

**Orqen brings those steps together.** It is a local control plane that coordinates AI tools around a task: read an issue, gather project knowledge, prepare a workspace, guide implementation, collect review evidence and prepare a draft merge request for your approval.

It is built for developers who want to follow the whole delivery process from one place. You can see where a task stands, inspect its evidence and answer the questions that need your judgment.

The application runs on your machine, with a browser UI, a Go server and MongoDB for persistent state. Connected providers and AI tools may communicate with external services when configured.

## Why Orqen?

| What you need | What Orqen brings together |
| --- | --- |
| A clear starting point | Jira intake, issue snapshots, triage and project mappings |
| Context that follows the task | Repository context, Confluence sources, versioned knowledge and dependency graphs |
| A workspace for each change | Managed Git worktrees and a staged execution workflow |
| A visible review process | Separate implementation and review roles, verification results and readiness checks |
| Control over publishing | An approval Inbox, dry-run previews and explicit gates for GitLab writes |
| Continuity between sessions | Persistent jobs, task events, artifacts, backups and restore tools |

### Your tools, coordinated

Orqen includes adapters for **Jira**, **GitLab** and **Confluence**, a **Codex App Server** host adapter, and **catherd** profiles and role orchestration. Optional extension paths cover knowledge indexing, code graphs and security scans through tools such as OpenClaw Wiki, GitNexus and Strix.

External tools must be installed and configured separately. Some extensions have additional license requirements; see the [developer guide](docs/DEVELOPMENT.md). These integrations still need real-tool validation.

### A workspace you can read

The React interface brings tasks, approvals, project knowledge, connections and execution evidence into a single workspace. **English and Persian**, right-to-left layouts, and light and dark themes are supported.

## How it works

1. **Connect a project.** Configure your Jira and GitLab connections, project mappings and delivery policies.
2. **Bring in a task.** Orqen snapshots an eligible Jira issue and gathers the context needed to evaluate it.
3. **Review the plan.** Triage and planning stages surface missing information and route decisions to your Inbox.
4. **Prepare the change.** A managed worktree gives the configured AI host and roles a place to work. Verification and independent review collect evidence about the result.
5. **Decide what ships.** Inspect the evidence and approve publishing when ready. The workflow can prepare a draft GitLab merge request; merging remains your decision.

## Built around explicit control

- **Local application state.** The server listens on loopback, with MongoDB and artifacts stored locally.
- **Dry-run publishing by default.** Live GitLab writes require a release build, explicit activation, an allowlisted project, a separate write credential and operation approval.
- **Credentials through the OS store.** The default credential configuration uses macOS Keychain. Secrets are entered through write-only API operations.
- **Task-scoped execution.** Managed worktrees and explicit process environments separate task execution from your personal checkout and configuration.
- **Evidence you can inspect.** Job state, events, artifacts and review results make the workflow traceable. Optional security scans report incomplete runs rather than treating them as successful checks.

These are implemented controls, with current validation limits documented in [project status](docs/STATUS.md).

## Get started

The development setup is oriented toward **macOS**. You will need:

- Go **1.25+**, Node.js **22+**, pnpm and Git **2.30+**.
- MongoDB with `mongod` and `mongosh`, running as a replica set. The included script starts a local single-node replica set.
- Apple developer tools and cgo for the macOS Keychain build.

```sh
git clone https://github.com/mahtifarahani/orqen.git
cd orqen

# Start the local database.
scripts/dev-mongo.sh start

# Install and build the web interface.
pnpm -C web install
make web-build

# Start Orqen.
make dev-server
```

Open the one-time bootstrap link printed by the server and select **Claim this Orqen**. The UI and API run at **http://127.0.0.1:4280**. The link is also saved to `.orqen-dev/data/bootstrap-url.txt` and expires after one hour.

You can explore the interface and simulated integrations before configuring real services. The [developer guide](docs/DEVELOPMENT.md#manual-verification-with-fake-upstreams) explains the local fixtures; the [live trial guide](docs/LIVE-TRIAL.md) covers the checks required for real tools.

### Build from source

```sh
make build
ORQEN_DATA_DIR=.orqen-dev/data ./bin/orqen serve
```

The binary embeds the web interface; MongoDB remains a separate runtime dependency. Configuration options are listed in [`.env.example`](.env.example). Environment variables are read directly; `.env` files are not loaded automatically.

For frontend hot reload, run `make dev-web` in a second terminal and open http://localhost:5173.

## Explore the project

| Guide | What you will find |
| --- | --- |
| [Developer guide](docs/DEVELOPMENT.md) | Setup details, simulated integrations, API generation and verification commands |
| [Product specification](docs/SPEC.md) | Product goals, workflows and system design |
| [Current status](docs/STATUS.md) | Implemented capabilities and remaining validation gaps |
| [Live trial guide](docs/LIVE-TRIAL.md) | Procedures for validating real tools and services |
| [Release checklist](docs/RELEASE-CHECKLIST.md) | Build, packaging and recovery checks |
| [Implementation decisions](docs/DEVIATIONS.md) | Deliberate differences from the plans |

To check a development change, run `make check`. It covers Go formatting, vet and race tests, web lint and type checks, RTL checks, frontend tests, the web build and OpenAPI drift. Integration tests require the local MongoDB replica set.

---

**Orqen چیست؟** یک محیط محلی برای هماهنگ‌کردن فرایند توسعه با ابزارهای هوش مصنوعی است: از دریافت تسک و جمع‌آوری زمینه، تا برنامه‌ریزی، اجرای کد، بازبینی و آماده‌کردن درخواست ادغام. وضعیت کار و شواهد را یک‌جا می‌بینید و تصمیم انتشار در اختیار شماست. رابط فارسی و انگلیسی دارد و فعلاً در مرحلهٔ توسعه و اعتبارسنجی با سرویس‌های شبیه‌سازی‌شده است.
