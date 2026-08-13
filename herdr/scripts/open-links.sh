#!/usr/bin/env bash
# The picker closes itself when the reader picks an address, or gives up, so
# this launcher needs no toggle: a second press can only find the pane gone.
set -uo pipefail

herdr_bin="${HERDR_BIN_PATH:-herdr}"

for tool in "$herdr_bin" devstack; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not on PATH" >&2; exit 1; }
done

cwd="$("$herdr_bin" pane list 2>/dev/null | devstack panel --launch-cwd 2>/dev/null)"
if [ -z "$cwd" ]; then
  exec "$herdr_bin" plugin pane open \
    --plugin devstack --entrypoint links --placement overlay --focus
fi

exec "$herdr_bin" plugin pane open \
  --plugin devstack --entrypoint links --placement overlay --cwd "$cwd" --focus
