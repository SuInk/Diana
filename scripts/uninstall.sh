#!/bin/sh
# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.

set -eu

purge=false
assume_yes=false
for argument in "$@"; do
  case "$argument" in
    --purge) purge=true ;;
    --yes|-y) assume_yes=true ;;
    --help|-h)
      cat <<'EOF'
Usage: ./uninstall.sh [--purge] [--yes]

Stops Diana and removes its installed runtime and service registration.
Configuration, data, logs, and installer backups are preserved by default.
Use --purge to remove the entire installation directory.
EOF
      exit 0
      ;;
    *) printf 'Unknown option: %s\n' "$argument" >&2; exit 2 ;;
  esac
done

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
install_dir=$script_dir
case "$install_dir" in
  ""|/|"$HOME") printf 'Unsafe Diana install directory: %s\n' "$install_dir" >&2; exit 1 ;;
esac

install_scope=$(cat "$install_dir/.install-scope" 2>/dev/null || printf 'user')
service_user=$(cat "$install_dir/.service-user" 2>/dev/null || id -un)
if [ "$install_scope" = "system" ] && [ "$(id -u)" -ne 0 ]; then
  printf 'System Diana installation requires sudo to uninstall: sudo %s' "$0" >&2
  [ "$purge" = true ] && printf ' --purge' >&2
  [ "$assume_yes" = true ] && printf ' --yes' >&2
  printf '\n' >&2
  exit 1
fi

service_uid=$(id -u "$service_user" 2>/dev/null || id -u)
service_home=$HOME
if [ "$service_user" != "$(id -un)" ]; then
  if [ "$(uname -s)" = "Darwin" ] && command -v dscl >/dev/null 2>&1; then
    service_home=$(dscl . -read "/Users/$service_user" NFSHomeDirectory 2>/dev/null | awk '{print $2}')
  elif command -v getent >/dev/null 2>&1; then
    service_home=$(getent passwd "$service_user" | awk -F: '{print $6}')
  fi
  [ -n "$service_home" ] || service_home=$HOME
fi

if [ "$assume_yes" != true ]; then
  if [ "$purge" = true ]; then
    prompt="Remove Diana and permanently delete all data in $install_dir? [y/N] "
  else
    prompt="Remove Diana but keep its configuration and data in $install_dir? [y/N] "
  fi
  printf '%s' "$prompt"
  read -r answer
  case "$answer" in y|Y|yes|YES) ;; *) printf 'Cancelled.\n'; exit 0 ;; esac
fi

uid=$service_uid
plist="$service_home/Library/LaunchAgents/com.suink.diana.plist"
if [ -f "$plist" ] && grep -F "$install_dir" "$plist" >/dev/null 2>&1; then
  if command -v launchctl >/dev/null 2>&1; then
    launchctl bootout "gui/$uid/com.suink.diana" >/dev/null 2>&1 || true
  fi
  rm -f -- "$plist"
fi

if [ "$install_scope" = "system" ] && [ -f /etc/systemd/system/diana.service ] && grep -F "$install_dir" /etc/systemd/system/diana.service >/dev/null 2>&1; then
  if command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now diana.service >/dev/null 2>&1 || true
  fi
  rm -f -- /etc/systemd/system/diana.service
  systemctl daemon-reload >/dev/null 2>&1 || true
fi

# 服务没了，免密授权也不该留下。只删安装器自己写的那份（按标记行认）。
if [ "$install_scope" = "system" ] && [ -f /etc/sudoers.d/diana ] &&
  grep -Fq 'Managed by the Diana installer' /etc/sudoers.d/diana 2>/dev/null; then
  rm -f -- /etc/sudoers.d/diana
fi

unit="$service_home/.config/systemd/user/diana.service"
if [ -f "$unit" ] && grep -F "$install_dir" "$unit" >/dev/null 2>&1; then
  if command -v systemctl >/dev/null 2>&1; then
    systemctl --user disable --now diana.service >/dev/null 2>&1 || true
  fi
  rm -f -- "$unit"
  systemctl --user daemon-reload >/dev/null 2>&1 || true
fi

pid_file="$install_dir/.diana.pid"
if [ -f "$pid_file" ]; then
  pid=$(cat "$pid_file" 2>/dev/null || true)
  case "$pid" in
    *[!0-9]*|"") ;;
    *)
      command_line=$(ps -p "$pid" -o command= 2>/dev/null || true)
      case "$command_line" in *"$install_dir"*) kill "$pid" 2>/dev/null || true ;; esac
      ;;
  esac
fi

if [ "$install_scope" = "system" ]; then
  command_link="/usr/local/bin/diana"
else
  command_link="$service_home/.local/bin/diana"
fi
if [ -L "$command_link" ]; then
  link_target=$(readlink "$command_link" 2>/dev/null || true)
  case "$link_target" in "$install_dir/"*) rm -f -- "$command_link" ;; esac
fi

if [ "$purge" = true ]; then
  if [ ! -f "$install_dir/.installed-version" ] && [ ! -f "$install_dir/config.yaml" ]; then
    printf 'Refusing to purge a directory without a Diana installation marker: %s\n' "$install_dir" >&2
    exit 1
  fi
  rm -rf -- "$install_dir"
  printf 'Diana and its data were removed from %s\n' "$install_dir"
  exit 0
fi

for item in \
  diana-webui diana-webui-linux-amd64 diana-webui-linux-arm64 \
  diana-webui-darwin-amd64 diana-webui-darwin-arm64 \
  Diana.app frontend-next run.sh run.bat start-installed.sh \
  uninstall.ps1 .installed-version .diana.pid .diana-updates; do
  rm -rf -- "$install_dir/$item"
done

rm -f -- "$install_dir/.install-scope" "$install_dir/.service-user" "$install_dir/.migrated-from"

# Remove this script last. POSIX systems keep the opened script readable until exit.
rm -f -- "$install_dir/uninstall.sh"
rmdir "$install_dir" >/dev/null 2>&1 || true
printf 'Diana was removed. Configuration and data remain in %s\n' "$install_dir"
