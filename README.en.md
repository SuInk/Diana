<div align="center">

# Diana

**An AI agent that lives in your group chats — it chimes in without being @-mentioned, and it can search the web, play song requests, send stickers, and remember what your group agreed on. Self-hosted, single binary, your data never leaves your machine.**

<sub>OneBot v11 · Telegram · QQ Official Bot · DingTalk · Feishu · WeCom — one instance, all online at once</sub>

[![CI](https://github.com/SuInk/Diana/actions/workflows/ci.yml/badge.svg)](https://github.com/SuInk/Diana/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/SuInk/Diana?color=c83f76)](https://github.com/SuInk/Diana/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Limited%20Redistribution-informational)](./LICENSE)

[Website & Docs](https://suink.github.io/Diana/) · [Live Demo](https://suink.github.io/Diana/demo/) · [Latest Release](https://github.com/SuInk/Diana/releases/latest) · [中文](./README.md)

</div>

<br />

<img src="./docs/assets/diana-webui-overview.png" alt="Diana console overview: channel status, message stats, system resources and live event stream" width="100%" />

## What is Diana

You have an AI you want in your group chats: joining the conversation in a QQ group, taking song requests on Telegram, answering coworkers on Feishu. Diana is the layer in between — a single Go binary that talks to LLMs on one side (OpenAI-compatible APIs, Gemini, Anthropic) and to six chat platforms on the other, with a web console managing everything in the middle.

It's built for people who want to run their own bot:

- **Works out of the box** — one command to install, a few clicks in the browser to configure. No code required.
- **Your data stays yours** — configuration, chat memory, and logs all live in a local SQLite database. No cloud dependencies.
- **Every step is explainable** — why the bot replied, why it stayed quiet, which model it called, how many tokens it spent: all visible in the console's event center.
- **Batteries included** — web search, link/file parsing, song requests, stickers, OCR, and long-term memory are built in, with switches in the console.

## Up and running in three minutes

**① Install.** One command — the script detects your OS and architecture, downloads the latest release, verifies SHA-256, and starts the service:

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sudo sh
```

The recommended sudo installation uses the fixed `/opt/diana` directory and creates `/usr/local/bin/diana`. Without sudo access, run the command without sudo; the installer asks before falling back to a current-user installation, or set `DIANA_INSTALL_SCOPE=user` explicitly.

```powershell
# Windows PowerShell
irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1 | iex
```

```sh
# Docker (prebuilt image, no need to clone the repo)
docker run -d --name diana --restart unless-stopped \
  -p 18080:18080 \
  -v "$PWD/data:/app/data" \
  -v "$PWD/logs:/app/logs" \
  ghcr.io/suink/diana:latest
```

**② Log in to the console.** Open `http://127.0.0.1:18080`. The admin username and password are in the terminal output (for Docker, check `docker logs diana`; script installs also write them to `config.yaml` in the install directory — don't share that file).

**③ Configure.** Three things in the console:

1. On the **LLM** page, enter your API key, sync the model list, and pick a default model;
2. On the **Bots** page, choose a platform and fill in its credentials (see [Supported platforms](#supported-platforms) for what each one needs);
3. Send the bot a message — direct messages always trigger it; in groups, @-mention it or start with a trigger word.

That's it. No reply? The event center tells you why; `diana doctor` checks service health.

> [!TIP]
> **Installing on a server?** Diana only listens on localhost by default. To reach the console from another machine, set `DIANA_HOST` at install time:
>
> ```sh
> curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sudo env DIANA_HOST=0.0.0.0 sh
> ```
>
> ```powershell
> $env:DIANA_HOST="0.0.0.0"; irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1 | iex
> ```
>
> Already installed? Re-run the command with `DIANA_HOST` to change the binding. Once exposed, always put an HTTPS reverse proxy in front and open the port in your firewall — the console has admin privileges; don't leave it naked on the public internet.

<details>
<summary>Docker details / manual download / building from source</summary>

**Docker:** an image is published with every release (`ghcr.io/suink/diana:latest` plus version tags). OneBot clients connect to `ws://<docker-host>:18080/onebot/v11/ws`. To pre-seed configuration (unattended deployments), mount your `config.yaml` read-only at `/app/config.yaml`; the repo also ships a `docker-compose.yml` for local builds. To upgrade, pull the new image and recreate the container — your data lives in the mounted `data/` directory.

**Manual download:** grab the **full package** for your platform (`.tar.gz` / `.zip`, includes frontend assets and launch scripts) from [Releases](https://github.com/SuInk/Diana/releases), verify `SHA256SUMS`, then run `run.sh` / `run.bat`. The bare binaries don't include the frontend; they're for custom deployments.

**From source:** requires Go 1.26+ and Node.js 22.

```sh
git clone https://github.com/SuInk/Diana.git && cd Diana
cd frontend-next && npm ci && npm run build && cd ..
go build -o dist/diana-webui ./cmd/webui
./dist/diana-webui
```

More deployment options (systemd, cross-compiling, unattended pre-seeded config) are covered in the [deployment docs](https://suink.github.io/Diana/deploy.html).

</details>

## Supported platforms

Every enabled bot profile is online at the same time, and replies always go back to the channel the message came from.

| Platform | Credentials you need | Connection direction |
| --- | --- | --- |
| **OneBot v11** (NapCat, Lagrange.Core, go-cqhttp, …) | Point your OneBot client's reverse WebSocket at Diana and agree on an access token | Client → Diana, no public address needed |
| **Telegram** | Bot Token from BotFather (a proxy address is often needed from mainland China) | Diana connects outbound, no public address needed |
| **QQ Official Bot** | AppID + AppSecret from the open platform (sandbox available before listing) | Diana connects outbound, no public address needed |
| **DingTalk** | App Client ID + Client Secret (Stream mode) | Diana connects outbound, no public address needed |
| **Feishu** | App ID + App Secret + Verification Token (plus Encrypt Key if encrypted push is on) | Platform callback → Diana, **public address required** |
| **WeCom** | Corp ID + AgentId + Secret + callback Token/EncodingAESKey | Platform callback → Diana, **public address required** |

For Feishu and WeCom, the Bots page shows the exact callback URLs to paste into the platform's admin console. Platform-specific quirks (e.g. QQ Official Bot only receives @-mentions, voice playback is OneBot-only) are covered in the [configuration docs](https://suink.github.io/Diana/configuration.html).

## Built-in abilities

These ship with the binary — toggle and configure them in the console, no plugin installation needed:

- **Proactive chatting** — it decides on its own when to chime in and when to stay quiet, no @-mention required; every reply-or-not decision is logged in the event center (requires the platform to deliver all group messages — QQ Official Bot and DingTalk only push @-mentions, so it doesn't apply there).
- **Web search** — time-sensitive questions get researched before answering; Exa first, Tavily as fallback.
- **Link parsing** — videos and images from Bilibili / YouTube / X / Xiaohongshu / Douyin are resolved and posted right into the chat.
- **File parsing** — group files, PDF, Office, and EPUB content is extracted and handed to the model.
- **Song requests** — "play Sunny Day" gets you a voice message; NetEase Cloud Music, QQ Music, and Kugou are tried in turn.
- **Stickers** — picks a fitting sticker from the ones it has seen in the chat.
- **Image understanding** — vision model + OCR (fully offline local options available), so even text-only models can "see" images.
- **Rendered images** — tables, flowcharts, and sequence diagrams that plain text mangles get rendered to a picture when the model judges it worth it (Markdown / Mermaid / SVG; needs the headless browser from the "web rendering" plugin).
- **Memory & notebook** — layered memory keeps token costs down; important things (group rules, dietary restrictions, promises) go into a notebook that's auditable, editable, and recoverable.
- **Per-group policies** — reply hours, allow/deny lists, persona, and tool permissions, configured per group.
- **Built-in agent** — a minimal tool loop with file, command, and browser tools; loads Skills and MCP servers on demand.
- **Outbound API** — let CI or monitoring scripts speak through your bot (off by default, Bearer-key auth).

Full documentation for each ability is in the [configuration docs](https://suink.github.io/Diana/configuration.html).

## Day-to-day management

After installation the `diana` command is on your PATH:

```sh
diana status    # health, version, address
diana logs -f   # follow the logs
diana restart   # restart the service
diana doctor    # check config, directories, frontend assets, service health
```

**Upgrading**: re-run the install command, or click upgrade in the console. Both paths back up your data and verify the new version first, and roll back automatically if the health check fails. Docker deployments upgrade by pulling the new image and recreating the container.

**Uninstalling**: `diana uninstall` removes the service and program but keeps your data (reinstall to pick up where you left off); `diana uninstall --purge` deletes the data too — irreversible, with a second confirmation.

## Where settings live

One rule of thumb: **everyday settings are changed in the web console; `config.yaml` only covers the service itself.**

`config.yaml` (in the install directory) handles infrastructure: listen address, port, data paths, initial admin password — changes require a restart. Bot and model configuration lives in the database and takes effect immediately when changed in the console. The `bot:` / `llm:` sections of `config.yaml` are seeded **once**, on first startup with an empty database, and are ignored afterwards (the startup log says so explicitly) — they exist for unattended deployments.

Every field is documented in [`config.example.yaml`](./config.example.yaml).

## Security notes

- The console requires login from first startup; there are no anonymous endpoints. Failed logins back off and lock per source IP to resist brute force.
- API keys, tokens, and OAuth credentials are never returned in plaintext once stored — read endpoints only say "configured".
- Behind a reverse proxy you must declare `trusted_proxies` in `config.yaml` (or set the `DIANA_TRUSTED_PROXIES` environment variable for the process); otherwise rate limiting and logs will see the proxy's address as every client's IP.
- With an owner account configured, "quick login" is available: grab a 6-digit one-time code from the login page and DM it to the bot; the bot replies with the source IP and device, so a hijacked code is spotted immediately.

More detail (session management, exempted paths, agent command allowlist) in the [configuration docs](https://suink.github.io/Diana/configuration.html).

## Contributing

Backend in Go (Gin + SQLite), frontend in Vue 3 + TypeScript (`frontend-next/`). Full dev environment locally:

```sh
make dev        # backend :18080 + Vite frontend :5173, hot reload
go test ./...   # backend tests
```

Before committing, make sure `gofmt` is clean, `go mod tidy` produces no diff, and backend tests plus frontend `vue-tsc` pass — CI enforces all of these. Project conventions are in [AGENTS.md](./AGENTS.md).

## Documentation

| | |
| --- | --- |
| [Deployment](https://suink.github.io/Diana/deploy.html) | All install methods, server deployment, first login |
| [Configuration](https://suink.github.io/Diana/configuration.html) | Channels, model assignment, group policies, built-in abilities, security boundaries |
| [Implementation](https://suink.github.io/Diana/implementation.html) | Architecture, message decision pipeline, memory layers |
| [Operations](https://suink.github.io/Diana/operations.html) | Updates and rollback, log backup, troubleshooting |
| [Live Demo](https://suink.github.io/Diana/demo/) | The real console with simulated data — click around |

## License

`Limited Redistribution License (SuInk)` — see [LICENSE](./LICENSE).
