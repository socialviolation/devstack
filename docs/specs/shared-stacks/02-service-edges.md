# 02 — `needs` and `calls` on a service definition

## Today

- Three workspace-level maps: `dependencies`, `calls`, `startsAfter`
  (`internal/config/manifests.go:37-47`). A service definition cannot
  declare an edge.
- `ResourceDeps()` unions all three. So `calls` also orders start, and
  nothing means "talks to X, does not wait for it".
- The overlay walks only `CalledBy` (`internal/config/topology.go:158-184`).
- Navexa declares only `dependencies` (18 keys, 26 edges). No `calls`, no
  `startsAfter`, no edge in any `devstack.service.yaml`.

## Borrowed from nxAlmanac

nxAlmanac (`~/dev/navexa/nxAlmanac/model/SCHEMA.md`) puts edges on the
caller only, as `depends_on:` entries with `id`, `via`, optional
`entities`. Its `via` values: `http sql redis mcp queue blob webhook otlp
oidc`. It has no criticality field.

devstack takes the caller-side list and the `via` vocabulary. It adds what
nxAlmanac lacks: the split between critical and soft.

## Change

A service definition (`devstack.service.yaml`, or an inline `services:`
entry in the workspace manifest) accepts two lists:

```yaml
needs:                 # critical: cannot work without it
  - navexa-sql
  - service: navexa-api
    via: http
calls:                 # soft: talks to it, works without it
  - service: nxTradeImporter
    via: queue
    entities: [stage-trades-queue]
```

- An entry is a service name, or a map with `service`, optional `via`,
  optional `entities`.
- `via` uses nxAlmanac's values. devstack only displays `via` and
  `entities` (in `wiring` and `topology`). It does not act on them.

### Meaning

| Edge | Pulls into stack | Orders start | Shown in wiring |
|---|---|---|---|
| `needs` | yes, transitively | yes | yes |
| `calls` | no | no | yes |

- `resource_deps` come from `needs` only.
- A `needs` edge to a shared service orders start, and pulls nothing in.
- A `needs` cycle fails load and names the cycle.
- An edge to an undeclared service fails load.

### Building a stack's service list

```
named   = --repos services + members of named groups
members = named ∪ needs-closure(named) − shared
```

- `stack create` prints `members` and, for each service it added, the
  `needs` chain that pulled it in.
- Callers are not pulled in. Today `OverlaySet` adds transitive callers
  (`internal/config/stacks.go:15`), and base serves the rest. Now a stack
  that names only `navexa-api` gets no frontend. Name the frontend too if
  the feature needs it.
- If a member `calls` or references a service that is not a member and not
  shared, create and up warn and name `stack add`. They never refuse. The
  agent decides whether the change needs that service.
- `stack add` recomputes the closure. `stack remove` refuses to remove a
  service another member needs, and names that member.
- Groups keep their job: a named set for `--repos` and `group start|stop`.
  `Record.Groups` and coverage reporting stay. "Serves from base" becomes
  "not in this stack".

### Old fields

- `calls:` loads as `calls`.
- `dependencies:` and `startsAfter:` load as `calls`, not `needs`. Today's
  edges are start order, not a claim that a service cannot work without
  the other. Loading them as `needs` would pull large chains into every
  stack. The owner promotes the few real ones to `needs`.
- Each old field prints a deprecation warning once per load.
- `devstack migrate` moves workspace edges onto each service. A service
  with a `devstack.service.yaml` gets them there. A service declared inline
  gets them inline.

## Navexa after migration

Every edge from today's `dependencies:` becomes `calls`. The owner then
promotes a few to `needs`. Likely candidates (owner decides):

```
navexa-api          needs navexa-sql      (shared: orders start only)
nxContractNoteParser needs azurite        (shared: orders start only)
agent-rag           needs agent-psql      (shared: orders start only)
```

`navexa-frontend → navexa-api` is a judgement call. As `needs`, every
frontend stack runs the API. As `calls`, the agent adds the API when the
feature needs it.

nxAlmanac shows edges devstack lacks, for example navexa-api to azurite
and redis. Adding them is manifest work, not code.

## Open questions

- Should `nxFileProcessor → nxTradeImporter` stay `calls` (it is a Service
  Bus edge in nxAlmanac)? Owner's call per edge.
