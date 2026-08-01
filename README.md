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
devstack init --all --claude-hook           # wire up every service repo for agents
devstack status                             # what's running, on which port, pointing where
```

Then open Claude Code in any service repo. It reads `.mcp.json` and picks up the devstack tools, and the [session hook](#briefing-an-agent) briefs it on where it is and what is running. Drop `--claude-hook` if you would rather not commit a hook that runs for everyone who clones the repo.

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
| Host daemon | One Tilt daemon (`:10300`) for the whole machine. Every workspace's services and the overlay of every stack that is up run inside it as `<workspace>:<service>[:<stack>]`. There is no daemon per workspace. |
| Base | The workspace's own checkout and the instance it runs. What every command acts on when you pass no `--stack`, and spelled literally as `base` where a command takes a stack name |
| Feature stack | A parallel version of one or more services, run from a git worktree on a feature branch on its own port, beside base, reusing base for everything it doesn't change |
| Environment | A named config-var patch (`environments:` in the workspace manifest) applied at workspace, service or stack scope. Where a service points. |
| Hook | A shell command devstack runs on a lifecycle event — a stack created, a service started. How ephemeral stacks provision and de-provision the state they need outside this machine. |

## Workspaces

```bash
devstack workspace            # list registered workspaces
devstack workspace add [path]
devstack workspace remove <name>
devstack workspace up         # start the dev daemon in the background
devstack workspace down       # stop the daemon and the collector
devstack workspace doctor     # check manifests and topology integrity
devstack workspace topology   # the service graph: groups, dependencies, dependents
devstack workspace generate   # rebuild the daemon's Tiltfile from the manifests
devstack workspace open       # dev daemon dashboard
```

`devstack ws` is an alias. Registration is written to `~/.config/devstack/workspaces.json`, once per machine.

## Services

```bash
devstack service start   [name] [--stack <name>]   # and its dependencies
devstack service restart [name] [--stack <name>]   # the target alone
devstack service stop    [name] [--stack <name>]
devstack group   start|stop|restart <group>       # every service in the group
devstack status  [--all] [--stack <name>]
devstack workspace topology [service]
```

Commands are noun first: `devstack <noun> <action> [target]`. A name can be both a service and a group, so the noun says which you mean. devstack never guesses. Name nothing after `service` and devstack works out the service from your working directory. `start` resolves dependencies and brings them up first, and boots the dev daemon if it isn't already up. `restart` acts on the target alone. `status` collapses groups and stacks with nothing running, until you pass `--all`.

States are `running`, `starting`, `building`, `stopped`, `erroring`, `disabled`, `unknown`. Stopped means registered but not started, which is not the same as broken.

A service that will not start because its last process is still holding the port can reclaim it, without naming the port:

```yaml
runtime:
  prep:
    freePorts: true              # every port this service declares
    # freePorts: [http, grpc]    # or just some
    command: "dotnet build ..."  # your own prep still runs, after
```

devstack resolves those ports the same way it resolves `${self.port.http}`. From that one line, base frees the port it pins and a stack frees the port it was allocated. Never write the port as a literal: a stack's worktree copies the literal, and `fuser -k 63290/tcp` in a stack kills base.

An instance can only free ports it owns. Reclaiming names the process before killing it, and sends `SIGTERM` before `SIGKILL` so a dev server takes its own children down instead of orphaning them.

```bash
devstack ports check 4200      # what holds a port, IPv4 or IPv6
devstack ports free 4200       # kill it (refuses anything below 1024)
```

Dependencies and groups:

```bash
devstack dependencies add <service> <dep>       # <dep> starts first
devstack dependencies remove <service> <dep>
devstack dependencies list <service>            # full resolved startup sequence
devstack group list
devstack group add <group> <service> [service...]
devstack group remove <group> <service> [service...]
```

## Feature stacks

A feature stack runs a parallel version of a few services beside base. Each changed service gets a git worktree on a feature branch and a dynamically allocated port. Both fold into the one host daemon as `<workspace>:<service>:<stack>`. Everything it doesn't change resolves to base, so several features can run live at once without cloning the world.

```bash
devstack stack create <name> --repos <svc>[,<svc>]  # worktrees for the changed services and their callers
devstack stack up <name>                            # fold it into the host daemon
devstack stack down <name>                          # stop it, keep the worktrees
devstack stack status <name>                        # its instances: state, ports, env
devstack stack list
devstack stack rm <name>                            # remove worktrees, release ports, deregister
devstack stack config <svc> --stack <name>          # effective config that instance runs with
devstack stack note <name> [text]                   # what this stack is for
```

Work on a stack by `cd`-ing into its worktree, the path `stack create` and `stack list` print. It's already on the stack's branch, so you never `git checkout`, and reloading that instance alone with `service restart --stack <name>` leaves base and every other stack untouched.

`create` also takes `--branch` (defaults to the stack name, attaches if it exists) and `--note`. A note is the part you can't reconstruct a week later: a branch says what changed, a note says why.

> Shared state is not isolated. Stacks isolate code and ports, not the database, queues or caches. Two stacks on one DB see each other's data. Point one elsewhere with an [environment](#environments) if that matters.

The original design record is [docs/stacks-spec.md](docs/stacks-spec.md). Read it for the reuse and port model; its daemon topology predates the one-daemon-per-host change.

## Environments

An environment is a named config-var patch in the workspace manifest: DB URLs, feature flags, endpoints, repointed without touching code. Base can run against `local` while one stack runs against `staging`.

Environments are defined once in the base workspace manifest and inherited. A stack doesn't define its own; `env use --stack <name>` just points it at one of base's. Three scopes, most-specific winning: stack, service, workspace.

An environment carries a description saying what it is for. A name and a key list say what it sets; only this says why you would pick it, or why picking it is dangerous:

```yaml
environments:
  fx-prod:
    description: Exchange Rate PoC against the PRODUCTION database. Read-only comparison.
    values:
      SqlConnectionString: ...
```

It shows up in `env list`, `env show`, and the session briefing, so the warning travels with every place the environment is named.

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

## Hooks

A hook is a shell command devstack runs when a lifecycle event fires. Stacks are disposable. That only works if the state a stack needs beyond this machine appears with it and goes with it.

```bash
devstack hooks list [--stack <name>] [--event <event>]
devstack hooks run <event> [--stack <name>] [--services a,b]
```

`hooks run` fires an event's hooks without doing the lifecycle action, which is how you test one and how you retry after a failure.

Declare them in the workspace manifest, where the team shares them:

```yaml
hooks:
  - name: auth0-callbacks
    on: [stack.up]
    services: [navexa-frontend]
    run: ./scripts/auth0.sh add
    env:
      CALLBACK_URL: ${self.url}/callback
```

A stack's ports are allocated when it is created, so a hook cannot hardcode them. `${self.url}`, `${self.port.http}` and `${<service>.port.<key>}` resolve against the ports that instance actually got. The command also receives `DEVSTACK_*` variables (`DEVSTACK_STACK`, `DEVSTACK_SERVICE_URL`, `DEVSTACK_ENV` and the rest) and the whole event as JSON on stdin.

`services:` scopes a hook to named services, and it then runs once per service with that one as `${self}`. Leave it out and it runs once for the event. A service's own `devstack.service.yaml` declares hooks too, scoped to that service, and rejects a `services:` key. A feature stack inherits the workspace's hooks the same way it inherits its environments.

| Event | Fires |
|---|---|
| `stack.create` | after the worktrees exist, the ports are allocated and the record is written |
| `stack.up` | after the stack's services are triggered in the host daemon |
| `stack.down` | before the host Tiltfile drops the stack's resources |
| `stack.destroy` | before any worktree, port or record is removed |
| `service.start` | after a service and its dependencies are triggered |
| `service.stop` | after a service is disabled |
| `workspace.up` | after the daemon is up and the workspace's services are folded in |
| `workspace.down` | before the workspace's services are torn down |

Failure means opposite things in each direction. A setup hook that fails stops the rest and fails the command. A half-provisioned stack that looks healthy is worse than one that failed loudly. A teardown hook that fails is reported and skipped, and the teardown carries on. Otherwise one broken hook leaves you a worktree, a branch and a port you cannot reclaim. Override either with `onError: abort` or `onError: continue`, and bound a hook that talks to a slow API with `timeout: 90s`.

The lifecycle action is never rolled back. If a `stack.create` hook fails the stack still exists, unprovisioned, and the error says so. Fix the hook and run `devstack hooks run stack.create --stack <name>` rather than recreating it. `devstack hooks run` re-fires every hook on the event, including ones that already succeeded, so a hook that provisions external state should tolerate being run twice.

`stack.destroy` is the one failure with no retry, and it is the cost of guaranteeing a stack can always be removed. Removing the stack deletes the record its `${self...}` references resolve against, so by the time you read the failure there is nothing left to resolve. devstack prints the resolved URLs at the point of failure instead:

```
warning: hook "auth0-cleanup/api" failed on stack.destroy, continuing: exit status 1
  whatever "auth0-cleanup/api" was cleaning up outside this machine is probably still there.
  this CANNOT be retried: removing the stack deletes the record that ${self...} resolves against.
  clean it up by hand. Stack "login-fix" was serving:
    api                      http://localhost:20016
```

Agents get the same behaviour. `stack_create`, `stack_up`, `stack_down`, `stack_rm`, `start` and `stop` fire their events over [MCP](#mcp) and report what ran. The `hooks` tool lists and re-runs them.

## Observability

Opt-in per workspace. While it's off, no collector runs and nothing is injected into services.

```bash
devstack otel config on [--backend=openobserve|signoz|forwarding]   # writes config, starts nothing
devstack otel config off                                           # writes config, stops nothing
devstack otel start                                                # runs the collector now
devstack otel stop                                                 # kills it now
```

With it on, `workspace up` starts one `otelcol-contrib` and one backend for the whole machine — shared by every workspace, the same way one daemon runs everyone's services. `OTEL_EXPORTER_OTLP_ENDPOINT` is pushed down to every service (gRPC 4317, HTTP 4318), so you never repeat it. If the collector dies while enabled, `devstack status` warns and tries to restart it.

The collector needs `otelcol-contrib` on `$PATH`, or `OTELCOL_BIN` pointing at it. Without one the workspace still comes up, and `devstack otel start` fails until you install it from [opentelemetry-collector-releases](https://github.com/open-telemetry/opentelemetry-collector-releases/releases).

OpenObserve is the default backend: one container, ~230 MB idle, UI on `localhost:5080`. SigNoz is available and far heavier, ClickHouse plus Zookeeper plus UI at ~1.5-2 GB idle. To ship to your own OTLP endpoint instead of storing locally:

```bash
devstack otel config set --plugin=forwarding --set upstream=telemetry.example.com:443 --set protocol=grpc
devstack otel config set --plugin=forwarding --set upstream=https://otel.example.com:4318
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

Every instance reports to that one backend, stamped with `devstack.workspace`, `devstack.service`, `devstack.stack` and `devstack.env`. These are namespaced deliberately: `deployment.environment` belongs to whoever owns the destination, and the `forwarding` backend sets it per workspace. So you run one backend and slice at query time. A service often reports itself under a different name than devstack knows it by; filters match either, and `otel services` prints both:

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
devstack tunnel push --service navexa-api,navexa-frontend
devstack tunnel push --stacks                          # every stack that is up, each on its own port
devstack tunnel push --as-base agent                   # one stack, on the ports base normally serves
devstack tunnel push --otel                            # also the observability UI
devstack tunnel pull <host>
devstack tunnel status                                 # what is forwarded right now
devstack tunnel status --planned [--stacks]            # what a push would forward, no SSH
devstack tunnel check <host>                           # what already holds those ports on the far end
devstack tunnel stop [--service navexa-api]
devstack tunnel restart [--mode push|pull]
```

`--service` takes exact names, the ones `tunnel status --planned` prints, not partial matches. Repeat it or comma-separate it. Only ports actually serving traffic get forwarded. With `--otel` the remote reads this machine's traces at the address you use locally.

`--stacks` and `--as-base` do different jobs and can't be combined. `--stacks` gives every stack its own port on the far end. `--as-base <name>` puts one stack where base lives, so the far end reaches it at the address it already knows and nothing over there needs reconfiguring:

```
devstack tunnel push my-box.ts.net --as-base agent
# far end :4200 → here :20006, far end :63290 → here :20005
```

`stop` and `status` read the forwards actually running, not what discovery covers now. So a port whose service has since gone is still reported and still torn down, and so is the observability UI, which was never a Tilt resource.

`restart` repeats the last push or pull — same direction, same services, same stack mapping — and says what it's repeating. Otherwise it rebuilds from the defaults — base back on the ports a mapped stack was serving, and a push on the machine you ran `pull` from. Any flag you pass overrides the saved one.

`--reclaim` kills whatever already holds those ports on the far host, and only the ports being forwarded, so `--service` narrows the blast radius. What it kills may belong to a colleague or another stack. See whose it is first with `devstack tunnel check <host>`.

## MCP

`devstack serve` is the MCP server. `.mcp.json` invokes it over stdio; you don't run it yourself.

```
environment  status  topology  start  stop  restart  process_logs  service_env
configure    observability     investigate    tunnel      hooks
stack_create stack_up  stack_down  stack_list  stack_rm  stack_note
env_use      env_which env_set
```

The set adapts to the workspace: `investigate` appears only when observability is enabled, `tunnel` only when there's an ssh client. Call `environment` first. It reports the workspace's observability state, its in-flight stacks, and which tools actually exist here.

`investigate` is the trace tool, and it has three modes. Give it a `trace_id` for one full span tree. Give it `attribute` and `value` to find every root span where, say, `portfolio.id=57835`, then expand each trace. Give it neither and you get the most recent executions. `stack` scopes it: absent means base, a name means that stack, `"all"` means every instance. When OTEL logs aren't available it falls back to dev-daemon process logs. Matching root spans only means each result is a distinct trace entry point, whichever service owns the root.

`service_env` resolves and edits a service's env:

| | |
|---|---|
| `get` | each key, with the rung it came from |
| `diff` | compare two services side by side |
| `set` | write to the manifest or `.envrc` |
| `check` | audit for placeholders and missing keys |
| `drift` | what's resolved vs what the repo says it needs |

Six things have no tool and need a shell: `workspace up`, `workspace down`, `workspace doctor`, `stack config`, `ports`, and `init`. The otel queries are shell-only too — `otel traces`, `otel logs`, `otel services`, `otel open` — beyond what `observability` and `investigate` cover.

## Briefing an agent

`devstack prime` prints what an agent needs to work here, resolved when it runs — where you are, which copy of the service your checkout is, what's running, what each environment is for.

The binary generates it, so `go install` updates every workspace at once. There's no committed file to regenerate, and none to go stale.

```
## WHERE YOU ARE
workspace navexa · service navexa-api · stack nvxa-1422
  purpose NVXA-1422 wrong Holdings.Name: arbitrary Companies match at import
  Your changes here go on the branch of this stack, not on base.

## THIS SERVICE — navexa-api
runs as 5 copies:
    base         :63290   running   branch master
      /home/nick/dev/navexa/Navexa
  ▸ nvxa-1422    :20012   running   branch nick/nvxa-1422-wrong-company-name…
      /home/nick/dev/.devstack-stacks/nvxa-1422/navexa-api
  The marker ▸ shows the copy that you are in now: nvxa-1422.
```

It also works out which stack a session is probably for, and stays quiet when it can't tell. Those are two different questions: where you are comes from the filesystem and is marked `▸`. What you're here for is inference, and is marked `?`.

Wire it into Claude Code so a session is briefed without being asked:

```bash
devstack init --all --claude-hook
```

That writes a `SessionStart` hook running `devstack prime --json` for the `startup`, `resume`, `clear` and `compact` matchers. The last one matters most: compaction is exactly when the landscape drops out of context. It merges into an existing `.claude/settings.json` rather than replacing it, keeps hooks you already have, and adds nothing on a second run.

`.claude/settings.json` is committed, so the hook runs for everyone who clones the repo. That's why the flag is opt-in. `devstack init --all` on its own refreshes `AGENTS.md` and `.mcp.json`, and writes no hook.

`init --all` also cleans up after older versions. It drops a duplicate generated block and strips a legacy unsentinelled devstack section. Everything outside the sentinels is left alone.

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
| `DEVSTACK_DAEMON_HOST` | `localhost` | Host daemon host. Legacy alias: `TILT_HOST` |
| `OTELCOL_BIN` | found on `$PATH` | Path to the `otelcol-contrib` binary |

The daemon's port is not configurable. One daemon serves the machine on `:10300`, and every command reaches it there.

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

Because `devstack.service.yaml` is machine-local and gitignored, a stack's worktree doesn't inherit one. That's why `devstack stack create` copies ignored config into each worktree — git will not carry it there.

### Secrets and `devstack env set`

`env set <env> KEY=VALUE` writes the value into `devstack.workspace.yaml` in plaintext, and masking happens on display only, never at rest. So it depends on how you treat that file.

If you commit it, which is the default recommended above, keep real secrets out. Declare them in a service's `env.required` and supply them at runtime from `.envrc` or your own secret store. Use `env set` only for URLs, ports and feature flags. A secret written here is a committed secret.

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

Two of those bite. A stale binary doesn't fail on config it doesn't understand. It falls back — an older devstack read a workspace set to OpenObserve and quietly started SigNoz. And MCP tool descriptions are read once at server startup, so a session already running keeps the old tool list. New tools don't appear, new parameters get rejected. Restart it.

`devstack init --all` writes files only, nothing restarts. Run it from inside each workspace, and expect a git diff in every service repo.

If a workspace was mid-upgrade, `devstack otel start` replaces the observability container when its pinned image has moved.
