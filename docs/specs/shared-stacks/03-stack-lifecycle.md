# 03 — Stacks stop after one day; no agent quota

## Today

- Only stacks made by a non-owner MCP caller get `Owner` and `ExpiresAt =
  create + 24h` (`internal/mcp/tools_stack.go:130-137`,
  `internal/mcp/guard.go:101`).
- `CollectExpired` destroys an expired stack with `force=true`, so
  uncommitted work is lost (`internal/mcp/tools_stack_expiry.go:71-108`).
  The `stack gc` help says the opposite.
- The sweep runs every 10 min, but only inside `serve --transport http`
  (`cmd/serve.go:189-201,261`).
- Owner stacks never expire. Quota counts every stack with an owner, per
  workspace (`tools_stack_expiry.go:18`).

## Change

### The stop deadline

- Every stack gets `StopAt` on its record. Owner and agent stacks alike.
- `stack create`: `StopAt = CreatedAt + 24h`.
- `stack up`: `StopAt = now + 24h`.
- `stack extend <hours>`: `StopAt = max(StopAt, now) + hours`. Max 24 per
  call. No lifetime cap, because nothing is destroyed.
- `DEVSTACK_STOP_AFTER` (default `24h`) sets the window. It replaces
  `DEVSTACK_AGENT_TTL`. The old name loads with a warning.

### The sweep

- `devstack stack sweep [--dry-run]` stops every up stack past `StopAt`.
  It replaces `stack gc`. The old name stays as an alias for one release.
- Stop = today's `stack down`: fire `stack.down`, set `Active=false`,
  regenerate the host Tiltfile. Worktrees, record and ports stay.
- The sweep sets `StoppedBy: "sweep"` and `StoppedAt` on the record, and
  appends to the stack log: `stopped by devstack: 24h since up`. A manual
  `stack down` sets `StoppedBy: "user"`. herdr's `stopped-auto` state (04)
  reads this field.
- The sweep never removes a stack.
- The sweep covers every active workspace on the host, from the registry.
  It needs no cwd. Today `CollectExpired` takes one workspace.
- `workspace down` fires `stack.down` for each up stack and publishes to
  herdr. Today `DeactivateAll` (`down_cmd.go:56`) fires neither, so panes
  would show a stale `up`.

### Where the sweep runs

- A systemd user timer, `devstack-sweep.timer`, runs `devstack stack sweep`
  every 10 min. This is new: `devstack-tilt` is a transient
  `systemd-run --collect` unit with no unit file
  (`hostdaemon.go:558-560`). `workspace up` writes
  `devstack-sweep.service` and `.timer` to `~/.config/systemd/user/`, runs
  `daemon-reload`, and enables the timer. It skips this when both exist.
- A host without systemd (direct mode, `hostdaemon.go:400-403`) gets no
  timer. There, every devstack CLI and MCP call runs the sweep first, at
  most once per 10 min (a timestamp file in the data dir).
- The HTTP serve loop stops sweeping. One sweeper, one place.
- The same timer runs `devstack shared sync` once per hour (01).

### Expiry fields

- `ExpiresAt` is removed. Migration copies it into `StopAt`.
- `stack_adopt` clears `Owner` only. It no longer touches the deadline.
- The 72h `MaxLifetime` cap is removed.

### Agent quota: removed

- Today: max 2 stacks with an owner, per workspace.
- Change: no quota. An agent creates and starts as many stacks as it wants.
  (Owner decision: no arbitrary limits. The real limit is memory, and the
  24h stop handles it.)
- Remove `agentQuota`, `MaxStacks`, `DEVSTACK_AGENT_MAX_STACKS` and
  `agents.maxStacks`. The old env and config keys load with a warning.
- The ownership guard stays: an agent acts only on its own `agent-*`
  stacks.

### Visibility

- `stack list`, `status`, `environment` show the deadline:
  `up — stops 14:20 tomorrow` or `stopped by devstack 2026-11-27 10:00`.
- `doctor` drops the "expired, not collected" finding. It adds nothing new.

## Not in scope

The owner asked to stop stacks, not to clean them up. Stopped stacks still
hold worktrees on disk. `cleanup` and `stack rm` stay manual.

## Open questions

- Should an MCP call on a stack (restart, resync) push `StopAt` back? The
  owner chose a fixed clock, so this spec says no.
