<div align="center">

# Diana

[简体中文](./README.md) · **English** · [日本語](./README.ja.md) · [한국어](./README.ko.md) · [Français](./README.fr.md) · [Deutsch](./README.de.md) · [Nederlands](./README.nl.md)

**Talks like a human, works like an agent.**

A self-hosted, single-binary AI agent — knows when to chime in and when to stay quiet without being @-mentioned; handles memory, vision, links, songs, web search, Skills / MCP, and local coding agents while keeping all data and decisions on your machine.

<sub>OneBot v11 · Telegram · QQ Official Bot · WeChat · iMessage · Built-in Console — one instance, all online at once</sub>

[![CI](https://github.com/SuInk/Diana/actions/workflows/ci.yml/badge.svg)](https://github.com/SuInk/Diana/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/SuInk/Diana?color=159f6c)](https://github.com/SuInk/Diana/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Limited%20Redistribution-informational)](./LICENSE)

[Website & Docs](https://suink.github.io/Diana/en/) · [Live Demo](https://suink.github.io/Diana/demo/) · [Latest Release](https://github.com/SuInk/Diana/releases/latest)

[![QQ Group 1127671893](https://img.shields.io/badge/QQ%20Group-1127671893-12B7F5?logo=qq&logoColor=white)](https://qm.qq.com/q/4o6zEBs4h2)

</div>

<br />

<img src="./docs/assets/diana-webui-overview-green.jpg" alt="Diana console overview: channel status, message stats, system resources and live event stream" width="100%" />

## Why Diana

Most self-hosted chat bots fall into two extremes: either a toy that stays dead until @-mentioned (or spams the chat the moment proactive replies are enabled), or a tangle of Python/Node runtimes and YAML files that goes silent with zero clue why.

Diana packs natural chat presence and real agent capabilities into a single Go binary — connecting major LLMs (OpenAI-compatible APIs, Gemini, Anthropic) to the chat apps you already use, managed through a built-in web console.

- **Knows when to speak — and when not to** — chimes into group chats naturally without being @-mentioned, remembers group norms and preferences in layered memory + an editable notebook, and handles images, video links, songs, and stickers.
- **Gets real work done inside chat** — built-in web search, file parsing, and diagram rendering out of the box, plus an agent loop with Skills, MCP, browser tools, and local Claude Code / Codex coding agents.
- **Up in minutes, zero guesswork** — one command to install, outbound connections with no public IP needed, browser-based setup, and an event center explaining why every message was answered or skipped and how many tokens it cost.
- **Your data, clear security boundaries** — config, memory, and logs stay in local SQLite with no cloud dependency; **Safe Mode** isolates host commands, file writes, and coding agents in one switch to guard against prompt injection.

## Up and running in three minutes

**① Install.** Copy one command and run it; the script downloads, verifies and starts Diana:

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

```sh
# Android Termux (ARM64 only; run inside Termux, no root required)
pkg install -y curl tar coreutils && curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

```powershell
# Windows PowerShell
irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1 | iex
```

```sh
# Or Docker (run in the directory where you want the data)
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/docker.sh | sh
```

The native installer needs no administrator privileges. **To upgrade, rerun the same command**; if you installed with sudo before, the script picks up that installation automatically. More install options are in the [deployment guide](https://suink.github.io/Diana/en/deploy.html).

**Or install with a skill.** Paste this into an AI assistant that can run commands on the target machine; full instructions live in the [install skill](skills/diana-install/SKILL.md) and stay in sync with each release:

```text
Read https://raw.githubusercontent.com/SuInk/Diana/main/skills/diana-install/SKILL.md and follow it to install the latest stable release of Diana on this machine.
```

**Or download it yourself.** If you'd rather not run a script, grab the file for your platform (always the latest stable release):

| Platform | Download | How to run |
| --- | --- | --- |
| Windows x64 | [diana-windows-amd64.exe](https://github.com/SuInk/Diana/releases/latest/download/diana-windows-amd64.exe) | Put it in a folder of its own and double-click it; data goes in the `data` folder beside it |
| macOS Apple silicon | [diana-macos-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-macos-arm64.tar.gz) | Extract, then run `./run.sh` |
| macOS Intel | [diana-macos-amd64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-macos-amd64.tar.gz) | Extract, then run `./run.sh` |
| Linux x64 | [diana-linux-amd64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-linux-amd64.tar.gz) | Extract, then run `./run.sh` |
| Linux ARM64 | [diana-linux-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-linux-arm64.tar.gz) | Extract, then run `./run.sh` |
| Android (Termux) | [diana-android-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-android-arm64.tar.gz) | Extract inside Termux and run `./run.sh`. It is the server running on your phone, not an Android app; the Linux one-click command above also works in Termux |

The WebUI is built into the program, so no Node.js is needed. Each file's SHA-256 is listed in the same release's [`latest.json`](https://github.com/SuInk/Diana/releases/latest/download/latest.json) and shown next to the file on the Releases page; older versions are on the [Releases](https://github.com/SuInk/Diana/releases) page. A manual download does not register the `diana` command; use `./diana-webui <command>` (Windows: `diana-webui.exe <command>`) instead.

**② Log in to the console.** Open `http://127.0.0.1:18080`. The admin username and password are in the terminal output and shown only once, so save them right away (for Docker, check `docker logs diana`). The password is stored only as a hash in the database, not in `config.yaml`; if you forget it, reset it with `diana passwd` (see below).

**③ Configure.** Three things in the console:

1. On the **LLM** page, enter your API key, sync the model list, and pick a default model;
2. On the **Bots** page, choose a platform and fill in its credentials (see [Supported platforms](#supported-platforms) for what each one needs);
3. Send the bot a message — direct messages always trigger it; in groups, @-mention it or start with a trigger word.

That's it. No reply? The event center tells you why; `diana doctor` checks service health.

> [!TIP]
> **Can't open the console from another computer?** By default only this machine can connect. Rerun the install command with `-s -- --host 0.0.0.0` appended (Windows: `-BindHost 0.0.0.0`); see the [deployment guide](https://suink.github.io/Diana/en/deploy.html). Once exposed, put an HTTPS reverse proxy in front; don't leave the console open on the public internet.


## Supported platforms

Every enabled bot profile is online at the same time, and replies always go back to the channel the message came from. Home or LAN setups work with no public IP required.

| Platform | Credentials you need | Connection direction |
| --- | --- | --- |
| **OneBot v11** (SnowLuma recommended) | Supports reverse WS, forward WS, and HTTP API + event push — pick a mode on the Bots page | Depends on mode; works over LAN |
| **Telegram** | Bot Token from BotFather (a proxy address is often needed from mainland China) | Diana connects outbound, no public address needed |
| **QQ Official Bot** | AppID + AppSecret from the open platform (sandbox available before listing) | Diana connects outbound, no public address needed |
| **WeChat** | Nothing to fill in: save the bot, then scan the QR code under Connect (Tencent iLink Bot, private chats only) | Diana connects outbound, no public address needed |
| **iMessage** | Address and password of BlueBubbles Server on an always-on Mac; see the [iMessage setup guide](docs/imessage.md) (Chinese) | BlueBubbles webhook → Diana, the Mac only needs to reach Diana; optional polling fallback |
| **Built-in Bot** | Nothing to fill in: chat or start a voice call right inside the console's Chat page | Console-only; zero-friction way to try Diana |

For iMessage, the Bots page shows the exact callback URL to paste into BlueBubbles Server. Platform-specific quirks (e.g. QQ Official Bot only receives @-mentions by default; the group owner has to change the bot's group message scope) are covered in the [configuration docs](https://suink.github.io/Diana/en/configuration.html).

## Built-in abilities

These ship with the single binary — toggle and configure them in the console, no plugin installation needed:

- **Proactive chatting** — decides on its own when to chime in and when to stay quiet, no @-mention required; every reply-or-not decision is logged in the event center (requires full group message delivery).
- **Memory & notebook** — layered memory keeps token costs down; important things (group rules, dietary restrictions, promises) go into a notebook that's auditable, editable, and recoverable.
- **Built-in agent & Safe Mode** — tool loop with file, command, and browser tools plus Skills and MCP servers on demand; toggle **Safe Mode** per bot to disable host commands, file writes, coding agents, and logged-in browsers against prompt injection.
- **Coding agent** — owner-only, off by default: dispatch long-running code edits and tests to local Claude Code / Codex CLIs across registered repos; jobs survive restarts and pause in chat for confirmation before dangerous actions.
- **Web search** — time-sensitive questions are researched in parallel via a sandboxed browser by default, with global primary/fallback provider settings (Exa, Tavily, or custom).
- **Link & file parsing** — resolves Bilibili / YouTube / X / Xiaohongshu / Douyin media and X profiles, plus group files, PDF, Office, and EPUB documents.
- **Vision, OCR & rendered images** — vision models plus offline OCR let even text-only models "see" images; tables, flowcharts, and sequence diagrams are automatically rendered to images (Markdown / Mermaid / SVG) with on-demand font downloads.
- **Songs, stickers & group policies** — multi-source song requests (voice on OneBot QQ, native MP3 player on Telegram), contextual sticker replies, and per-group reply hours, allow/deny lists, personas, and tool permissions.

Full documentation for each ability is in the [configuration docs](https://suink.github.io/Diana/en/configuration.html).

## Day-to-day management

After installation the `diana` command is on your PATH:

```sh
diana status    # health, version, address
diana logs -f   # follow the logs
diana restart   # restart the service
diana doctor    # check config, directories, frontend assets, service health
```

For Docker, use `docker exec diana diana <command>` on the host, e.g. `docker exec diana diana logs -f`.

**Forgot the password**: the password is stored only as a hash in the database, so it cannot be recovered anywhere — reset it instead. For one-click installs (Linux / macOS / Windows) just run:

```sh
diana passwd
```

It stops the service, generates a new random password and shows it once, then starts the service again; every browser session is signed out, and bots, models, plugins and all other data are left untouched. It asks for `y` before changing anything; pass `-y` to skip the prompt (required when there is no terminal, e.g. in scripts). Add `--username NEW_NAME` to rename the account at the same time.

Docker cannot stop itself from inside the container, so run this in the deployment directory:

```sh
docker compose stop diana
docker compose run --rm diana passwd
docker compose start diana
```

A process you started by hand (`run.sh`, or double-clicking the .exe) is left alone: stop it yourself, then run `diana passwd`.

**Upgrading**: for complete Release packages, re-run the install command or click upgrade in the console; both paths back up data, verify the package and roll back if the health check fails. Docker deployments pull and recreate the container, either on the host or through the optional updater. Docker image updates do not automatically roll back after a failed health check; restore a known version tag on the host if needed.

**Uninstalling**: `diana uninstall` removes the service and program but keeps your data (reinstall to pick up where you left off); `diana uninstall --purge` deletes the data too — irreversible, with a second confirmation.

## Where settings live

One rule of thumb: **everyday settings are changed in the web console; `config.yaml` only covers the service itself.**

`config.yaml` (in the install directory) handles infrastructure: listen address, port, data paths — changes require a restart. The `admin:` section can preset the first administrator's username and password; it only applies while the database has no administrator, and the one-click installer removes the plain-text password once Diana has started. Bot and model configuration lives in the database and takes effect immediately when changed in the console. The `bot:` / `llm:` sections of `config.yaml` are seeded **once**, on first startup with an empty database, and are ignored afterwards (the startup log says so explicitly) — they exist for unattended deployments.

Every field is documented in [`config.example.yaml`](./config.example.yaml).

## Security notes

- The console requires login from first startup; there are no anonymous endpoints. Failed logins back off and lock per source IP to resist brute force.
- API keys and tokens are never returned in plaintext once stored — read endpoints only say "configured".
- Behind a reverse proxy you must declare `trusted_proxies` in `config.yaml` (or set the `DIANA_TRUSTED_PROXIES` environment variable for the process); otherwise rate limiting and logs will see the proxy's address as every client's IP.
- With an owner account configured, "quick login" is available: grab a 6-digit one-time code from the login page and DM it to the bot; the bot replies with the source IP and device, so a hijacked code is spotted immediately.

More detail (session management, exempted paths, agent command sandbox) in the [configuration docs](https://suink.github.io/Diana/configuration.html).

## Contributing

Backend in Go (Gin + SQLite), frontend in Vue 3 + TypeScript (`frontend/`). Full dev environment locally:

```sh
make dev        # backend :18080 + Vite frontend :5173, hot reload
go test ./...   # backend tests
```

Before committing, make sure `gofmt` is clean, `go mod tidy` produces no diff, and backend tests plus frontend `vue-tsc` pass — CI enforces all of these. See [CONTRIBUTING.md](./CONTRIBUTING.md) (in Chinese) for the full guide to the dev environment, tests, and PR requirements; release conventions are in [AGENTS.md](./AGENTS.md).

### Pull requests

PRs are very welcome! Bug fixes, docs, and features are all fair game; for larger changes, please open an issue first so we can agree on the approach.

- **One change per PR.** Branch off the latest `main` and keep unrelated edits and reformatting out.
- **Title describes the actual change**, as `type(scope): user-visible change`, e.g. `fix(onebot): add WebSocket heartbeat so dead connections stop holding the only slot`. Common types: `feat` / `fix` / `security` / `docs` / `chore`. Don't title a PR with just a version or number.
- **Body covers** what changed, why, the impact on users or developers, and how you verified it; bug fixes also state the root cause. Be upfront about anything you didn't actually run.
- **Merge only on green CI**: Go tests, frontend build, cross-platform builds, and Docker images must all pass. Update `docs/` or the README when behavior changes.
- When referencing another PR, include its title or a one-line summary, not just a bare `#123`.
- No agent attribution trailers such as `Co-Authored-By` in the title or body.

## Documentation

| | |
| --- | --- |
| [Deployment](https://suink.github.io/Diana/deploy.html) | All install methods, server deployment, first login |
| [Configuration](https://suink.github.io/Diana/configuration.html) | Channels, model assignment, group policies, built-in abilities, security boundaries |
| [Implementation](https://suink.github.io/Diana/implementation.html) | Architecture, message decision pipeline, memory layers |
| [Pipeline](https://suink.github.io/Diana/en/pipeline.html) | Reply triggers and backfill, reply flow, agent runs, browsers, pre-send review, with flowcharts |
| [Operations](https://suink.github.io/Diana/operations.html) | Updates and rollback, log backup, troubleshooting |
| [Live Demo](https://suink.github.io/Diana/demo/) | The real console with simulated data — click around |

## License

`Limited Redistribution License (SuInk)` — see [LICENSE](./LICENSE).
