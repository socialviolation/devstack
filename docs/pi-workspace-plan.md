# Pi Workspace Plan

## Objective
Use pi as the programmable interaction layer for local development, service control, diagnostics, and agent guidance.

This plan focuses on building a dedicated pi playground workspace that makes it easy to test:
- service control behavior
- topology discovery
- telemetry confidence rules
- AI truthfulness and operator guidance

## Why use pi here
Pi is a good fit because it already supports:
- project-local extensions
- project-local skills
- commands and tools
- custom terminal UI overlays
- reloadable local development

The fastest path is to build on top of pi's extension and skill system rather than starting with a separate SDK app.

## Recommended approach
Use pi in this order:
1. **Extensions** for capabilities and runtime behavior
2. **Skills** for operating procedures and diagnostic ladders
3. **Prompt templates** only for repeated human workflows
4. **Custom TUI** after the tools and skills are working
5. **SDK** only if a separate app is still needed later

## Workspace shape
Create a dedicated playground workspace focused on testing agent behavior against known scenarios.

### Proposed structure
```text
devstack-playground/
├── devstack.workspace.yaml
├── compose.yaml
├── .pi/
│   ├── settings.json
│   ├── extensions/
│   │   └── devstack-playground/
│   │       ├── package.json
│   │       ├── index.ts
│   │       ├── tools/
│   │       ├── lib/
│   │       ├── ui/
│   │       └── prompts/
│   └── skills/
│       ├── devstack-debugging/
│       └── devstack-operations/
├── services/
│   ├── frontend/
│   ├── api/
│   ├── worker/
│   ├── telemetry-good/
│   ├── telemetry-bad/
│   └── crashy/
└── scripts/
```

## Service plan
The fake services should be intentionally small but behaviorally useful.

### `frontend`
Purpose:
- visible request entry point
- sync dependency on `api`

### `api`
Purpose:
- normal success path
- slow path
- failing path
- telemetry-enabled control service

### `worker`
Purpose:
- async/background flow
- dependency realism
- process-log-heavy behavior

### `telemetry-good`
Purpose:
- traces/logs work correctly
- control sample for confidence checks

### `telemetry-bad`
Purpose:
- broken or partial telemetry sample
- used to train/test anti-lie behavior

Support scenarios like:
- traces missing, logs present
- logs missing, traces present
- wrong OTEL service name
- bad OTLP endpoint
- intermittent emission

### `crashy`
Purpose:
- starts then exits
- tests restart/status/cleanup flows

## Extension plan
Build one project-local pi extension package first.

### Extension responsibilities
- register workspace-aware tools
- register human-facing commands
- inject evidence/confidence rules before agent runs
- optionally provide a simple dashboard overlay
- centralize manifest/process/telemetry helpers

### Suggested internal modules
- `index.ts` — wiring
- `tools/topology.ts`
- `tools/status.ts`
- `tools/telemetry-health.ts`
- `tools/restart.ts`
- `tools/doctor.ts`
- `lib/manifests.ts`
- `lib/processes.ts`
- `lib/telemetry.ts`
- `lib/confidence.ts`
- `ui/dashboard.ts`

## First tool set
These tools should exist before worrying about a complex UI.

### `workspace_topology`
Return:
- services
- groups
- dependencies
- dependents
- source paths
- telemetry expectations

### `workspace_status`
Return:
- service state
- PID if known
- ports
- health state
- recent exit/start information if available

### `telemetry_health`
Return:
- collector reachable or not
- backend query healthy or not
- spans seen recently
- logs seen recently
- service expected to emit telemetry
- env/config mismatch indicators
- confidence classification
- interpretation note

### `workspace_restart`
Support:
- restart service
- restart group
- report dependency-safe order used

### `workspace_doctor`
Check:
- manifest validity
- graph issues
- stale runtime state
- residue after stop/down
- telemetry misconfiguration

## Command plan
Add commands that are useful both to humans and during extension development:
- `/topology`
- `/status`
- `/telemetry`
- `/doctor`
- `/dashboard`

## Skill plan
Use skills for policy and repeatable diagnostic behavior.

### Skill: `devstack-debugging`
Teach the diagnostic ladder:
1. inspect topology
2. inspect status
3. inspect telemetry health
4. inspect process logs and investigation output
5. only then restart or stop services

Required language rules:
- do not claim a path did not run based only on missing telemetry
- say whether evidence is observed, absent, or inconclusive
- report collector/backend health before making stronger claims

### Skill: `devstack-operations`
Teach operational behavior:
- when to restart one service
- when to restart a group
- how dependency ordering works
- how to verify cleanup
- when not to stop the entire workspace

## Prompt injection plan
Use the extension's `before_agent_start` hook to inject short standing rules on every run.

These rules should enforce:
- missing telemetry is not proof of non-execution
- topology must be checked before dependency claims
- telemetry confidence must be reported explicitly
- process logs and runtime state should be preferred over guessing

## UI plan
Only add a custom TUI overlay after the core tools are useful.

### Minimal dashboard overlay
Show:
- current workspace
- services and states
- selected service
- deps/dependents
- spans/logs indicators
- confidence note
- last restart result

The UI should summarize tool output, not replace tool design.

## Development loop
Use pi's local development loop:
- keep the extension under `.pi/extensions/`
- keep skills under `.pi/skills/`
- iterate using `/reload`

This should be the default workflow during playground development.

## Verification plan
The playground is successful when pi can consistently produce evidence-based output against controlled scenarios.

### Topology verification
- service graph is correct
- group expansion is correct
- dependency ordering is correct

### Status verification
- running services are identified correctly
- crashed services are identified correctly
- port/state reporting matches reality

### Telemetry verification
- healthy service reports high confidence
- broken service reports partial or low confidence
- collector-down case is reported as inconclusive
- missing spans never cause a false definitive claim by themselves

### Agent behavior verification
Test prompts should verify that pi says things like:
- "I found no spans in the current window"
- "Telemetry is inconclusive because collector health is degraded"
- "Process logs show the handler ran"
- "Restarting group X uses order A -> B -> C"

and does not say things like:
- "this definitely was not triggered" when that conclusion is only based on absent telemetry

## Implementation sequence

### Phase 1: playground skeleton
- create workspace structure
- add 3 minimal services: `frontend`, `api`, `telemetry-bad`
- add project-local pi settings

### Phase 2: extension core
- implement `workspace_topology`
- implement `workspace_status`
- implement `telemetry_health`
- wire `/topology`, `/status`, `/telemetry`

### Phase 3: skill layer
- add `devstack-debugging`
- add `devstack-operations`
- validate that pi loads and can invoke them

### Phase 4: anti-lie layer
- inject evidence rules through `before_agent_start`
- refine confidence wording from real test runs

### Phase 5: operational tools
- add restart tool
- add doctor tool
- add cleanup verification helpers
- add `worker` and `crashy`

### Phase 6: optional UI
- build `/dashboard` overlay if the core workflow still benefits from it

## Initial success criteria
The first meaningful milestone is reached when the playground can:
1. describe topology correctly
2. report service state correctly
3. classify telemetry confidence correctly for at least one healthy and one broken service
4. guide the model away from false claims based on missing OTEL data
