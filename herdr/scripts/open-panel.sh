#!/usr/bin/env bash
# herdr has no focus-by-id, so a focus is a zoom on and then off.
set -uo pipefail

herdr_bin="${HERDR_BIN_PATH:-herdr}"

for tool in "$herdr_bin" devstack; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not on PATH" >&2; exit 1; }
done

# herdr runs this from the plugin's own directory, which belongs to no
# workspace. A panel opened there shows every workspace on the machine, and not
# the one the reader pressed the key in. So the directory comes from devstack,
# which reads it out of the herdr context, and the panel opens with no --cwd at
# all when nothing answers.
open_panel() {
  if [ -n "${1:-}" ]; then
    exec "$herdr_bin" plugin pane open \
      --plugin devstack --entrypoint panel \
      --placement split --direction right --cwd "$1" --focus
  fi
  exec "$herdr_bin" plugin pane open \
    --plugin devstack --entrypoint panel \
    --placement split --direction right --focus
}

decision="$("$herdr_bin" pane list 2>/dev/null | devstack panel --launch-decision 2>/dev/null)"

case "$decision" in
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
