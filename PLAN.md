# Plan: nvxdev-mcp — Tilt MCP Server for Navexa Dev Stack

## Context

`dev.py` gives good ergonomics for spinning up the Navexa dev stack but has no API surface for AI agents. The replacement is Tilt (`nvxdev/Tiltfile`), which exposes a CLI and HTTP API. The missing piece is an MCP server that bridges Tilt's interface to Claude — so that when you say "what the fuck happened to the importer" or "spin up the API and frontend", I can act on it without you telling me which CLI commands to run.

`tilt-mcp` (0xBigBoss, TypeScript/Bun) covers the basics but doesn't fit the existing Go MCP pattern (`navexa_mcp`) and lacks smart/aggregate tooling. We build our own in Go.

---

## Implementation

### Location: `nvxdev/mcp/`

Mirrors `navexa_mcp/` structure exactly:

```
nvxdev/mcp/
├── go.mod                        # module nvxdev-mcp, mark3labs/mcp-go v0.43.1
├── main.go                       # entrypoint: cmd.Execute()
├── cmd/
│   ├── root.go                   # Cobra root, Viper bindings (TILT_PORT, TILT_HOST)
│   └── serve.go                  # serve --transport=stdio|http
└── internal/
    ├── tilt/
    │   └── client.go             # wraps tilt CLI + GET /api/view
    └── mcp/
        ├── server.go             # NewMCPServer + RegisterTools
        └── tools.go              # all tool handlers
```

### Config (Viper)

| Flag | Env var | Default |
|------|---------|---------|
| `--tilt-port` | `TILT_PORT` | `10350` |
| `--tilt-host` | `TILT_HOST` | `localhost` |

### Tilt client (`internal/tilt/client.go`)

- `GetView() (*TiltView, error)` — `GET http://{host}:{port}/api/view`, unmarshals UIResource list
- `RunCLI(args ...string) (string, error)` — exec `tilt <args>` with 30s timeout, captures stdout+stderr

### Tools (`internal/mcp/tools.go`)

**Service control:**

| Tool | Args | What it does |
|------|------|--------------|
| `stack_status` | — | Calls `GetView()`, returns table: name / status / ready / last-error for all services |
| `start_service` | `name` (required) | `tilt trigger <name>` |
| `restart_service` | `name` (required) | `tilt trigger <name>` (Tilt kills+relaunches automatically) |
| `stop_service` | `name` (required) | `tilt disable <name>` |
| `start_stack` | `services` (optional string array) | trigger each named service, or all if empty |
| `stop_stack` | — | `tilt disable` for all known services |

**Log & diagnostic:**

| Tool | Args | What it does |
|------|------|--------------|
| `get_logs` | `name` (required), `lines` (default 100) | `tilt logs --tail=N <name>` |
| `get_errors` | `name` (optional), `lines` (default 50) | Runs `get_logs`, filters lines containing `error\|exception\|panic\|fatal\|fail` (case-insensitive). If no name: all services in parallel, results labelled by service |
| `what_happened` | `name` (optional), `since_minutes` (default 15) | Pulls recent logs from one or all services, applies error filter, formats as chronological summary per service. This is the "what the fuck happened" tool. |

**Environment:**

| Tool | Args | What it does |
|------|------|--------------|
| `set_environment` | `env` (required): Development/Staging/Production | `tilt args --set env=<value>` — affects all .NET services |

### Service name aliases

`client.go` holds a map of friendly → canonical Tilt resource names:

```go
var aliases = map[string]string{
    "trade importer":    "nxTradeImporter",
    "file importer":     "nxFileImporter",
    "ai importer":       "ai-file-importer",
    "api":               "navexa-api",
    "frontend":          "navexa-frontend",
    "tunnel":            "ssh-tunnel",
}
```

`ResolveService(name string)` checks exact match first, then alias map, then returns an error with available names listed.

### Integration: global user-scoped MCP

Claude Code resolves `.mcp.json` only for the directory it's opened in. Sub-repos (`nxFileImporter/`, `Navexa/`, etc.) need dev stack tools too — so this server is registered at **user scope**:

```bash
claude mcp add --scope user --transport stdio nvxdev \
  --env TILT_PORT=10350 \
  -- go run /home/nick/dev/navexa/nvxdev/mcp/. serve --transport=stdio
```

This writes into `~/.claude.json` and makes it available in every Claude Code session regardless of working directory.

**Stdio transport is fine** — the server is stateless (wraps tilt CLI), so multiple Claude sessions each spawning their own instance causes no conflicts.

**When Tilt is not running**, all tools return a clear error:
> "Tilt is not running. Start it with: `cd /home/nick/dev/navexa/nvxdev && tilt up`"

Checked in `client.go` by attempting `GET /api/view` before any operation.

---

## Critical files

- **Reference pattern**: `navexa_mcp/internal/mcp/server.go`, `navexa_mcp/cmd/serve.go`, `navexa_mcp/cmd/root.go`
- **Tiltfile**: `nvxdev/Tiltfile` (add mcp resource if desired)
- **New files**: everything under `nvxdev/mcp/`

---

## Verification

1. `cd nvxdev/mcp && go build ./...` — compiles clean
2. Start Tilt: `cd nvxdev && tilt up`
3. `echo '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | go run . serve --transport=stdio` — lists all tools
4. `stack_status` call returns JSON with all service states
5. Trigger a service in Tilt UI, confirm `stack_status` reflects it
6. Intentionally crash a service, call `what_happened` — confirm errors surface
7. Run `claude mcp add --scope user ...`, open Claude Code in `nxFileImporter/` — confirm `nvxdev` MCP tools are available conversationally
