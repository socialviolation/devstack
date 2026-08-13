#!/usr/bin/env bash
set -uo pipefail

herdr_bin="${HERDR_BIN_PATH:-herdr}"

for tool in "$herdr_bin" devstack; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not on PATH" >&2; exit 1; }
done

# See scripts/open-panel.sh: the working directory of this script belongs to no
# workspace, so the panel opens with no --cwd rather than with the wrong one.
open_panel() {
  if [ -n "${1:-}" ]; then
    exec "$herdr_bin" plugin pane open \
      --plugin devstack --entrypoint panel --placement tab --cwd "$1" --focus
  fi
  exec "$herdr_bin" plugin pane open \
    --plugin devstack --entrypoint panel --placement tab --focus
}

decision="$("$herdr_bin" pane list 2>/dev/null | devstack panel --launch-decision --tab 2>/dev/null)"

case "$decision" in
  "SWITCHTAB "*)
    tab="${decision#SWITCHTAB }"
    "$herdr_bin" tab focus "$tab" || open_panel ""
    ;;
  "FOCUS "*)
    pane="${decision#FOCUS }"
    "$herdr_bin" pane zoom "$pane" --on >/dev/null 2>&1 || true
    exec "$herdr_bin" pane zoom "$pane" --off
    ;;
  "CLOSE "*)
    exec "$herdr_bin" pane close "${decision#CLOSE }"
    ;;
  "OPEN "*)
    open_panel "${decision#OPEN }"
    ;;
  *)
    open_panel ""
    ;;
esac
