---
name: devstack-operations
description: Operate the devstack playground safely. Use when resetting scenarios, checking cleanup residue, or choosing between single-service and group-level actions.
---

# Devstack Operations

## Workflow
1. Use `/devstack-doctor` to inspect workspace integrity.
2. Use `playground/devstack-playground/scripts/reset.sh` to reset the playground.
3. Use `playground/devstack-playground/scripts/verify-cleanup.sh` before claiming teardown is complete.
4. Prefer the smallest affected service or group over whole-workspace disruption.

## Scenario controls
- `playground/devstack-playground/scripts/break-telemetry.sh no-traces`
- `playground/devstack-playground/scripts/break-telemetry.sh collector-down`
- `playground/devstack-playground/scripts/restore-telemetry.sh`
