#!/usr/bin/env bash
set -uo pipefail

if ! command -v devstack >/dev/null 2>&1; then
  echo "This plugin runs 'devstack panel', and this machine has no devstack on PATH." >&2
  echo "Install devstack first, then install this plugin again." >&2
  exit 1
fi

# The panel is a subcommand of the binary on PATH. An older devstack answers
# every other command and not this one, so the install must fail here rather
# than open a pane that closes at once.
if ! devstack panel --help >/dev/null 2>&1; then
  echo "The devstack on PATH has no 'panel' command. Run 'devstack upgrade', then install this plugin again." >&2
  exit 1
fi

exec devstack --version
