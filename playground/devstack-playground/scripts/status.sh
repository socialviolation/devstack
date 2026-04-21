#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
runtime_dir="$root/state/runtime"

python3 - "$runtime_dir" <<'PY'
import json
import pathlib
import sys

runtime_dir = pathlib.Path(sys.argv[1])
files = sorted(runtime_dir.glob("*.json"))
if not files:
    print("no runtime status files")
    raise SystemExit(0)
for path in files:
    data = json.loads(path.read_text())
    print(f"{data.get('service')}: state={data.get('state')} pid={data.get('pid')} mode={data.get('mode')} exit_code={data.get('exit_code')}")
PY
