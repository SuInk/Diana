#!/bin/sh
# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.


set -eu

repo="${DIANA_REPOSITORY:-SuInk/Diana}"
install_dir="${DIANA_INSTALL_DIR:-$HOME/.local/share/diana}"
version="${DIANA_VERSION:-latest}"
port="${DIANA_PORT:-18080}"
# 默认只绑回环:WebUI 是带管理权限的控制台,装完就对公网敞开不是合理默认。
# 要从别的机器访问就显式设 DIANA_HOST=0.0.0.0(或某张网卡的地址)。
host="${DIANA_HOST:-}"
host_explicit=false
[ -n "$host" ] && host_explicit=true
[ -n "$host" ] || host='127.0.0.1'
# DIANA_ENV_FILE 指向一个额外的 KEY=VALUE 文件,内容原样并进 runtime.env。
# 安装器不可能把几十个可选配置都做成参数,给一个统一入口。
extra_env_file="${DIANA_ENV_FILE:-}"
start_after_install="${DIANA_START_AFTER_INSTALL:-true}"

fail() {
  printf 'Diana installer: %s\n' "$*" >&2
  exit 1
}

info() {
  printf '==> %s\n' "$*"
}

download() {
  label=$1
  source_url=$2
  target_path=$3
  info "$label"
  curl -fL --retry 3 --retry-delay 1 --progress-bar -o "$target_path" "$source_url"
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

case "$host" in
  *[!0-9A-Za-z.:_-]*) fail "DIANA_HOST must be a host name or IP address" ;;
esac

if [ -n "$extra_env_file" ] && [ ! -f "$extra_env_file" ]; then
  fail "DIANA_ENV_FILE does not exist: $extra_env_file"
fi

need_command curl
need_command tar
need_command awk
need_command mktemp
need_command sed

shell_quote() {
  printf '%s' "$1" | sed "s/'/'\\\\''/g"
}

random_hex() {
  byte_count=$1
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$byte_count"
  else
    od -An -N"$byte_count" -tx1 /dev/urandom | tr -d ' \n'
  fi
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
binary_name="diana-webui"
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

download "Download → Diana $version for $os/$arch" "$base_url/$archive_name" "$archive_path"
download "Download → SHA256SUMS" "$base_url/SHA256SUMS" "$sums_path"

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
[ -x "$package_dir/$binary_name" ] || fail "release package does not contain $binary_name"
[ -f "$package_dir/frontend-next/dist/index.html" ] || fail "release package does not contain the WebUI"

mkdir -p "$install_dir" "$install_dir/data" "$install_dir/logs" "$install_dir/.installer/backups"
timestamp=$(date -u '+%Y%m%dT%H%M%SZ')
backup_dir="$install_dir/.installer/backups/$timestamp"
mkdir -p "$backup_dir/runtime" "$backup_dir/data"

had_previous=false
for item in "$binary_name" "$package_name" run.sh frontend-next; do
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
chmod +x "$install_dir/run.sh" "$install_dir/$binary_name"

# macOS 按代码签名身份记住授权（麦克风、完全磁盘访问、App 管理都挂在上面）。
# 裸二进制没有签名，每次更新换一份新的 Mach-O，系统就当成一个全新程序：授权重新
# 弹窗，「隐私与安全性」里堆出一排同名 Diana。把运行时装进一个路径固定的 .app，
# 每次都用同一个 identifier ad-hoc 重签，并把 designated requirement 改写成只认
# identifier（默认的 DR 会连 cdhash 一起钉死，换个二进制就不满足了），系统里就
# 始终只有一条 Diana。
macos_app_identifier='com.suink.diana'
macos_app_dir="$install_dir/Diana.app"
macos_app_binary="$macos_app_dir/Contents/MacOS/$binary_name"

write_macos_app_plist() {
  cat >"$macos_app_dir/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key><string>zh_CN</string>
	<key>CFBundleDisplayName</key><string>Diana</string>
	<key>CFBundleExecutable</key><string>$binary_name</string>
	<key>CFBundleIdentifier</key><string>$macos_app_identifier</string>
	<key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
	<key>CFBundleName</key><string>Diana</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>${version#v}</string>
	<key>CFBundleVersion</key><string>1</string>
	<key>LSMinimumSystemVersion</key><string>12.0</string>
	<key>LSUIElement</key><true/>
	<key>NSHumanReadableCopyright</key><string>Copyright SuInk</string>
</dict>
</plist>
PLIST
}

assemble_macos_app() {
  [ "$os" = "darwin" ] || return 0
  # 运行时和前端都放进 bundle：自更新器要求前端目录在可执行文件所在目录之内，
  # 否则会判定「不支持 Release 自更新」。
  mkdir -p "$macos_app_dir/Contents/MacOS"
  write_macos_app_plist
  cp -f "$install_dir/$binary_name" "$macos_app_binary"
  chmod +x "$macos_app_binary"
  rm -rf "$macos_app_dir/Contents/MacOS/frontend-next"
  if [ -d "$install_dir/frontend-next" ]; then
    cp -R "$install_dir/frontend-next" "$macos_app_dir/Contents/MacOS/frontend-next"
  fi
  sign_macos_app || info "macOS → codesign unavailable, permissions may need re-approval"
}

sign_macos_app() {
  command -v codesign >/dev/null 2>&1 || return 1
  codesign --force --deep --sign - \
    --identifier "$macos_app_identifier" \
    --requirements "=designated => identifier \"$macos_app_identifier\"" \
    "$macos_app_dir" >/dev/null 2>&1 || return 1
  codesign --verify --deep --strict "$macos_app_dir" >/dev/null 2>&1 || return 1
  return 0
}

# 这些环境变量在装的时候就填好比装完再进 WebUI 改一遍省事,尤其是无人值守
# 部署。只写调用方真的传了的项:没传就不落进 runtime.env,让应用用自己的默认。
optional_env_keys='LLM_API_KEY LLM_BASE_URL LLM_MODEL LLM_API_FORMAT LLM_IMAGE_MODEL DIANA_PUBLIC_BASE_URL DIANA_LOCAL_MEDIA_BASE_URL DIANA_NAPCAT_WEBUI_URL DIANA_NAPCAT_WEBUI_TOKEN'

append_optional_env() {
  target=$1
  for key in $optional_env_keys; do
    value=$(printenv "$key" 2>/dev/null || true)
    [ -n "$value" ] || continue
    printf "%s='%s'\n" "$key" "$(shell_quote "$value")" >>"$target"
  done
  if [ -n "$extra_env_file" ]; then
    # 原样并入:这个文件由部署者自己写,不做键名白名单,只跳过空行和注释。
    awk 'NF && $0 !~ /^[[:space:]]*#/' "$extra_env_file" >>"$target"
  fi
}

# set_env_value 在已有的 runtime.env 里改一个键,没有就追加。重装时用得上:
# 用户带着新的 DIANA_HOST 重跑安装,不该因为 runtime.env 已存在就被忽略。
set_env_value() {
  target=$1
  key=$2
  value=$3
  rewritten="$temp_dir/runtime.env.rewritten"
  if grep -q "^$key=" "$target"; then
    sed "s|^$key=.*|$key='$(shell_quote "$value")'|" "$target" >"$rewritten"
    mv "$rewritten" "$target"
  else
    printf "%s='%s'\n" "$key" "$(shell_quote "$value")" >>"$target"
  fi
}

assemble_macos_app

generated_password=""
generated_username=""
if [ ! -f "$install_dir/runtime.env" ]; then
  username="${DIANA_ADMIN_USERNAME:-diana#$(random_hex 8)}"
  generated_password="${DIANA_ADMIN_PASSWORD:-}"
  if [ -z "$generated_password" ]; then
    generated_password=$(random_hex 16)
  fi
  port_q=$(shell_quote "$port")
  db_path_q=$(shell_quote "$db_path")
  log_path_q=$(shell_quote "$install_dir/logs/diana.log")
  if [ "$os" = "darwin" ]; then
    frontend_dist_q=$(shell_quote "$macos_app_dir/Contents/MacOS/frontend-next/dist")
  else
    frontend_dist_q=$(shell_quote "$install_dir/frontend-next/dist")
  fi
  username_q=$(shell_quote "$username")
  password_q=$(shell_quote "$generated_password")
  host_q=$(shell_quote "$host")
  cat >"$install_dir/runtime.env" <<EOF
HOST='$host_q'
PORT='$port_q'
APP_DB_PATH='$db_path_q'
LOG_PATH='$log_path_q'
FRONTEND_DIST='$frontend_dist_q'
DIANA_ADMIN_USERNAME='$username_q'
DIANA_ADMIN_PASSWORD='$password_q'
EOF
  append_optional_env "$install_dir/runtime.env"
  chmod 600 "$install_dir/runtime.env"
else
  # 重装时显式传的绑定地址要生效,否则「改成 0.0.0.0 再跑一遍安装」不起作用,
  # 用户只能自己去翻 runtime.env。端口同理。
  if [ "$host_explicit" = "true" ]; then
    set_env_value "$install_dir/runtime.env" HOST "$host"
    info "Configuration → bind address set to $host"
  fi
  if [ -n "${DIANA_PORT:-}" ]; then
    set_env_value "$install_dir/runtime.env" PORT "$port"
  fi
  append_optional_env "$install_dir/runtime.env"
  chmod 600 "$install_dir/runtime.env"
  existing_username=$(sed -n "s/^DIANA_ADMIN_USERNAME='\([^']*\)'$/\1/p" "$install_dir/runtime.env" | head -n 1)
  username_suffix=${existing_username#diana#}
  username_valid=true
  case "$username_suffix" in *[!A-Za-z0-9]*) username_valid=false ;; esac
  if [ "$existing_username" = "diana#admin" ] || [ "$existing_username" = "diana#admin0000" ] || [ "$username_suffix" = "$existing_username" ] || [ "${#username_suffix}" -lt 8 ] || [ "$username_valid" != "true" ]; then
    generated_username="diana#$(random_hex 8)"
    username_q=$(shell_quote "$generated_username")
    repaired_env="$temp_dir/runtime.env.repaired"
    if grep -q '^DIANA_ADMIN_USERNAME=' "$install_dir/runtime.env"; then
      sed "s/^DIANA_ADMIN_USERNAME=.*/DIANA_ADMIN_USERNAME='$username_q'/" "$install_dir/runtime.env" >"$repaired_env"
    else
      cp "$install_dir/runtime.env" "$repaired_env"
      printf "DIANA_ADMIN_USERNAME='%s'\n" "$username_q" >>"$repaired_env"
    fi
    mv "$repaired_env" "$install_dir/runtime.env"
    chmod 600 "$install_dir/runtime.env"
    info "Configuration → repaired invalid administrator username"
  fi
fi

if [ "$os" = "darwin" ]; then
  # 每次启动先确认 .app 的签名还满足固定 identifier：WebUI 自更新替换了 bundle
  # 里的文件之后签名会失效，这里补签回来，避免下次启动被系统当成新程序登记。
  cat >"$install_dir/start-installed.sh" <<EOF
#!/bin/sh
set -eu
install_root=\$(CDPATH= cd -- "\$(dirname -- "\$0")" && pwd)
set -a
. "\$install_root/runtime.env"
set +a
app_dir="\$install_root/Diana.app"
app_binary="\$app_dir/Contents/MacOS/$binary_name"
if [ -x "\$app_binary" ]; then
  if command -v codesign >/dev/null 2>&1; then
    if ! codesign --verify --deep --strict "\$app_dir" >/dev/null 2>&1; then
      codesign --force --deep --sign - \\
        --identifier "$macos_app_identifier" \\
        --requirements "=designated => identifier \\"$macos_app_identifier\\"" \\
        "\$app_dir" >/dev/null 2>&1 || true
    fi
  fi
  exec "\$app_binary"
fi
exec "\$install_root/run.sh"
EOF
else
  cat >"$install_dir/start-installed.sh" <<'EOF'
#!/bin/sh
set -eu
install_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
set -a
. "$install_root/runtime.env"
set +a
exec "$install_root/run.sh"
EOF
fi
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

stop_other_diana_instances() {
  stop_service
  if command -v pgrep >/dev/null 2>&1; then
    for existing_pid in $(pgrep -f '(^|/)diana-webui(-[A-Za-z0-9_-]+)?([[:space:]]|$)' 2>/dev/null || true); do
      [ "$existing_pid" = "$$" ] && continue
      info "Single instance → stopping existing Diana PID $existing_pid"
      kill "$existing_pid" 2>/dev/null || true
    done
  fi
  command -v lsof >/dev/null 2>&1 || return 0
  listeners=$(lsof -nP -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null || true)
  [ -n "$listeners" ] || return 0
  for listener_pid in $listeners; do
    listener_command=$(ps -p "$listener_pid" -o command= 2>/dev/null || true)
    case "$listener_command" in
      *diana-webui*)
        info "Single instance → stopping existing Diana PID $listener_pid"
        kill "$listener_pid" 2>/dev/null || true
        ;;
      *) fail "port $port is already used by PID $listener_pid ($listener_command); Diana did not start a second instance" ;;
    esac
  done
  attempts=0
  while lsof -nP -tiTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1 && [ "$attempts" -lt 10 ]; do
    attempts=$((attempts + 1))
    sleep 1
  done
  lsof -nP -tiTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1 && fail "existing Diana instance did not release port $port"
}

print_startup_diagnostics() {
  printf '\nDiana startup diagnostics:\n' >&2
  found=false
  for log_file in "$install_dir/logs/launchd-error.log" "$install_dir/logs/launchd.log" "$install_dir/logs/installer-service.log" "$install_dir/logs/diana.log"; do
    if [ -s "$log_file" ]; then
      found=true
      printf '%s\n' "--- $log_file" >&2
      tail -n 30 "$log_file" >&2 || true
    fi
  done
  [ "$found" = "true" ] || printf '%s\n' "No startup log was written." >&2
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
# 让自更新器知道重启由 systemd 负责：两边都去启动新实例会抢同一个监听端口。
Environment=DIANA_SERVICE_MANAGER=systemd
Environment=DIANA_SERVICE_LABEL=diana.service
Environment=DIANA_SERVICE_DOMAIN=user

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
  <!-- 让自更新器知道重启由 launchd 负责：两边都去启动新实例会抢同一个监听端口。 -->
  <key>EnvironmentVariables</key><dict>
    <key>DIANA_SERVICE_MANAGER</key><string>launchd</string>
    <key>DIANA_SERVICE_LABEL</key><string>com.suink.diana</string>
    <key>DIANA_SERVICE_DOMAIN</key><string>gui/$(id -u)</string>
  </dict>
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
  for item in "$binary_name" "$package_name" run.sh frontend-next; do
    if [ -e "$backup_dir/runtime/$item" ]; then
      rm -rf -- "$install_dir/$item"
      mv "$backup_dir/runtime/$item" "$install_dir/$item"
    fi
  done
  # .app 里装的是同一份运行时，回滚后按恢复出来的旧文件重新组装并重签，
  # 否则 launchd 下次拉起的还是新版本。
  assemble_macos_app
  for suffix in "" -wal -shm; do
    if [ -f "$backup_dir/data/diana.db$suffix" ]; then
      cp -p "$backup_dir/data/diana.db$suffix" "$db_path$suffix"
    fi
  done
  start_service || true
}

if [ "$start_after_install" = "true" ]; then
  info "Start → enforcing one Diana instance"
  stop_other_diana_instances
  info "Start → launching Diana"
  start_service
  # 绑定地址不是回环时,健康检查得打到真正在听的那个地址上;0.0.0.0 和 ::
  # 是通配符,本机仍从回环探测。IPv6 字面量要加方括号。
  case "$host" in
    ''|0.0.0.0|::|'*') health_host='127.0.0.1' ;;
    *:*) health_host="[$host]" ;;
    *) health_host="$host" ;;
  esac
  health_url="http://$health_host:$port/api/health"
  healthy=false
  attempts=0
  while [ "$attempts" -lt 45 ]; do
    if curl -fsS --max-time 2 "$health_url" >/dev/null 2>&1; then
      healthy=true
      break
    fi
    attempts=$((attempts + 1))
    percent=$((attempts * 100 / 45))
    printf '\r==> Start → health check %3d%%' "$percent"
    sleep 1
  done
  printf '\n'
  if [ "$healthy" != "true" ]; then
    print_startup_diagnostics
    restore_previous
    fail "health check failed; the previous runtime was restored when available. See $install_dir/logs"
  fi
  info "Diana is healthy at http://$health_host:$port"
  printf 'Service: %s\n' "$service_kind"
  case "$host" in
    127.0.0.1|localhost|::1)
      # 装在服务器上却只绑回环,是「装完打不开」的头号原因。默认不改,
      # 但必须让人知道开关在哪,而不是自己去翻 runtime.env。
      printf 'Access:    local only (bound to %s).\n' "$host"
      printf '           To reach it from another machine, reinstall with DIANA_HOST=0.0.0.0,\n'
      printf '           or set HOST in %s/runtime.env and restart.\n' "$install_dir"
      printf '           The console has admin rights: keep it behind a firewall, security\n'
      printf '           group or reverse proxy with TLS rather than exposing it to the internet.\n'
      ;;
    *)
      printf 'Access:    listening on %s — make sure the port is allowed by the firewall\n' "$host"
      printf '           and security group, and prefer a reverse proxy with TLS.\n'
      ;;
  esac
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
if [ -n "$generated_username" ]; then
  printf 'Username:  %s\n' "$generated_username"
  printf 'The existing password remains stored in %s/runtime.env.\n' "$install_dir"
fi
