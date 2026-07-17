# Stacks

Run more than one feature live at once, without standing up a full replica of
the world for each.

> Revision 2. Revised after three independent reviews. Revision 1 claimed "no
> re-keying of the registry is required" — that was false and is corrected in
> §Identity. Line references verified against HEAD.

## Concept

A **stack** is a running instance of the workspace. The **base stack** is the one
you run today: every service, from the primary checkouts, on pinned ports.

A **feature stack** is an *overlay*. It instantiates only the services it must,
reuses the base stack for the rest, and never mutates it.

Vocabulary: *base stack*, *feature stack*, *overlay set*, *reuse set*.

## Layout

Services are **N separate git repos**. The workspace root is a container
directory holding them plus `devstack.workspace.yaml`.

A feature stack is:

- one **git worktree per overlay service**, as a **sibling** of the base root —
  never nested inside it (see §Identity for why nesting is broken)
- a **synthesised stack root**: a new directory containing a *generated*
  `devstack.workspace.yaml` whose `repoDiscovery.repos` point at those worktrees

    ~/dev/navexa/                  base root  (name: navexa)
    ~/dev/.stacks/import-review/   stack root (name: navexa--import-review)
      devstack.workspace.yaml      generated, not committed
    ~/dev/.stacks/import-review-wt/
      frontend/                    worktree
      backend/                     worktree

The generated manifest is what makes the stack's identity ours to control. It is
not a copy of the base manifest; it is derived, and it deliberately **omits**
`runtime.infra` (see §Infrastructure).

## The reuse rule

**Scope: this rule governs synchronous, address-based calls only.** It is not a
general theory of service coupling. See §Outside the rule.

Reuse flows downstream, never upstream.

- A service your code **calls** is an address. Point at it. Reuse it.
- A service that **calls you** cannot be reused: it points at one backend, and
  the shared instance points at base's. Reusing it makes your change invisible.

So:

    overlay = changed ∪ transitive_dependents_via_calls(changed)

Every service in the overlay set gets a **worktree**: changed services on the
feature branch, dependents on the base branch. Dependents are unmodified code —
they exist only to be re-wired — but they need a private directory to hold
per-stack config, and to avoid two Tilt daemons running builds concurrently in
one tree over shared `obj/`, `.next/`, `node_modules/.vite`.

Worked example. Feature touches `backend`. `frontend → backend → db(remote)`.

| Service  | Role               | Action                                       |
|----------|--------------------|----------------------------------------------|
| db       | downstream dep     | reuse base's tunnel                          |
| backend  | changed            | worktree on feature branch, dynamic port     |
| frontend | upstream dependent | worktree on **base branch**, wired to overlay backend |

### `dependencies:` is not a call graph

`WorkspaceManifest.Dependencies` feeds Tilt's `resource_deps`
(`tiltgen.go:110-114`). Tilt semantics are **"start after"**, not **"calls"**.
The playground declares `api → worker`, which is sequencing. Reverse-closing over
this field pulls in every service that merely starts after you; from a shared
`auth` or `api-gateway` the closure is the entire workspace, and the
"don't clone the world" premise collapses.

**Required: a second edge type.** `calls:` (address dependency, drives the reuse
closure and injection) distinct from `startsAfter:` (ordering, drives
`resource_deps`). The reuse rule is unimplementable until this exists.
`BuildTopology` (`topology.go:40-112`) already builds forward and reverse edges
with cycle detection — it gains a second edge set, not a rewrite. No transitive
closure helper exists yet (`Dependents` at `topology.go:92` is direct-only); the
closure must be visited-set guarded, since `tiltgen` never calls `BuildTopology`
and cyclic graphs generate today.

### Outside the rule

These are **not** covered, and no address rewiring can cover them. State them;
do not pretend the equation handles them.

- **Queues.** Consumers bind to a topic, not an address. `backend` publishes,
  `worker` consumes; there is no edge between them. The base's worker eats your
  overlay's messages with old code, nondeterministically (competing consumers).
- **Shared caches.** Redis is a downstream dep → reused. Overlay writes a new
  serialization; **base reads it and breaks.** Downstream reuse propagating
  *upstream* into the baseline.
- **Service discovery.** An overlay service self-registering under its own name
  makes base's clients round-robin onto it.
- **Webhooks.** A fixed registered URL lands on base.

A service in any of these relationships must be named explicitly or left alone.

### Shared downstream state

Two stacks on one remote DB see each other's data. Accepted, not solved — a
direct consequence of not cloning the DB. Migrations from a feature branch hit
the shared remote. This is the sharpest edge in the design and users must know it.

## Resolution

Addresses resolve **overlay-first, base-fallback**:

1. Does this stack instantiate the named service? Use its allocated address.
2. Otherwise use the base stack's address.

**The base stack must be running.** Downstream deps come from it by
construction. If it is down, a stack comes up green and broken — health checks
only probe a service's own port (`tiltgen.go:118-120`, `:162-191`). "I only run
feature stacks" is impossible under this rule.

**Required: base identity.** `workspace.Workspace` (`registry.go:42-65`) has no
`base` flag and no stack→base pointer. Overlay-first resolution has no way to
name its fallback target. Registry schema change, and it is a precondition.

## Ports

**Local ports: fully dynamic, allocated per stack-up.** Nothing hardcoded.

**Remote/tunnel ports: pinned.** This is required, not cosmetic. `Launch`
forwards 1:1 (`tunnel.go:200`, `fwd := fmt.Sprintf("%d:localhost:%d", port, port)`),
so a floating local port drags the remote port with it and every webhook, OAuth
redirect, and out-of-band registration breaks on each `up`. Tunnels must remap:
local floats, remote pins. `strayForwards` (`tunnel.go:284`) hardcodes the same
1:1 shape and must move with it.

Consequence of dynamic local ports: **links change on every `up`**. Bookmarks and
CORS allowlists churn. This is accepted for local service ports; the remote side
is the stable entry point.

### The port model does not exist

- `ServiceManifest.Ports` (`manifests.go:130`) is written by `devstack init`
  (`init.go:282-285`) and `workspace scaffold-service` (`scaffold.go:79-80`) as
  hand-built YAML text, and **read by nobody**. `grep '\.Ports\b'` → zero hits.
- `devstack init` writes the port literal into **three** places
  (`init.go:265-303`): `healthcheck.port` (:277), `ports.http` (:284), and the
  `links:` URL (:299). The run command is **not** one of them — it is passed
  verbatim from `--cmd` (`init.go:161`, `:210`), so the port inside it is the
  user's literal that devstack never sees. Of the three, `healthcheck.port` is
  consumed (`tiltgen.go:179-187`) and `links:` is consumed (`tiltgen.go:121-131`);
  **`ports.http` is the dead one**.
- `links:` is the *de facto* port model — the sole `EndpointLinks` source, hence
  how `status`, `services`, and `tunnel.Discover` (`tunnel.go:92-98`) learn ports.
- The only allocator, `NextPort` (`registry.go:484-502`), is max+1 over registry
  entries: no free probe, no reclaim, races (`Register` calls `Load`, `NextPort`
  calls `Load` again, then `Save`, unlocked), Tilt port only, and only fires when
  `TiltPort == 0`.

### `ports:` needs two concepts

After this lands, what does a literal `3000` in `ports:` mean — a **pin request**
or **stale output**? It must mean pin, because a service with `3000` baked into
`vite.config.ts` cannot be moved. So:

- `ports:` — *requested*. A literal is a pin, honoured. Absent means "allocate".
- allocated ports — runtime state, persisted per stack, never written back to the
  manifest.

Two stacks both pinning one service fail at **create** with a clear message, not
silently rehomed.

### Hard constraint

devstack can only place a service on a dynamic port **if the service reads its
port from the environment**. A hardcoded `vite.config.ts` cannot be moved.
Rolling this out touches every service manifest and may touch the services.

### Allocation race

Probe-then-bind is a *worse* race than max+1: the probe runs at generate time,
the bind after prep/build, seconds to minutes later. Two concurrent `stack
create`s both see a port free and both write it. Allocation needs a lock and an
ownership record, not just a probe. (`session.go:107-114` and `tunnel.go:106-113`
already dial `127.0.0.1:<port>` — the probe exists twice; the *ownership* does
not exist at all.)

## Config injection

Injection is load-bearing. Dynamic ports are unguessable, so every address a
service needs must be injected.

### The precedence ladder

**Principle: the more devstack knows about a value, the more it wins.** `.envrc`
is the developer's local machine. Manifest config is intent. Stack-resolved
addresses are facts about what is actually running. Facts beat intent beats local.

Lowest to highest:

1. `.envrc` — developer local
2. workspace `env.files`
3. service `env.files`
4. workspace `env.values`
5. service `env.values`
6. **stack-resolved addresses** — devstack's live facts

Everything merges in Go and lands in `serve_env`. One mechanism, one ordering,
no runtime shell games.

### Precedence is inverted today

`mergedEnv(opts.ManagedEnv, ws.Env.Values, m.Env.Values)` (`tiltgen.go:96`,
`:152-160`) is last-wins, so `ManagedEnv` is the **lowest** layer. `devstack init`
writes literal `env.values` into every service manifest (`init.go:286-296`), so a
manifest carrying `BACKEND_URL: http://localhost:8080` beats an injected overlay
address — before `.envrc` is even reached. And `env.files` are sourced *after*
Tilt applies `serve_env`, so files currently beat values too.

Revision 1 called `ManagedEnv` "the right shape, wrongly scoped"; it is also
wrongly **ordered**. Rungs 4-6 are exactly inverted.

### `ManagedEnv` is workspace-scoped

It carries two pairs (`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`)
when observability is on, and none when off (`generate_cmd.go:59-67`). One map
for all services cannot express "backend gets frontend's address" — `Generate`
passes the same `opts` to every `renderService` (`tiltgen.go:59`). Must become
per-service.

### Reference syntax

No templating exists anywhere: no `text/template`, no `os.Expand`, no
interpolation. Every manifest value is a literal copied verbatim. Proposed
`${backend.url}` / `${backend.port}`, resolved overlay-first at generate time,
hung off `BuildTopology` — which `tiltgen` currently bypasses (`tiltgen.go:110`
reads `ws.Dependencies` directly), so an unknown dep silently emits a broken
`resource_deps`.

### Blocker: `.envrc` defeats injection

`serveCmd` (`tiltgen.go:139-150`) prepends to every run command:

    [ -f './.envrc' ] && set -a && . './.envrc' && set +a; <command>

`.envrc` is always first in the list, so the `len(files)==0` early return
(`:141`) is dead. `ws.Env.Files` and `m.Env.Files` get the same treatment.

**Proven, not assumed:** the sourcing runs *inside* the process Tilt started, so
`set -a` re-exports over `serve_env`. Verified —
`FOO=from_serve_env sh -c "… . './.envrc' …; echo $FOO"` yields `from_envrc`.
The only escape is a `${FOO:-default}` form, and `service_env set` writes the
unconditional `export K=V` (`tools_service_env.go:405`), so in practice `.envrc`
wins.

Making `.envrc` the *floor* is impossible while it is shell-sourced at runtime:
`set -a` re-exports over everything by construction. The sourcing must go.

### Reading `.envrc`: evaluate and capture

Resolve `.envrc` in Go at generate time:

    sh -c 'set -a; . ./.envrc; set +a; env -0'

Parse the output into a map; merge as rung 1. The file is **executed**, so
conditionals and `${VAR:-default}` resolve exactly as they do in the developer's
shell — then the *result* is captured rather than allowed to clobber the process
at service start. Remove the sourcing from `serveCmd` entirely.

Line-parsing was considered and rejected. Surveying the real files: 10 of 12 are
plain `export K=V` and would parse fine, but `navexa_mcp/.envrc` branches on
`if [ "$ENV" = "dev" ]` and `nxFileProcessor/.envrc` guards a **production SQL
connection string** behind `if [[ "$NAVEXA_ENV" == "prod" ]]` with the dev value
in the other branch. A line-wise parser reads **both** branches and takes
last-wins — landing on dev only by accident of file order. One reorder and a dev
service gets prod credentials. It also cannot expand `${NAVEXA_ENV:-dev}`.

This is not new trust: devstack already executes these files at every service
start. It moves execution earlier and captures the result.

No direnv dependency — the survey found no direnv stdlib usage (`use flake`,
`layout python`) in any real file, so `sh` suffices. If that changes,
`direnv export json` is the drop-in upgrade.

**Fallout to handle:** `service_env set` (`tools_service_env.go:375-435`) becomes
a lie for any key devstack manages — it writes `.envrc` (now the floor), reports
success, and the value loses. It must write to the manifest instead
(`manifest_edit.go` is the seam). `init.go:413` documents `.envrc` as *the*
per-developer OTEL override and needs rewriting. Secrets are unaffected: devstack
sets no opinion on those keys, so the conflict only exists where it does.

`parseEnvrc` (`tools_service_env.go:84-119`) reads the file line-wise as
KEY=VALUE, so the drift audit inspects a different environment than the service
gets — same bug, same fix, and it should share the new resolver.

## Owning configuration

devstack has **no model of correct configuration**. Not a weak one — none.

- `ServiceEnv.Required` (`manifests.go:191-196`) is declared and **read by
  nobody**. No service can state what it needs.
- `pidForService` (`tools_service_env.go:142-157`) returns 0 on **every path**,
  including success — Tilt does not expose a PID. The live `/proc/<pid>/environ`
  inspection above it is dead code. devstack has never looked at what a running
  service actually has; it audits files and infers.
- "Correct" is two hardcoded heuristics, both matching on key *names*: OTEL
  endpoint keys must equal the workspace's ports (`:562-563`), and seven
  `_DATABASE_URL`-ish suffixes must **agree across services** (`:438-440`,
  `:649-664`). The second is **consensus, not correctness** — if every service is
  wrong identically, it reports healthy.
- Nothing declares what a service *provides* or *calls*. `ports:` is dead,
  `links:` are hand-written URLs, `dependencies:` is start-order.

`service_env check` exists **because** there is no resolution. Drift detection is
a symptom of hand-maintained config — devstack noticing two services disagree
while being structurally unable to make them agree. Resolution does not extend
the checker; it **deletes** it.

### Computed vs required

Config splits in two, and the boundary is firm.

**Computed** — devstack owns these, injects them at the top rung, and they are
correct by construction. Nothing to check:

- addresses of services devstack runs (allocated port)
- the OTEL endpoint
- tunnel-backed remote addresses (devstack manages the tunnel)
- anything derivable from the graph plus allocation

**Required** — devstack **demands these and never invents them**:

- secrets: API keys, tokens, DB passwords
- external URLs: Auth0, OpenRouter
- app config: feature flags, model names

**Revive `env.required`.** A required key absent from every rung of the ladder
fails at **generate**, naming the key and which rung to put it on — not at
runtime with a 500. devstack never fabricates a secret.

### Where inference belongs

"Match services" splits into a deterministic half and a fuzzy half. Keep them
apart.

- **devstack resolves explicit references.** `env.values: { NAVEXA_API_URL:
  "${api.url}" }` — the reference is declared, devstack resolves it from the
  graph. It never guesses.
- **The agent infers the reference.** It notices `NAVEXA_API_URL` means the `api`
  service, writes `${api.url}` into the manifest (`manifest_edit.go` is the write
  seam), and from then on it is explicit config resolved deterministically.

The fuzziness happens **once**, under review, and is **crystallised into git** —
not re-guessed every run. Baking name-matching heuristics into devstack itself
(the `dbURLPatterns` approach) is the anti-pattern: zero-config when it guesses
right, silently wrong when it doesn't.

This is also what makes the agent's write path real. `service_env set` re-homing
from `.envrc` to the manifest is not just a fix for the ladder — the manifest
**is** the agent's medium.

### To delete

- the `MISMATCH` consensus detector (`:649-664`) and `dbURLPatterns` (`:438-440`)
- the OTEL endpoint drift audit (`:562-563`) — computed, so it cannot drift
- `pidForService` and the dead live-env inspection (`:122-157`)

## Links

Links are the primary output — dynamic ports make them the only way to find a
stack. `stack up` prints the URL set.

`links:` stops being hand-written and becomes **derived** from the allocation.

## Infrastructure

**Feature stacks reuse the base's infra. They never start or stop it.**

`ResolveComposeSpec` derives the project name from `manifest.Workspace.Name`
(`infra/compose.go:33`). Because the stack root has a *generated* manifest with a
*distinct* name, it would get a **distinct compose project** and boot its own
Postgres/Redis — the opposite of reuse.

**The generated manifest must omit `runtime.infra` entirely.** Then
`infra.Down` / `infra.Up` are no-ops for the stack (`down_cmd.go:125-133`,
`start_cmd.go:140-148`), and base's infra is untouched in both directions.

Revision 1 missed this: with a *shared* name, `stack down` would have run
`docker compose -p devstack-<name>-infra down` and **destroyed the base's
Postgres**.

## Observability

**Shared. One collector, one SigNoz, stacks tagged by name.** Not a preference —
per-stack is impossible as built:

- `signoz.go:73-75` hardcodes `0.0.0.0:13133` (health) and `0.0.0.0:1777` (pprof)
  in the collector config. The collector is a **host process**
  (`collector.go:90`), not containerised, so a second one always fails to bind —
  even with OTLP overrides.
- `signozProjectName` keys on the registry name (`signoz.go:178`), so a stack
  gets a *different* project → `isOtelRunning` false → `runStart`
  (`start_cmd.go:154-196`) boots a **second ClickHouse + Zookeeper + migrators**,
  then fails to bind `SIGNOZ_UI_PORT` 3301 (`signoz.go:286`, `registry.go:292`)
  *after* the RAM is spent.
- The second `otelcol-contrib` binds-conflicts and exits, but `StartCollector`
  returns nil because `cmd.Start()` succeeded (`collector.go:90-104`), so
  `start_cmd.go:190` prints `✓ OTEL http://localhost:3301` — **the base's**.

**Required: a stack must skip its own collector and SigNoz and attach to base's.**
No such mechanism exists. Without it, stack #2 is a silent RAM bomb.

## Identity and state

Runtime state is keyed by **name**, not path:

- `DataDir`/`PIDFile`/`LogFile` (`registry.go:120-141`)
- collector config dir (`collector.go:25`), SigNoz project (`signoz.go:178`)
- compose project — from the **manifest** name (`infra/compose.go:33`)

`Register` dedupes by *path* (`registry.go:212-218`), which is why revision 1
wrongly concluded no re-keying was needed. Path only decides registry rows.

Because a stack root has a **generated** manifest, its name is ours: derive
`<base>--<stack>` and it is unique by construction. But uniqueness must also be
**enforced** — `FindByName` is first-match case-insensitive
(`registry.go:230-244`) and nothing anywhere enforces it. The same
first-match-lowercase pattern is duplicated across `UpdateOtelPlugin`,
`UpdateOtelPorts`, `UpdateTunnelRemote`, `UpdatePort`, `AddEnvironment`,
`RemoveEnvironment`.

**Without a unique name, `stack up` is a silent no-op:** `start_cmd.go:48` reads
`PIDFile(ws.Name)` → base's PID → alive → prints `Dev daemon already running` →
`return nil`. Exit 0, nothing started, base's port reported as the stack's.

### Two detectors that disagree

- `config.FindWorkspaceRoot` (`manifests.go:538-549`) — walk up from cwd, nearest
  wins, registry-free. **Longest-match by construction.**
- `workspace.DetectFromCwd` (`registry.go:262-290`) — first-match over registry
  *file order*.

They disagree for nested worktrees. Verified against the real binary: a stack
nested under the base root resolves to **base**, deterministically (base is
registered first) — so `down`, `status`, and MCP all silently target base. A
**sibling** worktree is safe: the `+"/"` in the prefix check stops
`base-import-review` matching `base`.

**Therefore: worktrees are siblings, mandated.** And `DetectFromCwd` becomes
longest-match, or delegates to `FindWorkspaceRoot`.

### `.mcp.json`

`init.go:355-357` bakes `DEVSTACK_WORKSPACE` (path) and `DEVSTACK_DAEMON_PORT`
into a **committed** file. A worktree inherits base's values, so
`serve.go:157` (`FindByPath`) points an agent in the stack at **base**.

Regenerating it per stack dirties a tracked file — `git status` noise, and
`git add -A` commits your machine's port onto the feature branch. **Rejected.**

**Stop baking env instead.** `DEVSTACK_WORKSPACE` is only an *override*:
`resolveWorkspaceRoot` (`resolved.go:204-231`) already falls back to
`FindWorkspaceRoot`, the cwd walk-up. `DEVSTACK_DAEMON_PORT` is derivable from
the resolved path via `FindByPath` (`registry.go:247`). Then `.mcp.json` is
byte-identical in every checkout, stays committed, and never regenerates.

### Session state

`SessionState` (`session.go:13-24`) is a post-hoc leak detector, not an instance
handle: one `session.json` per name, overwritten (`:38`); `SessionID` minted
(`:55`) and never read; `ActivePorts` holds only the Tilt port
(`start_cmd.go:117`). Shape is a reasonable start; storage model is not.

## Generation must filter to the overlay set

`tiltgen.Generate` renders **every** service in `rw.Services`
(`tiltgen.go:45-64`), and `resolveManifestServices` returns everything in
`repoDiscovery` (`manifests.go:378-442`). "Instantiates only the services it
must" has **no mechanism**. Either the generated manifest lists only overlay
services in `repoDiscovery.repos`, or `Generate` gains a filter. The former is
simpler and falls out of the synthesised root.

## Lifecycle and reconciliation

Current teardown is not stack-safe:

- There is no top-level `devstack down` — it is `devstack workspace down`
  (`down_cmd.go:28`).
- `runDownAll` (`down_cmd.go:142-185`) iterates **every** registered workspace,
  which now includes base. It is also a *degraded* path vs `runDown`: it skips
  graceful `tilt disable` (`:70-80`) and goes straight to `proc.Kill()` —
  **SIGKILL**, orphaning every `local_resource` child, which survive **holding
  their ports**. It skips `DetectResidue`/`CloseSession` (`:100-108`), so
  `session.json` stays `"open"` forever, and skips `infra.Down` (`:125-133`).
- `workspace remove` (`workspace_cmd.go:229-258`) drops the registry row and
  **cleans nothing** — no DataDir, no SigNoz down, no PID kill.

Nothing ever stats a registered path (`registry.go:145-260`), so a hand-deleted
worktree leaves: a registry row with a dead path, a **still-running Tilt** serving
from deleted inodes and holding every port, `DataDir` debris,
`~/.config/devstack/collector/<stack>/`, and `devstack-signoz-<stack>` containers
plus named volumes.

Stale `tunnels/*.pid` are read by `trackedForwards` (`tunnel.go:248-277`), so a
**recycled PID reads as an owned forward** and `strayForwards` refuses to reap it.
(Introduced by `bf17727`; ownership records need liveness checks.)

**Required: reconciliation.** `workspace doctor` should own it — dead paths,
orphaned SigNoz projects, PID files for processes that aren't ours, prunable
worktrees.

## Resource budget

Each stack = a Tilt daemon + N service processes, with frontend dev servers the
dominant cost. Nothing caps, warns, or accounts. Note `--host 0.0.0.0`
(`start_cmd.go:98`) means every stack exposes an unauthenticated Tilt API on all
interfaces, multiplied by N.

## Interface

Primary entry point is conversational, via MCP. The user says "need a new stack
for file import review in the frontend and backend"; the agent resolves repos,
calls the tool, reports links.

    stack_create(name, repos[], branch?)  → worktrees, overlay set, configs, up, links
    stack_list()                          → stacks, status, links
    stack_down(name)                      → stop; leave worktrees
    stack_rm(name)                        → stop, remove worktrees, release ports, clean state

CLI wraps the same. Note `status --all` (`status_cmd.go:134-232`) is the closest
existing thing to `stack list`.

### `stack create` semantics

Underspecified in revision 1; all of this is required:

- **Worktrees give you HEAD, not your working tree.** Verified: `git worktree add`
  succeeds on a dirty base and the worktree contains **committed state only**.
  The first-week bite is a stack that doesn't contain the change you're working
  on. Must refuse on a dirty base, or offer to carry the work explicitly.
- **Branch exists** → `fatal: a branch named 'x' already exists`. Needs
  create-vs-attach semantics.
- **Branch checked out in base** → `fatal: 'main' is already used by worktree at …`.
- Every raw git failure needs a devstack-level message.

## Implementation order

Each step gates the next. Steps 0-2 are preconditions the feature cannot exist
without; revision 1 had them as step 5.

0. **Identity.** Unique derived stack name, enforced. Base pointer in the registry
   schema. `DetectFromCwd` longest-match (or delegate to `FindWorkspaceRoot`).
   Sibling worktrees mandated.
   → verify: two stacks + base, each `up`/`down`/`status` hits only itself
1. ~~Defuse tunnel cross-stack killers~~ (`bf17727`)
2. **Injection wins.** Implement the precedence ladder: resolve `.envrc` by
   evaluate-and-capture and drop the sourcing from `serveCmd`; order the six
   rungs in `mergedEnv`; per-service `ManagedEnv`; re-home `service_env set` to
   the manifest.
   → verify: an injected value survives a conflicting `.envrc` line **and** a
     conflicting `env.values` manifest literal; a conditional `.envrc` resolves
     to the same branch the developer's shell picks
3. **Reuse without duplication.** Generated manifest omits `runtime.infra`; stack
   attaches to base's collector/SigNoz instead of booting its own.
   → verify: `stack up`/`down` leaves base's compose project and collector
     untouched; no second ClickHouse
4. **Port model.** `ports:` live, pin-vs-allocate split, locked allocation with
   ownership, links derived. Tunnel local↔remote remapping.
   → verify: two stacks bind different local ports, neither hardcoded; remote
     tunnel port stays pinned across `up`
5. **Call graph.** `calls:` vs `startsAfter:`; transitive closure, visited-guarded.
   → verify: overlay set from a shared `auth` dep doesn't swallow the workspace
6. **Reference syntax + overlay-first resolution.** Revive `env.required`:
   validate against the merged ladder, fail at generate naming the key and rung.
   Delete the drift checker, `dbURLPatterns`, and `pidForService`.
   → verify: overlay backend's address reaches overlay frontend; untouched
     service resolves to base; a missing required secret fails at generate, not
     at runtime
6a. **Agent inference.** `service_env` gains the power to match a required key to
   a provider and write `${svc.url}` into the manifest via `manifest_edit.go`.
   → verify: agent resolves an unwired service end to end and the result is a
     reviewable manifest diff, not a runtime side effect
7. **`stack create`/`rm` + MCP tool.** Worktree lifecycle, dirty-base handling,
   `.mcp.json` de-baking, reconciliation in `workspace doctor`.
   → verify: prose request → two stacks live, independently reachable; `rm`
     leaves no debris

## Open questions

- Does `stack create` refuse on a dirty base, stash, or carry the work?
- What is the resource budget per stack, and does anything enforce it?
- Should `stack rm` refuse when a worktree has uncommitted changes?
- Do queue-coupled services get an explicit `isolate:` escape hatch, or are they
  simply out of scope?
- Does the base stack need a `stack` identity of its own, or stay implicit?
