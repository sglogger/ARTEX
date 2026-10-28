<div align="center">

# ARTEX

Autonomous AI penetration testing system (Go backend + Next.js frontend)
# Issues are welcome
Maintained when time allows; not the original author. Some updates may turn out to be regressions; please report them if you need changes.

🌐 **Live demo**: [https://artex-demo.vercel.app/](https://artex-demo.vercel.app/)

</div>

---

## v0.4.0 update

This release adds background sync history for ScopeSentry, per-item failure retries, recovery for large paginated imports, and raw JSON source tracking. Before a task completes, the remaining work is assessed, and workers save the results they already have during a time-boxed wrap-up phase. The sync and task pages show the corresponding progress, stop reasons, and unfinished items. It also fixes Windows evidence saving, Python tools, and skill path compatibility issues.

For the full list of feature changes, code organization changes, verification scope, and upgrade advice, see the [v0.4.0 Release](https://github.com/sharenjun/ARTEX-main/releases/tag/v0.4.0); for earlier versions, see the [CHANGELOG](CHANGELOG.md).

## Screenshots

> For the full interactive experience, see the [live demo](https://artex-demo.vercel.app/).

| Dashboard (overview / token usage / activity feed) | Task list |
| :---: | :---: |
| ![Dashboard](screenshots/dashboard.png) | ![Tasks](screenshots/tasks.png) |

| Task · execution (conversations / tool calls) | Exploration chain |
| :---: | :---: |
| ![Execution](screenshots/sessions.png) | ![Exploration chain](screenshots/graph.png) |

| Findings | Assets |
| :---: | :---: |
| ![Findings](screenshots/findings.png) | ![Assets](screenshots/assets.png) |

| Asset coverage map (force-directed layout · tested assets highlighted · collapsible nodes) |
| :---: |
| ![Asset coverage map](screenshots/assets_test.png) |

| Traffic recording | Human-in-the-loop conversation |
| :---: | :---: |
| ![Traffic](screenshots/traffic.png) | ![Conversation](screenshots/chat.png) |

| Agent management | LLM configuration |
| :---: | :---: |
| ![Agent](screenshots/agents.png) | ![LLM](screenshots/llm.png) |

| Intercept approvals | Backend logs |
| :---: | :---: |
| ![Intercept](screenshots/intercept.png) | ![Logs](screenshots/logs.png) |


---

## Approval record details

The global "Approval records" page, the in-task "Intercept approvals" tab, and approval cards in conversations can all be expanded to show details. The layout follows
[AegisHook's approval detail component](https://github.com/RuoJi6/AegisHook/blob/main/web/src/components/CallDetail.vue), using ARTEX's own components and theme:


## Asset sync (ScopeSentry)

Asset data can be synced directly from [ScopeSentry](https://github.com/Autumn-27/ScopeSentry), so you do not have to collect it twice:

- On the "**Asset sync**" page, enter the ScopeSentry address and API Key to connect the data source;
- Choose the targets and asset types to sync (domains / subdomains / IPs / ports / sites / endpoints…) by **project** or by **task**;
- Import with one click; assets are merged into the company's asset scope and go straight into the ARTEX asset graph for agents to explore.
- Jobs keep running in the background; after leaving or refreshing the page you can check per-type progress, failure reasons, and source records in the history. Interrupted jobs can be resumed, and failed records can be retried on their own.
- Fetched record counts and distinct asset changes are counted separately, and the raw JSON can be downloaded for each item, so partial success is not mistaken for full completion.

---

## Installation

> Requires a **PostgreSQL** database; exploration needs an **LLM** configured (`ANTHROPIC_API_KEY` or `OPENAI_API_KEY`, or configure it in the UI).

### Release artifacts and platform support

The default Docker Hub image is `dgddw/artex`:

- Stable releases provide both a version tag (e.g. `vX.Y.Z`) and `latest`; pre-releases only push the version tag and never overwrite `latest`.
- Every image tag is a multi-arch manifest containing **Linux AMD64 / ARM64**; Docker picks the host architecture automatically, so `--platform` is not needed.
- `docker-compose.yml` does not pin an architecture either. In `.env`, use `ARTEX_IMAGE` to set a custom repository and `ARTEX_TAG` to pin a version.

```bash
docker pull dgddw/artex:latest
# inspect the architecture manifest of a tag:
docker buildx imagetools inspect dgddw/artex:latest
```

GitHub Release packages are named `artex-X.Y.Z-<os>-<arch>.zip`; each contains the binary with the embedded frontend, the supervisor start script for that OS, `skills`, the example configuration, and the README:

| OS | Architecture | File suffix |
| --- | --- | --- |
| Linux | AMD64 / ARM64 | `linux-amd64.zip` / `linux-arm64.zip` |
| macOS | AMD64 / ARM64 | `darwin-amd64.zip` / `darwin-arm64.zip` |
| Windows | AMD64 | `windows-amd64.zip` |

Each Release includes a `SHA256SUMS` covering these five zips. Download the packages and the checksum file into the same directory to verify them:

```bash
sha256sum --check SHA256SUMS  # Linux; needs all five packages of the same version
shasum -a 256 -c SHA256SUMS  # macOS; needs all five packages of the same version
```

If you only download your own platform, check just the matching line from the checksum file; on Windows, compare `Get-FileHash <package.zip> -Algorithm SHA256` with the corresponding value.

Before releasing, maintainers must set the GitHub repository secrets `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` (with push access to the target repository). The repository variable `DOCKERHUB_IMAGE` can override the default image repository. Pushing a `vX.Y.Z` or `vX.Y.Z-rc.1` tag triggers a release; CHANGELOG must contain non-empty notes for that version, which become the Release body. `doc/` and `docs/` are kept locally only and are not committed; CI rejects commits that track either directory. The pipeline verifies the test layout, Linux database race regressions, Windows compatibility, and the frontend, then builds all five platforms, checks the zips, pushes and inspects the dual-arch image, and creates the Release only after everything passes. Locally, `ARTEX_BUILD_VERSION=vX.Y.Z ./build.sh --release` produces all packages and the checksum file.

### Option 1: one-click install script (recommended)

```bash
git clone https://github.com/Autumn-27/ARTEX.git
cd ARTEX
./install.sh
```

The script detects / installs Docker, then lets you choose **① all Docker** or **② build and run locally**:

- **① All Docker**: enter a Postgres password (press Enter for a random one) → `.env` is written automatically → `docker compose up -d`.
- **② Run locally**: choose a database (connect to an existing one / start one with Docker) → generate `config.json` → build a single binary with embedded frontend using `go` → start.

Once installed, open **http://localhost:8787** (on first visit, `/setup` asks you to set the admin password).

### Option 2: Docker Compose (manual)

```bash
git clone https://github.com/Autumn-27/ARTEX.git
cd ARTEX
cp .env.example .env          # set POSTGRES_PASSWORD, optionally ANTHROPIC_API_KEY
docker compose up -d          # pulls the dgddw/artex image + postgres
# → http://localhost:8787
```

The image already includes common tools (ripgrep/curl/vim/npm/nmap…); `./skills` and `./data` are persisted through bind mounts.

Remote MCP servers can use `http` (Streamable HTTP) or `sse` (legacy SSE) in the system settings.
Legacy SSE servers usually open the event stream with `GET /sse` and then accept JSON-RPC requests at
the `/message?sessionId=...` URL returned by the server; set the URL to `/sse` and fill in the request header as
`Authorization=Bearer <token>`.

### Option 3: download prebuilt binaries (Releases)

Download the zip for your platform from [Releases](https://github.com/Autumn-27/ARTEX/releases); it unpacks to `artex` + `start.sh` (`start.bat` on Windows) + `skills/` + `config.example.json`:

```bash
cp config.example.json config.json   # fill in the database connection
./start.sh                           # → http://localhost:8787
```

> Start with `start.sh` / `start.bat` instead of running `./artex` directly. It is a supervisor script: when the program exits, it decides from the exit code whether to restart it, and **the [one-click update in the UI](#option-1-one-click-update-in-the-ui-recommended) relies on it to swap in the new binary**. If you run `./artex` directly, it will not be restarted after an update.
> To keep it running in the background: `nohup ./start.sh >artex.log 2>&1 &`.

### Option 4: build a single binary from source

```bash
# 1) static frontend export
cd web && npm ci && npm run build:static && cd ..
# 2) copy into the embed directory
cp -r web/out server/webui/dist
# 3) build (only -tags embedui embeds the frontend)
CGO_ENABLED=0 go build -tags embedui -o artex ./cmd/artex
./start.sh
```

### Option 5: build cross-platform Release archives

`build.sh` first builds and embeds the frontend, then strips debug information with the Go linker and packs the release files into zips. Release mode produces zips for Linux amd64/arm64, macOS amd64/arm64, and Windows amd64 by default:

```bash
./build.sh --release
# output: dist/artex-0.3.3-*.zip
```

UPX self-extracting binaries can be incompatible with some Linux kernels, virtualization environments, or security policies, so UPX is off by default. Use `ARTEX_TARGETS` to customize targets; once you have confirmed the target environment is compatible, pass `--upx` explicitly to shrink the binary further:

```bash
ARTEX_TARGETS=linux/amd64,windows/amd64 ./build.sh --release
./build.sh --target linux/amd64 --upx
```

---

## Upgrading

> Upgrades only replace the program, never the data: the Postgres volume `pgdata`, `./data` (jwt.key / SQLite, etc.), and `./skills` are all kept. **Database migrations do not need to be run by hand** — every time `artex` starts it idempotently reruns `schema.sql` (including `ADD COLUMN` / `CREATE INDEX IF NOT EXISTS`), i.e. "restart to migrate". Backing up `./data` and the database before upgrading is still recommended.

### Option 1: one-click update in the UI (recommended)

On the **System settings** page (sidebar "System settings" → `/system/settings`), the **Version and updates** card lets you check for and install new versions without logging in to the server.

After you click "Update": the release package for the current platform is downloaded → checked against the Release `SHA256SUMS` → the new binary is smoke-tested with `-h` → staged as `artex.new` → the program exits and `start.sh` / `start.bat` restarts it to complete the swap. The page waits for the new version to come up and then refreshes.

- **A failure never leaves a broken program**: if verification or the smoke test fails, the staged file is discarded and the current version keeps running; if the swapped-in version fails to start 3 times in a row, it automatically rolls back to `artex.old` (the failed binary is kept as `artex.failed` for troubleshooting).
- **You can roll back at any time**: the previous version is kept as `artex.old`, and the card has "Roll back to previous version". Note that the database schema is not rolled back.
- **Updating interrupts running tasks** — an update is a restart, so do it while idle.
- **Development builds cannot be updated**: updates are disabled when the version is `dev` or a `git describe` string with a suffix, so a release does not overwrite a locally debugged binary.
- **In Docker only the program is replaced, not the image**: toolchains in the image such as playwright / nmap are not upgraded, and after `docker compose up -d` recreates the container it falls back to the version bundled with the image. To upgrade the image too, still use `docker compose pull artex && docker compose up -d artex`.
- If GitHub can only be reached through a proxy, configure the **global proxy** on the same page; the update path uses it. Updates are downloaded only from GitHub domains, and HTTPS is enforced.

### Option 2: one-click update script

```bash
cd ARTEX
./update.sh
```

The script optionally runs `git pull` to fetch the latest code, then lets you choose **① Docker update** or **② local build update** (matching `install.sh`):

- **① Docker**: optionally specify the target image tag (Enter keeps `ARTEX_TAG` from `.env`, default `latest`) → `docker compose pull` → `docker compose up -d` (restarting on the new image migrates automatically).
- **② Local**: rebuild the static frontend → rebuild `./artex` (restart the process afterwards for it to take effect).

### Option 3: Docker Compose (manual)

```bash
cd ARTEX
git pull                       # update compose / scripts (optional)
# pin a version: set ARTEX_TAG=v0.2.0 in .env; latest is used otherwise
docker compose pull artex
docker compose up -d artex     # restart on the new image → schema migrates automatically
docker image prune -f          # remove old images (optional)
```

### Option 4: prebuilt binaries (Releases)

Download the new zip from [Releases](https://github.com/Autumn-27/ARTEX/releases), stop the old process, overwrite `artex` and `skills/` (keep your `config.json` and `data/`), and restart:

```bash
cp -r <unpacked-dir>/skills ./ && cp <unpacked-dir>/artex ./
./start.sh
```

### Option 5: build from source

```bash
git pull
cd web && npm ci && npm run build:static && cd ..
cp -r web/out server/webui/dist
CGO_ENABLED=0 go build -tags embedui -o artex ./cmd/artex
# restart ./start.sh
```

---

## Configuration

**Database** (`config.json`, or override with the `ARTEX_PG_DSN` environment variable):

```json
{
  "database": {
    "host": "127.0.0.1", "port": 5432,
    "user": "artex", "password": "yourpass",
    "dbname": "artex", "sslmode": "disable"
  }
}
```

**LLM**: `export ANTHROPIC_API_KEY=sk-...` (or `OPENAI_API_KEY`), or fill it in on the "LLM configuration" page in the UI.
Optional: `ARTEX_LLM_PROVIDER` / `ARTEX_LLM_MODEL` / `ARTEX_LLM_BASE_URL` / `ARTEX_LLM_PROXY`.

**Concurrency**: the number of work agents per task is configured in "System settings" (default 3).

**Common flags**: `./start.sh -addr :8787 -proxy :8788` (`-addr` for frontend+API, `-proxy` for the traffic recording proxy). The start script passes flags through to `artex` unchanged.

### Reverse proxy deployment (HTTPS / only 443 exposed)

The frontend and the API/SSE are all served from the same backend port (default `:8787`), and the live activity feed uses the **same origin** by default, so **`NEXT_PUBLIC_SSE_BASE` does not need to be set**; expose only 443 publicly and keep 8787 internal.

SSE is a long-lived connection with continuous pushes, so the reverse proxy **must disable buffering**; otherwise the browser connects but never receives events (the activity feed keeps spinning). Nginx example:

```nginx
server {
    listen 443 ssl;
    server_name your.domain.com;
    # ssl_certificate / ssl_certificate_key ...

    location / {
        proxy_pass http://127.0.0.1:8787;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;

        # SSE essentials: buffering off, long timeout, HTTP/1.1
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 3600s;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
    }
}
```

> Set `NEXT_PUBLIC_SSE_BASE` at **build time** only when SSE must use a different origin than the page (e.g. a separate subdomain); the variable is baked into the static bundle during `next build`, so setting it at container runtime has no effect.

---



## Development

### Merging duplicate findings and unified reports

The findings page shows "By finding" by default. Within one task, the reporter groups findings into a folder after confirming from the target, endpoint parameters, permission boundary, root cause, and verification evidence that they are the same real defect; having the same type or name alone is never grounds for automatic merging. Findings with different severities can still be the same vulnerability. A folder's `C/H/M/L` counts are based on the original records, e.g. `0/1/1/1`; distinct-vulnerability statistics use a separately assessed unified severity and show "Awaiting assessment" until it has been assessed. Original submissions, ratings, statuses, reports, and evidence are never overwritten.

Click the arrow to expand the original records, and click the folder title to see the unified report, the original records, and the merge history. When findings are clearly the same defect, the agent merges them and writes the report automatically, without manual approval; when evidence is insufficient, separate reports are kept, and suspected-duplicate suggestions are only for optional later review and do not block report generation. After a correction with "Confirm merge", a unified report is queued automatically; if the reporter is unavailable, the merge is kept and the UI states clearly that the report has not been queued. Once rejected or removed, those relationships are never merged automatically by the agent again. Removing a member does not delete the original finding; when fewer than two remain, the folder is dissolved and its history kept. Finding groups of inherited tasks stay read-only.

New submissions get their own report from the existing reporter, which also checks for duplicates. The first upgrade adds the reporter's merge tools and operating guidance while keeping custom prompts and trigger settings; deleted or disabled reporters are not recreated or enabled. History is not scanned automatically: expand and select original records, click "Organize selected", and the work is queued per task, with run records and failure details available. A folder's details can regenerate the unified report; this needs an enabled reporter with a usable model. Judging and writing consume model tokens; candidate lookup returns paginated summaries first and detailed evidence is read on demand, and targets are never probed again.

After members, reports, or evidence change, the unified report is marked as needing an update, and writes are checked against the version actually read. Exports default to one unified report per group plus the unmerged records, and "Include original sub-reports" can be ticked; when a unified report has not been generated or is stale, the export includes a clear note and all original material. The "Raw reports", "Grouped by task", and "By asset" views remain available. Upgrades use the same idempotent database migration at startup; no separate SQL needs to be run.

"Organize selected" and the organize status after a correction are marked "Completed" only when the agent finished normally, the selected separate reports were actually saved with matching evidence versions, and the affected unified reports were actually saved with matching group versions. Missing or stale reports, model failures, stops, and deleted source tasks all show a failure reason; suspected-duplicate suggestions that have not been reviewed do not affect the completion status.

Development regressions use the overlay runner for the in-package `tests/` directories (a plain `go test ./...` does not load these tests), and the database must be a separate temporary PostgreSQL, never the production database:

```bash
export ARTEX_PG_DSN="postgres://postgres:artex_test@localhost:5432/artex_test?sslmode=disable"
go run ./tests/run.go ./agent ./db ./server -run 'TestFindingCase|TestFindingWorkflow|TestReportFinding|TestRetest|TestFindingGroups|TestDeepenFinding|TestInheritedActivity|TestConversationListRunningState|TestMerge|TestFinalTrigger|TestTaskContext' -count=1
go run ./tests/run.go --all -run '^$' # compile all tests of the main module and norma
cd web && npm ci && npm test && npm run build
```

The GitHub Actions `finding-case-regression` job provides an isolated PostgreSQL, `-race` checks for the paths above, and a frontend build. The results of this verification and pre-existing baseline failures are in [CHANGELOG.md](CHANGELOG.md). CodeGraph is used to check call relationships and change impact; it does not replace regression tests, and the merge judgment and report quality of real models still need separate acceptance on authorized data.

### Manual finding retest

The "Retest" tab in task details lets you page through and select this task's findings, view past verdicts and evidence, and start a retest manually. After starting, the current tab stays open and shows a spinner and "Retesting"; once a fix is confirmed, the finding status is updated accordingly.

Click "Retest" in a row's action area in the findings list, or "Start retest" in the "Finding retest" section of the finding details, and fill in an optional fixed version, test conditions, or limitations; the system creates a separate retest agent conversation and keeps the current page open. The flat list, the by-task grouping, and the asset view all offer this entry point; while a retest runs it shows a spinner and "Retesting", you can click through to the corresponding conversation, and it returns to "Retest" when finished. A retest does not need the original scan task to be restarted. Verdicts are "Still reproducible", "Fixed", or "Inconclusive", and each run's verdict, evidence, and conversation link are kept in the finding details.

On first startup the new backend seeds an editable "Finding retest" (`retester`) agent, whose prompt, LLM, run budget, and tools can be configured in agent management. It uses its bound LLM by default, or the globally active configuration if none is bound. When a retest conversation completes successfully with the verdict "Fixed", the finding's handling status is changed to "Fixed" automatically; running, failed, stopped, or other verdicts keep the original status. Original evidence and reports are always kept. You can also pick "Fixed" manually in the status drop-down. While a finding is being retested, the existing conversation is reused; after a stop, failure, or service restart a new retest can be started.

In this version, retest history is viewed through finding details and conversations; it is not yet included in finding report exports or task archive packages, and is not automatically linked to traffic captures. Demo mode only generates clearly labeled simulated records and never sends requests to real targets.

### Local run and tests

```bash
./dev.sh    # backend (:8787) + traffic proxy (:8788) + frontend next dev (:5173) → http://localhost:5173
```

- Backend: `go run ./cmd/artex` (without `-tags embedui` the frontend is not embedded)
- Frontend: `cd web && npm run dev` (`/api` is proxied to the backend, with hot reload)
- Go main module tests: `go run ./tests/run.go`; main module + norma: `go run ./tests/run.go --all`
- Specific packages / race: `go run ./tests/run.go -race ./sidequestion ./db ./server -run TestSide -count=1`
- Frontend tests: `cd web && npm test` (Node.js 22.6+, using built-in TypeScript type stripping)
- Release script tests: `python tests/build_test.py` (needs Bash; simulates the Go build and actually verifies zip contents and SHA256)
- See [`tests/README.md`](tests/README.md) for test directory and entry point details. Go test files live in each package's `tests` subdirectory and must go through the overlay entry point; a plain `go test ./...` does not load them.
- Mock preview (no backend): `cd web && NEXT_PUBLIC_MOCK=1 npm run dev`

---

## System architecture

ARTEX is an **autonomous penetration testing system driven by multiple LLM agents**: a monolithic Go backend (with the Next.js frontend embedded) + PostgreSQL, with agent capabilities provided by the [`norma`](https://github.com/Autumn-27/norma) SDK (`agentcore` / `tool` / `permission` / `harness` / `memory` / `transcript`). At its core is a **dual-graph architecture**, plus two autonomy mechanisms built around it: **process-level information exchange between workers** and **a planner todo list shared across rounds that keeps attack chains stable**.

### Overall layers

```mermaid
flowchart TB
  subgraph FE["Frontend Next.js (embedded in a single binary via go:embed)"]
    UI["Dashboard · Tasks · Assets · Coverage map · Traffic · Workspace · System settings"]
  end
  subgraph SRV["server (Go net/http)"]
    API["REST /api/* · JWT auth · SSE"]
    ENG["engine scheduling loop"]
    MGR["Manager · task/engine/store lifecycle"]
  end
  subgraph AG["agent (norma SDK)"]
    GO["goals · goal decomposition + scope extraction"]
    PL["planner · (sole intent producer)"]
    WK["worker ×N"]
    MA["mainagent · human in the loop"]
  end
  subgraph DB["PostgreSQL"]
    AGRAPH["Asset graph · assets / companies / task_scope"]
    EGRAPH["Exploration graph · exploration_nodes / anchors / activity"]
  end
  subgraph SUB["Supporting subsystems"]
    PROXY["Traffic recording proxy · MITM + CA audit trail"]
    GUARD["guard / intercept · tool approval gate"]
    ENR["enrich · async DNS / HTTP enrichment"]
    EXT["MCP · skills · memory · report"]
  end

  UI -->|HTTP| API
  API --> MGR --> ENG
  ENG --> PL
  ENG --> WK
  API --> MA
  API --> GO
  PL --> DB
  WK --> DB
  MA --> DB
  GO --> DB
  WK -->|"Bash / HTTP fully recorded"| PROXY
  WK --> GUARD
  WK --> ENR
  PL -.-> EXT
  WK -.-> EXT
  MA -.-> EXT
```

| Layer | Responsibility |
| --- | --- |
| **Frontend** | Next.js static export embedded into the single binary with `go:embed`; visualizes tasks/assets/exploration chains/coverage map and hosts the human-in-the-loop conversation |
| **server** | `net/http` routing + JWT auth + SSE; `Manager` owns the lifecycle of tasks, engines, and DB stores |
| **engine** | One `plannerLoop` + N worker goroutines per task; intent claiming, timeout/pause/drain |
| **agent** | goals / planner / worker / mainagent; `ToolSet` exposes the two graphs as LLM tools |
| **db** | Postgres persistence of the two graphs (pgx); the schema is embedded with `go:embed` and created idempotently on every start |
| **Supporting** | Recording MITM proxy, approval gate, async enrichment, MCP/skills/memory/reports |

### Dual-graph architecture: exploration graph + asset graph

The system splits "**what the target is**" and "**how far it has been tested**" into two independent graphs connected through anchors:

- **Asset graph (global, shared)**: a single source of truth for assets, shared across tasks. Nodes are `root_domain / subdomain / ip / service / app / endpoint` and belong to a company; the domain → subdomain → service → endpoint parent relations and dedup keys are all computed by code, and agents only submit raw information.
- **Exploration graph (per task)**: the "thinking and progress" of one task. Nodes are `goal / intent / fact / finding / hint`, connected into a **lineage chain** by edges such as `spawns / derived_from / yields / proves`, answering "which facts this direction derives from and what it produced".
- **The two graphs are linked through anchors**: `exploration_anchors(node_id, asset_id)` anchors intents/facts/findings to concrete assets, so you can see from an "exploration direction" which assets it targets, and from "an asset" which intents tested it in this task and which facts they produced. This also powers **asset test coverage** and the **asset coverage map** (in-scope assets + tested ones highlighted).

```mermaid
flowchart LR
  subgraph EG["Exploration graph (per task · progress chain)"]
    direction TB
    G["goal"]
    I1["intent A"]
    F1["fact"]
    I2["intent B"]
    FD["finding"]
    G -->|spawns| I1
    I1 -->|yields| F1
    F1 -->|derived_from| I2
    I2 -->|proves| FD
  end
  subgraph AG["Asset graph (global, shared · source of truth)"]
    direction TB
    RD["root_domain"]
    SD["subdomain"]
    SV["service"]
    EP["endpoint"]
    RD --> SD --> SV --> EP
  end
  I1 -. anchor .-> SD
  F1 -. anchor .-> SV
  I2 -. anchor .-> EP
  FD -. anchor .-> EP
```

> Division of labor: the **planner** reads the state of the exploration graph, judges the goal, and only dispatches **intents** into the frontier when there is a new uncovered direction; a **worker** claims **one intent**, executes it with real tools, writes new assets/facts/findings back into the two graphs, and then stops. The asset graph is shared fact; the exploration graph is each task's progress chain.

### Engine and intent lifecycle (the loop of one exploration)

The engine is an **event-driven** loop: any graph change wakes the planner, the planner dispatches intents, workers claim and execute intents and write back, and the write-back triggers the next round — until the goal is proven (`prove_goal`).

```mermaid
sequenceDiagram
  autonumber
  participant EV as Graph change debounce
  participant P as planner
  participant FR as frontier intent queue
  participant W as worker
  participant PX as Recording proxy
  participant DB as Two graphs + activity

  EV-->>P: Wake up
  P->>DB: Read state (graph_overview prefetch + coverage/scope)
  P->>FR: Dispatch 0..N intents (with asset_ids)
  Note over P,FR: Most wake-ups dispatch 0 — no new direction, done
  W->>FR: claimNext claims one intent
  W->>DB: Load the raw assets of the intent's asset_ids as initial information
  W->>PX: Real tool execution (Kali / Bash / HTTP)
  PX-->>W: Response (fully recorded + CA verification)
  W->>DB: Write back fact / asset / finding + per-step activity
  DB-->>EV: Graph change
  EV-->>P: Wake up again (loop)
```

### Process-level information exchange between workers

In a deep exploration, many valuable observations (an error message, a piece of a response, a hidden parameter) show up in one worker's **execution process** without necessarily being written down as a formal fact. To avoid duplicated work and let workers along a chain build on each other, workers can **search the processes of other work items**:

- `search_all_worker_traces(q)`: keyword search across **the execution processes of other work items in this task** (steps of the worker's own intent are excluded automatically); hits carry an `intent_id`;
- `list_worker_traces` / `get_worker_trace(intent_id, step_ids=[…])`: first see which work items have run, then fetch the full content of specific steps of one work item to exchange details.

So even when the exploration graph has no corresponding fact yet, later workers can reuse observations from other workers' processes — **information flows between workers at the granularity of "execution processes"**, while the boundary stays the same (each worker still only does the one intent it claimed).

```mermaid
flowchart LR
  WA["worker A (intent #12)"] -->|"per-step activity"| ACT[("Exploration graph · activity process store")]
  WB["worker B (intent #34)"] -->|"per-step activity"| ACT
  WC["worker C (intent #56)"] ==>|"1) search_all_worker_traces(q)"| ACT
  ACT ==>|"2) hits in A/B steps (own steps excluded)"| WC
  WC ==>|"3) get_worker_trace(id, step_ids)"| ACT
  ACT ==>|"4) full process content returned"| WC
```

### Planner todo list shared across rounds → stable attack chains

Real attack chains are often **multi-step sequences with dependencies** (e.g. find an injection point → obtain credentials → move laterally → escalate privileges), and dispatching all of them in parallel at once only creates chaos. The planner therefore keeps a **planning todo list that is retained per task and shared across wake-ups**:

- The planner is event-driven — it wakes up whenever the graph changes, but **every wake-up is a fresh conversation**; the shared todo list lets it **record a serial exploitation chain once** and then **dispatch intents step by step according to dependencies** over later rounds, instead of laying out the whole chain up front in a single round;
- Each round only dispatches an intent for the next step whose prerequisites are done and whose required facts already exist, and updates the list as progress is made (marking steps satisfied by facts as done).

```mermaid
flowchart TB
  subgraph TODO["Shared todo list (retained per task · persists across wake-ups)"]
    direction LR
    T1["1 injection point · [done]"]
    T2["2 obtain credentials · [in progress]"]
    T3["3 lateral movement · [waiting on prerequisite]"]
    T4["4 privilege escalation · [waiting on prerequisite]"]
    T1 -.prerequisite met.-> T2 -.-> T3 -.-> T4
  end
  R1["Wake-up 1 · dispatch intent ①"] --> T1
  R2["Wake-up 2 (① produced a fact) · dispatch intent ②"] --> T2
  R3["Wake-up 3 (② produced a fact) · dispatch intent ③"] --> T3
```

As a result, attack chains still **advance steadily, without repetition or misordering**, in an "event-driven + stateless conversation" environment — this is what lets ARTEX complete multi-step exploitation chains autonomously.

---

## Community

Scan the QR code to follow the WeChat official account **SecSentry**, then send a private message in the account to join the group.

<div align="center">

<img src="screenshots/wx.png" alt="WeChat official account SecSentry" width="480" />

</div>

---
## References

https://github.com/oritera/Cairn


## License and disclaimer

### License

This project is licensed under the **GNU Affero General Public License v3.0 (AGPL-3.0)**; see the [LICENSE](LICENSE) file in the repository root for the full terms.

This means anyone may freely use, modify, and distribute this project, but **derivative works must also be open-sourced under AGPL-3.0**; in particular, **if you modify this project and provide it to users over a network (e.g. deployed as an online service), you must also make the complete corresponding source code available to those users**.

> ⚠️ **Important**: the license itself does not restrict how the software is used. The "Usage restrictions" and "Disclaimer" below are additional terms and a formal statement from the author to users; please be sure to follow them.

**ARTEX is intended only for personal learning, code study, and local technical validation, and must not be used to run actual tests against any online system or website.**

### Permitted use

- Only for **reading, learning from, and studying the source code of this project**, and for validating technical principles in an **isolated local environment**;
- Suitable for non-offensive purposes such as personal learning, academic research, and code review.

### Prohibited use

- **Using this tool to scan, probe, exploit, or attack any website, online service, or networked system is strictly prohibited** (whether or not authorized, and whether or not the assets are your own);
- Using this tool for any actual penetration test, red-versus-blue exercise, or production environment is strictly prohibited;
- Using this tool for illegal intrusion, data theft, extortion, denial of service, or any destructive or criminal activity is strictly prohibited;
- Using this tool for anything that violates the laws and regulations of your country/region is strictly prohibited.

### Compliance responsibility

Users must comply with all laws and regulations of their country/region on cybersecurity, data protection, and computer crime (in mainland China, including but not limited to the Cybersecurity Law, the Data Security Law, the Personal Information Protection Law, and related judicial interpretations). **All legal liability and consequences arising from the use of this tool are borne solely by the user.**

### Disclaimer

This project is provided "AS IS", without any express or implied warranty. The author and contributors are not liable for any direct or indirect loss, data loss, system damage, or legal dispute caused by the use of this tool (whether used properly or not). **By downloading, installing, or using this project, you confirm that you have read, understood, and agreed to all of the above terms.**
