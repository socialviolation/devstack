#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
mode="${1:-collector-down}"
case "$mode" in
  collector-down|no-traces|logs-only)
    ;;
  *)
    echo "unsupported mode: $mode" >&2
    exit 1
    ;;
esac
mkdir -p "$root/state"
printf '%s\n' "$mode" > "$root/state/telemetry-bad.mode"
printf '%s\n' "$mode" > "$root/state/telemetry.mode"
echo "telemetry-bad mode set to $mode"
