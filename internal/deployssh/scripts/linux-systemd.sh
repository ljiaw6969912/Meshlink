#!/bin/sh
set -eu

SERVICE_NAME="${SERVICE_NAME:-meshlink-agent}"
BASE_DIR="${BASE_DIR:-/opt/meshlink}"
BIN_DIR="${BIN_DIR:-$BASE_DIR/bin}"
CONFIG_ROOT="${CONFIG_ROOT:-/etc/meshlink}"
CONFIG_DIR="${CONFIG_DIR:-$CONFIG_ROOT/configs}"
CERT_DIR="${CERT_DIR:-$CONFIG_ROOT/certs}"
INVITE_DIR="${INVITE_DIR:-$CONFIG_ROOT/invites}"
LOG_DIR="${LOG_DIR:-/var/log/meshlink}"
BACKUP_DIR="${BACKUP_DIR:-$BASE_DIR/backups}"
AGENT_BINARY="$BIN_DIR/mesh-agent"
CONFIG_PATH="$CONFIG_DIR/active.json"
SERVICE_PATH="/etc/systemd/system/$SERVICE_NAME.service"

need_root() {
  if [ "$(id -u)" -eq 0 ]; then
    return 0
  fi
  if command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
    exec sudo -n env \
      SERVICE_NAME="$SERVICE_NAME" BASE_DIR="$BASE_DIR" BIN_DIR="$BIN_DIR" \
      CONFIG_ROOT="$CONFIG_ROOT" CONFIG_DIR="$CONFIG_DIR" CERT_DIR="$CERT_DIR" \
      INVITE_DIR="$INVITE_DIR" LOG_DIR="$LOG_DIR" BACKUP_DIR="$BACKUP_DIR" \
      sh "$0" "$@"
  fi
  echo "permission denied: run as root or configure passwordless sudo" >&2
  exit 70
}

write_service() {
  cat > "$SERVICE_PATH" <<EOF
[Unit]
Description=Meshlink self-hosted relay hub
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$AGENT_BINARY -config $CONFIG_PATH
WorkingDirectory=$CONFIG_ROOT
Restart=on-failure
RestartSec=3
LimitNOFILE=1048576
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF
}

backup_current() {
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  backup="$BACKUP_DIR/$stamp"
  mkdir -p "$backup"
  [ -e "$BIN_DIR" ] && cp -a "$BIN_DIR" "$backup/bin" || true
  [ -e "$CONFIG_ROOT" ] && cp -a "$CONFIG_ROOT" "$backup/config-root" || true
  [ -e "$SERVICE_PATH" ] && cp -a "$SERVICE_PATH" "$backup/service" || true
  echo "$backup"
}

restore_backup() {
  backup="$1"
  [ -d "$backup/bin" ] && rm -rf "$BIN_DIR" && cp -a "$backup/bin" "$BIN_DIR"
  [ -d "$backup/config-root" ] && rm -rf "$CONFIG_ROOT" && cp -a "$backup/config-root" "$CONFIG_ROOT"
  [ -f "$backup/service" ] && cp -a "$backup/service" "$SERVICE_PATH"
  systemctl daemon-reload || true
  systemctl restart "$SERVICE_NAME" || true
}

case "${1:-install-service}" in
  install-service)
    need_root "$@"
    command -v systemctl >/dev/null 2>&1 || {
      echo "systemd is required" >&2
      exit 71
    }
    mkdir -p "$BIN_DIR" "$CONFIG_DIR" "$CERT_DIR" "$INVITE_DIR" "$LOG_DIR" "$BACKUP_DIR"
    backup="$(backup_current)"
    write_service
    systemctl daemon-reload
    systemctl enable "$SERVICE_NAME"
    if ! systemctl restart "$SERVICE_NAME"; then
      mkdir -p "$LOG_DIR"
      systemctl status "$SERVICE_NAME" --no-pager > "$LOG_DIR/deploy-last-status.log" 2>&1 || true
      journalctl -u "$SERVICE_NAME" -n 120 --no-pager > "$LOG_DIR/deploy-last-journal.log" 2>&1 || true
      restore_backup "$backup"
      echo "service failed; restored backup $backup" >&2
      exit 72
    fi
    systemctl is-active "$SERVICE_NAME"
    ;;
  rollback)
    need_root "$@"
    restore_backup "${2:?backup directory is required}"
    ;;
  uninstall)
    need_root "$@"
    systemctl stop "$SERVICE_NAME" || true
    systemctl disable "$SERVICE_NAME" || true
    rm -f "$SERVICE_PATH"
    systemctl daemon-reload || true
    ;;
  *)
    echo "usage: $0 install-service|rollback <backup>|uninstall" >&2
    exit 64
    ;;
esac
