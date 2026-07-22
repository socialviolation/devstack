# Fresh Pi Session Prompt

Use this prompt to drive a fresh pi session that implements and verifies the full devstack + pi playground plan in `/home/nick/dev/devstack`.

---

You are working in `/home/nick/dev/devstack`.

## Mission
Implement and verify the open beads for the devstack rebuild and the pi playground/operator layer. The goal is not just to write code. The goal is to solve the actual problems the project is trying to address:

1. **Config ownership and precedence are confusing**
   - Workspace config, registry config, repo config, generated `.mcp.json`, generated `AGENTS.md`, and env vars currently overlap.
   - The rebuild must make ownership explicit and explainable.

2. **Process lifecycle is not trustworthy enough**
   - `workspace down` must become something operators can trust.
   - Runtime state, cleanup, residue reporting, and infra separation must be verified by running code.

3. **Agents over-claim from missing telemetry**
   - Missing traces/logs are not proof that something did not happen.
   - The system must move to evidence-based wording and explicit confidence levels.

4. **Humans need better visibility into what the AI is doing**
   - The pi playground should become a truthfulness and operator-experience lab with fake services and controlled scenarios.

## Primary docs to read first
Read these before making changes:
- `docs/rebuild-spec.md`
- `docs/devstack-rebuild-plan.md`
- `docs/pi-workspace-plan.md`
- `docs/plans-overview.md`
- `AGENTS.md`
- `CLAUDE.md`

Also check the current beads state with `bd`.

## Mandatory workflow rules
Follow these rules strictly:

- Use **bd** for task tracking.
- Use `bd ready --json`, `bd show <id>`, `bd update <id> --status in_progress --json`, and `bd close <id> --reason "..." --json`.
- Do **not** claim code works without running tests or verification commands.
- If env vars are missing, check and source `.envrc`.
- Do **not** create extra markdown planning/docs files unless needed for the requested work. The prompt file you are reading is already the plan handoff.
- If you discover new work, create a new bead with `--deps discovered-from:<parent-id>`.
- At the end of the session, run the required quality gates, `bd sync`, `git pull --rebase`, `git push`, and verify the branch is up to date.

## Open epics
- `nvxdev-h2w` — Devstack rebuild epic: explicit workspace model, reliable lifecycle, evidence-based diagnostics
- `nvxdev-qx3` — Pi playground epic: operator-facing extension, skills, and truthfulness lab

## Open beads and dependency graph

### Devstack stream
1. `nvxdev-fdw` — Manifest schemas and legacy adapter for workspace/repo ownership
2. `nvxdev-bt3` — Resolved config engine and explainability commands
   - depends on `nvxdev-fdw`
3. `nvxdev-quc` — Topology graph, doctor checks, and group/dependency discovery
   - depends on `nvxdev-bt3`
4. `nvxdev-cio` — Runtime session supervisor and verified cleanup semantics
   - depends on `nvxdev-bt3`, `nvxdev-quc`
5. `nvxdev-dqi` — Compose-backed infra integration for shared local dependencies
   - depends on `nvxdev-fdw`, `nvxdev-cio`
6. `nvxdev-7uc` — Telemetry confidence model and evidence-based MCP/CLI wording
   - depends on `nvxdev-bt3`, `nvxdev-quc`
7. `nvxdev-82z` — Generated file policy: minimal AGENTS, derived `.mcp.json`, no config drift
   - depends on `nvxdev-bt3`, `nvxdev-7uc`

### Pi/playground stream
1. `nvxdev-wcm` — Playground workspace skeleton with manifests, compose infra, and reset scripts
   - depends on `nvxdev-fdw`
2. `nvxdev-5p4` — Fake services for control, dependency, crash, and telemetry scenarios
   - depends on `nvxdev-wcm`
3. `nvxdev-jvq` — Project-local pi extension package with topology, status, and telemetry tools
   - depends on `nvxdev-wcm`, `nvxdev-5p4`, `nvxdev-quc`
4. `nvxdev-1qs` — Pi skills for debugging ladder and workspace operations
   - depends on `nvxdev-jvq`
5. `nvxdev-9i0` — Anti-lie prompt injection and confidence-oriented agent rules
   - depends on `nvxdev-jvq`, `nvxdev-1qs`
6. `nvxdev-hgi` — Scenario harness for telemetry breakage, cleanup residue, and restart behavior
   - depends on `nvxdev-5p4`, `nvxdev-jvq`, `nvxdev-cio`
7. `nvxdev-dxk` — Pi commands and optional overlay dashboard for operator visibility
   - depends on `nvxdev-jvq`, `nvxdev-9i0`

## Execution strategy

### Phase 1: establish the new config foundation
Start with:
- `nvxdev-fdw`
- then `nvxdev-bt3`
- then `nvxdev-quc`

This phase creates the model everything else depends on.

### Phase 2: branch into runtime and playground setup
Once Phase 1 is verified, two streams can progress:

#### Stream A: devstack runtime/observability
- `nvxdev-cio`
- `nvxdev-dqi`
- `nvxdev-7uc`
- `nvxdev-82z`

#### Stream B: pi playground
- `nvxdev-wcm`
- `nvxdev-5p4`
- `nvxdev-jvq`
- `nvxdev-1qs`
- `nvxdev-9i0`
- `nvxdev-hgi`
- `nvxdev-dxk`

## Subagent guidance
If your harness supports subagents, task fan-out, or worktrees, use them **only where dependencies allow it**.

Recommended parallelization points:

### After `nvxdev-fdw` is complete
You may split into:
- Agent A: `nvxdev-bt3`
- Agent B: `nvxdev-wcm`

### After `nvxdev-bt3` is complete
You may split into:
- Agent A: `nvxdev-quc`
- Agent B: continue `nvxdev-wcm` / `nvxdev-5p4` if not done

### After `nvxdev-quc` and `nvxdev-5p4` are complete
You may split into:
- Agent A: `nvxdev-cio`
- Agent B: `nvxdev-jvq`

### After `nvxdev-jvq` is complete
You may split into:
- Agent A: `nvxdev-1qs`
- Agent B: begin parts of `nvxdev-hgi` that do not need `nvxdev-cio` finished yet

### After `nvxdev-cio`, `nvxdev-jvq`, and `nvxdev-1qs` are complete
You may split into:
- Agent A: `nvxdev-9i0`
- Agent B: `nvxdev-7uc`
- Agent C: `nvxdev-dqi`

### Final convergence
Complete:
- `nvxdev-82z`
- `nvxdev-hgi`
- `nvxdev-dxk`

If subagents are **not** available, do the work sequentially in dependency order.

## Per-bead execution protocol
For each bead:
1. `bd show <id> --json`
2. `bd update <id> --status in_progress --json`
3. Read all relevant files and docs before editing
4. Implement only what the bead calls for
5. Run the bead’s verification commands
6. If verification fails, fix it before moving on
7. Only then `bd close <id> --reason "Verified" --json`

Do not close a bead if you have not actually run the verification.

## Verification expectations
You must prefer real verification over explanation.

Examples:
- If adding manifest loaders/resolution: add tests and run `go test ./...`
- If changing runtime lifecycle: start and stop real sample workspaces/processes and verify residue
- If changing telemetry confidence: run healthy and degraded cases and inspect actual output
- If adding pi extensions/skills: launch pi in the playground and verify the tools/commands/skills load and behave correctly

## Coding principles for this effort
- Keep ownership boundaries crisp.
- Make outputs explainable.
- Make runtime cleanup explicit and verifiable.
- Treat telemetry as evidence, not truth.
- Prefer small, composable primitives over giant hidden logic.
- Generated files must be convenience artifacts, not hidden sources of authority.

## Expected end state
By the end of this session series, the repository should provide:
- a manifest-driven devstack model
- explicit config explainability
- topology and doctor commands
- verified session/runtime cleanup semantics
- telemetry confidence modeling and honest wording
- a pi playground workspace with fake services
- a project-local pi extension and skills
- anti-lie prompt injection
- repeatable scenarios for healthy/broken telemetry and cleanup residue

## Finish requirements
Before ending the session:
1. Run the relevant test/build/verification commands for the completed beads
2. Run `bd sync`
3. `git status`
4. `git pull --rebase`
5. `git push`
6. `git status` again and confirm the branch is up to date
7. Provide a concise handoff stating:
   - which beads were completed
   - which were verified and how
   - which remain blocked and by what

Start by checking ready work with `bd ready --json`, then begin with `nvxdev-fdw` unless it is already complete.
