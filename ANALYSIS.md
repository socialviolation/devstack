# Navexa Dev Stack: dev.py → Tilt Migration Analysis

## What dev.py provides

| Feature | dev.py |
|---|---|
| Service lifecycle | Start / Kill / Restart per service |
| Log viewer | In-TUI RichLog, one service at a time |
| Status indicator | STOPPED / STARTING / RUNNING / ERROR dots |
| Selective startup | Manual (s key), nothing auto-starts |
| SSH tunnel | Toggle with Shift+F |
| Copy logs | `c` key → `wl-copy` |
| Port freeing | `fuser -k` on kill/restart |
| Environment | Hardcoded `ASPNETCORE_ENVIRONMENT=Development` |
| AI/agent queryable | No |

---

## What Tilt replaces this with

### Service lifecycle

`local_resource()` with `serve_cmd=` maps directly to a long-running process.
`cmd=` runs first (used here for `fuser` port-freeing) then `serve_cmd=` starts.

If `serve_cmd` exits with a non-zero code the resource goes red — same as `Status.ERROR` in dev.py.

Restart: `tilt trigger <name>` (CLI) or the ↺ button in the Web UI.
Stop: not a first-class operation — workaround is `tilt disable <name>` or use TRIGGER_MODE_MANUAL and don't trigger it.

### Logs

Tilt's Web UI (localhost:10350) shows per-resource logs with colour, filtering, and search.
Copy from the UI with normal browser text selection/Cmd+C — no `wl-copy` glue needed.
Stream logs from CLI: `tilt logs -f navexa-api`

### Status

Tilt tracks: Pending → Running → Error per resource, displayed in the Web UI sidebar and queryable via `tilt get uiresource`.

### Selective startup (cost-conscious)

All resources in the Tiltfile are configured with:
```
trigger_mode=TRIGGER_MODE_MANUAL
auto_init=False
```
Running `tilt up` consumes ~zero CPU — no service starts until you explicitly trigger it via the UI or CLI.
You can also bring up only a subset at launch: `tilt up navexa-api nxTradeImporter`

### Environment targeting

Pass `--env` at launch:
```
tilt up -- --env=Staging
tilt up -- --env=Production
```
All .NET services pick up `ASPNETCORE_ENVIRONMENT` from that value.
Falls back to `$ASPNETCORE_ENVIRONMENT` in your shell, then `Development`.

---

## AI / Agent control surface

This is the primary gain over dev.py.

### CLI (scriptable, JSON output)

```bash
# All service statuses as JSON
tilt get uiresource -o json

# Single service status
tilt get uiresource navexa-api -o json

# Human-readable detail (Ready state, last exit code, etc.)
tilt describe uiresource navexa-api

# Restart a service
tilt trigger navexa-api

# Stream logs
tilt logs -f navexa-api

# List all known resource types
tilt api-resources
```

### HTTP API

Tilt exposes its full state at `http://localhost:10350/api/view` as JSON.
An agent can poll this endpoint to check:
- Which services are running / errored
- Last log lines
- Readiness probe results
- Timestamps of last start/restart

No authentication — only accessible locally while `tilt up` is running.

### Readiness probes

HTTP readiness probes on each .NET service and the frontend mean an agent can reliably determine "is this service actually up and serving" rather than just "is the process alive".

---

## Feature mapping

| dev.py feature | Tilt equivalent | Notes |
|---|---|---|
| `s` — Start service | Click ↺ in UI or `tilt trigger <name>` | |
| `Shift+R` — Kill & restart | `tilt trigger <name>` | Tilt kills the serve_cmd and relaunches |
| `x` — Kill service | `tilt disable <name>` | Or just don't trigger it |
| `q` — Quit all | `tilt down` or Ctrl+C in tilt up | |
| `c` — Copy logs | Browser text select in Web UI | |
| `Shift+F` — SSH tunnel | `tilt trigger ssh-tunnel` | ssh-tunnel is a local_resource |
| Status dots | Web UI sidebar colours + `tilt get` JSON | |
| Log viewer | Web UI log panel + `tilt logs -f` | Searchable, filterable |
| Port freeing | `cmd=` runs `fuser -k` before serve_cmd | |
| Env var | `tilt up -- --env=Staging` | |
| AI queryable | `tilt get`, `tilt trigger`, HTTP `/api/view` | New capability |

---

## What Tilt does NOT replicate cleanly

1. **Stop without restart** — Tilt has no "stop this service but keep it in the list" first-class action. `tilt disable <name>` is the closest but removes it from view. Workaround: leave it in error state or file a manual port-kill script.

2. **`wl-copy` clipboard** — Tilt's UI is browser-based, so standard browser clipboard works fine but there's no programmatic copy-to-wayland-clipboard equivalent. If you need that in an agent workflow, pipe `tilt logs <name>` into `wl-copy`.

3. **The TUI itself** — Replaced by the Web UI. If you want a terminal-native view, `tilt up` prints a live summary; for full logs use `tilt logs -f`. There's no ncurses panel, but the Web UI is significantly more capable.

---

## Installation

```bash
# Install tilt (via mise or direct)
curl -fsSL https://raw.githubusercontent.com/tilt-dev/tilt/master/scripts/install.sh | bash

# Or via mise
mise install tilt

# Run from the nvxdev directory
cd /home/nick/dev/navexa/nvxdev
tilt up
```

Tilt opens the Web UI in your browser automatically at `http://localhost:10350`.
