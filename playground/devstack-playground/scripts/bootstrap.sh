#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$root/state/runtime" "$root/logs" "$root/.pi/extensions/devstack-playground" "$root/.pi/skills/devstack-debugging" "$root/.pi/skills/devstack-operations"
: > "$root/state/.gitkeep"
: > "$root/state/runtime/.gitkeep"
: > "$root/logs/.gitkeep"

cat > "$root/state/telemetry-good.mode" <<'EOF'
healthy
EOF
cat > "$root/state/telemetry-bad.mode" <<'EOF'
no-traces
EOF
cat > "$root/state/frontend.mode" <<'EOF'
healthy
EOF
cat > "$root/state/api.mode" <<'EOF'
healthy
EOF
cat > "$root/state/worker.mode" <<'EOF'
healthy
EOF
cat > "$root/state/crashy.mode" <<'EOF'
healthy
EOF

echo "playground bootstrap complete"
echo "root=$root"
