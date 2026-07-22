# Plans Overview

This directory currently contains three planning/spec documents for the next devstack and pi work.

## Documents

### `rebuild-spec.md`
High-level target architecture and rebuild spec covering:
- config ownership
- runtime model
- observability model
- CLI and MCP direction
- migration strategy

### `devstack-rebuild-plan.md`
Implementation-oriented plan for rebuilding devstack around:
- workspace and repo manifests
- resolved config and explainability
- runtime session tracking
- cleanup verification
- topology and telemetry confidence

### `pi-workspace-plan.md`
Implementation-oriented plan for using pi as the operator/agent layer via:
- project-local extensions
- skills
- evidence-based prompt injection
- a dedicated playground workspace with fake services

## Recommended order
1. Use `rebuild-spec.md` as the target model.
2. Use `devstack-rebuild-plan.md` to sequence backend/runtime work.
3. Use `pi-workspace-plan.md` to build the playground and agent-operating layer in parallel.

## Immediate next step
Start with the smallest end-to-end slice that exercises both plans:
- create the playground workspace skeleton
- add workspace and repo manifests
- implement topology/status/telemetry primitives
- verify the agent reports confidence correctly on one healthy and one broken telemetry case
