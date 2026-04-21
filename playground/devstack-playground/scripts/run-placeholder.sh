#!/usr/bin/env bash
set -euo pipefail

service="${1:-}"
state_dir="$(cd "$(dirname "$0")/.." && pwd)/state"
mkdir -p "$state_dir"

if [[ -z "$service" ]]; then
  echo "usage: $0 <service>" >&2
  exit 1
fi

mode_file="$state_dir/${service}.mode"
mode="healthy"
if [[ -f "$mode_file" ]]; then
  mode="$(cat "$mode_file")"
fi

echo "[$service] placeholder runtime starting (mode=$mode)"
case "$service:$mode" in
  crashy:healthy)
    echo "[$service] intentional crash for restart/status scenarios" >&2
    exit 1
    ;;
  telemetry-bad:no-traces|telemetry-bad:collector-down)
    echo "[$service] degraded telemetry scenario active: $mode"
    ;;
  *)
    echo "[$service] healthy placeholder loop"
    ;;
esac

while true; do
  sleep 60
done
