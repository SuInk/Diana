#!/bin/sh
# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.


set -eu

repo="${DIANA_REPOSITORY:-SuInk/Diana}"
install_scope="${DIANA_INSTALL_SCOPE:-auto}"
install_dir="${DIANA_INSTALL_DIR:-}"
version="${DIANA_VERSION:-latest}"
port="${DIANA_PORT:-18080}"
# 默认只绑回环:WebUI 是带管理权限的控制台,装完就对公网敞开不是合理默认。
# 要从别的机器访问就显式设 DIANA_HOST=0.0.0.0(或某张网卡的地址)。
host="${DIANA_HOST:-}"
host_explicit=false
[ -n "$host" ] && host_explicit=true
[ -n "$host" ] || host='127.0.0.1'
# DIANA_CONFIG_FILE 指向一份 YAML 片段,内容原样并进生成的 config.yaml。
# 安装器不可能把所有可选配置都做成参数,给一个统一入口。
extra_config_file="${DIANA_CONFIG_FILE:-}"
start_after_install="${DIANA_START_AFTER_INSTALL:-true}"

case "$install_scope" in
  auto|system|user) ;;
  *) printf 'Diana installer: DIANA_INSTALL_SCOPE must be auto, system, or user\n' >&2; exit 1 ;;
esac

if [ "$install_scope" = "user" ] && [ "$(id -u)" -eq 0 ] && [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
  printf '%s\n' 'Diana user installation must be run without sudo.' >&2
  exit 1
fi

tty_available=false
if [ -e /dev/tty ] && ( : </dev/tty ) 2>/dev/null; then
  tty_available=true
fi

if [ -z "$install_dir" ]; then
  if [ "$(id -u)" -eq 0 ]; then
    install_scope=system
    install_dir=/opt/diana
  elif [ "$install_scope" = "system" ]; then
    printf '%s\n' 'Diana recommends a fixed system installation in /opt/diana.' >&2
    printf '%s\n' 'Run the installer again with sudo, or set DIANA_INSTALL_SCOPE=user.' >&2
    exit 1
  elif [ "$install_scope" = "user" ]; then
    install_dir="$HOME/.local/share/diana"
  elif [ "$tty_available" = "true" ]; then
    printf '%s\n' 'Diana recommends a fixed system installation in /opt/diana.' >/dev/tty
    printf '%s\n' 'Re-run this command with sudo for the recommended installation.' >/dev/tty
    printf 'Install only for the current user instead? [y/N] ' >/dev/tty
    IFS= read -r install_answer </dev/tty || install_answer=""
    case "$install_answer" in
      y|Y|yes|YES)
        install_scope=user
        install_dir="$HOME/.local/share/diana"
        ;;
      *)
        printf '%s\n' 'Cancelled. Re-run with sudo for the recommended system installation.' >&2
        exit 1
        ;;
    esac
  else
    printf '%s\n' 'Diana recommends a fixed system installation in /opt/diana.' >&2
    printf '%s\n' 'Re-run with sudo, or set DIANA_INSTALL_SCOPE=user for a user-only installation.' >&2
    exit 1
  fi
elif [ "$install_scope" = "auto" ]; then
  install_scope=custom
fi

if [ "$install_scope" = "system" ] && [ "$(id -u)" -ne 0 ]; then
  printf '%s\n' 'Diana system installation requires sudo.' >&2
  exit 1
fi

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

if [ -n "$extra_config_file" ] && [ ! -f "$extra_config_file" ]; then
  fail "DIANA_CONFIG_FILE does not exist: $extra_config_file"
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

# sudo 只负责把程序放进固定目录；桌面服务仍归发起安装的用户运行，避免 macOS
# 的浏览器、麦克风和 NapCat 被丢进没有 GUI 会话的 root 环境。
service_user=$(id -un)
service_uid=$(id -u)
service_home=$HOME
if [ "$install_scope" = "system" ] && [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
  service_user=$SUDO_USER
  service_uid=$(id -u "$service_user")
  if [ "$os" = "darwin" ] && command -v dscl >/dev/null 2>&1; then
    service_home=$(dscl . -read "/Users/$service_user" NFSHomeDirectory 2>/dev/null | awk '{print $2}')
  elif command -v getent >/dev/null 2>&1; then
    service_home=$(getent passwd "$service_user" | awk -F: '{print $6}')
  fi
  [ -n "$service_home" ] || service_home=$HOME
fi
if [ "$os" = "darwin" ] && [ "$install_scope" = "system" ] && [ "$service_uid" = "0" ]; then
  fail "macOS system installation must be started with sudo from the desktop user, not from a root login"
fi

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

legacy_install_dir="$service_home/.local/share/diana"
new_system_install=false
legacy_migrated_from=""
if [ "$install_scope" = "system" ] && [ ! -e "$install_dir/.installed-version" ] && [ -e "$legacy_install_dir/.installed-version" ]; then
  new_system_install=true
fi

# 一台机器只能有一个 Diana 在跑：两个实例会抢同一个端口，更糟的是各写各的
# 数据库，聊天记忆和配置从此分叉。迁移只复制数据，旧服务仍然注册着且是
# enabled——光把进程 kill 掉，systemd/launchd 转头就会把它拉起来，所以这里必须
# 把旧的服务单元一起退役。
retire_legacy_service() {
  legacy_retired=false
  legacy_plist="$service_home/Library/LaunchAgents/com.suink.diana.plist"
  if [ -f "$legacy_plist" ] && grep -F "$legacy_install_dir" "$legacy_plist" >/dev/null 2>&1; then
    if command -v launchctl >/dev/null 2>&1; then
      launchctl bootout "gui/$service_uid/com.suink.diana" >/dev/null 2>&1 || true
    fi
    mv -f "$legacy_plist" "$legacy_plist.migrated" 2>/dev/null || rm -f -- "$legacy_plist"
    legacy_retired=true
  fi
  legacy_unit="$service_home/.config/systemd/user/diana.service"
  if [ -f "$legacy_unit" ] && grep -F "$legacy_install_dir" "$legacy_unit" >/dev/null 2>&1; then
    if command -v systemctl >/dev/null 2>&1; then
      if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ] && command -v runuser >/dev/null 2>&1; then
        runuser -u "$service_user" -- systemctl --user disable --now diana.service >/dev/null 2>&1 || true
      else
        systemctl --user disable --now diana.service >/dev/null 2>&1 || true
      fi
    fi
    mv -f "$legacy_unit" "$legacy_unit.migrated" 2>/dev/null || rm -f -- "$legacy_unit"
    legacy_retired=true
  fi
  [ "$legacy_retired" = true ] &&
    info "Migration → retired the previous per-user service; only one Diana runs per machine"
  return 0
}

mkdir -p "$install_dir" "$install_dir/data" "$install_dir/logs" "$install_dir/.installer/backups"
if [ "$new_system_install" = "true" ]; then
  info "Migration → copying configuration and data from $legacy_install_dir"
  for item in config.yaml runtime.env; do
    if [ -e "$legacy_install_dir/$item" ] && [ ! -e "$install_dir/$item" ]; then
      cp -R "$legacy_install_dir/$item" "$install_dir/$item"
    fi
  done
  for item in data logs; do
    if [ -d "$legacy_install_dir/$item" ]; then
      cp -R "$legacy_install_dir/$item/." "$install_dir/$item/"
    fi
  done
  printf '%s\n' "$legacy_install_dir" >"$install_dir/.migrated-from"
  legacy_migrated_from="$legacy_install_dir"
fi
# 每次系统级安装都退役一次旧的用户级服务：首装之后如果有人又跑了一遍旧的
# 用户级安装，这里同样能把它收拾掉，而不是留两个实例互相抢端口。
if [ "$install_scope" = "system" ] && [ "$install_dir" != "$legacy_install_dir" ]; then
  retire_legacy_service
fi
timestamp=$(date -u '+%Y%m%dT%H%M%SZ')
backup_dir="$install_dir/.installer/backups/$timestamp"
mkdir -p "$backup_dir/runtime" "$backup_dir/data"

had_previous=false
for item in "$binary_name" "$package_name" run.sh uninstall.sh frontend-next; do
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
if [ -f "$install_dir/uninstall.sh" ]; then
  chmod +x "$install_dir/uninstall.sh"
fi

command_dir=""
command_path_hint=""

# 用户级安装的 ~/.local/bin 不一定在 PATH 里（root 的 .profile 没有那段，zsh 或
# 定制过 rc 的用户也没有）。只在安装结尾打一行提示的话，提示会被后续输出刷走，
# 用户的第一条 diana 命令就是 command not found——检测到缺失就把 export 幂等
# 追加进按 $SHELL 选择的 rc，重跑安装不重复追加，写不进去再退回提示。
ensure_command_dir_on_path() {
  case ":${PATH:-}:" in *":$command_dir:"*) return 0 ;; esac
  path_marker='# added by Diana installer'
  case "${SHELL:-}" in
    */zsh)  rc_file="$HOME/.zshrc" ;;
    */bash) rc_file="$HOME/.bashrc" ;;
    *)      rc_file="$HOME/.profile" ;;
  esac
  if [ -f "$rc_file" ] && grep -Fq "$path_marker" "$rc_file" 2>/dev/null; then
    command_path_hint="restart your shell (or run \`. $rc_file\`) to use \`diana\` directly."
    return 0
  fi
  if printf '\nexport PATH="$HOME/.local/bin:$PATH" %s\n' "$path_marker" >>"$rc_file" 2>/dev/null; then
    command_path_hint="added $command_dir to PATH in $rc_file — restart your shell (or run \`. $rc_file\`) to use \`diana\` directly."
  else
    command_path_hint="add $command_dir to PATH to run \`diana\` directly."
  fi
}

if [ -f "$install_dir/uninstall.sh" ]; then
  # 系统安装提供所有登录用户都能找到的稳定命令；无权限模式才落在当前用户目录。
  if [ "$install_scope" = "system" ]; then
    command_dir="/usr/local/bin"
  else
    command_dir="$HOME/.local/bin"
  fi
  mkdir -p "$command_dir"
  ln -sfn "$install_dir/$binary_name" "$command_dir/diana"
  if [ "$install_scope" != "system" ]; then
    ensure_command_dir_on_path
  fi
fi

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

# 这些项在装的时候就填好比装完再进 WebUI 改一遍省事,尤其是无人值守部署。
# 只写调用方真的传了的项:没传就不落进 config.yaml,让应用用自己的默认。
# 键名沿用环境变量的写法只是为了让调用方式不变,实际写进的是 YAML。
optional_llm_keys='LLM_API_KEY LLM_BASE_URL LLM_MODEL LLM_API_FORMAT LLM_IMAGE_MODEL'
optional_storage_keys='DIANA_LOCAL_MEDIA_BASE_URL'
optional_napcat_keys='DIANA_NAPCAT_WEBUI_URL DIANA_NAPCAT_WEBUI_TOKEN'

# yaml_quote 把值包成单引号 YAML 标量,内部单引号按 YAML 规则翻倍。
yaml_quote() {
  printf "'%s'" "$(printf '%s' "$1" | sed "s/'/''/g")"
}

# yaml_key 把 LLM_API_KEY 这类环境变量名转成 config.yaml 里的字段名。
yaml_key() {
  case $1 in
    LLM_*) printf '%s' "$(printf '%s' "${1#LLM_}" | tr 'A-Z' 'a-z')" ;;
    DIANA_LOCAL_MEDIA_BASE_URL) printf 'local_media_base_url' ;;
    DIANA_NAPCAT_WEBUI_URL) printf 'webui_url' ;;
    DIANA_NAPCAT_WEBUI_TOKEN) printf 'webui_token' ;;
    *) printf '%s' "$(printf '%s' "$1" | tr 'A-Z' 'a-z')" ;;
  esac
}

# append_optional_section 把一组可选项写成一个 YAML 段,一个都没传就整段不写。
append_optional_section() {
  target=$1
  section=$2
  shift 2
  written=false
  for key in "$@"; do
    value=$(printenv "$key" 2>/dev/null || true)
    [ -n "$value" ] || continue
    if [ "$written" = "false" ]; then
      printf '%s:\n' "$section" >>"$target"
      written=true
    fi
    printf '  %s: %s\n' "$(yaml_key "$key")" "$(yaml_quote "$value")" >>"$target"
  done
}

# set_yaml_value 改写指定顶层段下的一个键,段内没有就追加到段末。重装时用得上:
# 用户带着新的 DIANA_HOST 重跑安装,不该因为 config.yaml 已存在就被忽略。
set_yaml_value() {
  target=$1
  section=$2
  key=$3
  value=$4
  rewritten="$temp_dir/config.yaml.rewritten"
  awk -v section="$section" -v key="$key" -v value="$(yaml_quote "$value")" '
    BEGIN { in_section = 0; done = 0 }
    # 顶层段名:行首无缩进、以冒号结尾。
    /^[a-z_]+:[[:space:]]*$/ {
      if (in_section && !done) { print "  " key ": " value; done = 1 }
      in_section = ($0 == section ":")
      print
      next
    }
    {
      if (in_section && !done && $0 ~ "^[[:space:]]+" key ":") {
        print "  " key ": " value
        done = 1
        next
      }
      print
    }
    END {
      if (!done) {
        if (!in_section) { print section ":" }
        print "  " key ": " value
      }
    }
  ' "$target" >"$rewritten"
  mv "$rewritten" "$target"
}

# read_yaml_value 读回指定顶层段下的一个键,用于重装时保留已生成的凭据。
read_yaml_value() {
  awk -v section="$2" -v key="$3" '
    /^[a-z_]+:[[:space:]]*$/ { in_section = ($0 == section ":"); next }
    in_section && $0 ~ "^[[:space:]]+" key ":" {
      line = $0
      sub("^[[:space:]]+" key ":[[:space:]]*", "", line)
      gsub(/^'"'"'|'"'"'$/, "", line)
      gsub(/'"''"'/, "'"'"'", line)
      print line
      exit
    }
  ' "$1"
}
assemble_macos_app

generated_password=""
generated_username=""
config_file="$install_dir/config.yaml"
if [ ! -f "$config_file" ]; then
  username="${DIANA_ADMIN_USERNAME:-diana#$(random_hex 8)}"
  generated_password="${DIANA_ADMIN_PASSWORD:-}"
  if [ -z "$generated_password" ]; then
    generated_password=$(random_hex 16)
  fi
  if [ "$os" = "darwin" ]; then
    frontend_dist="$macos_app_dir/Contents/MacOS/frontend-next/dist"
  else
    frontend_dist="$install_dir/frontend-next/dist"
  fi
  cat >"$config_file" <<EOF
# Diana 配置。基础设施段每次启动生效;bot / llm 段只在数据库为空时播种一次,
# 之后以 WebUI 里的配置为准。完整字段见仓库里的 config.example.yaml。
server:
  host: $(yaml_quote "$host")
  port: $(yaml_quote "$port")
  frontend_dist: $(yaml_quote "$frontend_dist")
storage:
  db_path: $(yaml_quote "$db_path")
  log_path: $(yaml_quote "$install_dir/logs/diana.log")
admin:
  username: $(yaml_quote "$username")
  password: $(yaml_quote "$generated_password")
EOF
  append_optional_section "$config_file" storage $optional_storage_keys
  append_optional_section "$config_file" napcat $optional_napcat_keys
  append_optional_section "$config_file" llm $optional_llm_keys
  if [ -n "$extra_config_file" ]; then
    # 原样并入:这段由部署者自己写,内容必须是合法 YAML 顶层段。
    printf '\n' >>"$config_file"
    cat "$extra_config_file" >>"$config_file"
  fi
  chmod 600 "$config_file"
else
  # 重装时显式传的绑定地址要生效,否则「改成 0.0.0.0 再跑一遍安装」不起作用,
  # 用户只能自己去翻 config.yaml。端口同理。
  if [ "$host_explicit" = "true" ]; then
    set_yaml_value "$config_file" server host "$host"
    # 绑定地址是进程启动时读的:默认路径后面会重启服务,改动立刻生效;
    # 但 DIANA_START_AFTER_INSTALL=false 时不重启,得说清楚还没生效,
    # 否则改完发现连不上会以为是配置没写进去。
    if [ "$start_after_install" = "true" ]; then
      info "Configuration → bind address set to $host"
    else
      info "Configuration → bind address set to $host (restart Diana to apply)"
    fi
  fi
  if [ -n "${DIANA_PORT:-}" ]; then
    set_yaml_value "$config_file" server port "$port"
  fi
  chmod 600 "$config_file"
  existing_username=$(read_yaml_value "$config_file" admin username)
  username_suffix=${existing_username#diana#}
  username_valid=true
  case "$username_suffix" in *[!A-Za-z0-9]*) username_valid=false ;; esac
  if [ "$existing_username" = "diana#admin" ] || [ "$existing_username" = "diana#admin0000" ] || [ "$username_suffix" = "$existing_username" ] || [ "${#username_suffix}" -lt 8 ] || [ "$username_valid" != "true" ]; then
    generated_username="diana#$(random_hex 8)"
    set_yaml_value "$config_file" admin username "$generated_username"
    chmod 600 "$config_file"
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
export DIANA_CONFIG="\$install_root/config.yaml"
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
export DIANA_CONFIG="$install_root/config.yaml"
exec "$install_root/run.sh"
EOF
fi
chmod +x "$install_dir/start-installed.sh"
printf '%s\n' "$version" >"$install_dir/.installed-version"
printf '%s\n' "$install_scope" >"$install_dir/.install-scope"
printf '%s\n' "$service_user" >"$install_dir/.service-user"

if [ "$os" = "darwin" ] && [ "$install_scope" = "system" ] && [ "$service_uid" != "0" ]; then
  # 运行用户必须能写数据库、日志和自更新暂存目录；系统级的命令入口仍由 root 管理。
  chown -R "$service_uid" "$install_dir"
fi

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
  if [ "$os" = "linux" ] && [ "$install_scope" = "system" ] && command -v systemctl >/dev/null 2>&1; then
    systemctl stop diana.service >/dev/null 2>&1 || true
  elif [ "$os" = "linux" ] && command -v systemctl >/dev/null 2>&1; then
    systemctl --user stop diana.service >/dev/null 2>&1 || true
  fi
  if [ "$os" = "darwin" ] && command -v launchctl >/dev/null 2>&1; then
    launchctl bootout "gui/$service_uid/com.suink.diana" >/dev/null 2>&1 || true
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
  if [ "$os" = "linux" ] && [ "$install_scope" = "system" ] && command -v systemctl >/dev/null 2>&1; then
    cat >/etc/systemd/system/diana.service <<EOF
[Unit]
Description=Diana AI Assistant
After=network-online.target

[Service]
Type=simple
WorkingDirectory=$install_dir
ExecStart=$install_dir/start-installed.sh
Restart=on-failure
RestartSec=3
Environment=HOME=$HOME
Environment=DIANA_SERVICE_MANAGER=systemd
Environment=DIANA_SERVICE_LABEL=diana.service
Environment=DIANA_SERVICE_DOMAIN=system

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable --now diana.service >/dev/null
    systemctl restart diana.service
    service_kind="systemd system service"
    return
  fi

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
    launch_agents="$service_home/Library/LaunchAgents"
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
    <key>DIANA_SERVICE_DOMAIN</key><string>gui/$service_uid</string>
  </dict>
  <key>StandardOutPath</key><string>$install_dir/logs/launchd.log</string>
  <key>StandardErrorPath</key><string>$install_dir/logs/launchd-error.log</string>
</dict></plist>
EOF
    if [ "$install_scope" = "system" ] && [ "$service_uid" != "0" ]; then
      chown -R "$service_uid" "$launch_agents"
    fi
    launchctl bootout "gui/$service_uid/com.suink.diana" >/dev/null 2>&1 || true
    launchctl bootstrap "gui/$service_uid" "$plist"
    launchctl kickstart -k "gui/$service_uid/com.suink.diana"
    service_kind="launchd user service"
    return
  fi

  start_fallback
  service_kind="background process"
}

restore_previous() {
  [ "$had_previous" = "true" ] || return 0
  stop_service
  for item in "$binary_name" "$package_name" run.sh uninstall.sh frontend-next; do
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
      # 但必须让人知道开关在哪,而不是自己去翻 config.yaml。
      printf 'Access:    local only (bound to %s).\n' "$host"
      printf '           To reach it from another machine, reinstall with DIANA_HOST=0.0.0.0,\n'
      printf '           or set server.host in %s/config.yaml and restart.\n' "$install_dir"
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
  printf 'Note:      configuration changes apply the next time Diana starts.\n'
fi

printf 'Installed: %s\n' "$install_dir"
if [ -n "$legacy_migrated_from" ]; then
  printf 'Migrated:  configuration, database and logs copied from %s\n' "$legacy_migrated_from"
  printf '           That per-user installation was retired — one machine runs one Diana,\n'
  printf '           otherwise two instances fight for the port and split the database.\n'
  printf '           Its files are kept as a fallback; delete them once the new one looks good.\n'
fi
if [ -n "$command_dir" ]; then
  printf 'Command:   %s\n' "$command_dir/diana"
  if [ -n "$command_path_hint" ]; then
    printf 'PATH:      %s\n' "$command_path_hint"
  fi
fi
printf 'Backup:    %s\n' "$backup_dir"
if [ -n "$generated_password" ]; then
  printf 'Username:  %s\n' "$username"
  printf 'Password:  %s\n' "$generated_password"
  printf 'Credentials are stored in %s/config.yaml (mode 600).\n' "$install_dir"
fi
if [ -n "$generated_username" ]; then
  printf 'Username:  %s\n' "$generated_username"
  printf 'The existing password remains stored in %s/config.yaml.\n' "$install_dir"
fi
