# Agent Instructions

This project uses **bd** (beads) for issue tracking. Run `bd onboard` to get started.

## Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --status in_progress  # Claim work
bd close <id>         # Complete work
bd sync               # Sync with git
```

<!-- BEGIN BEADS INTEGRATION -->
## Issue Tracking with bd (beads)

**IMPORTANT**: This project uses **bd (beads)** for ALL issue tracking. Do NOT use markdown TODOs, task lists, or other tracking methods.

### Why bd?

- Dependency-aware: Track blockers and relationships between issues
- Git-friendly: Auto-syncs to JSONL for version control
- Agent-optimized: JSON output, ready work detection, discovered-from links
- Prevents duplicate tracking systems and confusion

### Quick Start

**Check for ready work:**

```bash
bd ready --json
```

**Create new issues:**

```bash
bd create "Issue title" --description="Detailed context" -t bug|feature|task -p 0-4 --json
bd create "Issue title" --description="What this issue is about" -p 1 --deps discovered-from:bd-123 --json
```

**Claim and update:**

```bash
bd update bd-42 --status in_progress --json
bd update bd-42 --priority 1 --json
```

**Complete work:**

```bash
bd close bd-42 --reason "Completed" --json
```

### Issue Types

- `bug` - Something broken
- `feature` - New functionality
- `task` - Work item (tests, docs, refactoring)
- `epic` - Large feature with subtasks
- `chore` - Maintenance (dependencies, tooling)

### Priorities

- `0` - Critical (security, data loss, broken builds)
- `1` - High (major features, important bugs)
- `2` - Medium (default, nice-to-have)
- `3` - Low (polish, optimization)
- `4` - Backlog (future ideas)

### Workflow for AI Agents

1. **Check ready work**: `bd ready` shows unblocked issues
2. **Claim your task**: `bd update <id> --status in_progress`
3. **Work on it**: Implement, test, document
4. **Discover new work?** Create linked issue:
   - `bd create "Found bug" --description="Details about what was found" -p 1 --deps discovered-from:<parent-id>`
5. **Complete**: `bd close <id> --reason "Done"`

### Auto-Sync

bd automatically syncs with git:

- Exports to `.beads/issues.jsonl` after changes (5s debounce)
- Imports from JSONL when newer (e.g., after `git pull`)
- No manual export/import needed!

### Important Rules

- ✅ Use bd for ALL task tracking
- ✅ Always use `--json` flag for programmatic use
- ✅ Link discovered work with `discovered-from` dependencies
- ✅ Check `bd ready` before asking "what should I work on?"
- ❌ Do NOT create markdown TODO lists
- ❌ Do NOT use external issue trackers
- ❌ Do NOT duplicate tracking systems

For more details, see README.md and docs/QUICKSTART.md.

<!-- END BEADS INTEGRATION -->

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd sync
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds

## Dev Stack (devstack MCP)

devstack is an MCP server that controls this workspace's services via Tilt (a local process orchestrator).
Workspace: `/tmp/test-init-ws`
Default service: `fake-svc` — MCP tools that accept `name` use this when `name` is omitted.

**First step**: always call `status` to see what's running and get exact service names before taking any action.

**If Tilt is not running**: call `devstack start` from the shell before using any MCP tools.

> Note: a Stop hook is configured to call `devstack disable fake-svc` when this Claude session ends.

### MCP Tools

| Tool | Args | What it does |
|------|------|--------------|
| `status` | — | List all services with build status, runtime status, and ports. **Always call this first** — do not guess service names. |
| `start` | `name` (optional) | Tell Tilt to start/build a single service. Does not resolve dependencies — use `devstack enable` (CLI) if deps are needed. |
| `restart` | `name` (optional) | Rebuild and restart a service. Use after code changes. |
| `stop` | `name` (optional) | Stop a single service without touching others. |
| `start_all` | `services` (comma-separated, optional) | Start multiple services at once. Omit `services` to start everything. |
| `stop_all` | — | Stop all services. Tilt daemon keeps running. |
| `logs` | `name` (optional), `lines` (default 100) | Fetch recent log output from a service. |
| `errors` | `name` (optional), `lines` (default 50) | Fetch current error lines from a service — raw stderr/failure output. |
| `what_happened` | `name` (optional), `since_minutes` (default 15) | Get a chronological timeline of recent events: crashes, restarts, errors. Use this to diagnose *why* something broke, not just *what* is broken. |
| `set_environment` | `key`, `value` | Set a named Tilt argument, causing Tilt to reload affected services. Valid keys are declared in the Tiltfile via `config.parse` — grep the Tiltfile or ask the user what arg to set. Example: `key=ENV value=Staging` switches all .NET services to Staging. |

### Shell CLI

Use the shell CLI for lifecycle management and dependency-aware service control.
Prefer CLI over MCP tools when starting services that have dependencies.

| Command | What it does |
|---------|-------------|
| `devstack status` | Same as the MCP `status` tool — live service table with ports |
| `devstack enable <service>` | Start a service **and all its declared dependencies** (reads `.devstack.json`) |
| `devstack enable --group=<name>` | Start a named group of services with dep resolution |
| `devstack disable <service>` | Stop one service; leaves other services running |
| `devstack start` | Start the Tilt daemon for this workspace (required before MCP tools work) |
| `devstack down` | Stop the Tilt daemon — **this breaks all MCP tools until `devstack start` is run again** |
| `devstack open` | Open the Tilt UI in a browser |
| `devstack deps show` | Show declared service dependencies |
| `devstack deps add <svc> <dep>` | Declare that `<svc>` depends on `<dep>` |
| `devstack otel open` | Open Aspire Dashboard (OTEL traces + logs UI) in browser |

> The Aspire Dashboard (http://localhost:18888) receives traces and logs from all instrumented services. Query by service, trace ID, or business attributes (user ID, portfolio ID).

### Service Dependencies

Dependencies are declared in `/tmp/test-init-ws/.devstack.json`. When you run `devstack enable <service>`, devstack reads this file and starts all deps first, in order.

**How to add a dependency**

Use the CLI — do not hand-edit the JSON:

```
devstack deps add <service> <dependency>
```

Example: you are working on `service-a` and it fails to connect because `service-b` is not running:

```
devstack deps add service-a service-b   # declare the dependency
devstack deps show service-a            # verify: shows resolved start order
devstack enable service-a              # now starts service-b first, then service-a
```

**When to add a dependency**

Add a dep when a service consistently fails to start because another service is not running — e.g. a connection refused error on startup pointing at another service in this workspace. Do not add deps speculatively.

**Confirm before adding** — `.devstack.json` is committed to the repo and shared. Ask the user before running `devstack deps add` if you are not certain the dependency is real.

**Check existing deps first**

```
devstack deps show              # all declared deps
devstack deps show <service>    # resolved start order for one service
```

### Adding New Services

To add a new service to this workspace, run from the workspace root:

```
devstack onboard <service-name> <service-path>
```

This will:
1. Auto-detect the service language (dotnet, go, python, node) from files in `<service-path>`
2. Append a `local_resource(...)` block to the workspace Tiltfile
3. Register the service path in `.devstack.json`
4. Write `.mcp.json` into the service directory (wires up devstack MCP)
5. Append devstack instructions to `AGENTS.md` in the service directory

Options:
```
devstack onboard <name> <path> --port=<port>    # specify HTTP port for readiness probe
devstack onboard <name> <path> --lang=<lang>    # override language detection
devstack onboard <name> <path> --label=<label>  # override Tiltfile label
```
