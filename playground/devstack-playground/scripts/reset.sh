#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
rm -rf "$root/state" "$root/logs"
"$root/scripts/bootstrap.sh" >/dev/null

echo "playground reset complete"
