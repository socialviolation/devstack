#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
export PLAYGROUND_FRONTEND_PORT="${PLAYGROUND_FRONTEND_PORT:-13000}"
export PLAYGROUND_API_PORT="${PLAYGROUND_API_PORT:-18080}"
export PLAYGROUND_WORKER_PORT="${PLAYGROUND_WORKER_PORT:-19090}"
export PLAYGROUND_API_URL="http://127.0.0.1:${PLAYGROUND_API_PORT}"
export PLAYGROUND_WORKER_URL="http://127.0.0.1:${PLAYGROUND_WORKER_PORT}"
export OTEL_EXPORTER_OTLP_ENDPOINT="${OTEL_EXPORTER_OTLP_ENDPOINT:-http://127.0.0.1:14318}"

mkdir -p "$root/logs"
: > "$root/logs/restart-order.log"

pids=()
cleanup() {
  for pid in "${pids[@]:-}"; do
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT

python3 "$root/scripts/fake_collector.py" 14318 >/tmp/restart-collector.log 2>&1 & pids+=("$!")
sleep 1

for service in worker api frontend; do
  echo "$service" >> "$root/logs/restart-order.log"
  python3 "$root/services/$service/app.py" >"/tmp/$service-restart.log" 2>&1 & pids+=("$!")
  sleep 1
done

curl -sf "http://127.0.0.1:${PLAYGROUND_FRONTEND_PORT}/chain" >/dev/null
echo "restart scenario ok"
