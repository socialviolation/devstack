#!/usr/bin/env bash
set -uo pipefail

if command -v devstack >/dev/null 2>&1; then
  devstack --version
  exit 0
fi

echo "This plugin runs 'devstack panel', and this machine has no devstack on PATH." >&2
echo "Install devstack first, then install this plugin again." >&2
exit 1
