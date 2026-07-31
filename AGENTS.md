# Agent Instructions

## Landing the plane (session completion)

When you end a work session, work is not complete until every change is committed and its state is reported. Steps:

1. File issues for anything that needs follow-up.
2. Run the quality gates if code changed: tests, linters, build.
3. Update issue status: close what is finished, update what is in progress.
4. Commit everything, on the branch you were working on. Clear your stashes. Nothing you wrote is left uncommitted.
5. Push only where pushing is already the agreed flow for that branch: one that already tracks a remote, or one the human asked you to push. A feature stack's branch lives in its own worktree and is not that by default, so ask first, and say what you would push and where.
6. Hand off with context for the next session, naming anything still unpushed.

Rules:

- Never leave work stranded. Uncommitted or stashed at the end of a session is a failure.
- Pushing a branch nobody asked you to push is also a failure. Ask, then push.
- Never force-push, `--force-with-lease` included, without being asked in that session.
- If a push fails, stop and report the error. Do not retry your way through it.
- Merging is a separate decision and always the human's. Ask before you merge a branch into base or open a PR; never merge unilaterally.


## Dev Stack (devstack MCP)

> **LOCAL DEV ONLY** — devstack manages local services running under Tilt.
> Do not use it to investigate staging or production issues.

Default service: `devstack`
### Starting the stack (CLI)

MCP tools require Tilt already running. Always use the shell CLI to spin up.

```bash
devstack status                    # check if Tilt is running
devstack workspace up              # start the dev daemon if stopped
devstack status                    # every instance, its port, env and state
devstack workspace topology        # services, groups, dependencies, dependents
devstack group start <group>       # start a named group (resolves deps)
devstack service start devstack    # start this service + its deps
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
