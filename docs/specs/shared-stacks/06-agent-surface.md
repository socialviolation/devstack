# 06 — Every text that must change

Rule: every changed string follows CLAUDE.md rule 5 (ASD-STE100).
After plan steps 1–8, run `/tool-surface-review` over the repo.

## In this repo

### MCP (`internal/mcp/`)

- `baseTermDesc` (`tools_stack.go:45-47`) feeds `environment`, `base`,
  `stack_config` and every `stack` parameter through `stackParamDesc` and
  `mutatingStackParamDesc` (`tools_stack_target.go:18-30`). Rewrite it
  first: shared set, stack service, stack.
- `base` tool (`tools_base.go`) → `shared`.
- Tools that name base: `environment`, `stack_create`, `stack_list`,
  `stack_up`, `stack_down`, `stack_config`, `start`, `restart`, `stop`,
  `status`, `process_logs`, `configure`, `tunnel` (`as_base`, now `as_declared_ports`),
  `investigate` (`StackScopeDesc` in `internal/observability/backend.go`),
  `doctor`, `cleanup`, `urls`, `wiring`, `hooks`, `env_use`, `env_which`,
  `env_set`, `service_env`, `task_run`, `observability`.
- Expiry text: `stack_extend`, `stack_adopt`, quota errors (removed with
  the quota)
  (`tools_stack_expiry.go:17-34`), doctor finding
  (`internal/doctor/doctor.go:350-352`).
- Guard hints (`guard.go:208-237`).
- `stack_create` description adds: what `needs` pulls in, the shared set,
  the 24h stop, and how to extend.

### prime (`cmd/prime_cmd.go`)

- Base passages: `:159`, `:243-283`, `:386-477`, `:502`, `:511-521`,
  `:571-572`, glossary `:620-627`, legend `:641-664`, restart hint
  `:694-717`, copies table `:752,828-835`.
- Add: the shared set, the 24h stop, the pane token.

### CLI

- `base`, `workspace up`, `stack`, `stack create`, `stack gc`,
  `stack adopt`, `otel services`, root, `service`, `stack config`,
  `env set`, `doctor`, `serve` (tool list and AGENT LIMITS), `help`.
- Flag help: `doctor_cmd.go:57`, `hooks_cmd.go:70`,
  `env_patch_cmd.go:75`, `nouns_cmd.go:98`, `services_cmd.go:62`,
  `stack_cmd.go:319,321`, `tunnel_cmd.go:171`.
- New: `stack sweep`, `shared *`, `herdr publish`, `herdr open`.

### Docs

- README: `:15`, `:44`, `:54`, `:67`, concepts table `:109-115`.
- REFERENCE: "Base and the replica" `:24-56`, feature stacks `:152-206`,
  environments `:211`, otel `:331-348`, tunnel `:360-375`, MCP list
  `:467-485` (also add the eight tools it already misses), prime sample
  `:513-525`. Document `needs`/`calls`, `shared:`, `${stack.suffix}`.

## Outside this repo (separate PR per repo)

- `~/.claude/skills/stack/SKILL.md`: `:125`, `:138`, `:145`, `:152`,
  `:299-319`.
- factory-takeover (in foundry `core/standards/claude-code/skills/`):
  `:6`, `:90-107`.
- `~/.claude/CLAUDE.md:75`: `devstack start` → current command.
- Foundry: `core/skills/factory-fix/SKILL.md` `:135,:253`;
  `handoff.schema.yaml:34-35`; `quality-bar.md:102,:142`;
  `factory-investigate/SKILL.md:32-34,:195-196`;
  `factory-review/SKILL.md:31-33,:69,:78`;
  `core/standards/factory-sequence.md:52,:258-260`;
  `core/policy/platform.yaml:281,:335`; regenerate Kelos outputs.
- Navexa agent docs: nxAccountEvents, nxAccountSync, nxAgent, nxOrbit,
  nxDataPlatform, nxFileProcessor skill, pricing-harness, and the
  workspace manifest comments (`:103,:110,:125`).

Line numbers are from 2026-11-26 and will drift. Re-grep for `base`,
`expir`, `72`, `TTL`, `gc` before editing.
