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

uid=$(id -u)
plist="$HOME/Library/LaunchAgents/com.suink.diana.plist"
if [ -f "$plist" ] && grep -F "$install_dir" "$plist" >/dev/null 2>&1; then
  if command -v launchctl >/dev/null 2>&1; then
    launchctl bootout "gui/$uid/com.suink.diana" >/dev/null 2>&1 || true
  fi
  rm -f -- "$plist"
fi

unit="$HOME/.config/systemd/user/diana.service"
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

command_link="$HOME/.local/bin/diana"
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

# Remove this script last. POSIX systems keep the opened script readable until exit.
rm -f -- "$install_dir/uninstall.sh"
rmdir "$install_dir" >/dev/null 2>&1 || true
printf 'Diana was removed. Configuration and data remain in %s\n' "$install_dir"
