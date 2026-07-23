<img width="1672" height="941" alt="ChatGPT Image Jul 22, 2026, 03_59_46 PM" src="https://github.com/user-attachments/assets/7b9c8b3e-5f8f-4298-9870-60073ee4f2db" />

A CLI and [MCP](https://modelcontextprotocol.io) server that gives Claude Code programmatic control over any [Tilt](https://tilt.dev)-managed development stack.

Devstack sits on top of Tilt to handle workspace registration, service dependency ordering, group management, and observability. When running as an MCP server it exposes tools so Claude Code can start/stop/restart services, read logs, and investigate distributed traces — without you having to copy-paste output.

---

## Install

```bash
go install github.com/socialviolation/devstack@main
```

Requires Go 1.25+ and [Tilt](https://docs.tilt.dev/install.html) on `$PATH`.

---

## Getting Started

### New developer setup

If you're joining a team that already uses devstack, follow these steps once per machine. **Claude Code can run all of this for you** — open any repo, ask it to set up your workspace, and it will follow the steps below.

**Prerequisites:** Go 1.25+, [Tilt](https://docs.tilt.dev/install.html), and Docker (only needed if you enable local SigNoz observability)

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

Starts the dev daemon in the background. If observability is enabled for the workspace, the OTEL collector starts too. Leave it running during development.

**4. Generate agent files for all services**

If services are already declared in the workspace's `devstack.workspace.yaml` manifest (committed to the repo):

```bash
devstack init --all
```

This creates (or refreshes) `.mcp.json` and `AGENTS.md` in each service directory. Claude Code reads `.mcp.json` automatically and gains access to devstack MCP tools whenever you open a service repo.

If you're registering services fresh (not yet in the manifest):

```bash
devstack init --name=<service> --path=~/dev/my-workspace/<service> --cmd="<start command>" --port=<port>
```

**5. Verify**

```bash
devstack status       # shows running services
devstack otel status  # collector state, ports, and telemetry evidence
```

**6. Open Claude Code in any service repo**

The devstack MCP server loads automatically from `.mcp.json`. Claude can now start/stop services, read logs, and investigate distributed traces without you pasting output.

---

## Concepts

| Term | Meaning |
|------|---------|
| **Workspace** | A directory with a `devstack.workspace.yaml` manifest that groups one or more services |
| **Service** | A process defined by a `devstack.service.yaml` manifest — an API, worker, importer, etc. |
| **Group** | A named set of services you can start/stop together |
| **Dependency** | A declared ordering constraint: service A won't start until service B is running |
| **Host daemon** | A single Tilt daemon (`:10300`) for the whole machine. Every active workspace's services and every active stack's overlay run inside it as `<workspace>:<service>[:<stack>]` resources. There is no daemon per workspace. |
| **Feature stack** | A parallel version of one or more services, run from a git worktree on a feature branch on its own dynamic port, beside base — reusing base for everything it doesn't change. Lets you run several features live at once without cloning the world. |
| **Environment** | A named config bundle (`environments:` in the workspace manifest) carrying an infra target (local/remote + observability) and config-var patches, applied at workspace / service / stack scope (most-specific wins). "Where a service points." |

---

## CLI Commands

### Workspaces

```bash
devstack workspace list              # List all registered workspaces
devstack workspace add [path]        # Register a directory as a workspace
devstack workspace remove <name>     # Unregister a workspace
devstack workspace up                # Start the dev daemon (background)
devstack workspace down              # Stop the dev daemon and OTEL collector
```

`devstack ws` is an alias for `devstack workspace`.

### Services

```bash
devstack start [service] [--stack <name>]     # Start a service + deps (base, or a stack's instance)
devstack start --group=<name>                 # Start all services in a group
devstack restart [service] [--stack <name>]   # Rebuild/restart base, or a stack's instance
devstack stop [service] [--stack <name>]      # Stop base, or a stack's instance
devstack status [--stack <name>]              # Live service tree: state, ports, deps, and ENV (where each points)
```

`start`/`restart`/`stop` auto-detect the current service from the working directory when no name is given. If the dev daemon isn't running, `start` boots it automatically. `--stack <name>` targets that stack's instance instead of base (see [Feature stacks](#feature-stacks)).

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

### Feature stacks

A **feature stack** runs a parallel version of one or more services beside base, each changed service in its own **git worktree** on a feature branch and its own dynamically-allocated port, all folded into the one host daemon as `<workspace>:<service>:<stack>` resources. It reuses base for everything it doesn't change, so you can run several features live at once without cloning the world.

```bash
devstack stack create <name> --repos <svc>[,<svc>]  # worktrees for the changed services (+ their callers)
devstack stack up <name>                             # fold the stack's services into the host daemon
devstack stack down <name>                           # stop the stack (keeps its worktrees)
devstack stack list                                  # stacks, their ports, and active state
devstack stack rm <name>                             # remove worktrees, release ports, delete the record
devstack stack config <svc> --stack <name>           # effective config the stack's service runs with
```

Work on a stack by **`cd`-ing into its worktree** (path shown by `stack create`/`stack list`) and editing there — it's already on the stack's branch, so you never `git checkout`. Reload only that instance with `restart --stack <name>`. Base and other stacks are untouched; each edit stays on its own branch.

> **Shared state is not isolated.** Stacks isolate code and ports, not the database, queues, or caches — two stacks on one DB see each other's data. Point a service elsewhere with an [environment](#environments) if you need to.

### Environments

An **environment** is a named config bundle in the workspace manifest that repoints services — DB URLs, feature flags, endpoints, and the observability target — without code changes. Environments are defined **once in the base workspace manifest** and inherited by feature stacks — a stack doesn't define its own; `env use --stack <name>` just points a stack at one of the base's environments. It applies at three scopes, most-specific winning: **stack > service > workspace**. So base can run against `local` while one stack runs against `prod`.

```bash
devstack env add <name> [--type local|remote] [--url ...] [--api-key ...]   # define an environment
devstack env set <name> KEY=VALUE                    # set a config-var patch (any value, incl. secrets — masked in output)
devstack env use <name> [--service <svc>] [--stack <name>]   # point base, a service, or a stack at <name>
devstack env which [--service <svc>] [--stack <name>]        # which env an instance resolves to + its values
devstack env show <name>                             # an environment's values (secrets masked)
devstack env list                                    # environments and the active one
```

`devstack status` shows each instance's active env in the **ENV** column, so you can see where every running copy points. Env values live in the workspace manifest and are masked on display.

### Observability (OTEL collector)

Observability is **opt-in per workspace** — devstack does not assume your services are OTEL-instrumented. While it's off, no collector runs and nothing is injected into services. Turn it on when you want traces/logs:

```bash
devstack otel enable                 # sets observability.enabled in the workspace manifest
devstack otel enable --backend=forwarding   # enable with a specific backend
devstack otel disable
```

Or set it directly in `devstack.workspace.yaml`:

```yaml
observability:
  enabled: true
  backend: signoz        # default when enabled; use "forwarding" for collector-only / BYO backend
```

When enabled, `devstack workspace up` starts a local `otelcol-contrib` collector (detached, logging to its own file) and the OTLP endpoint (`localhost:4317`, gRPC) is pushed down to every service automatically — you never repeat it. If the collector is ever down while enabled, `devstack status` warns and tries to restart it.

> **Prereq:** the collector needs `otelcol-contrib` on `$PATH`. If it's missing, the workspace still comes up but `devstack otel start` fails until you install it — download the matching binary from [opentelemetry-collector-releases](https://github.com/open-telemetry/opentelemetry-collector-releases/releases), or point `OTELCOL_BIN=/path/to/otelcol-contrib`.

**Backends.** The default backend when enabled is **SigNoz** (local UI via Docker — spins up ClickHouse + SigNoz). To forward to your own OTLP endpoint instead of running SigNoz, use the `forwarding` backend:

```bash
# Forward to any OTLP endpoint via gRPC
devstack otel configure --plugin=forwarding --set upstream=telemetry.example.com:443 --set protocol=grpc

# Forward via HTTP
devstack otel configure --plugin=forwarding --set upstream=https://otel.example.com:4318
```

`--plugin` (the backend) is persisted to the workspace manifest, so the choice sticks. Per-developer endpoint override: set `OTEL_EXPORTER_OTLP_ENDPOINT` in `.envrc` in any service repo.

```bash
devstack otel status                 # collector state, ports, upstream + per-service telemetry evidence
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

The tool set adapts to the active workspace: trace tools (`investigate`) appear only when observability is enabled, and the `tunnel` tool only when Tailscale is installed. Call `environment` first to see what's actually available.

### `environment`
Orientation tool — shows the active infra environment (local vs remote) and which tools exist in this context, and points at the config-patch [environments](#environments) a service can be aimed at via `env_use`. Call this first.

### `status`
Show all services with state (`running` / `building` / `starting` / `stopped` / `disabled` / `error`), ports, source path, ENV (the active environment each instance points at), and last build error. Pass `stack` to see a feature stack's instances.

### `restart`
Trigger a rebuild/restart for a service. Auto-enables the service first if it was disabled.

Parameters: `service` (optional — uses `DEVSTACK_DEFAULT_SERVICE` if omitted), `stack` (optional — target a stack's instance).

### `stop`
Disable one or all services.

Parameters: `service` (optional — if omitted, stops everything), `stack` (optional — target a stack's instance).

### `stack_create` / `stack_up` / `stack_down` / `stack_list` / `stack_rm`
Manage [feature stacks](#feature-stacks) over MCP — create a stack's worktrees, bring it up/down in the host daemon, list them, or tear one down. So an agent can spin up a parallel version of a service, work on it, and clean it up without shelling out.

### `env_use` / `env_which` / `env_set`
Manage [environments](#environments) over MCP — point a workspace/service/stack at an env (`env_use`), see which env an instance resolves to and its values (`env_which`), or set a config-var patch (`env_set`). Environments are defined once in the base workspace manifest and inherited by stacks; `env_use` with `stack` points a stack at one of the base's environments. Secrets are masked in output.

### `configure`
Set a Tilt runtime argument (`key=value`). Tilt reloads affected services automatically. Useful for feature flags and environment switching.

Parameters: `key` (required), `value` (required).

### `process_logs`
Fetch raw stdout/stderr from a service via Tilt. Use this for services that don't export OTEL logs, or when you need unstructured process output.

Parameters: `service` (optional), `lines` (default 100), `errors_only` (filter to error/exception/panic/fatal/fail lines).

If no service is given and no default is configured, fetches all services in parallel.

### `service_env`
Inspect and edit a service's resolved env: `get` shows the value each key resolves to **and the rung/env it came from**, `diff` compares across services, `set` writes to the manifest or `.envrc`, `check` audits required keys. Takes an optional `stack` to inspect a stack's instance.

### `observability`
Inspect and change the workspace's OTEL config: `status` (enabled, backend, collector, + per-service telemetry evidence), `enable`, `disable`, `configure`. Always available locally, so an agent can discover and turn observability on.

### `tunnel`
Forward service ports to/from a remote host over SSH (push/pull/list/status/stop). Only registered when Tailscale is installed.

### `investigate`
Primary trace tool — **only available when observability is enabled**. Queries the backend (SigNoz by default) for distributed traces and correlated logs, then falls back to dev-daemon process logs if OTEL logs are unavailable.

Three modes:

| Mode | Trigger | What happens |
|------|---------|-------------|
| **Trace lookup** | `trace_id` given | Full span tree + logs for that specific trace |
| **Attribute search** | `attribute` + `value` given | Find all root spans where e.g. `portfolio.id=57835`, then expand each trace |
| **Recent executions** | Neither given | Most recent executions (scoped to default service if set) |

Parameters: `trace_id`, `attribute`, `value`, `service`, `stack` (absent = base instance only, a name = that stack, `"all"` = every instance), `since_minutes` (default 5), `limit` (default 3), `errors_only`.

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
| `<workspace>/devstack.workspace.yaml` | Workspace manifest: services, groups, dependencies, observability |
| `<service>/devstack.service.yaml` | Service manifest: run command, ports, healthcheck, env |
| `<service>/.mcp.json` | MCP config for that service repo |
| `<service>/AGENTS.md` | Tool reference injected for Claude Code |
