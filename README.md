# devstack

An MCP (Model Context Protocol) server that gives Claude Code programmatic control over any [Tilt](https://tilt.dev)-managed development stack.

Instead of running CLI commands manually, Claude can start, stop, restart, and diagnose services directly — e.g. _"what happened to the API in the last 15 minutes?"_ or _"start the frontend and worker"_.

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

Service names are resolved live from Tilt. Aliases can be configured per-workspace via `SetAliases()` — if none are set, exact name matching is used.

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
# 1. Register your workspace
devstack register --name=myproject --path=/path/to/workspace

# 2. Start Tilt daemon
devstack start --workspace=myproject

# 3. Register devstack with Claude Code (one-time, user-scoped)
claude mcp add --scope user --transport stdio devstack \
  -- devstack serve --transport=stdio

# 4. Inject MCP instructions into AGENTS.md (run inside your project repo)
DEVSTACK_WORKSPACE=/path/to/workspace devstack init
```

### Per-repo MCP config (`.mcp.json`)

To scope devstack to a specific project with a default service and port:

```json
{
  "mcpServers": {
    "devstack": {
      "type": "stdio",
      "command": "devstack",
      "args": ["serve", "--transport=stdio"],
      "env": {
        "TILT_PORT": "10350",
        "DEVSTACK_DEFAULT_SERVICE": "my-api",
        "DEVSTACK_WORKSPACE": "/path/to/workspace"
      }
    }
  }
}
```

### Stop hook (auto-shutdown on session end)

`devstack init` automatically injects a Stop hook into `.claude/settings.local.json` when `DEVSTACK_DEFAULT_SERVICE` is set. It stops the default service when the Claude session ends, skipping shutdown if other sessions are still active.

To inject manually:

```json
{
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "devstack stop --default-service=my-api --if-last-session --workspace=/path/to/workspace"
          }
        ]
      }
    ]
  }
}
```

## Configuration

| Environment Variable | Default | Purpose |
|---------------------|---------|---------|
| `TILT_PORT` | `10350` | Tilt API port |
| `TILT_HOST` | `localhost` | Tilt API host |
| `DEVSTACK_DEFAULT_SERVICE` | — | Default service when no `name` arg is provided |
| `DEVSTACK_WORKSPACE` | — | Root workspace directory |

**Runtime files:**
- `~/.config/devstack/workspaces.json` — registered workspaces
- `~/.local/share/devstack/<name>/tilt.pid` — Tilt daemon PID
- `~/.local/share/devstack/<name>/tilt.log` — Tilt daemon output
