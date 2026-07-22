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

### Rules

- **`investigate` first** when something is broken — it correlates traces and logs in one call
- **Stop only what you started** — don't tear down the whole stack unless asked
- **Never use devstack for prod/staging** — it only sees local Tilt-managed processes
