# Devstack Rebuild Plan

## Objective
Rebuild devstack so local multi-repo development is easier to understand, easier to control, and less likely to mislead either humans or AI agents.

## Problems to solve
- Config ownership is split between global registry, workspace config, repo config, and generated files.
- Workspace shutdown is not a reliable "everything is gone" operation.
- Service/group/dependency discovery is too implicit.
- Telemetry is treated as stronger evidence than it really is.
- Generated agent instructions drift from real runtime behavior.

## Target architecture

### Config model
Separate ownership clearly:
- **Workspace manifest** owns composition:
  - repo membership
  - groups
  - cross-service dependencies
  - environments
  - infra provider
  - shared observability defaults
- **Repo manifest** owns service self-description:
  - service name
  - run command
  - healthcheck
  - ports
  - env files
  - telemetry expectations
- **Runtime state** owns live session/process data:
  - active session
  - tracked processes
  - active ports
  - infra status
  - telemetry confidence snapshots

### Runtime model
Use a hybrid local runtime:
- **Docker Compose** for shared infra:
  - OTEL stack
  - databases
  - caches
  - queues
  - emulators
- **Local supervisor** for code services:
  - start/stop/restart
  - dependency ordering
  - health checks
  - process logs
  - cleanup verification

### AI/observability model
Make all diagnostic output evidence-based:
- distinguish `observed`, `not observed`, and `inconclusive`
- never treat missing telemetry as proof of non-execution
- expose telemetry health as a first-class capability

## Deliverables

### 1. New manifests
Add:
- `<workspace>/devstack.workspace.yaml`
- `<repo>/devstack.service.yaml`

Define exact ownership for each field and keep generated files out of the authoritative config path.

### 2. Config resolution layer
Implement a resolved config layer that merges:
1. CLI flags
2. invocation env vars
3. runtime session overrides
4. workspace manifest
5. repo manifest
6. defaults

Add explainability commands so users and agents can see where every effective value came from.

### 3. Runtime session model
Introduce workspace session state under a local state directory with:
- session ID
- supervisor PID
- tracked child PIDs
- compose project info
- active services
- active ports
- closed/unclean shutdown markers

### 4. Topology and doctoring
Add first-class support for:
- service graph discovery
- groups
- dependencies
- dependents
- missing manifest detection
- cycle detection
- stale state detection
- cleanup residue detection

### 5. Telemetry confidence layer
Add a telemetry status surface that checks:
- collector reachability
- backend query health
- service env/config correctness
- recent spans
- recent logs
- confidence level for telemetry-backed diagnosis

### 6. Minimal generated artifacts
Reduce generated files to convenience only:
- `.mcp.json` should be generated from resolved config
- `AGENTS.md` should be a short helper, not the system model

## Implementation sequence

### Phase 1: manifests and loaders
- Define workspace manifest schema.
- Define repo manifest schema.
- Implement loaders and validation.
- Add compatibility adapter for existing `.devstack.json`.

### Phase 2: resolved config and explainability
- Build resolved workspace/service config types.
- Implement precedence rules.
- Add commands for config explanation.
- Add service discovery from workspace manifest and repo manifests.

### Phase 3: topology and status model
- Add service graph builder.
- Add commands for groups, dependencies, and dependents.
- Add doctor checks for config graph issues.

### Phase 4: runtime session and cleanup
- Implement supervisor-backed runtime session state.
- Track process groups and descendant PIDs.
- Integrate compose-managed infra.
- Make `workspace down` verify cleanup before succeeding.

### Phase 5: telemetry confidence
- Add telemetry health checks.
- Classify results into observed/not-observed/inconclusive.
- Update investigate/status flows to use the new language.

### Phase 6: generated files and migration tightening
- Minimize AGENTS generation.
- Regenerate `.mcp.json` from resolved config.
- Prefer manifests over legacy config by default.

## Command plan
Add or reshape the CLI around these capabilities:
- `devstack topology`
- `devstack explain config`
- `devstack explain service <name>`
- `devstack telemetry status`
- `devstack workspace doctor`
- `devstack workspace up`
- `devstack workspace down`
- `devstack start <service>`
- `devstack start --group <group>`
- `devstack restart <service|group>`

## Verification plan
Each phase closes only after running code verifies it.

### Config/model verification
- Resolve manifests from workspace root and repo cwd.
- Confirm precedence behavior with automated tests.
- Confirm explain output identifies config sources correctly.

### Topology verification
- Validate correct group/dependency expansion.
- Validate cycle detection.
- Validate missing/duplicate manifest reporting.

### Runtime verification
- Start workspace.
- Start services and groups.
- Shut workspace down.
- Verify tracked PIDs are gone.
- Verify expected ports are freed.
- Verify compose infra is stopped.
- Repeat up/down cycles cleanly.

### Telemetry verification
- Test healthy telemetry.
- Test collector-down scenario.
- Test logs-only scenario.
- Test traces-only scenario.
- Test misconfigured exporter scenario.
- Confirm output never claims absence as proof without stronger evidence.

## Initial build slice
First slice should be small but end-to-end:
1. workspace manifest loader
2. repo manifest loader
3. resolved service graph
4. `topology` command
5. `explain service` command
6. runtime session state skeleton
7. cleanup verification command

## Files most likely to change
- `cmd/root.go`
- `cmd/init.go`
- `cmd/start_cmd.go`
- `cmd/down_cmd.go`
- `cmd/resolve.go`
- `cmd/services_cmd.go`
- `cmd/serve.go`
- `internal/config/*`
- `internal/workspace/*`
- `internal/mcp/*`

## Migration rules
- Keep legacy config readable during migration.
- Prefer manifests when both old and new config exist.
- Keep generated files non-authoritative.
- Make drift visible through `doctor` and `explain` commands.
