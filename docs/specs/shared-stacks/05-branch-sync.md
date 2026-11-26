# 05 — Branch attach and resync

## Today

- Attach at create: if origin has the branch, the worktree tracks it
  (`internal/worktree/worktree.go:48-66`).
- Resync fast-forwards each stack worktree to `origin/<branch>`. It refuses
  dirty, diverged or off-branch worktrees (`internal/stack/resync.go`).
- If the stack is up, resync triggers the moved services and their
  dependents within the stack (`internal/mcp/tools_stack_resync.go:92-128`).
- `rec.Overlay` is fixed at create. A pushed change to a repo outside the
  overlay stays on base's copy, silently.

## Change

### Resync

- Mechanics stay: fetch, fast-forward only, same refusals.
- New: resync also moves each clean **detached** worktree to the latest
  default branch tip. Today resync skips detached repos
  (`resync.go:92-93`). Base followed main through `syncBase`
  (`start_cmd.go:99`), but base is gone. Without this, a stack's detached
  `navexa-api` stays old forever, and runs against migrations that newer
  stacks applied to the shared database.
- New: for each workspace repo **not** in the stack, run
  `git ls-remote origin refs/heads/<branch>`. If the branch exists there,
  report: "branch also changes `<repo>`, which this stack does not run.
  Run `stack add <service>`." Resync does not add it.
- New: after the fast-forward, recompute the `needs` closure. If a member
  now needs a service the stack lacks, report it and name `stack add`.
- Resync sets the pane binding (04). It does not move `StopAt` (03).
- Resync on a stopped stack moves the worktrees and starts nothing, as
  today.

### Up after an auto-stop

- `stack up` runs the "behind origin" check that `doctor` runs today. It
  also checks each detached worktree against the default tip. If either is
  behind, `up` reports it and names `stack_resync`. It does not resync by
  itself.

### Attach

No change. A stack cut from a pushed branch tracks it.

### Data

- Resync runs no migrations, as today.
- With shared databases, a pushed migration that one stack applies changes
  data for every stack. Resync output names `migrate` and this risk when
  the moved commits touch a migrations directory. (Detection rule: open.)

### Agents and handoff

- Agent stacks are stopped, never destroyed (03). A factory handoff no
  longer races a destroy deadline.
- `stack_adopt` clears the owner only.
- Foundry text that names the 72h cap, `expires_at`, or "adopt before
  expiry" must change (06).

## Open questions

- How does resync know the moved commits touch migrations? A per-service
  `migrations:` path in the service definition is one option.
