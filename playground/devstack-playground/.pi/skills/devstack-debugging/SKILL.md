---
name: devstack-debugging
description: Debug the devstack playground with an evidence-first ladder. Use when you need topology, runtime status, telemetry confidence, or process evidence before making claims.
---

# Devstack Debugging

## Workflow
1. Run `/devstack-topology` or use `workspace_topology` first.
2. Run `/devstack-status` or use `workspace_status` to inspect runtime state.
3. Run `/devstack-telemetry` or use `telemetry_health` before drawing conclusions from missing traces.
4. If telemetry is partial, inspect process logs under `playground/devstack-playground/logs/`.
5. Only then suggest restart, stop, or cleanup actions.

## Language rules
- Missing traces are not proof that a path did not run.
- Say what was observed, what was not observed, and what remains inconclusive.
- Mention collector/export health before making stronger claims.
