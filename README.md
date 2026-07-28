<img width="1672" height="941" alt="ChatGPT Image Jul 22, 2026, 03_59_46 PM" src="https://github.com/user-attachments/assets/7b9c8b3e-5f8f-4298-9870-60073ee4f2db" />

Devstack runs your team's local services in dependency order, across every repo, and hands the same controls to Claude Code over [MCP](https://modelcontextprotocol.io).

It sits on [Tilt](https://tilt.dev): one daemon for the whole machine, one shared OpenTelemetry backend. An agent can start a service, read its logs and pull the trace that broke it without you pasting output.

## Install

```bash
go install github.com/socialviolation/devstack@main
```

Needs Go 1.25+ and [Tilt](https://docs.tilt.dev/install.html) on `$PATH`. Docker as well, if you turn on local observability.

## Quick start

```bash
devstack workspace add ~/dev/my-workspace   # register the directory holding your services
devstack workspace up                       # start the dev daemon (and the collector, if enabled)
devstack init --all                         # write .mcp.json + AGENTS.md into every registered service
devstack status                             # what's running, on which port, pointing where
```

Then open Claude Code in any service repo. It reads `.mcp.json` and picks up the devstack tools.

To register a service that isn't in the workspace manifest yet:

```bash
devstack init --name=api --path=~/dev/my-workspace/api --cmd="go run ." --port=8080
```

Language is detected from `go.mod`, `package.json`, `requirements.txt` or `*.csproj`. Override with `--language`.

## Concepts

| Term | Meaning |
|------|---------|
| Workspace | A directory with a `devstack.workspace.yaml` manifest, grouping one or more services |
| Service | A process defined by a `devstack.service.yaml` manifest: an API, worker, importer |
| Group | A named set of services you start and stop together |
| Host daemon | One Tilt daemon (`:10300`) for the whole machine. Every workspace's services and every active stack's overlay run inside it as `<workspace>:<service>[:<stack>]`. There is no daemon per workspace. |
| Base | The workspace's own checkout and the instance it runs. What every command acts on when you pass no `--stack`, and spelled literally as `base` where a command takes a stack name |
| Feature stack | A parallel version of one or more services, run from a git worktree on a feature branch on its own port, beside base, reusing base for everything it doesn't change |
| Environment | A named config-var patch (`environments:` in the workspace manifest) applied at workspace, service or stack scope. Where a service points. |

## Workspaces

```bash
devstack workspace            # list registered workspaces
devstack workspace add [path]
devstack workspace remove <name>
devstack workspace up         # start the dev daemon in the background
devstack workspace down       # stop the daemon and the collector
devstack workspace doctor     # check manifests and topology integrity
devstack workspace open       # dev daemon dashboard
```

`devstack ws` is an alias. Registration is written to `~/.config/devstack/workspaces.json`, once per machine.

## Services

```bash
devstack start   [service|group] [--stack <name>]
devstack restart [service|group] [--stack <name>]
devstack stop    [service|group] [--stack <name>]
devstack status  [--all] [--stack <name>]
devstack topology [service]
```

Name nothing and devstack works out the service from your working directory. `start` resolves dependencies and brings them up first, and boots the dev daemon if it isn't already up. `restart` acts on the target alone. `status` collapses groups and stacks with nothing running, until you pass `--all`.

States are `running`, `starting`, `building`, `stopped`, `erroring`, `disabled`, `unknown`. Stopped means registered but not started, which is not the same as broken.

Dependencies and groups:

```bash
devstack deps add <service> <dep>       # <dep> starts first
devstack deps remove <service> <dep>
devstack deps list <service>            # full resolved startup sequence
devstack groups list
devstack groups add <group> <service> [service...]
devstack groups remove <group> <service> [service...]
```

## Feature stacks

A feature stack runs a parallel version of a few services beside base. Each changed service gets its own git worktree on a feature branch and its own dynamically allocated port, folded into the one host daemon as `<workspace>:<service>:<stack>`. Everything it doesn't change resolves to base, so several features can run live at once without cloning the world.

```bash
devstack stack create <name> --repos <svc>[,<svc>]  # worktrees for the changed services and their callers
devstack stack up <name>                            # fold it into the host daemon
devstack stack down <name>                          # stop it, keep the worktrees
devstack stack list
devstack stack rm <name>                            # remove worktrees, release ports, deregister
devstack stack config <svc> --stack <name>          # effective config that instance runs with
devstack stack note <name> [text]                   # what this stack is for
```

Work on a stack by `cd`-ing into its worktree, the path `stack create` and `stack list` print. It's already on the stack's branch, so you never `git checkout`, and reloading that instance alone with `restart --stack <name>` leaves base and every other stack untouched.

`create` also takes `--branch` (defaults to the stack name, attaches if it exists) and `--note`. A note is the part you can't reconstruct a week later: a branch says what changed, a note says why.

> Shared state is not isolated. Stacks isolate code and ports, not the database, queues or caches. Two stacks on one DB see each other's data. Point one elsewhere with an [environment](#environments) if that matters.

The original design record is [docs/stacks-spec.md](docs/stacks-spec.md). Read it for the reuse and port model; its daemon topology predates the one-daemon-per-host change.

## Environments

An environment is a named config-var patch in the workspace manifest: DB URLs, feature flags, endpoints, repointed without touching code. Base can run against `local` while one stack runs against `staging`.

Environments are defined once in the base workspace manifest and inherited. A stack doesn't define its own; `env use --stack <name>` just points it at one of base's. Three scopes, most-specific winning: stack, service, workspace.

```bash
devstack env set <name> KEY=VALUE [KEY=VALUE ...]    # creates the env if it's new
devstack env use <name> [--service <svc>] [--stack <name>]
devstack env which [--service <svc>] [--stack <name>]   # what a service resolves to, key by key
devstack env show <name>
devstack env list
devstack env remove <name>
```

`env which` names the rung each value came from: `.envrc`, `env.files`, manifest `env.values`, the active env, then devstack's computed values, each overriding the one before. `--shadowed` lists what got overridden.

Credentials are redacted in place, so a connection string still shows its server and database. `--reveal` prints them in the clear. Redaction is display-only. Read [what to commit](#files-and-what-to-commit) before you put a secret in an environment.

## Observability

Opt-in per workspace. While it's off, no collector runs and nothing is injected into services.

```bash
devstack otel enable [--backend=openobserve|signoz|forwarding]
devstack otel disable
```

With it on, `workspace up` starts one `otelcol-contrib` and one backend for the whole machine, shared by every workspace, the same way one daemon runs everyone's services. `OTEL_EXPORTER_OTLP_ENDPOINT` is pushed down to every service (gRPC 4317, HTTP 4318), so you never repeat it. If the collector dies while enabled, `devstack status` warns and tries to restart it.

The collector needs `otelcol-contrib` on `$PATH`, or `OTELCOL_BIN` pointing at it. Without one the workspace still comes up, and `devstack otel start` fails until you install it from [opentelemetry-collector-releases](https://github.com/open-telemetry/opentelemetry-collector-releases/releases).

OpenObserve is the default backend: one container, ~230 MB idle, UI on `localhost:5080`. SigNoz is available and far heavier, ClickHouse plus Zookeeper plus UI at ~1.5-2 GB idle. To ship to your own OTLP endpoint instead of storing locally:

```bash
devstack otel configure --plugin=forwarding --set upstream=telemetry.example.com:443 --set protocol=grpc
devstack otel configure --plugin=forwarding --set upstream=https://otel.example.com:4318
```

The plugin choice is written to the workspace manifest and travels with the project, so workspaces can differ. When they do, the one collector routes each workspace's telemetry to its own backend on the `devstack.workspace` attribute. Per-developer override: set `OTEL_EXPORTER_OTLP_ENDPOINT` in a service repo's `.envrc`.

Querying names no backend, URL or credential. Devstack resolves the workspace's own, and the same resolution backs the `investigate` MCP tool:

```bash
devstack otel services               # which variants are reporting
devstack otel traces                 # recent root spans
devstack otel traces --service=api --since=15m
devstack otel traces --stack=feat-x
devstack otel traces <trace-id>      # full span tree
devstack otel logs --trace=<trace-id>
devstack otel status                 # collector state, ports, per-service evidence
devstack otel open                   # the UI
devstack otel plugins                # backends and their config keys
```

Every instance reports to that one backend, stamped with `devstack.workspace`, `devstack.service`, `devstack.stack` and `devstack.env`. These are namespaced deliberately: `deployment.environment` belongs to whoever owns the destination, and the `forwarding` backend sets it per workspace. So you slice at query time rather than running a backend each. A service often reports itself under a different name than devstack knows it by; filters match either, and `otel services` prints both:

```
Navexa.API      (devstack: navexa-api)  stack=agent env=dev
Navexa.API      (devstack: navexa-api)  stack=base  env=dev
nxTradeImporter                         stack=base  env=dev
```

## Tunnels

Forward this workspace's service ports over SSH. Any host you can ssh to works, including a plain config alias; a tailnet address is one such host, not a requirement. Key-based auth only. Run `push` where the services are, `pull` where you want to reach them from.

```bash
devstack tunnel push my-box.ts.net --user alice        # first run saves the remote
devstack tunnel push                                   # later runs reuse it
devstack tunnel push --services navexa-api,navexa-frontend
devstack tunnel push --stacks                          # every active stack, each on its own port
devstack tunnel push --as-base agent                   # one stack, on the ports base normally serves
devstack tunnel push --otel                            # also the observability UI
devstack tunnel pull <host>
devstack tunnel list [--stacks] [--as-base <name>]     # what would be forwarded, no SSH
devstack tunnel status
devstack tunnel stop [--services navexa-api]
devstack tunnel restart [--mode push|pull]
```

`--services` takes exact names, the ones `tunnel list` prints, not partial matches. Only ports actually serving traffic get forwarded. With `--otel` the remote reads this machine's traces at the address you use locally.

`--stacks` and `--as-base` do different jobs and can't be combined. `--stacks` gives every stack its own port on the far end. `--as-base <name>` puts one stack where base lives, so the far end reaches it at the address it already knows and nothing over there needs reconfiguring:

```
devstack tunnel push my-box.ts.net --as-base agent
# far end :4200 → here :20006, far end :63290 → here :20005
```

`stop` and `status` work off the forwards actually running rather than what's discoverable now, so the observability UI (never a Tilt resource) and any port whose service has since gone are still reported and still torn down.

`restart` repeats the last push or pull — same direction, same services, same stack mapping — and says what it's repeating. Otherwise it would rebuild from the defaults: base back on the ports a mapped stack was serving, and a push on the machine you ran `pull` from. Any flag you pass overrides the saved one.

`--reclaim` kills whatever already holds those ports on the far host before forwarding. It may belong to a colleague. Check first: `ssh <host> 'ss -ltnp | grep <port>'`.

## MCP

`devstack serve` is the MCP server. `.mcp.json` invokes it over stdio; you don't run it yourself.

```
environment  status  topology  start  stop  restart  process_logs  service_env
configure    observability     investigate    tunnel
stack_create stack_up  stack_down  stack_list  stack_rm  stack_note
env_use      env_which env_set
```

The set adapts to the workspace: `investigate` appears only when observability is enabled, `tunnel` only when there's an ssh client. Call `environment` first. It reports the workspace's observability state, its in-flight stacks, and which tools actually exist here.

`investigate` is the trace tool, and it has three modes. Give it a `trace_id` for one full span tree. Give it `attribute` and `value` to find every root span where, say, `portfolio.id=57835`, then expand each trace. Give it neither and you get the most recent executions. `stack` scopes it: absent means base, a name means that stack, `"all"` means every instance. When OTEL logs aren't available it falls back to dev-daemon process logs. Matching root spans only means each result is a distinct trace entry point, whichever service owns the root.

`service_env` resolves and edits a service's env: `get` shows each key with the rung it came from, `diff` compares services, `set` writes to the manifest or `.envrc`, `check` audits required keys, `drift` compares what's resolved against what the repo declares it needs.

Some commands have no tool and still need a shell: `workspace up` and `down`, `workspace doctor`, `stack config`, and every otel command the `observability` and `investigate` tools don't cover (`otel services`, `otel traces`, `otel logs`, `otel open`). Registration is CLI-only too: `init`, `deps`, `groups`, `workspace add`.

## Per-repo setup

`devstack init` writes one `.mcp.json` per service repo. It names the default service and nothing else, so it's machine-agnostic and safe to commit:

```json
{
  "mcpServers": {
    "devstack": {
      "type": "stdio",
      "command": "devstack",
      "args": ["serve", "--transport=stdio"],
      "env": {
        "DEVSTACK_DEFAULT_SERVICE": "my-api"
      }
    }
  }
}
```

If an older copy still carries `DEVSTACK_WORKSPACE` or `TILT_PORT`, refresh it with `devstack init --all`.

## Configuration

| Variable | Default | Purpose |
|----------|---------|---------|
| `DEVSTACK_WORKSPACE` | auto-detected from cwd | Workspace name or path |
| `DEVSTACK_DEFAULT_SERVICE` | — | Service used when a command or tool is given no name |
| `DEVSTACK_DAEMON_PORT` | `10300` | Host daemon API port. Legacy alias: `TILT_PORT` |
| `DEVSTACK_DAEMON_HOST` | `localhost` | Host daemon host. Legacy alias: `TILT_HOST` |
| `OTELCOL_BIN` | found on `$PATH` | Path to the `otelcol-contrib` binary |

## Files, and what to commit

| Artefact | Location | Commit? | Why |
|----------|----------|---------|-----|
| `devstack.workspace.yaml` | workspace root | Yes | Source of truth: services, groups, dependencies, environments. Portable, no machine paths. |
| `devstack.service.yaml` | service repo | No | Machine-local: it bakes absolute tool paths. Gitignore it. |
| `.mcp.json` | service repo | Yes | Generated machine-agnostic. |
| `AGENTS.md` | service repo | Yes | Agent instructions. devstack owns only the block between its sentinels; the rest is yours. |
| `CLAUDE.md` / `GEMINI.md` / `.cursorrules` / `.github/copilot-instructions.md` | service repo | Yes | Yours, with a devstack-managed block appended between sentinels. Updated only if they already exist. |
| `.devstack.json` | workspace root | No | Retired. Otel plugin config now lives under `observability` in the workspace manifest. Safe to delete once `devstack otel status` shows what you expect. |
| `Tiltfile` | anywhere | No | Generated build artifact, never hand-edited. |
| `~/.config/devstack/workspaces.json` | home | n/a | Machine-local registry of workspaces and their ports. |
| `~/.local/share/devstack/**` | home | n/a | Host daemon state: pids, logs, `stacks.json`, the generated host Tiltfile. |

Suggested per-repo `.gitignore`:

```
devstack.service.yaml
.devstack.json
Tiltfile
```

Because `devstack.service.yaml` is machine-local and gitignored, a stack's worktree doesn't inherit one. That's why `devstack stack create` materialises ignored config into each worktree rather than relying on git.

### Secrets and `devstack env set`

`env set <env> KEY=VALUE` writes the value into `devstack.workspace.yaml` in plaintext, and masking happens on display only, never at rest. So it depends on how you treat that file.

If you commit it, which is the default recommended above, keep real secrets out. Declare them in a service's `env.required` and supply them at runtime from `.envrc` or your own secret store, and use `env set` only for URLs, ports and feature flags. A secret written here is a committed secret.

If it's machine-local, either gitignored or the workspace root isn't a repo, `env set` is fine for API keys. That's what it was built for, though the file is still plaintext on disk.

Unsure which you are: `git check-ignore -v devstack.workspace.yaml` in the workspace root.

## Updating devstack

```bash
cd <devstack repo> && git pull
go install ./...              # replace the binary on your PATH
devstack init --all           # per workspace: refresh AGENTS.md and .mcp.json
                              # then restart your MCP server / agent session
devstack status               # check the daemon and services still answer
```

Two of those bite. A stale binary doesn't fail on config it doesn't understand, it falls back: an older devstack reading a workspace set to OpenObserve quietly started SigNoz instead. And MCP tool descriptions are read once at server startup, so a session already running keeps the old tool list. New tools don't appear, new parameters get rejected. Restart it.

`devstack init --all` writes files only, nothing restarts. Run it from inside each workspace, and expect a git diff in every service repo.

If a workspace was mid-upgrade, `devstack otel start` replaces the observability container when its pinned image has moved.
