# Devstack Rebuild Spec

## What counts as infra

In this spec, **infra** means the supporting local systems around application code, not the app services themselves.

Examples:
- databases
- caches
- queues/brokers
- object storage emulators
- local cloud emulators
- OTEL collector / SigNoz / Jaeger / ClickHouse / Grafana style tooling
- mailcatchers
- auth/idp mocks
- search engines
- any shared backing service that local repos talk to

For devstack specifically, this includes at minimum:
- the observability stack: SigNoz / ClickHouse / OTEL collector
- any shared local services used by multiple repos

The recommended model is:
- **infra in Docker Compose**
- **code services run locally**
- **devstack orchestrates local code services and AI-facing introspection**

This preserves a fast edit/run/debug loop while making infra teardown much more reliable.

---

## 1. Goals

### Primary goal
Make devstack the reliable bridge between:
- AI operating a local multi-repo system
- humans being able to see, trust, and control what is happening

### Secondary goals
- simplify config ownership
- make startup/shutdown predictable
- improve AI operational behavior
- reduce false conclusions from missing telemetry
- make service/group/dependency relationships discoverable instead of implicit

---

## 2. Non-goals

- rebuilding into full Kubernetes locally
- requiring every code service to run in containers
- making OTEL the sole source of truth
- hiding all underlying tools; humans should still be able to inspect the real system

---

## 3. Current problems to solve

### Problem A: config ownership is confused
Today config is split across:
- global registry
- workspace config
- repo-generated `.mcp.json`
- repo-generated `AGENTS.md`
- ad hoc `.envrc`

This creates overlapping ownership and unclear precedence.

### Problem B: process lifecycle is weak
Today `workspace down` is not a trustworthy “everything is gone” operation.

### Problem C: AI instructions are static and misleading
Generated instructions drift from actual tools and do not teach the right diagnostic model.

### Problem D: telemetry absence is treated as proof
The system encourages agents to infer “did not happen” from “not observed.”

---

## 4. New architecture

### 4.1 Ownership model

### Workspace owns composition
The workspace defines:
- which repos belong to it
- groups
- cross-service dependencies
- infra stack
- environment definitions
- shared runtime defaults

### Repo owns service self-description
Each repo defines:
- service identity
- run command(s)
- healthcheck
- ports
- env sources
- telemetry expectations
- optional local-only metadata

### Runtime owns active state
Runtime state tracks:
- active session
- process tree / process groups
- live ports
- local infra status
- service state snapshots
- observed telemetry health

That gives a clean split:
- **config**
- **runtime**
- **generated convenience files**

---

### 4.2 Files and schemas

### Workspace manifest
Path:
- `<workspace>/devstack.workspace.yaml`

Example:

```yaml
version: 1

workspace:
  name: navexa
  repoDiscovery:
    mode: explicit # explicit | scan
    repos:
      - ../api
      - ../worker
      - ../web

runtime:
  orchestrator: local-supervisor # local-supervisor | tilt
  infra:
    provider: compose
    composeFiles:
      - .devstack/infra/docker-compose.yml

observability:
  backend: signoz
  local:
    enabled: true
  defaults:
    requireTraces: false
    requireLogs: false

groups:
  backend:
    - api
    - worker
  frontend:
    - web

dependencies:
  api:
    - worker
  web:
    - api

environments:
  local:
    type: local
    observability:
      backend: signoz
      url: http://localhost:3301
      otlpEndpoint: http://localhost:4318
  staging:
    type: remote
    observability:
      backend: signoz
      url: https://signoz.staging.example.com
```

### Repo manifest
Path:
- `<repo>/devstack.service.yaml`

Example:

```yaml
version: 1

service:
  name: api
  aliases: [app-api]

runtime:
  workDir: .
  run:
    command: go run .
  restart:
    strategy: process
  healthcheck:
    type: http
    url: http://localhost:8080/health

ports:
  http: 8080

env:
  files:
    - .envrc
  required:
    - DATABASE_URL
    - OTEL_EXPORTER_OTLP_ENDPOINT

telemetry:
  traces:
    expected: true
  logs:
    expected: true
  serviceName: api

dev:
  language: go
```

---

## 5. Configuration precedence

This must be explicit and enforced.

### Resolution order
1. CLI flags
2. environment variables for current invocation
3. runtime session overrides
4. workspace manifest
5. repo manifest
6. defaults

### Ownership rules
To avoid ambiguity:
- workspace may override only **composition/runtime concerns**
- repo is canonical for **service-local concerns**
- generated files are never canonical
- runtime state is never config input except as an explicit override layer

### Examples

#### Owned by repo
- run command
- working directory
- healthcheck
- service-local ports
- telemetry expectations
- env file paths

#### Owned by workspace
- group membership
- cross-service deps
- infra provider
- environment catalog
- observability backend defaults

#### Overrideable at invocation time
- selected workspace
- selected service
- selected environment
- ad hoc port/feature overrides

---

## 6. Discovery model

### 6.1 Workspace-first, repo-aware
The workspace should be declared first.

Proposed flow:
1. user registers or opens a workspace
2. workspace either:
   - explicitly lists repos, or
   - scans configured directories for `devstack.service.yaml`
3. each repo self-describes
4. workspace composes them into the runtime graph

### 6.2 Registration modes
Support both:

#### Explicit mode
Workspace lists repo paths directly.

Best for:
- deterministic teams
- monorepo-adjacent multi-repo setups

#### Scan mode
Workspace scans configured roots for repo manifests.

Best for:
- looser local setups
- onboarding convenience

---

## 7. Runtime architecture

### 7.1 Recommended runtime split

### Infra: Docker Compose
Managed by compose:
- SigNoz / ClickHouse / OTEL collector companions
- DBs
- queues
- caches
- emulators
- support services

### App services: local supervisor
Managed by devstack:
- code services started from repo manifests
- dependency-ordered startup
- restart by service or group
- process logs
- env inspection
- healthcheck polling

This gives:
- reliable infra teardown
- fast local code iteration
- no need to dockerize every app

---

### 7.2 Session model
Each workspace has a runtime session record:
- session ID
- workspace path/name
- supervisor PID
- process groups
- child PIDs
- started services
- compose project name
- active ports
- collector status
- timestamps

Suggested state dir:
- `~/.local/state/devstack/<workspace>/session.json`

### 7.3 Shutdown contract
`devstack workspace down` must mean:
1. stop managed services gracefully
2. kill remaining local child processes
3. stop compose infra
4. verify expected ports are free
5. verify tracked processes are gone
6. mark session closed

If any part fails, the command exits non-zero with exact residue reported.

That is the “compose down” confidence target.

---

## 8. Service lifecycle behavior

### Start
`devstack start <service>`
- resolve target service
- resolve dependency graph
- ensure infra is available if required
- start dependencies in order
- wait on readiness/health gates
- start target
- report actual running state

### Start group
`devstack start --group backend`
- expand group
- resolve full dependency graph
- start in dependency-safe order
- parallelize where possible

### Restart
`devstack restart <service>`
- restart target only by default
- optional `--with-dependents`
- optional `--group`

### Stop
`devstack stop <service>`
- stop target only
- optional protection if another running service depends on it

---

## 9. Observability model

### 9.1 OTEL is evidence, not truth
All tooling must distinguish:
- observed
- not observed
- unknown

### 9.2 Telemetry expectations
Per service:
- traces expected: yes/no
- logs expected: yes/no
- metrics expected: yes/no
- required for healthy diagnosis: yes/no

Not every service should be judged by the same telemetry standard.

### 9.3 New telemetry health layer
Add:
- `devstack telemetry status`
- MCP tool: `telemetry_health`

This should report, per service:
- collector reachable
- service configured to emit telemetry
- spans seen recently
- logs seen recently
- backend query healthy
- mismatch between config and actual env
- confidence level for telemetry-based diagnosis

Example:
- `api`: traces expected, logs expected, collector reachable, no spans in last 10m, logs present, confidence: partial
- `worker`: traces not expected, process logs only, confidence: process-log-only
- `web`: traces expected, collector unreachable, confidence: low

---

## 10. AI / MCP behavior spec

### 10.1 Core principle
Agents must report **evidence**, not invent conclusions.

### 10.2 Required language rules
Never say:
- “this wasn’t triggered”
- “this endpoint didn’t run”
- “this does not work”

Unless directly verified through stronger evidence than missing telemetry.

Instead say:
- “I found no matching traces in the current window”
- “OTEL did not show evidence of this path”
- “telemetry is inconclusive because collector/log export appears unhealthy”
- “process logs do/don’t show the event”

### 10.3 MCP tool set
Recommended MCP tools:

#### `environment`
What workspace/service/env is active and what actions are available.

#### `topology`
Show:
- services
- groups
- dependencies
- dependents
- source paths

#### `status`
Show live runtime/service health.

#### `telemetry_health`
Show telemetry coverage and confidence.

#### `investigate`
Correlate traces/logs/process logs, with explicit confidence output.

#### `process_logs`
Raw process logs, structured around recent service execution.

#### `restart`
Restart service/group.

#### `stop`
Stop service/group.

#### `config_context`
Explain resolved config and its sources.

#### `service_env`
Keep, but tighten scope and explainability.

### 10.4 Diagnostic ladder for agents
Every agent instruction set should teach this order:
1. `environment`
2. `topology`
3. `status`
4. `telemetry_health`
5. `investigate`
6. `process_logs`
7. restart/stop if needed

This is better than using `investigate` first as a blanket rule.

---

## 11. CLI spec

### Commands to add

#### `devstack topology`
Show services, groups, dependencies, dependents.

#### `devstack explain config`
Explain effective config values and where they came from.

#### `devstack explain service <name>`
Explain how a service resolves:
- repo
- run command
- env files
- healthcheck
- groups
- dependencies
- telemetry expectations

#### `devstack telemetry status`
Show telemetry health and confidence.

#### `devstack workspace doctor`
Check:
- workspace manifest validity
- repo discovery
- missing manifests
- stale runtime state
- dead pids
- occupied ports
- compose health
- telemetry health

### Commands to de-emphasize
`init` should become primarily:
- scaffold manifest
- optionally generate convenience files

It should no longer be the central registration mechanism.

---

## 12. Generated files policy

### `.mcp.json`
Generated convenience only.

It may contain:
- server command
- resolved workspace
- default service

But it is **not** config truth.

### `AGENTS.md`
Generated minimal helper only.
It should not try to fully describe the system.

Better content:
- devstack is available here
- call `environment` first
- use `topology` to discover groups/deps
- use `telemetry_health` before inferring from absence of traces
- use `process_logs` when telemetry is partial

No drift-prone long prose.

---

## 13. Migration strategy

### Phase 1: introduce new manifests alongside old config
- add loader for `devstack.workspace.yaml`
- add loader for `devstack.service.yaml`
- preserve `.devstack.json` compatibility
- add warning when running legacy-only mode

### Phase 2: resolve from new model first
- if new manifests exist, prefer them
- still read legacy registry/config as fallback

### Phase 3: move generated files to convenience mode
- `.mcp.json` generated from resolved config
- `AGENTS.md` minimized

### Phase 4: introduce new runtime session model
- supervisor state
- cleanup verification
- compose integration

### Phase 5: tighten MCP semantics
- evidence language
- telemetry health
- topology tool
- config explain

### Phase 6: deprecate legacy workspace config
- `.devstack.json` remains importable but not preferred

---

## 14. Recommended implementation beads

### Bead 1: Manifest schemas and config resolution
**Goal**  
Introduce explicit workspace and service manifests with deterministic precedence.

**Project context**  
Current config logic is spread across `internal/workspace/registry.go`, `internal/config/devstack.go`, `cmd/root.go`, `cmd/resolve.go`, and `cmd/init.go`. Ownership is split between workspace registry, workspace config, and per-repo generated files.

**Implementation**  
- add workspace manifest loader
- add service manifest loader
- define resolved config model
- implement precedence rules
- add legacy adapter for `.devstack.json`

**Verification criteria**  
- `go test ./...`
- unit tests for precedence
- sample workspace resolves correctly from cwd and explicit flags

### Bead 2: Topology and explainability
**Goal**  
Make services, groups, dependencies, and resolved config discoverable to humans and AI.

**Project context**  
Current discovery is partial and split between `cmd/resolve.go`, `cmd/services_cmd.go`, and MCP tool descriptions. There is no first-class config explainability.

**Implementation**  
- add `devstack topology`
- add `devstack explain config`
- add `devstack explain service`
- add MCP `topology`
- add MCP `config_context`

**Verification criteria**  
- verify CLI outputs against a sample multi-repo workspace
- verify MCP tools list correct groups/deps
- tests cover cwd resolution and cross-repo dependency expansion

### Bead 3: Runtime session model and cleanup guarantees
**Goal**  
Make `workspace up/down` reliable and verifiable.

**Project context**  
Current lifecycle in `cmd/start_cmd.go` and `cmd/down_cmd.go` relies on pidfiles and Tilt reachability, without strong cleanup verification.

**Implementation**  
- add workspace session state
- track process groups and descendant PIDs
- implement graceful stop then forced cleanup
- verify port release and process exit
- integrate compose-managed infra lifecycle

**Verification criteria**  
- repeated up/down leaves no orphaned tracked processes
- expected ports are released
- session file shows closed state after teardown
- `go test ./...` plus manual lifecycle verification

### Bead 4: Telemetry confidence model
**Goal**  
Stop agents and humans over-trusting missing OTEL data.

**Project context**  
Current MCP guidance in `internal/mcp/tools.go` and generated AGENTS text elevates `investigate` without enough caveats about telemetry coverage.

**Implementation**  
- add `telemetry_health` CLI/MCP
- classify observed / not observed / unknown
- add confidence reporting
- update investigate outputs to be evidence-based

**Verification criteria**  
- simulate healthy telemetry, broken collector, and no-emission cases
- verify tool output never claims absence as proof without evidence
- manual validation against local OTEL stack conditions

### Bead 5: Minimal, correct AI instructions
**Goal**  
Replace drift-prone generated instructions with compact, accurate operational guidance.

**Project context**  
`buildAgentInstructions()` in `cmd/init.go` currently generates static content that already drifts from actual MCP tools.

**Implementation**  
- generate minimal AGENTS section
- ensure it references only real tools
- instruct agents to call `environment`, `topology`, and `telemetry_health` first
- demote `.mcp.json` and AGENTS to convenience artifacts

**Verification criteria**  
- generated instructions match actual MCP tool set
- fresh-session walkthrough confirms agent can discover service/group/deps without guessing

---

## 15. Opinionated choices

### Keep
- local code execution
- MCP as the AI control interface
- workspace concept
- groups/dependencies
- OTEL integration

### Change
- move service truth into repos
- move composition truth into workspace manifest
- add compose for infra
- add supervisor/session model
- add explainability tools
- change AI semantics to confidence/evidence-based

### Avoid
- full “everything in Docker” by default
- static AGENTS as the operational source of truth
- multiple files owning the same concepts
