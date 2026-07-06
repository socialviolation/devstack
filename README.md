# devstack

A CLI and [MCP](https://modelcontextprotocol.io) server that gives Claude Code programmatic control over any [Tilt](https://tilt.dev)-managed development stack.

Devstack sits on top of Tilt to handle workspace registration, service dependency ordering, group management, and observability. When running as an MCP server it exposes tools so Claude Code can start/stop/restart services, read logs, and investigate distributed traces — without you having to copy-paste output.

---

## Install

```bash
go install github.com/socialviolation/devstack@main
```

Requires Go 1.24+ and [Tilt](https://docs.tilt.dev/install.html) on `$PATH`.

---

## Getting Started

### New developer setup

If you're joining a team that already uses devstack, follow these steps once per machine. **Claude Code can run all of this for you** — open any repo, ask it to set up your workspace, and it will follow the steps below.

**Prerequisites:** Go 1.24+, [Tilt](https://docs.tilt.dev/install.html), Docker

**1. Install devstack**

```bash
go install ./...
```

**2. Register a workspace**

```bash
devstack workspace add ~/dev/my-workspace   # register a workspace — a group of interlinked services under a directory
```

This writes the workspace path and a Tilt port to `~/.config/devstack/workspaces.json`. One-time, per machine.

**3. Start the dev daemon**

```bash
devstack workspace up
```

Starts Tilt and the OTEL collector in the background. Leave it running during development.

**4. Generate agent files for all services**

If services are already declared in the workspace's `.devstack.json` (committed to the repo):

```bash
devstack init --all
```

This creates (or refreshes) `.mcp.json` and `AGENTS.md` in each service directory. Claude Code reads `.mcp.json` automatically and gains access to devstack MCP tools whenever you open a service repo.

If you're registering services fresh (no `.devstack.json` yet):

```bash
devstack init --name=<service> --path=~/dev/my-workspace/<service> --cmd="<start command>" --port=<port>
```

**5. Verify**

```bash
devstack status       # shows running services
devstack otel status  # shows collector mode and upstream
```

**6. Open Claude Code in any service repo**

The devstack MCP server loads automatically from `.mcp.json`. Claude can now start/stop services, read logs, and investigate distributed traces without you pasting output.

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
devstack workspace down              # Stop the Tilt dev daemon and OTEL collector
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

`devstack init` auto-detects the language (`go`, `python`, `node`, `dotnet`), writes `devstack.service.yaml`, registers the repo in the workspace manifest, creates `.mcp.json`, and writes `AGENTS.md` with tool instructions for Claude Code.

Flags: `--language`, `--port` (for health checks), `--group`, `--force`.

### Dependencies

```bash
devstack deps add <service> <dep>    # Declare that <service> depends on <dep>
devstack deps remove <service> <dep>
devstack deps list <service>         # Show full resolved startup sequence
```

### Groups

```bash
devstack groups list
devstack groups add <group> <service> [service...]
devstack groups remove <group> <service> [service...]
```

### Observability (OTEL collector)

devstack starts a local `otelcol-contrib` collector per workspace. Services always send telemetry to `localhost:4317` (gRPC) — they never need to know where it goes from there. The collector handles routing.

By default the collector runs in **debug mode**: telemetry is written to collector stdout and not forwarded anywhere. Configure an upstream when you're ready:

```bash
# Forward to any OTLP endpoint via gRPC
devstack otel configure --plugin=forwarding --set upstream=telemetry.example.com:443 --set protocol=grpc

# Forward via HTTP
devstack otel configure --plugin=forwarding --set upstream=https://otel.example.com:4318

# Opt-in to local SigNoz UI (requires Docker — spins up ClickHouse + SigNoz)
devstack otel configure --plugin=signoz
```

Per-developer endpoint override: set `OTEL_EXPORTER_OTLP_ENDPOINT` in `.envrc` in any service repo.

```bash
devstack otel status                 # show mode, ports, upstream
devstack otel start                  # start the collector (and companion if signoz)
devstack otel stop
devstack otel open                   # open the UI (signoz only)
devstack otel plugins                # list available plugins and their config keys
```

Flags for `start`: `--otlp-grpc-port` (default 4317), `--otlp-http-port` (default 4318), `--ui-port` (default 3301, signoz only).

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
