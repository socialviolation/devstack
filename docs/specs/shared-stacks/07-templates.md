# 07 — Templates

Status: implemented. Date: 2026-11-27.

## Problem

- The same stack is made again and again, with the same services and the
  same environment.
- Each time, the owner or an agent must remember the list and run
  `env use --stack` after create.

## Change

The workspace manifest takes a `templates:` key. A template is a
named recipe for a new stack. It never runs anything. It is not in Tilt,
in `status`, or in a group command.

```yaml
templates:
  agent:
    purpose: An nx-agent stack that uses prod RAG.
    services: [navexa-api, navexa-frontend, nx-agent, nxmcp, agent-rag]
    env: dev-agent-prod
```

- `services` names services, groups, or both.
- `env` is optional.
- `purpose` is free text for the listings.

### Load validation

devstack refuses to load the manifest if:

- a name in `services` is not a service and not a group;
- `env` is not in `environments:`;
- a template has the same name as a service or a group;
- `services` is empty.

A shared service in `services` is skipped, not refused, as in a group.

### Create

```
named   = template services (− shared) + --repos
members = named ∪ needs-closure(named) − shared
```

- `stack create <name> --template <t>` (MCP: `stack_create` with
  `template`). `--repos` is optional with a template.
- If the template has `env`, create sets the stack's env, as
  `env use <env> --stack <name>` does.
- The record keeps `Template`. `stack list` (CLI and MCP) shows it.
- An unknown template fails and lists the templates.

### Discovery

- `devstack stack templates` lists each template.
- The MCP `environment` tool lists the templates after the groups.
- `devstack prime` lists the template names in one line.

## Term

"Template" means only this feature. The owner's checkout, which docs
once called the template, is now the "source".
