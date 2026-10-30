<div align="center">

# Diana

**An AI agent that lives in your group chats — it chimes in without being @-mentioned, and it can search the web, play song requests, send stickers, and remember what your group agreed on. Self-hosted, single binary, your data never leaves your machine.**

<sub>OneBot v11 · Telegram · QQ Official Bot · WeChat · iMessage — one instance, all online at once</sub>

[![CI](https://github.com/SuInk/Diana/actions/workflows/ci.yml/badge.svg)](https://github.com/SuInk/Diana/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/SuInk/Diana?color=c83f76)](https://github.com/SuInk/Diana/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Limited%20Redistribution-informational)](./LICENSE)

[Website & Docs](https://suink.github.io/Diana/) · [Live Demo](https://suink.github.io/Diana/demo/) · [Latest Release](https://github.com/SuInk/Diana/releases/latest) · [中文](./README.md)

[![QQ Group 1127671893](https://img.shields.io/badge/QQ%20Group-1127671893-12B7F5?logo=qq&logoColor=white)](https://qm.qq.com/q/4o6zEBs4h2)

</div>

<br />

<img src="./docs/assets/diana-webui-overview.png" alt="Diana console overview: channel status, message stats, system resources and live event stream" width="100%" />

## What is Diana

You have an AI you want in your group chats: joining the conversation in a QQ group, taking song requests on Telegram, answering questions for your group. Diana is the layer in between — a single Go binary that talks to LLMs on one side (OpenAI-compatible APIs, Gemini, Anthropic) and to five chat platforms on the other, with a web console managing everything in the middle.

It's built for people who want to run their own bot:

- **Works out of the box** — one command to install, a few clicks in the browser to configure. No code required.
- **Your data stays yours** — configuration, chat memory, and logs all live in a local SQLite database. No cloud dependencies.
- **Every step is explainable** — why the bot replied, why it stayed quiet, which model it called, how many tokens it spent: all visible in the console's event center.
- **Batteries included** — web search, link/file parsing, song requests, stickers, OCR, and long-term memory are built in, with switches in the console.

## Up and running in three minutes

**① Install.** Copy one command and run it; the script downloads, verifies and starts Diana:

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
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

**Or install with a prompt.** Copy this into an AI assistant that can run commands on the target machine:

```text
Help me install the latest stable release of Diana on this machine. First read the README at https://github.com/SuInk/Diana and scripts/install.sh (Linux/macOS) or scripts/install.ps1 (Windows) in that repository. Detect the OS and architecture, then use the official one-click installer. Default to current-user permissions and listen only on 127.0.0.1. If Diana is already installed, preserve its configuration and data and follow the official upgrade procedure. Run diana doctor afterward to check its health, tell me the console URL, administrator username, and password initially generated during installation, and remind me to store them securely. For an existing installation with no newly generated password, explain that the original password still applies and do not reset it automatically. Leave model API keys and bot credentials for me to enter in the console.
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

Every enabled bot profile is online at the same time, and replies always go back to the channel the message came from.

| Platform | Credentials you need | Connection direction |
| --- | --- | --- |
| **OneBot v11** (SnowLuma recommended) | Point your OneBot client's reverse WebSocket at Diana and agree on an access token | Client → Diana, no public address needed |
| **Telegram** | Bot Token from BotFather (a proxy address is often needed from mainland China) | Diana connects outbound, no public address needed |
| **QQ Official Bot** | AppID + AppSecret from the open platform (sandbox available before listing) | Diana connects outbound, no public address needed |
| **WeChat** | Nothing to fill in: save the bot, then scan the QR code under Connect (Tencent iLink Bot, private chats only) | Diana connects outbound, no public address needed |
| **iMessage** | Address and password of BlueBubbles Server on an always-on Mac; see the [iMessage setup guide](docs/imessage.md) (Chinese) | BlueBubbles webhook → Diana, the Mac only needs to reach Diana; optional polling fallback |

For iMessage, the Bots page shows the exact callback URL to paste into BlueBubbles Server. Platform-specific quirks (e.g. QQ Official Bot only receives @-mentions by default; the group owner has to change the bot's group message scope) are covered in the [configuration docs](https://suink.github.io/Diana/configuration.html).

## Built-in abilities

These ship with the binary — toggle and configure them in the console, no plugin installation needed:

- **Proactive chatting** — it decides on its own when to chime in and when to stay quiet, no @-mention required; every reply-or-not decision is logged in the event center (requires the platform to deliver all group messages — on QQ Official Bot the group owner has to widen the bot's group message scope first).
- **Web search** — time-sensitive questions get researched before answering; Exa first, Tavily as fallback.
- **Link parsing** — videos and images from Bilibili / YouTube / X / Xiaohongshu / Douyin are resolved and posted right into the chat.
- **File parsing** — group files, PDF, Office, and EPUB content is extracted and handed to the model.
- **Song requests** — NetEase Cloud Music and QQ Music are tried in turn. OneBot QQ sends voice messages. Telegram uploads MP3 files with title and artist metadata for its native music player; no QQ music-card signing service is needed. Unavailable audio or preparation errors are reported as failures. QQ Official Bot currently provides song-page links, not an in-chat player.
- **Stickers** — picks a fitting sticker from the ones it has seen in the chat.
- **Image understanding** — vision model + OCR (fully offline local options available), so even text-only models can "see" images.
- **Rendered images** — tables, flowcharts, and sequence diagrams that plain text mangles get rendered to a picture when the model judges it worth it (Markdown / Mermaid / SVG; needs the headless browser from the "web rendering" plugin).
- **Memory & notebook** — layered memory keeps token costs down; important things (group rules, dietary restrictions, promises) go into a notebook that's auditable, editable, and recoverable.
- **Per-group policies** — reply hours, allow/deny lists, persona, and tool permissions, configured per group.
- **Built-in agent** — a minimal tool loop with file, command, and browser tools; loads Skills and MCP servers on demand.
- **Coding agent** — hand code changes to a Claude Code / Codex CLI on the host: long-running edits and test runs inside registered repositories, progress on request, a report when it finishes, and jobs that survive a Diana restart. Dangerous steps like pushing or publishing stop and ask you in chat, continuing only once you reply with the code. Owner-only, off by default.

Full documentation for each ability is in the [configuration docs](https://suink.github.io/Diana/configuration.html).

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
- API keys, tokens, and OAuth credentials are never returned in plaintext once stored — read endpoints only say "configured".
- Behind a reverse proxy you must declare `trusted_proxies` in `config.yaml` (or set the `DIANA_TRUSTED_PROXIES` environment variable for the process); otherwise rate limiting and logs will see the proxy's address as every client's IP.
- With an owner account configured, "quick login" is available: grab a 6-digit one-time code from the login page and DM it to the bot; the bot replies with the source IP and device, so a hijacked code is spotted immediately.

More detail (session management, exempted paths, agent command sandbox) in the [configuration docs](https://suink.github.io/Diana/configuration.html).

## Contributing

Backend in Go (Gin + SQLite), frontend in Vue 3 + TypeScript (`frontend/`). Full dev environment locally:

```sh
make dev        # backend :18080 + Vite frontend :5173, hot reload
go test ./...   # backend tests
```

Before committing, make sure `gofmt` is clean, `go mod tidy` produces no diff, and backend tests plus frontend `vue-tsc` pass — CI enforces all of these. Project conventions are in [AGENTS.md](./AGENTS.md).

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
