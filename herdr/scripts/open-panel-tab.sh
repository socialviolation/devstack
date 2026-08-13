#!/usr/bin/env bash
set -uo pipefail

herdr_bin="${HERDR_BIN_PATH:-herdr}"

open_panel() {
  exec "$herdr_bin" plugin pane open \
    --plugin devstack \
    --entrypoint panel \
    --placement tab \
    --cwd "$1" \
    --focus
}

decision="$("$herdr_bin" pane list 2>/dev/null | devstack panel --launch-decision --tab 2>/dev/null)"

case "$decision" in
  "SWITCHTAB "*)
    tab="${decision#SWITCHTAB }"
    "$herdr_bin" tab focus "$tab" || open_panel "$PWD"
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
    cwd="${decision#OPEN }"
    open_panel "${cwd:-$PWD}"
    ;;
  *)
    open_panel "$PWD"
    ;;
esac
