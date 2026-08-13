#!/bin/sh

set -eu

repo="${DIANA_REPOSITORY:-SuInk/Diana}"
install_dir="${DIANA_INSTALL_DIR:-$HOME/.local/share/diana}"
version="${DIANA_VERSION:-latest}"
port="${DIANA_PORT:-18080}"
start_after_install="${DIANA_START_AFTER_INSTALL:-true}"

fail() {
  printf 'Diana installer: %s\n' "$*" >&2
  exit 1
}

info() {
  printf '==> %s\n' "$*"
}

need_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

case "$install_dir" in
  ""|"/") fail "DIANA_INSTALL_DIR must be a dedicated directory" ;;
esac

case "$port" in
  *[!0-9]*|"") fail "DIANA_PORT must be a number" ;;
esac

need_command curl
need_command tar
need_command awk
need_command mktemp
need_command sed

shell_quote() {
  printf '%s' "$1" | sed "s/'/'\\\\''/g"
}

os_name=$(uname -s)
case "$os_name" in
  Darwin) os="darwin" ;;
  Linux) os="linux" ;;
  *) fail "unsupported operating system: $os_name" ;;
esac

machine=$(uname -m)
case "$machine" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) fail "unsupported architecture: $machine" ;;
esac

if [ "$version" = "latest" ]; then
  info "Resolving the latest stable release"
  release_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest")
  version=${release_url##*/}
fi

case "$version" in
  v[0-9]*) ;;
  *) fail "invalid release version: $version" ;;
esac

package_name="diana-webui-$os-$arch"
archive_name="$package_name.tar.gz"
base_url="https://github.com/$repo/releases/download/$version"
temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/diana-install.XXXXXX")
archive_path="$temp_dir/$archive_name"
sums_path="$temp_dir/SHA256SUMS"
stage_dir="$temp_dir/stage"

cleanup() {
  rm -rf -- "$temp_dir"
}
trap cleanup EXIT HUP INT TERM

info "Downloading Diana $version for $os/$arch"
curl -fL --retry 3 --retry-delay 1 -o "$archive_path" "$base_url/$archive_name"
curl -fL --retry 3 --retry-delay 1 -o "$sums_path" "$base_url/SHA256SUMS"

expected=$(awk -v name="$archive_name" '$2 == name || $2 == "*" name { print $1; exit }' "$sums_path")
case "$expected" in
  ""|*[!0-9a-fA-F]*) fail "SHA-256 entry for $archive_name was not found" ;;
esac
[ "${#expected}" -eq 64 ] || fail "invalid SHA-256 entry for $archive_name"

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$archive_path" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$archive_path" | awk '{print $1}')
else
  fail "sha256sum or shasum is required"
fi

[ "$actual" = "$expected" ] || fail "SHA-256 verification failed for $archive_name"
info "SHA-256 verified"

mkdir -p "$stage_dir"
tar -xzf "$archive_path" -C "$stage_dir"
package_dir="$stage_dir/$package_name"
[ -x "$package_dir/$package_name" ] || fail "release package does not contain $package_name"
[ -f "$package_dir/frontend-next/dist/index.html" ] || fail "release package does not contain the WebUI"

mkdir -p "$install_dir" "$install_dir/data" "$install_dir/logs" "$install_dir/.installer/backups"
timestamp=$(date -u '+%Y%m%dT%H%M%SZ')
backup_dir="$install_dir/.installer/backups/$timestamp"
mkdir -p "$backup_dir/runtime" "$backup_dir/data"

had_previous=false
for item in "$package_name" run.sh frontend-next; do
  if [ -e "$install_dir/$item" ]; then
    had_previous=true
    mv "$install_dir/$item" "$backup_dir/runtime/$item"
  fi
done

db_path="$install_dir/data/diana.db"
for suffix in "" -wal -shm; do
  if [ -f "$db_path$suffix" ]; then
    cp -p "$db_path$suffix" "$backup_dir/data/diana.db$suffix"
  fi
done

cp -R "$package_dir/." "$install_dir/"
chmod +x "$install_dir/run.sh" "$install_dir/$package_name"

generated_password=""
if [ ! -f "$install_dir/runtime.env" ]; then
  username="${DIANA_ADMIN_USERNAME:-diana#admin}"
  generated_password="${DIANA_ADMIN_PASSWORD:-}"
  if [ -z "$generated_password" ]; then
    if command -v openssl >/dev/null 2>&1; then
      generated_password=$(openssl rand -hex 16)
    else
      generated_password=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
    fi
  fi
  port_q=$(shell_quote "$port")
  db_path_q=$(shell_quote "$db_path")
  log_path_q=$(shell_quote "$install_dir/logs/diana.log")
  frontend_dist_q=$(shell_quote "$install_dir/frontend-next/dist")
  username_q=$(shell_quote "$username")
  password_q=$(shell_quote "$generated_password")
  cat >"$install_dir/runtime.env" <<EOF
HOST='127.0.0.1'
PORT='$port_q'
APP_DB_PATH='$db_path_q'
LOG_PATH='$log_path_q'
FRONTEND_DIST='$frontend_dist_q'
DIANA_ADMIN_USERNAME='$username_q'
DIANA_ADMIN_PASSWORD='$password_q'
EOF
  chmod 600 "$install_dir/runtime.env"
fi

cat >"$install_dir/start-installed.sh" <<'EOF'
#!/bin/sh
set -eu
install_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
set -a
. "$install_root/runtime.env"
set +a
exec "$install_root/run.sh"
EOF
chmod +x "$install_dir/start-installed.sh"
printf '%s\n' "$version" >"$install_dir/.installed-version"

start_fallback() {
  if [ -f "$install_dir/.diana.pid" ]; then
    old_pid=$(cat "$install_dir/.diana.pid" 2>/dev/null || true)
    case "$old_pid" in
      *[!0-9]*|"") ;;
      *) kill "$old_pid" 2>/dev/null || true ;;
    esac
  fi
  nohup "$install_dir/start-installed.sh" >>"$install_dir/logs/installer-service.log" 2>&1 &
  printf '%s\n' "$!" >"$install_dir/.diana.pid"
}

stop_service() {
  if [ "$os" = "linux" ] && command -v systemctl >/dev/null 2>&1; then
    systemctl --user stop diana.service >/dev/null 2>&1 || true
  fi
  if [ "$os" = "darwin" ] && command -v launchctl >/dev/null 2>&1; then
    launchctl bootout "gui/$(id -u)/com.suink.diana" >/dev/null 2>&1 || true
  fi
  if [ -f "$install_dir/.diana.pid" ]; then
    old_pid=$(cat "$install_dir/.diana.pid" 2>/dev/null || true)
    case "$old_pid" in
      *[!0-9]*|"") ;;
      *) kill "$old_pid" 2>/dev/null || true ;;
    esac
  fi
}

start_service() {
  if [ "$os" = "linux" ] && command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
    unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
    mkdir -p "$unit_dir"
    cat >"$unit_dir/diana.service" <<EOF
[Unit]
Description=Diana AI Assistant
After=network-online.target

[Service]
Type=simple
WorkingDirectory=$install_dir
ExecStart=$install_dir/start-installed.sh
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
EOF
    systemctl --user daemon-reload
    systemctl --user enable --now diana.service >/dev/null
    systemctl --user restart diana.service
    service_kind="systemd user service"
    return
  fi

  if [ "$os" = "darwin" ] && command -v launchctl >/dev/null 2>&1; then
    launch_agents="$HOME/Library/LaunchAgents"
    plist="$launch_agents/com.suink.diana.plist"
    mkdir -p "$launch_agents"
    cat >"$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.suink.diana</string>
  <key>ProgramArguments</key><array><string>$install_dir/start-installed.sh</string></array>
  <key>WorkingDirectory</key><string>$install_dir</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$install_dir/logs/launchd.log</string>
  <key>StandardErrorPath</key><string>$install_dir/logs/launchd-error.log</string>
</dict></plist>
EOF
    launchctl bootout "gui/$(id -u)/com.suink.diana" >/dev/null 2>&1 || true
    launchctl bootstrap "gui/$(id -u)" "$plist"
    launchctl kickstart -k "gui/$(id -u)/com.suink.diana"
    service_kind="launchd user service"
    return
  fi

  start_fallback
  service_kind="background process"
}

restore_previous() {
  [ "$had_previous" = "true" ] || return 0
  stop_service
  for item in "$package_name" run.sh frontend-next; do
    if [ -e "$backup_dir/runtime/$item" ]; then
      rm -rf -- "$install_dir/$item"
      mv "$backup_dir/runtime/$item" "$install_dir/$item"
    fi
  done
  for suffix in "" -wal -shm; do
    if [ -f "$backup_dir/data/diana.db$suffix" ]; then
      cp -p "$backup_dir/data/diana.db$suffix" "$db_path$suffix"
    fi
  done
  start_service || true
}

if [ "$start_after_install" = "true" ]; then
  info "Starting Diana"
  start_service
  health_url="http://127.0.0.1:$port/api/health"
  healthy=false
  attempts=0
  while [ "$attempts" -lt 45 ]; do
    if curl -fsS --max-time 2 "$health_url" >/dev/null 2>&1; then
      healthy=true
      break
    fi
    attempts=$((attempts + 1))
    sleep 1
  done
  if [ "$healthy" != "true" ]; then
    restore_previous
    fail "health check failed; the previous runtime was restored when available. See $install_dir/logs"
  fi
  info "Diana is healthy at http://127.0.0.1:$port"
  printf 'Service: %s\n' "$service_kind"
else
  info "Installation completed without starting Diana"
fi

printf 'Installed: %s\n' "$install_dir"
printf 'Backup:    %s\n' "$backup_dir"
if [ -n "$generated_password" ]; then
  printf 'Username:  %s\n' "$username"
  printf 'Password:  %s\n' "$generated_password"
  printf 'Credentials are stored in %s/runtime.env (mode 600).\n' "$install_dir"
fi
