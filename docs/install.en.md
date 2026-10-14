# Installation and upgrades

The README shows only the most common command. This page covers the details.

## Native install (Linux / macOS)

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

By default, no administrator privileges are needed: Diana installs to `~/.local/share/diana`, creates `~/.local/bin/diana`, and configures your user PATH for new terminals. Use `--dir PATH` to choose a writable directory.

**One Diana per machine.** If `/opt/diana` already exists (an older or `sudo` install), just rerun the same command: the installer upgrades it in place and reruns itself with `sudo` (you'll be asked for a password; without `sudo` it prints a copyable command) instead of creating a second per-user install. To do it by hand: `curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sudo sh -s -- --dir /opt/diana --yes`.

- Personal machine: a user install is fine; the service belongs to the current user, so another login is treated as another machine.
- Server or shared machine: use a system install (`… | sudo sh -s -- --scope system`); the service runs as the installing user and survives logout.
- On Linux a user install is a `systemctl --user` service: the installer tries `loginctl enable-linger` and, if that is refused, tells you to run `sudo loginctl enable-linger $USER`; otherwise the service stops at logout and does not start at boot.
- If another user's directory holds an installation the current user can read, the installer warns (two instances fight for the port and split the database); upgrade that one or migrate it first.

Installation options now use interactive choices or command-line arguments (`--yes` / `-Yes` for unattended use); legacy installer environment variables are no longer read. Select application configuration with `--config PATH`; existing YAML files and databases remain usable.

## Windows

```powershell
irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1 | iex
```

Windows installs to `%LOCALAPPDATA%\Diana` and configures the current user PATH without administrator privileges.

## Docker

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/docker.sh | sh
```

When run directly in a terminal, the script asks which image you want. The **full** image preinstalls Chromium, CJK fonts, ffmpeg, yt-dlp and tesseract, so page rendering, screenshots, media downloads and OCR work out of the box; the **slim** image ships none of them (~156 MB to pull, versus ~619 MB for the full one). Pick full if unsure.

Piped execution (the command above) never prompts and installs the full image on a first deployment. To get the slim one, say so up front:

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/docker.sh | DIANA_VARIANT=slim sh
```

The choice is stored as `DIANA_IMAGE=` in the deployment directory's `.env`, and re-running the installer leaves it alone. To switch later, re-run the installer in a terminal or edit that line.

The one-click installer includes a separate updater for the Diana container by default and generates an internal token in `.env`. The Docker socket is mounted into the updater, never into Diana, and the updater API has no published host port. You can request an image update from the WebUI; its automatic install switch remains off until you enable it. Mounting the Docker socket grants the updater control of the host Docker daemon, so use it only in a trusted deployment. For an existing Docker deployment, rerun the same one-click command above in the **original deployment directory**; a running container cannot install the updater by itself. Disable any existing Watchtower, Portainer, or other automatic updater for Diana so it cannot bypass the WebUI switch. To omit or remove the updater, run `curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/docker.sh | DIANA_DOCKER_SELF_UPDATE=0 sh`.

For subsequent updates, run in the same directory:

```sh
docker compose -f docker-compose.yml -f docker-compose.update.yml pull && docker compose -f docker-compose.yml -f docker-compose.update.yml up -d
```

The command above applies to the default installation. If you used `DIANA_DOCKER_SELF_UPDATE=0`, use `docker compose pull && docker compose up -d`. The image tag in `.env` must match the WebUI channel: `latest` for Release, `beta` for Beta, or `canary` for Canary (`-slim` for the slim variants). Changing the WebUI channel does not change the host's image reference. Fixed version tags cannot advance automatically. If an update fails, inspect `docker compose -f docker-compose.yml -f docker-compose.update.yml logs diana-updater`.

If an older image fails on Apple Silicon / ARM64 with `no matching manifest for linux/arm64/v8`, temporarily add `platform: linux/amd64` under `services.diana` in `docker-compose.yml` (requires amd64 emulation; performance and browser compatibility may be affected). Remove it once a native ARM64 image is published. Alternatively, use the native installer above. Build configuration changes do not update existing registry images.

## Reaching the console from another machine

Diana only listens on localhost by default. To reach the console from another machine, set `--host` (`-BindHost` on Windows) at install time:

```sh
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh -s -- --host 0.0.0.0
```

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1))) -BindHost 0.0.0.0
```

Already installed? Two options: re-run the install command with `--host` (`-BindHost` on Windows) (which also upgrades you), or edit the config file to set `server.host` to `0.0.0.0` and restart.

First edit the config (no sudo for a user install; a `/opt/diana` system install may require it):

```sh
vim "$(diana config path)"
```

```powershell
notepad (diana config path)
```

Find the `server:` block near the top of the file and change the `host` line from `127.0.0.1` to `0.0.0.0` (two-space indent, leave the other lines alone):

```yaml
server:
  host: 0.0.0.0
  port: "18080"
```

Then restart — `diana restart` handles service privileges itself:

```sh
diana restart
```

Once exposed, always put an HTTPS reverse proxy in front and open the port in your firewall — the console has admin privileges; don't leave it naked on the public internet.
