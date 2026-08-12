# Diana

[中文](./README.md)

Diana is a multi-platform AI assistant service written in Go, with an LLM compatibility layer, platform adapters, a Gin WebUI, and plugin management. It currently ships with a QQ adapter for OneBot v11; the WebUI manages multiple assistant profiles, models, platform connections, trigger aliases, and built-in plugins.

## Requirements

- A client with OneBot v11 reverse WebSocket support when using the QQ adapter
- Go `1.26.5`, Node.js `22`, and npm when installing from source
- Docker or Docker Compose when deploying with Docker

## Docker Deployment

Build the image:

```sh
docker build -t diana:latest .
```

Run the container:

```sh
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
  -e LLM_USER_AGENT=codex-cli/0.142.0 \
  diana:latest
```

Docker Compose:

```sh
cp docker-compose.yml docker-compose.local.yml
# Edit token, QQ number, and LLM settings in docker-compose.local.yml.
docker compose -f docker-compose.local.yml up -d --build
```

After startup, open:

```text
http://127.0.0.1:18080
```

Configure the OneBot v11 client to connect to the exposed reverse WebSocket endpoint:

```text
ws://127.0.0.1:18080/onebot/v11/ws
```

If the OneBot v11 client and Diana are not on the same machine, replace `127.0.0.1` with the Diana host IP or domain.

## Install From Source

```sh
git clone <your-repo-url> diana
cd diana

go mod download

cd frontend-next
npm ci
npm run build
cd ..

go build -o dist/diana-webui ./cmd/webui
```

Start the service:

```sh
./dist/diana-webui
```

Default WebUI:

```text
http://127.0.0.1:18080
```

## macOS Deployment

Apple Silicon:

```sh
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o dist/diana-webui-darwin-arm64 ./cmd/webui
./dist/diana-webui-darwin-arm64
```

Intel Mac:

```sh
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -o dist/diana-webui-darwin-amd64 ./cmd/webui
./dist/diana-webui-darwin-amd64
```

You can also download the `darwin-arm64` or `darwin-amd64` binary from GitHub Releases. Standalone binaries do not include frontend assets; regular users should download the matching complete release package.

## Linux Deployment

amd64:

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/diana-webui-linux-amd64 ./cmd/webui
./dist/diana-webui-linux-amd64
```

arm64:

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o dist/diana-webui-linux-arm64 ./cmd/webui
./dist/diana-webui-linux-arm64
```

For background operation, use the systemd example below.

## Windows Deployment

PowerShell:

```powershell
$env:GOOS="windows"
$env:GOARCH="amd64"
$env:CGO_ENABLED="0"
go build -o dist\diana-webui-windows-amd64.exe .\cmd\webui
.\dist\diana-webui-windows-amd64.exe
```

You can also download the `windows-amd64.exe` binary from GitHub Releases. Standalone binaries do not include frontend assets; regular users should download the matching complete release package.

### Complete Release Packages

Each platform also has a complete release package (`.tar.gz` for Linux/macOS and `.zip` for Windows). It contains the backend binary, the `frontend-next/dist` build, and a launch script. Extracting the package is enough to run Diana without Go, Node.js, npm, or the source tree. Run `run.sh` on Unix platforms or `run.bat` on Windows.

Every Release also includes `SHA256SUMS`. Verify the downloaded file before extracting or replacing Diana; forced updates never bypass this check:

```sh
sha256sum -c SHA256SUMS --ignore-missing
```

On macOS, compare `shasum -a 256 <file>` with `SHA256SUMS`. On Windows, use `Get-FileHash <file> -Algorithm SHA256`.

When Diana is running from a complete Release package, the WebUI can install later stable releases directly. The backend downloads the package for the current OS and architecture together with `SHA256SUMS`, verifies it, and extracts it into a staging directory. After the old process exits, a detached update helper backs up the SQLite database and current binary/frontend, switches the files, starts the new version, and probes `/api/health`. A failed probe restores both the database and the old version automatically. Backups and the latest result are kept under `.diana-updates` in the installation directory. Containers remain managed by their image updater, while source checkouts continue to use the Git update path.

## Quick Run

For local development or testing, start the Go backend and Vite frontend together:

```sh
make dev
```

By default the backend runs at `http://127.0.0.1:18080` and the frontend runs at `http://127.0.0.1:5173`; Vite proxies `/api` and `/onebot` to the backend. You can change ports with environment variables:

```sh
make dev BACKEND_PORT=18081 FRONTEND_PORT=5174
```

If `make` is not installed, use the cross-platform Node script directly:

```sh
node scripts/dev.mjs
```

For backend-only or production builds:

```sh
make backend
make build
```

## Configure LLM

You can configure LLM settings in the WebUI or through environment variables:

```sh
LLM_PROVIDER=openai_compatible \
LLM_API_KEY=your-key \
LLM_BASE_URL=https://example.com/v1 \
LLM_MODEL=gpt-4o-mini \
LLM_USER_AGENT=codex-cli/0.142.0 \
LLM_IMAGE_MODEL=gpt-image-1 \
LLM_MAX_OUTPUT_TOKENS=1024 \
./dist/diana-webui
```

Supported providers:

- `openai_compatible`
- `gemini`
- `anthropic`

The WebUI LLM configuration page directly displays the saved API key for local copy/edit workflows. Plain `GET /api/llm/config` still omits secrets by default; the frontend explicitly uses `include_secrets=true` when it needs the full configuration.

## WebUI Access Security

The WebUI requires authentication from the first startup, with identical rules for local and public access. The default administrator username is generated securely as `diana#` followed by 16 random characters and persisted in SQLite.

- If `DIANA_ADMIN_PASSWORD` is not provided on first startup, Diana generates a cryptographically secure random password. The generated username and password are printed once to standard error.
- You may provide `DIANA_ADMIN_USERNAME=diana#yourname` and `DIANA_ADMIN_PASSWORD` on first startup instead. They never overwrite credentials already stored in SQLite.
- After login, the username and password can be updated under Settings > Access Security. Usernames must start with `diana#`; passwords must contain at least 8 characters.

**Administrator quick login:** enable it on the Assistant page and configure the owner account to expose two one-time login methods. Code login sends a six-digit code to the owner's QQ or Telegram account and also requires the browser-bound challenge token. Private-message confirmation displays a code in the browser which the owner sends privately to the bot. Codes expire after five minutes, are attempt-limited and single-use, while delivery has a 60-second cooldown. The active bot must be online for either method.

## WebUI Log Center

The WebUI `Log Center` page shows persistent operation logs and error logs. Operation logs cover actions such as saving or switching LLM profiles, starting or stopping the bot, managing plugins, and running system updates. Error logs record failed API operations. Logs include an `actor`: WebUI operations default to `web:<client IP>` and can be overridden by a gateway with headers such as `X-Diana-Actor`, `X-Operator`, or `X-Forwarded-User`; the QQ built-in LLM config skill records `qq:<user QQ>`.

```text
GET /api/logs?kind=operation&limit=100
GET /api/logs?kind=error&limit=100
```

These structured logs are stored in the SQLite database pointed to by `APP_DB_PATH`; `LOG_PATH` is still used for plain runtime log file output.

## Configure OneBot v11

This project directly serves a OneBot v11 reverse WebSocket endpoint:

```text
ws://127.0.0.1:18080/onebot/v11/ws
```

Add a OneBot v11 reverse WebSocket connection in the compatible client and set its endpoint to the address above. If you configure an access token, the client and Diana must use the same token. Implementations such as NapCat, Lagrange.Core, and go-cqhttp share this platform configuration instead of appearing as separate platforms.

Bot startup example:

```sh
QQBOT_ENABLED=true \
ONEBOT_REVERSE_WS_ENDPOINT=ws://127.0.0.1:18080/onebot/v11/ws \
ONEBOT_ACCESS_TOKEN=your-onebot-token \
QQBOT_QQ=10001 \
DIANA_GROUP_TRIGGERS=嘉然,然然,Diana,diana \
LLM_PROVIDER=openai_compatible \
LLM_API_KEY=your-key \
LLM_MODEL=gpt-4o-mini \
LLM_USER_AGENT=codex-cli/0.142.0 \
./dist/diana-webui
```

After startup, private messages trigger directly. In group chats, mentioning the bot or starting a message with a configured alias triggers the bot.

## Reply Admission

The "Reply admission" panel on the bot page and each group on the "Groups" page control the conditions under which the bot replies. The global settings act as defaults, and a group's settings replace the global ones as a whole (not merged field by field).

- **Group admission mode**: defaults to blacklist, which works in every group except disabled ones and matches the previous behavior. Switching to whitelist restricts the bot to the listed groups, so it stays silent when added to any other group. The disabled-group list still applies in whitelist mode.
- **QQ group level threshold**: this is the in-group activity level (Lv.1–6), not the QQ account level (the sun/moon/star rank, which the OneBot protocol does not expose). Levels accumulate per group, so the same person can have different levels in different groups.
- **Active hours**: an end time earlier than the start time means the window crosses midnight, for example `22:00-06:00`; identical values mean always open. Set the time zone to an IANA name such as `Asia/Shanghai`, or leave it empty to use the server's local zone. You can configure a quiet-hours notice, which is sent at most once per hour per conversation.
- **Exempt / blocked users**: exempt users bypass the level and time checks; blocked users get no reply in either group or private chats.
- **Owner bypass**: enabled by default, and worth keeping. Otherwise a wrong time or level setting locks you out too, with no way to recover from QQ.

Two notes about group levels:

Some OneBot implementations omit `level` from message events. Diana fills the gap through `get_group_member_info` when needed and caches the result in memory, keyed by group ID plus user ID, valid for 10 minutes and rebuilt from ordinary chat traffic after a restart.

When the level cannot be read, the bot **allows the message through** by default. Implementations vary widely in what they return for `level`, and treating "unknown" as level 0 would silence an entire group. Switch "when the level is unknown" to "block" if you need strict enforcement, but understand the tradeoff.

## Platforms

The bot page groups profiles by chat platform and lets you filter them with the tabs at the top. All enabled QQ and Telegram profiles run concurrently, while replies, media, and reminders are routed back through the source channel. Cross-platform conversation context is isolated per profile by default and can be shared by disabling isolation at the top of the bot list.

| Category | Platform | How it connects |
| --- | --- | --- |
| QQ | OneBot v11 | Reverse WebSocket; the OneBot v11 client dials into Diana |
| Telegram | Telegram | Official Bot API long polling — Diana dials out |

Telegram only needs the bot token from BotFather. There is no public address to expose and no webhook to configure; on restricted networks you will usually also want the proxy field. Point the custom Bot API field at a local Bot API server to lift the 50MB upload limit.

Capability differences between the two:

- **The group level threshold only applies to QQ.** Telegram has no group level, so the field is hidden for Telegram profiles and the backend never queries member info for them.
- **Voice messages and @-mentions** ride on OneBot CQ codes and degrade gracefully on Telegram: a welcome message still sends, it just does not ping the member.
- **Local media**: OneBot clients fetch Diana's `/media/resolver` URL, while Telegram cannot reach a local address and receives a direct multipart upload instead.

## Built-In Agent

You can enable the built-in Agent in the WebUI bot configuration page. When enabled, the bot follows the minimal state-and-tool-loop model used by [Pi Agent](https://github.com/earendil-works/pi): model planning, tool call, observation, and final response. The runtime remains native Go, so complete release packages do not acquire a Node.js runtime dependency.

Built-in tools:

- `list_files`: list files under the Agent working directory.
- `read_file`: read text files under the Agent working directory.
- `run_command`: execute allowlisted local commands inside the Agent working directory, without a shell, with timeout and output limits.
- `browser_open` / `browser_text` / `browser_click` / `browser_type` / `browser_screenshot`: control a browser through Chrome DevTools Protocol.

The Agent exposes one extension catalog across three capability types:

- **Built-in plugins**: every existing official WebUI plugin is present in `extensions.list` by default. Its existing enablement, configuration, and permission behavior remains unchanged.
- **Skills**: following Agent Skills progressive disclosure, only names and descriptions stay in context; the full `SKILL.md` is loaded with `skills.read` when needed. The owner can ask the Agent to install a complete `SKILL.md`, a public HTTP(S) source, or a ZIP package. Managed skills live under `.agents/skills/` in the Agent work directory, and uninstall moves them to `.trash/` for recovery.
- **MCP servers**: stdio and Streamable HTTP transports are supported. The owner can install, replace, enable, disable, or uninstall a server in natural language. Configuration is persisted to `.mcp.json` only after a successful connection test, and discovered tools become available in the current session immediately. `env` and `headers` values may reference `${ENV_VAR}`; the config is written with private file permissions. Self-installed stdio servers inherit only a minimal runtime environment, so credentials must be declared explicitly in `env`.

Mutation tools are exposed only to the owner and run only when the current user message explicitly asks for the change. Instructions from web pages, tool results, Skills, or MCP responses never count as user authorization. Other users can only use already-enabled capabilities within their existing permission scope.

Browser tools require Chrome/Chromium with a remote debugging port, for example:

```sh
chrome --remote-debugging-port=9222
```

Set `Agent work dir` to a dedicated reference directory. Avoid pointing it at directories that contain secrets or production data. Command execution is powerful; in production, set `DIANA_AGENT_COMMAND_ALLOWLIST` to only the commands you need.

## Manage Plugins In WebUI

Open the WebUI and go to the bot plugins section:

1. View official built-in plugins.
2. Built-in capabilities require no installation or removal; enable, disable, and configure them directly.
3. The built-in Go social media resolver extracts and sends images or videos from Bilibili, YouTube, X, Xiaohongshu, and Douyin. Size, duration, resolution, and gallery limits are configurable in the plugin settings. Zhihu, Weibo, and GitHub links only yield a title and description with no media download; the exclude-platform list labels each entry as either downloadable media or title only.

   Per-platform cookies, the yt-dlp cookie file, and the proxy address can all be filled in directly from the plugin settings, so there is no need to edit environment variables and restart the container. Values set here take precedence over the matching environment variables. Once saved, read endpoints only return a "configured" flag rather than the plaintext; submitting an empty value keeps the stored credential, and clearing one requires the "Clear" button next to the field.

   The top of the plugins page reports whether `yt-dlp`, `ffmpeg`, and `node` are present. Downloads for the affected platforms fail when one is missing.
4. The default built-in Go file parser handles QQ file segments and text file links, extracting file text as LLM context.
5. `Web search` is installed and enabled as a built-in capability. The model can call `web_search.search` directly, using the free Exa MCP endpoint first and an API-key-configured Tavily provider as fallback. It can be disabled or configured, but not installed or removed, and it remains independent of the local Agent switch.
6. The default built-in `LLM config skill` lets the owner change the active provider and model with natural language in chat, for example: `把提供商切到 gemini`, `把模型换成 gemini-2.5-pro`, or `以后用 anthropic 的 claude-sonnet-4-5`; requested models are validated against the backend model list before they are saved.

## Use Third-Party NoneBot Plugins

The Go process cannot directly load Python NoneBot plugins. To use third-party NoneBot2 plugins, run a separate NoneBot sidecar:

1. Install third-party plugins in your NoneBot2 project.
2. Configure the OneBot v11 reverse WebSocket driver in NoneBot.
3. Enable `NoneBot plugin bridge` in the Diana WebUI bot page.
4. The default `NoneBot reverse WebSocket` endpoint is:

```text
ws://127.0.0.1:8080/onebot/v11/ws
```

Diana forwards events received from the OneBot client to the NoneBot sidecar. When third-party plugins call OneBot APIs such as `send_msg` or `get_group_info`, Diana forwards those calls to the current OneBot v11 client. This keeps third-party plugins running in their native NoneBot2 environment.

## Common Environment Variables

| Variable | Default | Description |
| --- | --- | --- |
| `PORT` | `18080` | WebUI and OneBot endpoint listen port |
| `FRONTEND_DIST` | auto-detected | Frontend build output directory; defaults to `frontend-next/dist` |
| `LOG_PATH` | empty | Log file path; when set, logs are written to both stdout and the file |
| `DIANA_LOG_PATH` | empty | Compatibility alias for `LOG_PATH` |
| `DIANA_MEDIA_DIR` | `data/media` | Where inbound images are persisted; vision requests submit the local file as base64 |
| `DIANA_MEDIA_MAX_MB` | `10` | Per-image download limit |
| `DIANA_MEDIA_CACHE_MB` | `512` | Total cache size; least recently used files are evicted past this |
| `DIANA_LOCAL_MEDIA_BASE_URL` | this service's `/media/resolver` | Diana media URL reachable by the OneBot v11 client; use `http://diana:18080/media/resolver` for separate containers |
| `DIANA_BILI_SESSDATA` | empty | Bilibili `SESSDATA` cookie for protected content; WebUI plugin settings take precedence |
| `DIANA_DOUYIN_CK` | empty | Douyin cookie; required for Douyin resolution, WebUI plugin settings take precedence |
| `DIANA_XHS_CK` | empty | Xiaohongshu cookie; required for Xiaohongshu resolution, WebUI plugin settings take precedence |
| `DIANA_YTDLP_COOKIES` | empty | Path to a Netscape cookie file for yt-dlp; WebUI plugin settings take precedence |
| `DIANA_RESOLVER_PROXY` | empty | Proxy used by the social resolver and yt-dlp; WebUI plugin settings take precedence |
| `APP_DB_PATH` | `data/diana.db` | Local SQLite configuration database path |
| `DIANA_RELEASE_UPDATE_ENABLED` | `true` | Allow complete Release packages to download, verify, back up, and self-update; source and container deployments do not enable package replacement |
| `DIANA_ADMIN_USERNAME` | securely generated | Initial administrator username; defaults to `diana#` followed by 16 random characters, then SQLite credentials take precedence |
| `DIANA_ADMIN_PASSWORD` | securely generated | Initial administrator password; then SQLite credentials take precedence |
| `LLM_PROVIDER` | `openai_compatible` | LLM provider |
| `LLM_API_KEY` | empty | LLM API key |
| `LLM_BASE_URL` | empty | Custom OpenAI-compatible base URL |
| `LLM_MODEL` | empty | Model ID (no default for openai_compatible; pick in WebUI or set here) |
| WebUI LLM profiles | multi-profile | Supports named LLM configuration profiles and switching the active one |
| `LLM_USER_AGENT` | `codex-cli/0.142.0` | OpenAI-compatible User-Agent; can be used to mimic Codex CLI |
| `LLM_IMAGE_MODEL` | provider default | Image generation model; defaults to `gpt-image-1` for OpenAI-compatible and `imagen-4.0-generate-001` for Gemini |
| `LLM_TEMPERATURE` | empty | temperature |
| `LLM_MAX_OUTPUT_TOKENS` | `1024` | Responses API maximum output tokens |
| `LLM_TIMEOUT_MS` | `30000` | LLM request timeout in milliseconds |
| `QQBOT_ENABLED` | `false` | Enable the bot automatically on startup |
| `ONEBOT_REVERSE_WS_ENDPOINT` | `ws://127.0.0.1:<PORT>/onebot/v11/ws` | Reverse WebSocket URL for the OneBot v11 client |
| `ONEBOT_ACCESS_TOKEN` | empty | OneBot access token |
| `NONEBOT_BRIDGE_ENABLED` | `false` | Enable the third-party NoneBot plugin bridge |
| `NONEBOT_BRIDGE_ENDPOINT` | `ws://127.0.0.1:8080/onebot/v11/ws` | Reverse WebSocket endpoint for the NoneBot sidecar |
| `NONEBOT_BRIDGE_TOKEN` | empty | NoneBot bridge access token |
| `QQBOT_QQ` | empty | Bot QQ number |
| `DIANA_OWNER_ID` | empty | Owner QQ number |
| `DIANA_GROUP_TRIGGERS` | `嘉然,然然,Diana,diana` | Group chat trigger aliases |
| `DIANA_SYSTEM_PROMPT` | built-in prompt | Bot system prompt |
| `DIANA_MAX_INPUT_CHARS` | `2000` | Max input characters per request |
| `DIANA_MAX_REPLY_CHARS` | `3500` | Max reply characters per request |
| `DIANA_DIRECT_REPLY_CHUNK_SIZE` | `500` | Text chunk size for direct sends |
| `DIANA_MAX_BOT_CONCURRENCY` | `5` | Global concurrency |
| `DIANA_AGENT_ENABLED` | `false` | Enable the built-in Agent |
| `DIANA_AGENT_WORK_DIR` | `.` | Working directory available to Agent tools |
| `AGENT_WORK_DIR` | `.` | Compatibility alias for `DIANA_AGENT_WORK_DIR` |
| `DIANA_AGENT_MAX_STEPS` | `4` | Max Agent tool-loop steps per reply, capped at `8` |
| `DIANA_AGENT_COMMAND_ALLOWLIST` | common dev commands | Commands available to Agent `run_command`, comma-separated; `*` allows all commands |
| `DIANA_AGENT_COMMAND_TIMEOUT_MS` | `10000` | Local command timeout, capped at `60000` |
| `DIANA_AGENT_SKILL_ROOTS` | `.agents/skills,skills` | Comma-separated Skill search roots; self-installed packages always go to `.agents/skills` under the Agent work directory |
| `DIANA_AGENT_MCP_CONFIG` | `.mcp.json` | MCP server config path; relative paths resolve from the Agent work directory |
| `DIANA_AGENT_BROWSER_CDP_URL` | `http://127.0.0.1:9222` | Chrome DevTools URL for browser tools |
| `AGENT_BROWSER_CDP_URL` | same | Compatibility alias for `DIANA_AGENT_BROWSER_CDP_URL` |
| `DIANA_AGENT_BROWSER_TIMEOUT_MS` | `15000` | Browser tool timeout, capped at `60000` |

## systemd Example

Create the log directory first:

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
Environment=LLM_USER_AGENT=codex-cli/0.142.0
ExecStart=/opt/diana/diana-webui
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

## Development Commands

Backend tests:

```sh
go test ./...
```

Frontend development:

```sh
cd frontend-next
npm run dev
```

Production build:

```sh
cd frontend-next
npm run build
cd ..
go build -o dist/diana-webui ./cmd/webui
```

## Project Layout

```text
.
├── cmd/webui/              # Gin WebUI and OneBot endpoint entrypoint
├── frontend-next/          # Supported Vue + TypeScript WebUI
├── model/llm/              # Unified LLM interface and provider adapters
├── model/assistant/            # QQ bot runtime, OneBot channel, and plugin system
├── webui/                  # WebUI API handlers
├── .github/workflows/      # GitHub Actions CI/CD
├── LICENSE
└── go.mod
```

## License

This project uses the `Limited Redistribution License (SuInk)`. See [LICENSE](./LICENSE).
