#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
residue=0

if find "$root/logs" -type f ! -name '.gitkeep' | grep -q . 2>/dev/null; then
  echo "log files present under $root/logs"
else
  echo "no log residue under $root/logs"
fi

if [[ -f "$root/state/session.pid" ]]; then
  echo "found stale session pid file: $root/state/session.pid" >&2
  residue=1
else
  echo "no stale session pid file"
fi

exit "$residue"
