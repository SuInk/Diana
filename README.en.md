<div align="center">

# Diana

**A self-hosted multi-platform AI assistant — OneBot v11 and Telegram online at the same time, with your data staying on your own machine.**

[![CI](https://github.com/SuInk/Diana/actions/workflows/ci.yml/badge.svg)](https://github.com/SuInk/Diana/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/SuInk/Diana?color=c83f76)](https://github.com/SuInk/Diana/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Limited%20Redistribution-informational)](./LICENSE)

[Website and Docs](https://suink.github.io/Diana/) · [Live Demo](https://suink.github.io/Diana/demo/) · [Latest Release](https://github.com/SuInk/Diana/releases/latest) · [中文](./README.md)

</div>

<br />

<img src="./docs/assets/diana-webui-overview.png" alt="Diana console overview: channel status, message statistics, system resources, and the live event stream" width="100%" />

## Contents

- [What Is Diana](#what-is-diana)
- [Features](#features)
- [Getting Started](#getting-started)
- [First-Run Setup](#first-run-setup)
- [Supported Channels](#supported-channels)
- [Access Security](#access-security)
- [Capabilities and Extensions](#capabilities-and-extensions)
- [Configuration File](#configuration-file)
- [Deployment Recipes](#deployment-recipes)
- [Development](#development)
- [Project Layout](#project-layout)
- [Documentation](#documentation)
- [License](#license)

## What Is Diana

Diana is a multi-platform AI assistant service written in Go: an LLM compatibility layer, platform adapters, a Gin WebUI, and a plugin system, all compiled into a single binary. It ships with OneBot v11 and Telegram channels; the WebUI manages multiple bot profiles, model assignments, per-group policies, plugins, and the built-in Agent.

Configuration, memory, and logs live in a local SQLite database — no hosted service required. For every message you can look up why it was answered, why it was skipped, which tools ran, and how many tokens it cost.

## Features

| | |
| --- | --- |
| **Channels in parallel** | OneBot v11 and Telegram run side by side; replies, images, and reminders always return to the originating channel, and session context can be isolated or shared per profile |
| **Split model duties** | Chat, vision, intent detection, and image generation each bind their own provider and model, validated with a real request before saving |
| **Built-in web search** | No plugin to install; the model can search before answering time-sensitive questions. Exa MCP first, Tavily as fallback |
| **Image text recognition** | Images can go through a vision model and OCR at once (LLM transcription, self-hosted OCR service, or local tesseract). When the chat model has no vision support, it can receive the recognized text only. Results are cached in the database by image content hash, so a repeated image or sticker is only recognized once |
| **Sticker sending** | Searches stickers already cached in the current group or private conversation; the Agent selects by name and cached image description, then sends through the source channel without mixing conversations |
| **Per-group policies** | Reply windows, allow/deny lists, trigger words, persona, group level thresholds, and tool permissions per group |
| **Layered long-term memory** | Recent context, compressed summaries, structured facts, and on-demand history search work in layers to keep token usage down |
| **Built-in Agent** | A minimal Pi-style tool loop with file, command, and browser tools, loading Skills and MCP servers on demand |
| **Full event auditing** | Reply reasons, model call chains, tokens, and errors are recorded; operation logs carry the acting operator |
| **One-click install and self-update** | The installer verifies SHA-256, backs up data, and rolls back automatically when the health check fails; the console can upgrade in place |

## Getting Started

### One-Click Installation (Recommended)

The installer detects the OS and architecture, downloads the latest stable complete package, verifies `SHA256SUMS`, generates local admin credentials, and starts Diana. **Running the same command again upgrades safely**: it backs up the database and current runtime first, and restores them if the health check fails.

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

```powershell
# Windows PowerShell
irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1 | iex
```

Then open `http://127.0.0.1:18080`. The generated administrator account and password are printed to the terminal once and stored in `config.yaml` inside the install directory — keep that file private.

Default install directory: `~/.local/share/diana` on Linux/macOS, `%LOCALAPPDATA%\Diana` on Windows.

The install script takes its own parameters through environment variables (installer arguments, not application configuration):

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | \
  DIANA_VERSION=v0.8.9 DIANA_INSTALL_DIR=/opt/diana DIANA_PORT=18081 sh
```

**Installing on a server and want to open the console from another machine? Set
`DIANA_HOST`.** The default binds to `127.0.0.1` only — the WebUI carries admin
rights, so exposing it the moment it is installed is not a sane default:

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | \
  DIANA_HOST=0.0.0.0 sh
```

When listening on a non-loopback address, allow the port in the firewall and your
cloud security group, and prefer a reverse proxy with TLS over exposing the console
directly.

To change the binding of an existing install, re-run the script with the new
`DIANA_HOST` — it restarts the service for you, so the change takes effect
immediately (with `DIANA_START_AFTER_INSTALL=false` nothing is restarted and you
start it yourself). Editing `server.host` in `config.yaml` directly also works, but then
the restart is on you:

```sh
systemctl --user restart diana.service   # Linux
launchctl kickstart -k "gui/$(id -u)/com.suink.diana"   # macOS
```

Common settings can be written into the generated `config.yaml` at install time instead
of filling them in through the WebUI afterwards: `LLM_API_KEY`, `LLM_BASE_URL`,
`LLM_MODEL`, `LLM_API_FORMAT`, `LLM_IMAGE_MODEL`, `DIANA_LOCAL_MEDIA_BASE_URL`,
`DIANA_NAPCAT_WEBUI_URL`, `DIANA_NAPCAT_WEBUI_TOKEN`. For anything else, point
`DIANA_CONFIG_FILE` at a YAML fragment and it is merged into `config.yaml` verbatim:

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | \
  DIANA_HOST=0.0.0.0 LLM_API_KEY=sk-xxx DIANA_CONFIG_FILE=/root/diana-extra.yaml sh
```

Note that the `LLM_*` names are installer arguments only: they are written into the
`llm:` section of `config.yaml`, which is seeded once while the database is empty.
Changing them after the install has no effect — use the WebUI.

### Docker

```sh
git clone https://github.com/SuInk/Diana.git && cd Diana
cp config.example.yaml config.yaml
# Edit config.yaml: administrator password, token, LLM settings
docker compose up -d --build
```

`config.yaml` is mounted read-only and `DIANA_CONFIG` points at it. Skipping the mount also works — open the WebUI and use the setup wizard.

<details>
<summary>Using <code>docker run</code></summary>

```sh
docker build -t diana:latest .

docker run -d \
  --name diana \
  --restart unless-stopped \
  -p 18080:18080 \
  -v "$PWD/data:/app/data" \
  -v "$PWD/logs:/app/logs" \
  -v "$PWD/config.yaml:/app/config.yaml:ro" \
  -e DIANA_CONFIG=/app/config.yaml \
  diana:latest
```

</details>

The container serves the same `http://127.0.0.1:18080`, and OneBot v11 clients connect to `ws://127.0.0.1:18080/onebot/v11/ws`. When the client runs on a different host, replace `127.0.0.1` with Diana's address.

### Complete Release Package

Without the installer, download a complete package from [GitHub Releases](https://github.com/SuInk/Diana/releases) (`.tar.gz` for Linux/macOS, `.zip` for Windows). It contains the backend binary, `frontend-next/dist`, and start scripts — no build required: run `run.sh` on Unix or `run.bat` on Windows.

Verify before replacing anything; forced updates never skip this check either:

```sh
sha256sum -c SHA256SUMS --ignore-missing
```

> On macOS use `shasum -a 256 <file>`, on Windows `Get-FileHash <file> -Algorithm SHA256`.

When running from a complete package, the WebUI can install later stable versions itself: it downloads the package for the current platform, verifies it, backs up, and switches, restoring the database and previous version if the health probe fails. Backups live under `.diana-updates` in the install directory. Container deployments rely on the image updater, and source checkouts use the Git update flow.

### Building from Source

Aimed at development and custom deployments. Requires Go `1.26.5`, Node.js `22`, and npm.

```sh
git clone https://github.com/SuInk/Diana.git && cd Diana
go mod download

cd frontend-next && npm ci && npm run build && cd ..
go build -o dist/diana-webui ./cmd/webui

./dist/diana-webui
```

For local development, one command starts both the backend and the Vite dev server:

```sh
make dev                                   # backend 18080, frontend 5173
make dev BACKEND_PORT=18081 FRONTEND_PORT=5174
node scripts/dev.mjs                       # when make is unavailable
```

## First-Run Setup

1. **Sign in** — open `http://127.0.0.1:18080` and use the credentials printed in the terminal.
2. **Configure models** — on the LLM page, enter the provider and API key, sync the model list, then pick the default model. Unattended deployments can seed this in `config.yaml` instead; it lands in the database on the first start:

   ```yaml
   llm:
     provider: openai_compatible
     api_key: your-key
     base_url: https://example.com/v1
     model: gpt-5.4-mini
     image_model: gpt-image-2
   ```

   Supported providers: `openai_compatible`, `gemini`, `anthropic`. Multiple named configurations can be saved and switched.
3. **Connect a bot** — create a profile on the Bots page with the platform account, owner ID, and trigger words.
4. **Check the events** — send a message and confirm the reply reason and model call chain in the event centre.

<details>
<summary>Starting an OneBot v11 bot purely from config.yaml</summary>

```yaml
bot:
  platform: onebot-v11
  enabled: true
  onebot_reverse_ws_endpoint: ws://127.0.0.1:18080/onebot/v11/ws
  onebot_access_token: your-onebot-token
  bot_account: "10001"
  group_triggers: [Diana, diana]

llm:
  provider: openai_compatible
  api_key: your-key
  model: gpt-5.4-mini
```

```sh
./dist/diana-webui --config ./config.yaml
```

Private messages always trigger a reply; group messages need an `@mention` or a trigger word prefix. Both sections are seeded once while the database is empty; change them in the WebUI afterwards.

</details>

## Supported Channels

| Category | Platform | Transport |
| --- | --- | --- |
| OneBot v11 | OneBot v11 | Reverse WebSocket; the OneBot v11 client connects to Diana (NapCat, Lagrange.Core, go-cqhttp all belong here) |
| Telegram | Telegram Bot API | Official long polling, outbound from Diana — no public address or webhook needed |

Every enabled profile stays online simultaneously. Telegram only needs the bot token from BotFather; a proxy address is usually required from mainland China, and a self-hosted Bot API server can be configured to bypass the 50 MB upload limit.

Platform differences:

- **Group level thresholds are OneBot v11-only.** Telegram has no such concept, so the option is hidden for Telegram bots.
- **Voice messages and mentions** rely on OneBot CQ codes and degrade gracefully on Telegram: the text is still sent, but nobody gets mentioned.
- **Local media**: OneBot clients fetch Diana's `/media/resolver` URL, while Telegram cannot reach local addresses and receives a direct multipart upload instead.

## Access Security

The WebUI requires a login from the very first start, with the same rules for local and public access. The default administrator account is generated securely (`diana#` plus 16 random characters) and persisted in SQLite.

- With no password in the `admin` section of `config.yaml` on first start, a random one is generated; the credentials appear once in that run's stderr log.
- `admin.username` and `admin.password` can seed the first initialization; existing credentials are never overwritten.
- After signing in, the account and password can be changed under Settings → Access Security. Usernames are 2–64 characters without spaces or control characters (`diana#` is just the shape of generated names, not a required prefix), and passwords need at least 8 characters.
- Every `/api` endpoint requires a session, valid for 30 days; changing the password invalidates all existing sessions. The settings page lists signed-in devices and can revoke them individually.

**Owner quick login**: once the owner account is configured on the Bots page, the login page can request a single-use 6-digit code. The owner sends that code to the bot in a private message and the session is issued, after which the bot replies with a receipt showing the source IP and device. The receipt needs no action from the owner — it exists so the message is never silently swallowed: if the code was socially engineered out of them, they see it immediately and can revoke the session and change the password. When the page fails to redirect on its own (polling dropped, the tab was reclaimed by a mobile browser, a different tab was used), typing that same already-sent code into the login page exchanges it directly. That endpoint is rate limited per source so a confirmed pairing cannot be brute-forced away. Codes last 5 minutes, are single-use, and need the bot online. **The server never sends a code on its own**, so there is no anonymous request that can be used to harass the owner.

**Brute-force protection**: password login and password changes share one failure budget per source — after 5 consecutive failures a lockout begins, backing off from 30 seconds up to 30 minutes, returning `429` with `Retry-After`; any success clears it. A global ceiling covers distributed credential stuffing, and lockouts are written to the operation log.

> [!IMPORTANT]
> Per-source counting depends on the real client IP. Diana trusts no reverse proxy by default, so behind one every request appears to come from the proxy itself. **Set `DIANA_TRUSTED_PROXIES`** (comma-separated IPs or CIDRs) for public deployments; only then is `X-Forwarded-For` parsed. Session cookies are not marked Secure so plain HTTP works on a LAN — put an HTTPS reverse proxy in front for public deployments.

Exempt paths: the login and quick-login endpoints, `/api/health` (monitoring probes), `/onebot/*` (authenticated by the OneBot access token), and the group management page (its own code flow).

## Capabilities and Extensions

### Admission Control

Both the Bots page ("admission control") and each group on the Groups page can restrict when the bot replies. Global settings act as defaults, and group settings replace them wholesale rather than merging field by field.

- **Group admission mode**: "deny list" by default — active everywhere except disabled groups. Switching to "allow list" means the bot only works in listed groups and stays silent when pulled into others; the disabled list still applies.
- **Group level threshold**: this is the in-group activity level (Lv.1–6), not the platform account level (which the OneBot protocol cannot read). Levels accumulate per group.
- **Reply windows**: an end time earlier than the start means overnight (e.g. `22:00-06:00`); identical values mean always open. Time zones use IANA names such as `Asia/Shanghai`. A quiet-hours notice can be configured and is sent at most once per hour per session.
- **Exempt / blocked users**: exempt users ignore level and time restrictions; blocked users get no reply anywhere.
- **Owner bypass**: on by default and best left that way — otherwise a misconfigured window or threshold locks you out too, with no way to recover from the chat side.

Some OneBot implementations omit `level` in message events, so Diana fills it in through `get_group_member_info` when needed and caches it for 10 minutes. **When the level cannot be read, the message is allowed through**: implementations vary wildly, and treating "unknown" as "level 0" would silence an entire group. Strict blocking is available, but know the trade-off.

### Plugins

The bot plugins area enables, disables, and configures the official built-in plugins; built-in capabilities are never installed or uninstalled.

- **Link resolution**: resolves and sends images or video from Bilibili, YouTube, X, Xiaohongshu, and Douyin; Zhihu, Weibo, and GitHub only yield title and description. Size, duration, quality, and gallery limits are adjustable. Per-platform cookies, the yt-dlp cookie file, and proxy addresses can be set in the plugin settings and take precedence over the matching environment variables; once saved, read endpoints only report that a credential is configured. The card also probes `yt-dlp`, `ffmpeg`, and `node`, and can install what is missing through a controlled package manager.
- **File parsing**: handles OneBot file segments and links to text-like files, feeding the content to the LLM as context.
- **Sticker sending**: with the built-in Agent enabled, treats cached images with sticker summaries as candidates while excluding ordinary `[图片]` images. The `diana.sticker` tool searches first and sends only a validated candidate; settings control history size, result count, unnamed `[动画表情]` candidates, and separate opt-in switches for other groups and private chats. Sharing stays inside one bot context namespace and does not expose source group or user IDs to the model.
- **Image text recognition**: runs OCR before images enter the context, using LLM vision transcription, a self-hosted OCR service (PaddleOCR / RapidOCR), or local `tesseract` — the latter two fully offline. Delivery is either "image plus text" or "text only", the latter letting a chat model without vision support handle image messages.
- **Web search**: installed and enabled by default; it can be disabled and configured but never uninstalled. It is independent of the Agent switch, so local file, command, and browser tools stay closed when the Agent is off.

### Built-in Agent

Once enabled, the bot handles messages with a minimal [Pi Agent](https://github.com/earendil-works/pi)-style state and tool loop: plan, call tools, observe, answer. The runtime is native Go, so complete release packages need no extra Node install.

Built-in tools: `list_files`, `read_file`, `run_command` (allow-listed commands inside the work directory, no shell, with timeout and output truncation), and `browser_open` / `browser_text` / `browser_click` / `browser_type` / `browser_screenshot` over the Chrome DevTools Protocol.

The Agent manages three kinds of capability from one extension directory: **built-in plugins** (existing enable/disable and permission rules apply), **Skills** (progressive disclosure — only names and descriptions enter the context, with `skills.read` pulling the full `SKILL.md` on demand; installable from a file, an HTTP(S) URL, or a ZIP, and moved to `.trash/` on uninstall so they can be recovered), and **MCP servers** (stdio and Streamable HTTP, written to `.mcp.json` only after a successful connection test, with `${ENV_VAR}` supported in `env` and `headers`).

> [!WARNING]
> Extension management tools are owner-only and run only when the current user message explicitly asks for the change — web pages, tool results, Skills, and MCP responses can never stand in for user authorization. Point the Agent work directory at a dedicated data directory rather than one holding secrets or production data, and use `DIANA_AGENT_COMMAND_ALLOWLIST` in production to permit only the commands you need.

Browser tools need Chrome/Chromium with remote debugging enabled: `chrome --remote-debugging-port=9222`.

### Third-Party NoneBot Plugins

The Go binary cannot load Python NoneBot plugins directly, so run a NoneBot sidecar: install the plugins in a NoneBot2 project, give it an OneBot v11 reverse WebSocket driver, then enable the `NoneBot plugin bridge` on Diana's bot page (default `ws://127.0.0.1:8080/onebot/v11/ws`). Diana forwards OneBot events to the sidecar, and forwards the plugins' `send_msg`, `get_group_info`, and other API calls back to the active OneBot client.

### Outbound API

External systems (CI, monitoring, scripts) can push messages to a chosen conversation over HTTP, using Diana as a notification outlet. It ships as a built-in plugin (`对外 API`) that is **disabled by default**: external calls are only accepted after enabling it on the Plugins page or via the "Settings → Security → Outbound API" card, and disabling it makes them return 403 immediately. API keys are created on that same card; the plaintext is shown exactly once at creation and only its SHA-256 hash is stored. Revocation takes effect immediately, and key management works regardless of the plugin switch, so you can prepare keys before opening the gate. Every call is logged in the Log Centre with `actor` set to `openapi:<key name>`.

```sh
curl -X POST http://127.0.0.1:18080/openapi/v1/messages \
  -H "Authorization: Bearer <key>" \
  -H "Content-Type: application/json" \
  -d '{"group_id": "123456", "text": "Build #42 passed"}'
```

- Address the target with `group_id` or `user_id`; a group target that also carries `user_id` mentions that member. On multi-channel deployments add `platform` (e.g. `onebot-v11`, `telegram`) or `profile_id` for routing; with a single enabled channel both can be omitted.
- `GET /openapi/v1/status` uses the same key for health checks and lists the deliverable channels.
- Each key is rate-limited to 60 requests per minute by default (adjustable in the plugin settings); exceeding it returns `429` with `Retry-After`. The endpoint only delivers text — it never triggers a model call.

### Log Centre

The Log Centre page shows persisted operation and error logs: saving or switching LLM configurations, starting and stopping bots, plugin management, and system updates are all recorded with an `actor` (the WebUI defaults to `web:<client IP>`; a gateway can pass `X-Diana-Actor`, `X-Operator`, or `X-Forwarded-User`; in-chat model commands record `qq:<user id>`).

```text
GET /api/logs?kind=operation&limit=100
GET /api/logs?kind=error&limit=100
```

## Configuration File

Everything lives in `config.yaml`; there are no configuration environment variables. The process looks for it via `--config`, `DIANA_CONFIG`, the working directory, then the directory holding the executable. If none exists it runs on built-in defaults — just open the WebUI and use the setup wizard. See [`config.example.yaml`](./config.example.yaml) and the [configuration docs](https://suink.github.io/Diana/configuration.html) for every field.

The file has two layers, split by **whether the WebUI can change the setting**:

| Section | Layer | How it applies |
| --- | --- | --- |
| `server` / `storage` / `admin` / `update` / `napcat` | Infrastructure, no WebUI entry point | Read on every start; `config.yaml` is the only source |
| `bot` / `llm` | Runtime configuration, editable in the WebUI | **Seeded once, only while the database is empty**; the database wins afterwards |

The `bot` / `llm` sections exist for unattended deployments: bring the token and API key along on the container's first start instead of walking the wizard by hand. Once the database holds a configuration they stop applying, and startup logs say so explicitly — `config: bot section in ... was NOT applied`. No more debugging against a file that is not in effect. Clear the database to re-seed; otherwise change settings in the WebUI.

```yaml
server:
  # Use 0.0.0.0 to reach the console from another machine; keep 127.0.0.1 for local-only.
  host: 127.0.0.1
  port: "18080"
  # Trusted reverse-proxy IPs or CIDRs; X-Forwarded-For is only parsed when set.
  trusted_proxies: []
  frontend_dist: ""

storage:
  db_path: data/diana.db
  # Empty means stdout only.
  log_path: logs/diana.log
  media_dir: ""
  media_max_mb: 0
  media_cache_mb: 0
  # Empty derives the URL from the address the client used for the reverse WebSocket handshake.
  local_media_base_url: ""

admin:
  # Leave both empty to generate an account and a strong password on first start, printed once.
  username: ""
  password: ""

update:
  root: "."
  apply_enabled: true
  release_enabled: true
  group_test_enabled: false

napcat:
  webui_url: ""
  webui_token: ""

# Seeded once while the database is empty. Field names match the WebUI config API payload.
bot:
  platform: onebot-v11
  enabled: true
  onebot_reverse_ws_endpoint: ws://127.0.0.1:18080/onebot/v11/ws
  onebot_access_token: ""
  bot_account: ""
  owner_id: ""
  group_triggers: [Diana, diana]

llm:
  provider: openai_compatible
  base_url: https://api.example.com/v1
  api_key: ""
  model: ""
```

Structured logs live in the SQLite database at `storage.db_path`; `storage.log_path` still holds the plain runtime log file.

### What Remains an Environment Variable

Only two kinds, neither of which has a WebUI counterpart, so neither creates a second source of truth:

- `DIANA_CONFIG` — where the config file is. It is a bootstrap pointer, not configuration.
- External integration credentials and external program paths — `BILI_SESSDATA`, `DOUYIN_CK`, `XHS_CK`, `RESOLVER_PROXY` for link resolution, `EXA_API_KEY` / `TAVILY_API_KEY` for search, plus host-probing settings such as `DIANA_FFMPEG_PATH` and `DIANA_SERVICE_MANAGER`. Per-platform cookies can also be entered in the plugin settings, which take precedence over the matching environment variable.

## Deployment Recipes

<details>
<summary>systemd</summary>

```sh
sudo mkdir -p /var/log/diana
sudo chown -R $USER:$USER /var/log/diana
```

```ini
[Unit]
Description=Diana
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/diana
Environment=DIANA_CONFIG=/opt/diana/config.yaml
ExecStart=/opt/diana/diana-webui
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

</details>

<details>
<summary>Cross-compiling</summary>

```sh
# macOS
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o dist/diana-webui ./cmd/webui
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -o dist/diana-webui ./cmd/webui

# Linux
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/diana-webui ./cmd/webui
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o dist/diana-webui ./cmd/webui
```

```powershell
# Windows
$env:GOOS="windows"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"
go build -o dist\diana-webui.exe .\cmd\webui
```

Releases also ship bare binaries per platform, but **a bare binary contains no frontend assets** — most users want the complete package instead.

</details>

## Development

```sh
go test ./...                 # backend tests
make dev                      # backend plus the Vite dev server
make deps                     # install frontend dependencies
make run                      # build the frontend and run the backend against it
make build                    # production build
```

The frontend lives in `frontend-next/` (Vue + TypeScript) and is the only supported console: dashboard overview (connection checklist, today/24h message statistics, live event stream), SSE push, a three-step setup wizard, and a mobile layout. See [frontend-next/README.md](./frontend-next/README.md).

Supporting backend endpoints:

```text
GET /api/stats    # dashboard statistics (in-process counters, reset on restart)
GET /api/events   # SSE stream: status / stats / bot_event
GET /api/health   # version and uptime
```

Before submitting, make sure `gofmt` is clean, `go mod tidy` produces no diff, and both `go test ./...` and the frontend `vue-tsc` pass — CI gates on all of them. Conventions are documented in [AGENTS.md](./AGENTS.md).

## Project Layout

```text
.
├── cmd/webui/          # Gin WebUI and OneBot endpoint entry point
├── model/assistant/    # Bot runtime, channel adapters, plugins, and Agent
├── model/llm/          # Unified LLM interface and provider adapters
├── model/storage/      # SQLite storage and message history
├── webui/              # WebUI API handlers and authentication
├── frontend-next/      # Vue + TypeScript console
├── docs/               # GitHub Pages documentation site
├── scripts/            # Installer and development scripts
└── .github/workflows/  # CI and Pages deployment
```

## Documentation

| Page | Contents |
| --- | --- |
| [Deploy](https://suink.github.io/Diana/deploy.html) | One-click install, complete packages, Docker, source builds, first login |
| [Configuration](https://suink.github.io/Diana/configuration.html) | Channels, model assignment, group policies, Agent tools, security boundaries |
| [Implementation](https://suink.github.io/Diana/implementation.html) | Architecture, the message decision chain, memory layers, media and storage |
| [Operations](https://suink.github.io/Diana/operations.html) | Updates and rollback, logs and backups, troubleshooting, release flow |
| [Live demo](https://suink.github.io/Diana/demo/) | The real console with mock data, not connected to a live bot |

## License

This project uses the `Limited Redistribution License (SuInk)`; see [LICENSE](./LICENSE).
