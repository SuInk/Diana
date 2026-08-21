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
- [Environment Variables](#environment-variables)
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

Then open `http://127.0.0.1:18080`. The generated administrator account and password are printed to the terminal once and stored in `runtime.env` inside the install directory — keep that file private.

Default install directory: `~/.local/share/diana` on Linux/macOS, `%LOCALAPPDATA%\Diana` on Windows.

Environment variables pick the version, directory, port, or install-without-start behaviour:

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | \
  DIANA_VERSION=v0.8.9 DIANA_INSTALL_DIR=/opt/diana DIANA_PORT=18081 sh
```

### Docker

```sh
git clone https://github.com/SuInk/Diana.git && cd Diana
cp docker-compose.yml docker-compose.local.yml
# Edit the token, bot account, and LLM settings in docker-compose.local.yml
docker compose -f docker-compose.local.yml up -d --build
```

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
  -e LOG_PATH=/app/logs/diana.log \
  -e DIANA_ADMIN_PASSWORD=change-this-admin-password \
  -e QQBOT_ENABLED=true \
  -e ONEBOT_REVERSE_WS_ENDPOINT=ws://127.0.0.1:18080/onebot/v11/ws \
  -e ONEBOT_ACCESS_TOKEN=your-onebot-token \
  -e QQBOT_QQ=10001 \
  -e LLM_PROVIDER=openai_compatible \
  -e LLM_API_KEY=your-key \
  -e LLM_MODEL=gpt-4o-mini \
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
2. **Configure models** — on the LLM page, enter the provider and API key, sync the model list, then pick the default model. Environment variables can seed this too:

   ```sh
   LLM_PROVIDER=openai_compatible \
   LLM_API_KEY=your-key \
   LLM_BASE_URL=https://example.com/v1 \
   LLM_MODEL=gpt-4o-mini \
   LLM_IMAGE_MODEL=gpt-image-1 \
   ./dist/diana-webui
   ```

   Supported providers: `openai_compatible`, `gemini`, `anthropic`. Multiple named configurations can be saved and switched.
3. **Connect a bot** — create a profile on the Bots page with the platform account, owner ID, and trigger words.
4. **Check the events** — send a message and confirm the reply reason and model call chain in the event centre.

<details>
<summary>Starting an OneBot v11 bot purely from environment variables</summary>

```sh
QQBOT_ENABLED=true \
ONEBOT_REVERSE_WS_ENDPOINT=ws://127.0.0.1:18080/onebot/v11/ws \
ONEBOT_ACCESS_TOKEN=your-onebot-token \
QQBOT_QQ=10001 \
DIANA_GROUP_TRIGGERS=Diana,diana \
LLM_PROVIDER=openai_compatible \
LLM_API_KEY=your-key \
LLM_MODEL=gpt-4o-mini \
./dist/diana-webui
```

Private messages always trigger a reply; group messages need an `@mention` or a trigger word prefix.

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

- Without `DIANA_ADMIN_PASSWORD` on first start, a random password is generated; the credentials appear once in that run's stderr log.
- `DIANA_ADMIN_USERNAME` and `DIANA_ADMIN_PASSWORD` can seed the first initialization; existing credentials are never overwritten.
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

### Log Centre

The Log Centre page shows persisted operation and error logs: saving or switching LLM configurations, starting and stopping bots, plugin management, and system updates are all recorded with an `actor` (the WebUI defaults to `web:<client IP>`; a gateway can pass `X-Diana-Actor`, `X-Operator`, or `X-Forwarded-User`; in-chat model commands record `qq:<user id>`).

```text
GET /api/logs?kind=operation&limit=100
GET /api/logs?kind=error&limit=100
```

These structured logs live in the SQLite database at `APP_DB_PATH`; `LOG_PATH` still controls the plain runtime log file.

## Environment Variables

Anything configurable in the WebUI needs no environment variable. The common ones are below; see [`.env.example`](./.env.example) and the [configuration docs](https://suink.github.io/Diana/configuration.html) for the rest.

| Variable | Default | Description |
| --- | --- | --- |
| `PORT` | `18080` | Port for the WebUI and the OneBot endpoint |
| `APP_DB_PATH` | `data/diana.db` | Local SQLite configuration database |
| `LOG_PATH` | empty | Log file path; when set, output goes to both stdout and the file |
| `DIANA_TRUSTED_PROXIES` | empty | Trusted reverse proxy IPs or CIDRs, comma separated; `X-Forwarded-For` is parsed only when set |
| `DIANA_ADMIN_USERNAME` | random | Administrator account for the first initialization only |
| `DIANA_ADMIN_PASSWORD` | random | Administrator password for the first initialization only |
| `LLM_PROVIDER` | `openai_compatible` | `openai_compatible` / `gemini` / `anthropic` |
| `LLM_API_KEY` | empty | LLM API key |
| `LLM_BASE_URL` | empty | Custom base URL for OpenAI-compatible endpoints |
| `LLM_MODEL` | empty | Model ID (no default for `openai_compatible`) |
| `QQBOT_ENABLED` | `false` | Start the bot automatically on launch |
| `ONEBOT_REVERSE_WS_ENDPOINT` | `ws://127.0.0.1:<PORT>/onebot/v11/ws` | Reverse WebSocket address for OneBot v11 clients |
| `ONEBOT_ACCESS_TOKEN` | empty | OneBot access token |
| `QQBOT_QQ` | empty | The bot's QQ number |
| `DIANA_OWNER_ID` | empty | Owner account ID (numeric user ID on Telegram) |
| `DIANA_GROUP_TRIGGERS` | `Diana,diana` | Group chat trigger words |
| `DIANA_AGENT_ENABLED` | `false` | Enable the built-in Agent |

<details>
<summary>All environment variables</summary>

| Variable | Default | Description |
| --- | --- | --- |
| `FRONTEND_DIST` | auto-detected | Frontend build output directory; falls back to `frontend-next/dist` |
| `DIANA_SEND_RETRY_ATTEMPTS` | `3` | Send retries per message (1–5) |
| `DIANA_SEND_CHUNK_INTERVAL_MS` | `300` | Delay between chunks of a split reply, in milliseconds |
| `DIANA_ERROR_REPLY_PREFIX` | `出错了：` | Prefix for in-chat error notices |
| `DIANA_LOG_PATH` | empty | Compatibility alias for `LOG_PATH` |
| `DIANA_MEDIA_DIR` | `data/media` | Inbound image storage; vision submits base64 from these local files |
| `DIANA_MEDIA_MAX_MB` | `10` | Download limit per inbound image |
| `DIANA_MEDIA_CACHE_MB` | `512` | Total image directory budget; least recently used files are evicted |
| `DIANA_LOCAL_MEDIA_BASE_URL` | this service's `/media/resolver` | Media address reachable by OneBot clients; set to `http://diana:18080/media/resolver` for split containers |
| `DIANA_BILI_SESSDATA` | empty | `SESSDATA` from Bilibili login cookies; WebUI plugin settings win |
| `DIANA_DOUYIN_CK` | empty | Douyin cookie, required for Douyin parsing; WebUI plugin settings win |
| `DIANA_XHS_CK` | empty | Xiaohongshu cookie, required for its parsing; WebUI plugin settings win |
| `DIANA_YTDLP_COOKIES` | empty | Path to a yt-dlp Netscape cookie file; WebUI plugin settings win |
| `DIANA_RESOLVER_PROXY` | empty | Proxy for social media parsing and yt-dlp; WebUI plugin settings win |
| `DIANA_RELEASE_UPDATE_ENABLED` | `true` | Allow downloading, verifying, backing up, and self-updating from complete release packages; never enabled for source or container deployments |
| `LLM_USER_AGENT` | `codex-cli/0.142.0` | User-Agent for OpenAI-compatible endpoints |
| `LLM_IMAGE_MODEL` | provider default | Image generation model; `gpt-image-1` for OpenAI-compatible, `imagen-4.0-generate-001` for Gemini |
| `LLM_TEMPERATURE` | empty | Temperature |
| `LLM_MAX_OUTPUT_TOKENS` | `1024` | Max output tokens for the Responses API |
| `LLM_TIMEOUT_MS` | `30000` | LLM request timeout in milliseconds |
| `NONEBOT_BRIDGE_ENABLED` | `false` | Enable the third-party NoneBot plugin bridge |
| `NONEBOT_BRIDGE_ENDPOINT` | `ws://127.0.0.1:8080/onebot/v11/ws` | Reverse WebSocket address of the NoneBot sidecar |
| `NONEBOT_BRIDGE_TOKEN` | empty | Access token for the NoneBot bridge |
| `DIANA_SYSTEM_PROMPT` | built-in prompt | Bot system prompt |
| `DIANA_MAX_INPUT_CHARS` | `2000` | Max characters per input |
| `DIANA_MAX_REPLY_CHARS` | `3500` | Max characters per reply |
| `DIANA_DIRECT_REPLY_CHUNK_SIZE` | `500` | Characters per chunk when splitting text replies |
| `DIANA_MAX_BOT_CONCURRENCY` | `5` | Global concurrency limit |
| `DIANA_AGENT_WORK_DIR` / `AGENT_WORK_DIR` | `.` | Work directory the Agent may access |
| `DIANA_AGENT_MAX_STEPS` | `4` | Max tool loop steps per reply, up to `8` |
| `DIANA_AGENT_COMMAND_ALLOWLIST` | common dev commands | Commands `run_command` may execute, comma separated; `*` allows everything |
| `DIANA_AGENT_COMMAND_TIMEOUT_MS` | `10000` | Local command timeout, up to `60000` |
| `DIANA_AGENT_SKILL_ROOTS` | `.agents/skills,skills` | Skill search directories, comma separated; self-installed Skills always land in `.agents/skills` under the work directory |
| `DIANA_AGENT_MCP_CONFIG` | `.mcp.json` | MCP server config file; relative paths resolve against the Agent work directory |
| `DIANA_AGENT_BROWSER_CDP_URL` / `AGENT_BROWSER_CDP_URL` | `http://127.0.0.1:9222` | Chrome DevTools address for the browser tools |
| `DIANA_AGENT_BROWSER_TIMEOUT_MS` | `15000` | Browser tool timeout, up to `60000` |

</details>

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
Environment=PORT=18080
Environment=LOG_PATH=/var/log/diana/diana.log
Environment=QQBOT_ENABLED=true
Environment=ONEBOT_REVERSE_WS_ENDPOINT=ws://127.0.0.1:18080/onebot/v11/ws
Environment=ONEBOT_ACCESS_TOKEN=change-me
Environment=QQBOT_QQ=10001
Environment=LLM_PROVIDER=openai_compatible
Environment=LLM_API_KEY=change-me
Environment=LLM_MODEL=gpt-4o-mini
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
