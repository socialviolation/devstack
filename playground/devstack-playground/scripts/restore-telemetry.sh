#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$root/state"
printf 'healthy\n' > "$root/state/telemetry-good.mode"
printf 'no-traces\n' > "$root/state/telemetry-bad.mode"
rm -f "$root/state/telemetry.mode"
echo "telemetry scenario restored"
