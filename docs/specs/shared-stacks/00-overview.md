# Small stacks, no base — overview

Status: IMPLEMENTED (pending live cutover). Date: 2026-11-26.

## Problem

- The owner makes too many stacks. They stay up and use the machine's
  memory.
- Base runs every app service, so each stack runs on top of a full copy.
- A stack borrows app services from base. Base must be up and current, and
  a stack is never complete without it.

## Goal

Small, fast, surgical stacks. A stack runs only the services a feature
needs. devstack helps fast iteration and does not add arbitrary limits.

## Direction

| Today | After |
|---|---|
| Base runs every service from the replica. | No base. Databases and stores run once per workspace as the shared set, from latest main. |
| A stack runs its overlay and borrows the rest from base. | A stack runs the services you name, plus their `needs`. Nothing else. |
| Infra is one copy, or a fragile second supervisor in a stack. | Infra is always shared. No per-stack databases. |
| Edges are workspace maps: `dependencies`, `calls`, `startsAfter`. | Each service declares `needs` (critical) and `calls` (soft). |
| Agent stacks are destroyed 24h after create. Quota of 2. | Every stack stops 24h after create or last up. Nothing is destroyed. No quota. |
| No link between a pane and a stack. | A herdr pane shows its stack, its state and its tailnet URL. |

## Glossary (new and changed terms)

- **shared service**: a service in the workspace `shared:` list. It runs
  once per workspace, never inside a stack, from the latest default branch.
  Every database and store is shared.
- **shared set**: all shared services of one workspace.
- **stack service**: any service that is not shared. It runs only inside a
  stack.
- **stack**: the stack services a feature needs, in git worktrees, on their
  own ports. It uses the shared set.
- **needs edge**: "A needs B" means A cannot work without B. A stack that
  runs A also runs B (or B is shared). A starts after B.
- **calls edge**: "A calls B" means A talks to B, but works without it.
  It does not pull B in and does not order start.
- **stop deadline**: the time when devstack stops a stack (`StopAt`).
- **pane binding**: the herdr pane that last controlled a stack.

Removed terms: **base**, **overlay**, **copy from base**. **Replica** stays,
but only for the repositories that hold shared services (see 01).

## Owner decisions (2026-11-26)

- Stop clock: 24h from create. A later `stack up` sets a new deadline of up
  + 24h. (Assumption: without this, a stopped stack could never run again.)
- Nothing is destroyed automatically. Agent stacks only stop.
- No agent quota. Agents create and start what they want.
- Stacks stay small. Only `needs` pulls a service in. `calls` never does.
- A `calls` edge or reference to a service the stack does not run warns,
  and names `stack add`. The agent decides whether to add it.
- Infra: every database and store is shared. No per-stack copies, no
  guidance on making them.
- Shared set: always up while the workspace is up. It runs from the latest
  default branch and refreshes every hour.
- Pane URL: the stack's tailnet URL. It shows even if the service is not
  serving, as a sign that the stack is up.
- Service edges: modelled on nxAlmanac's `depends_on`. No integration with
  nxAlmanac.

## Spec set

- `01-shared-services.md` — base goes; the shared set follows main.
- `02-service-edges.md` — `needs` and `calls` on a service definition.
- `03-stack-lifecycle.md` — the 24h stop, the sweep, no quota.
- `04-herdr-pane.md` — pane binding and the herdr plugin.
- `05-branch-sync.md` — how resync and attach change.
- `06-agent-surface.md` — every text that must change.

## Open questions

None block the plan. Smaller questions sit at the end of each spec.

## Plan

One branch, steps in order. Each step is `step → verify`.

**1. Edge model (02).** Add `needs`/`calls` to the service schema. Read the
old workspace maps as aliases. Only `needs` feeds `resource_deps`.
→ verify: unit tests for closure, cycles, unknown names; a test that a
`calls` edge adds no `resource_deps`.

**2. Shared set and stack membership (01, 02).** One step, because
stripping base before stacks are complete leaves refs to ports nobody
serves. Add `shared:`. Render shared services as `<ws>:<svc>`, and deps on
them with no stack suffix. Render stack services only inside stacks.
Create = named services + groups + `needs` closure − shared. Allow a
create with no branch. Rename `Overlay` to `Members`. Remove the base
fallback in the port book. Warn on refs to non-members. Scope the replica.
→ verify: new `GenerateHost` tests for shared deps (tiltgen has no golden
tests today); tests with real git repos; live on `devstack_test`:
`workspace up` runs only the shared set, and a stack serves with no base.

**3. Addressing (01, 06).** Remove `--stack base` and the `base` tool.
Add `scope: workspace` to env tools, and `--workspace-default` to `env use`. New guard rule for shared reads.
Trace and fix `investigate`, `tunnel`, `urls` and the panel defaults.
→ verify: tests for each mutating tool; guard tests for agents.

**4. Lifecycle (03).** `StopAt`, `StoppedBy`, host-wide `stack sweep`, the
systemd timer and its fallback, hourly shared sync from the same timer (the timer runs `devstack tick`), `stack.down` on `workspace down`. Remove
destroy on expiry and the agent quota.
→ verify: fake-clock tests; live with `DEVSTACK_STOP_AFTER=2m`, then run
`stack sweep` by hand (the timer runs every 10 min); push a commit to a
shared service's repo, run `shared sync`, and see only that service
restart.

**5. Branch sync (05).** Resync moves detached worktrees. Resync and up
report repos and needs the stack does not run, and a stack behind origin.
→ verify: tests with real git repos and a bare origin.

**6. herdr (04).** Pane binding, `devstack herdr publish`, plugin startup
hook and action.
→ verify: live in herdr. The owner signs off the sidebar look.

**7. Migration (01).** `devstack migrate shared-stacks` converts the navexa manifest and
existing stacks. Cut navexa over.
→ verify: live on navexa; every migrated stack serves.

**8. Surfaces (06).** MCP, prime, CLI help, README, REFERENCE, skills,
foundry and navexa agent docs. Then run `/tool-surface-review`.
→ verify: no text names base as a concept; `prime` reads correctly.

Dependencies: 1 → 2 → 3; 4 → 6; 2 → 5; 1–5 → 7; 8 last.
