#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
HUB="$SCRIPT_DIR/mesh-cloudhub"

if [ ! -f "$HUB" ]; then
  echo "mesh-cloudhub is missing." >&2
  echo "Copy this script into the directory containing mesh-cloudhub." >&2
  exit 1
fi

if [ ! -x "$HUB" ]; then
  chmod +x "$HUB"
fi

echo "Development only: this Hub uses in-memory storage and has no production TLS or persistence."
echo "Control API: http://0.0.0.0:18080"
echo "Relay TCP:   0.0.0.0:18082"
echo "Press Ctrl+C to stop."
echo

exec "$HUB" -listen 0.0.0.0:18080 -relay-listen 0.0.0.0:18082
