# 01 — Base goes; the shared set follows main

## Today

- `workspace up` builds the replica and runs every service from it as base
  (`cmd/start_cmd.go:84-170`, `internal/hostdaemon/hostdaemon.go:196-247`).
- A stack runs its overlay. Every other service resolves to base's pinned
  port through the port book (`stack.GenerateOptions`,
  `internal/stack/stack.go:578-610`; `MergeStackBook`,
  `internal/config/refs.go:42-52`).
- Infra is declared as ordinary services. In navexa: `navexa-sql`,
  `lake-clickhouse`, `azurite`, `agent-psql`.
- `agent-psql` is in the `roi` group. A stack with `roi` runs a second
  supervisor of the same compose containers on an allocated port. The
  containers still listen on 5433/6380. No navexa `${}` reference points at
  infra today, so nothing reads the wrong port yet.

## Change

### Two kinds of service

- **shared**: named in the workspace `shared:` list. Runs once per
  workspace. Every database and stateful store is shared. They are too
  heavy to run per stack, and one shared copy is simpler and faster.
- **stack service**: everything else. Runs only inside a stack, one copy
  per stack.

```yaml
shared:
  - navexa-sql
  - lake-clickhouse
  - azurite
  - agent-psql
```

- A name in `shared:` must be a declared service. Else, load fails.
- A shared service keeps its pinned ports. devstack never allocates a port
  for it.
- A shared service cannot `need` a stack service. Else, load fails.
- devstack gives no guidance on running a database per stack. Stacks use
  the shared copy.

### Shared services follow main

- A shared service runs from the replica: a worktree detached at the
  latest default branch tip. Never from the checkout.
- devstack refreshes the replica on a timer. The sweep timer (03) runs
  `devstack tick` every 10 min. Once an hour, the tick runs the same refresh
  as `devstack shared sync`. It fetches, moves each replica
  worktree to the default tip, and restarts only the shared services whose
  repository moved.
- If the fetch fails, devstack keeps the current tip and logs it. It never
  stops a running shared service because of a failed fetch.
- `devstack shared sync` runs the same refresh by hand.
- In practice the shared set is databases and stores. A shared app service
  (for example an API many stacks call) is allowed and follows main the
  same way, but it is not the normal case.

### Running

- `workspace up` starts the shared set. The shared set stays up until
  `workspace down`. (Owner decision.)
- Tilt names a shared resource `<ws>:<svc>`. A stack resource stays
  `<ws>:<svc>:<stack>`.
- A stack service never runs outside a stack. To run the default branch,
  create a stack with services and no branch:
  `devstack stack create main --repos navexa-frontend --detach`. Every worktree is
  detached at the default tip. Code change: today `stack.go:112-115` and
  `worktree.go:44-45` refuse this.
- The replica stays, scoped to the repositories that hold shared services.
  A workspace whose shared services need no repository builds no replica.
  `stack up` builds the replica if it is missing. Today only
  `workspace up` does.
- Tilt deps: a stack resource that needs a shared service gets the dep
  `<ws>:<svc>`, with no stack suffix. Today `tiltgen.go:287-291` suffixes
  every dep, which would name a resource that does not exist.

### Stacks

- A stack runs the services you name and their `needs`. Nothing else. See
  02.
- Every stack service in a stack gets a worktree. A repository the branch
  changes gets the branch. Every other repository is detached at the
  default branch tip.
- The port book has two sources: shared services (pinned) and the stack's
  own services (allocated). There is no base source.
- A `${svc.*}` reference to a service that is neither shared nor in the
  stack resolves to that service's declared port, where nothing serves.
  devstack warns: "`<svc>` is not in this stack. Run `stack add <svc>` if
  the change needs it." It does not fail. Today the ref resolves to base.
  Hooks resolve refs the same way (`internal/hooks/fire.go:52-59`).
- `Record.Overlay` becomes `Record.Members`. JSON reads the old key. Hook
  default scope (`fire.go:63-64`) uses members.
- `stack create` and `stack add` refuse a shared service, and name the
  shared set.
- `filterEdges` keeps edges to shared services. Today it drops every edge
  whose target is outside the overlay, so start order on infra is lost.

### Data

- Shared data. Every stack sees the same rows.
- Per-stack isolation inside a shared store stays opt-in:
  `${stack.suffix:...}` and the `stack.create`/`stack.destroy` hooks
  (both exist today).
- Risk: a migration that one stack runs changes the database for every
  stack. When resync moves commits that change a service's `migrations:`
  directory, it says so and names `task_run` (05).

### Addressing

- A service name resolves by kind:
  - shared → the shared resource. No stack needed.
  - stack service → needs `--stack` or a cwd inside a stack worktree.
- `--stack base` and `stack="base"` are removed. The guard keeps refusing
  the name `base` for a new stack, so old scripts fail loudly.
- `env_use` and `env set` used `stack="base"` to set the workspace default
  (`tools_env.go:33,40`, `env_cmd.go:31`). They get `scope: workspace`, and the CLI
  gets `env use --workspace-default`.
- The `base` tool and `devstack base` become `shared` (`status`, `path`,
  `sync`). `path` returns the replica path of a shared service.
- Agents may `status` and `process_logs` a shared service. Agents may not
  `stop` or `restart` one, because every stack depends on it. This is new
  guard logic: today a call with no stack is refused
  (`guard.go:210-211`).
- Defaults that point at base change behaviour, not only text:
  `investigate` scope defaults to base (`observability/backend.go:26,35`)
  and becomes "all stacks and shared". `tunnel` (`as_base`, now `as_declared_ports`), `urls` and
  the panel read base ports or rows. Plan step 3 traces each one.

## Migration (plan step 7)

`devstack migrate shared-stacks` does this, with a dry run first:

**1.** Writes `shared:` from a list the owner confirms.
**2.** Converts the workspace edge maps to per-service edges (02).
**3.** Rewrites each stack record: `Overlay` → `Members`. It adds no
services. A stack that relied on base for a service now warns at
`stack up`, and the agent or owner runs `stack add`.
**4.** Removes shared services from groups and from existing stack
records. A stack that ran its own `agent-psql` (the `roi` group) now uses
the shared one.

Cutover on navexa: `workspace down`, install, `migrate shared-stacks --apply --shared
navexa-sql,lake-clickhouse,azurite,agent-psql`,
`workspace up`, then `stack up` each stack. Today's cutover notes say an
old teardown can orphan a stack process on its port. Check with `doctor`
after `workspace down`.

Navexa work before cutover (manifest, not devstack code):

- Add `shared:` with `navexa-sql`, `lake-clickhouse`, `azurite`,
  `agent-psql`.
- `agent-psql` already says "Base and every stack share one compose
  project". As a shared service it runs once, so the second supervisor in
  `roi` stacks goes away.
