# devstack

An MCP (Model Context Protocol) server that gives Claude Code programmatic control over the Navexa microservices development stack via [Tilt](https://tilt.dev).

Instead of running CLI commands manually, Claude can start, stop, restart, and diagnose services directly — e.g. _"what happened to the trade importer in the last 15 minutes?"_ or _"start the API and frontend"_.

## Commands

| Command | Purpose |
|---------|---------|
| `devstack serve` | Start the MCP server (stdio transport) |
| `devstack init` | Inject MCP instructions into `AGENTS.md` + register Stop hook |
| `devstack register` | Register a workspace in the global registry |
| `devstack start` | Start the Tilt daemon for a workspace |
| `devstack down` | Shut down the Tilt daemon |
| `devstack stop` | Stop a specific service |

## MCP Tools (available to Claude)

### Service Control
| Tool | Description |
|------|-------------|
| `status` | Show all services with build/runtime status |
| `start` | Start or trigger a service |
| `restart` | Restart a service |
| `stop` | Disable a service |
| `start_all` | Start all or a specified list of services |
| `stop_all` | Stop all services |

### Logs & Diagnostics
| Tool | Description |
|------|-------------|
| `logs` | Fetch raw logs from a service |
| `errors` | Extract error lines (error, exception, panic, fatal, fail) |
| `what_happened` | Aggregate errors across all services with timestamps |

### Configuration
| Tool | Description |
|------|-------------|
| `set_environment` | Switch services between Development / Staging / Production |

Service names can be canonical (e.g. `nxTradeImporter`) or aliased (e.g. `trade importer`, `api`, `frontend`).

## Managed Services

| Service | Stack | Port |
|---------|-------|------|
| navexa-api | .NET 8 | 63290 |
| navexa-frontend | Angular | 4200 |
| nxTradeImporter | .NET 8 | 5178 |
| nxFileImporter | .NET 8 | 5001 |
| ai-file-importer | Python | — |
| ssh-tunnel | SSH | — |

## Setup

### Prerequisites
- Go 1.24+
- [Tilt](https://tilt.dev) installed and on `$PATH`

### Install

```bash
go install .
```

### First-time setup

```bash
# Register the workspace
devstack register --path=/home/nick/dev/navexa

# Start Tilt daemon
devstack start

# Inject MCP instructions into AGENTS.md
devstack init

# Register with Claude Code (user-scoped, one-time)
claude mcp add --scope user --transport stdio devstack \
  --env TILT_PORT=10350 \
  -- devstack serve --transport=stdio
```

## Configuration

| Environment Variable | Default | Purpose |
|---------------------|---------|---------|
| `TILT_PORT` | `10350` | Tilt API port |
| `TILT_HOST` | `localhost` | Tilt API host |
| `NVXDEV_DEFAULT_SERVICE` | — | Default service for MCP tools |
| `DEVSTACK_WORKSPACE` | — | Root workspace directory |

**Runtime files:**
- `~/.config/devstack/workspaces.json` — registered workspaces
- `~/.local/share/devstack/<name>/tilt.pid` — Tilt daemon PID
- `~/.local/share/devstack/<name>/tilt.log` — Tilt daemon output
