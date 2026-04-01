# devstack

A CLI and [MCP](https://modelcontextprotocol.io) server that gives Claude Code programmatic control over any [Tilt](https://tilt.dev)-managed development stack.

Devstack sits on top of Tilt to handle workspace registration, service dependency ordering, group management, and observability. When running as an MCP server it exposes tools so Claude Code can start/stop/restart services, read logs, and investigate distributed traces — without you having to copy-paste output.

---

## Install

```bash
go install ./...
```

Requires Go 1.24+ and [Tilt](https://docs.tilt.dev/install.html) on `$PATH`.

---

## Concepts

| Term | Meaning |
|------|---------|
| **Workspace** | A directory containing a Tiltfile that defines one or more services |
| **Service** | A single Tilt resource (`local_resource`) — an API, worker, importer, etc. |
| **Group** | A named set of services you can start/stop together |
| **Dependency** | A declared ordering constraint: service A won't start until service B is running |

---

## CLI Commands

### Workspaces

```bash
devstack workspace list              # List all registered workspaces
devstack workspace add [path]        # Register a directory as a workspace
devstack workspace remove <name>     # Unregister a workspace
devstack workspace up                # Start the Tilt dev daemon (background)
devstack workspace down              # Stop the Tilt dev daemon and SigNoz
```

`devstack ws` is an alias for `devstack workspace`.

### Services

```bash
devstack start [service]             # Start a service and all its dependencies
devstack start --group=<name>        # Start all services in a group
devstack stop [service]              # Stop a service
devstack status                      # Show live service tree: state, ports, deps
```

`start` and `stop` auto-detect the current service from the working directory when no name is given.

### Service Registration

```bash
devstack init --name=<n> --path=<p> --cmd=<c>   # Register a new service
devstack init                                     # Refresh AGENTS.md for current service
devstack init --all                               # Refresh AGENTS.md for all services
```

`devstack init` auto-detects the language (`go`, `python`, `node`, `dotnet`), writes a Tiltfile block, creates `.mcp.json`, and writes `AGENTS.md` with tool instructions for Claude Code.

Flags: `--language`, `--port` (for health checks), `--group`, `--force`.

### Dependencies

```bash
devstack deps add <service> <dep>    # Declare that <service> depends on <dep>
devstack deps remove <service> <dep>
devstack deps order <service>        # Show full resolved startup sequence
```

### Groups

```bash
devstack groups list
devstack groups add <group> <service> [service...]
devstack groups remove <group> <service> [service...]
```

### Observability (SigNoz)

```bash
devstack otel status
devstack otel start                  # Start the SigNoz container stack
devstack otel stop
devstack otel open                   # Open the SigNoz UI in the browser
```

Flags for `start`: `--ui-port` (default 3301), `--otlp-grpc-port` (default 4317), `--otlp-http-port` (default 4318).

### MCP Server

```bash
devstack serve                       # Start the MCP server (stdio transport)
```

This is what `.mcp.json` invokes. You don't run it directly.

---

## MCP Tools (available to Claude Code)

### `status`
Show all services with state (`running` / `building` / `starting` / `idle` / `disabled` / `error`), ports, source path, and last build error.

### `restart`
Trigger a rebuild/restart for a service. Auto-enables the service first if it was disabled.

Parameters: `service` (optional — uses `DEVSTACK_DEFAULT_SERVICE` if omitted).

### `stop`
Disable one or all services.

Parameters: `service` (optional — if omitted, stops everything).

### `configure`
Set a Tilt runtime argument (`key=value`). Tilt reloads affected services automatically. Useful for feature flags and environment switching.

Parameters: `key` (required), `value` (required).

### `process_logs`
Fetch raw stdout/stderr from a service via Tilt. Use this for services that don't export OTEL logs, or when you need unstructured process output.

Parameters: `service` (optional), `lines` (default 100), `errors_only` (filter to error/exception/panic/fatal/fail lines).

If no service is given and no default is configured, fetches all services in parallel.

### `investigate`
Primary diagnostic tool. Queries SigNoz for distributed traces and correlated logs, then falls back to Tilt process logs if OTEL logs are unavailable.

Three modes:

| Mode | Trigger | What happens |
|------|---------|-------------|
| **Trace lookup** | `trace_id` given | Full span tree + logs for that specific trace |
| **Attribute search** | `attribute` + `value` given | Find all root spans where e.g. `portfolio.id=57835`, then expand each trace |
| **Recent executions** | Neither given | Most recent executions (scoped to default service if set) |

Parameters: `trace_id`, `attribute`, `value`, `service`, `since_minutes` (default 5), `limit` (default 3), `errors_only`.

Attribute search queries SigNoz with `isRoot=true` so each result is a distinct trace entry point — no matter which service owns the root span.

---

## Per-repo setup (`.mcp.json`)

`devstack init` creates this automatically. Each service repo gets its own `.mcp.json` pointing at the workspace and naming itself as the default service:

```json
{
  "mcpServers": {
    "devstack": {
      "type": "stdio",
      "command": "devstack",
      "args": ["serve"],
      "env": {
        "DEVSTACK_WORKSPACE": "/path/to/workspace",
        "DEVSTACK_DEFAULT_SERVICE": "my-api",
        "TILT_PORT": "10350"
      }
    }
  }
}
```

Claude Code reads `.mcp.json` automatically and loads the MCP server when you open that repo.

---

## Configuration

| Variable | Default | Purpose |
|----------|---------|---------|
| `DEVSTACK_WORKSPACE` | (auto-detected from cwd) | Root workspace directory |
| `DEVSTACK_DEFAULT_SERVICE` | — | Default service when no name is given to a tool or command |
| `TILT_PORT` | `10350` | Tilt API port |
| `TILT_HOST` | `localhost` | Tilt API host |

**Files written by devstack:**

| Path | Purpose |
|------|---------|
| `~/.config/devstack/workspaces.json` | Registered workspaces and their ports |
| `~/.local/share/devstack/<name>/tilt.pid` | Tilt daemon PID |
| `~/.local/share/devstack/<name>/tilt.log` | Tilt daemon stdout |
| `<workspace>/.devstack.json` | Services, dependencies, groups |
| `<service>/.mcp.json` | MCP config for that service repo |
| `<service>/AGENTS.md` | Tool reference injected for Claude Code |
