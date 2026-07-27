# Agent Instructions

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
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

> **LOCAL DEV ONLY** — devstack manages local services running under Tilt.
> Do not use it to investigate staging or production issues.

Default service: `devstack`
### Starting the stack (CLI)

MCP tools require Tilt already running. Always use the shell CLI to spin up.

```bash
devstack status                    # check if Tilt is running
devstack workspace up              # start the dev daemon if stopped
devstack services                  # list services, groups, and declared deps
devstack start --group=<group>     # start a named group (resolves deps)
devstack start devstack                       # start this service + its deps
```

### While the stack is running (MCP tools)

| Need | Tool |
|------|------|
| Live service states + ports | `status` |
| Something is broken — start here | `investigate` — traces + correlated logs in one call |
| Raw stdout/stderr | `process_logs` (set `errors_only=true` to filter noise) |
| Rebuild after a code change | `restart [name]` |
| Stop service(s) | `stop [name]` — omit name to stop all |
| Change a Tilt config value | `configure key=<k> value=<v>` |

### Querying telemetry

One collector and one backend (OpenObserve by default) serve the whole machine, so every workspace and every feature stack lands in the same store. Nothing needs configuring to query it — devstack resolves the backend, endpoint and credentials, and confines results to the current workspace. Variants are told apart by resource attributes: `devstack.workspace`, `devstack.service`, `devstack.stack` (`base` or a stack name), `devstack.env`.

```bash
devstack otel services                  # which variants are reporting, with stack + env
devstack otel traces                    # recent traces (defaults to the service you are in)
devstack otel traces --stack <name>     # only that stack's instance
devstack otel traces --service all      # every service in the workspace
devstack otel logs --trace <trace-id>   # logs correlated with one trace
devstack otel status                    # per-variant evidence: which instances are emitting
```

A service usually reports itself under a name of its own choosing, not the one devstack uses (devstack `navexa-api` reports as `Navexa.API`). Filters match either; `otel services` shows both.

### Rules

- **`investigate` first** when something is broken — it correlates traces and logs in one call
- **Pass `stack` when a feature stack is involved** — omitting it queries the base instance only, so a stack's traffic reads as missing
- **Check `otel status` before calling a service silent** — it reports which variants actually emitted
- **Stop only what you started** — don't tear down the whole stack unless asked
- **Never use devstack for prod/staging** — it only sees local Tilt-managed processes
