<img width="1672" height="941" alt="ChatGPT Image Jul 22, 2026, 03_59_46 PM" src="https://github.com/user-attachments/assets/7b9c8b3e-5f8f-4298-9870-60073ee4f2db" />

Devstack runs the local services of your team in dependency order, across every repository. Devstack gives the same controls to Claude Code over [MCP](https://modelcontextprotocol.io).

Devstack runs on [Tilt](https://tilt.dev). One daemon serves the whole machine. One OpenTelemetry backend serves every workspace. An agent can start a service, read the logs of that service, and read the trace that shows the failure. You paste no output.

## Install

```bash
go install github.com/socialviolation/devstack@main
```

devstack needs Go 1.25+ and [Tilt](https://docs.tilt.dev/install.html) on `$PATH`. If you turn on local observability, devstack also needs Docker.

## Quick start

```bash
devstack workspace add ~/dev/my-workspace   # register the directory holding your services
devstack workspace up                       # start the dev daemon (and the collector, if enabled)
devstack init --all --claude-hook           # wire up every service repo for agents
devstack status                             # what runs, on which port, pointing where
```

`devstack workspace up` also builds the replica that base runs. Read [Base and the replica](#base-and-the-replica).

Then open Claude Code in any service repository. Claude Code reads `.mcp.json` and gets the devstack tools. The [session hook](#briefing-an-agent) briefs the agent on where it is and on what runs.

The `--claude-hook` flag writes a hook into a committed file, so the hook runs for everyone who clones the repository. If you do not want that, leave the flag out.

To register a service that the workspace manifest does not hold yet, run this command:

```bash
devstack init --name=api --path=~/dev/my-workspace/api --cmd="go run ." --port=8080
```

devstack detects the language from `go.mod`, `package.json`, `requirements.txt` or `*.csproj`. To name the language yourself, pass `--language`.

## Concepts

| Term | Meaning |
|------|---------|
| Workspace | A directory with a `devstack.workspace.yaml` manifest. It groups one or more services. |
| Service | A process that a `devstack.service.yaml` manifest defines: an API, a worker, an importer. |
| Group | A named set of services that you start and stop together. |
| Host daemon | One Tilt daemon (`:10300`) for the whole machine. The services of every workspace run inside it, and so does the overlay of every stack that is up. Each one runs as `<workspace>:<service>[:<stack>]`. There is no daemon for each workspace. |
| Template | Your own checkout of a service. It holds the git objects, the workspace manifest and the machine-local gitignored configuration. It runs nothing. |
| Replica | One git worktree for each service, under `<parent>/.devstack-base/<workspace>/<service>`. devstack builds the replica from the template. Each worktree is detached at the default branch tip of its repository. |
| Base | The workspace that runs with no stack. Base runs from the replica, and never from your checkout. Spell it literally as `base` where a command takes a stack name. |
| Copy | One running process of a service. Base runs one copy of a service. Each stack that overlays that service runs another copy. |
| Overlay | The set of services that a stack runs its own copies of. Every other service resolves to the copy of base. |
| Feature stack | A parallel version of one or more services. devstack runs it from a git worktree on a feature branch, on its own port, beside base. It reuses base for everything that it does not change. |
| Environment | A named set of configuration values (`environments:` in the workspace manifest). devstack applies it at workspace scope, service scope or stack scope. It sets where a service points. |
| Hook | A shell command that devstack runs on a lifecycle event, for example a stack created or a service started. A disposable stack uses hooks to provision and de-provision the state that it needs outside this machine. |

## Base and the replica

`devstack workspace up` builds one git worktree for each service, under `<parent>/.devstack-base/<workspace>/<service>`. devstack detaches each worktree at the default branch tip of its repository. The daemon runs those worktrees. Together they are the replica.

Your checkout is the template. It holds the git objects, the workspace manifest and the machine-local gitignored configuration. The template runs nothing.

```bash
devstack base path            # the replica root: where base runs
devstack base path <service>  # the replica worktree of one service
devstack base sync            # move base to the current default branch tip
```

The MCP `base` tool does both jobs. Pass `action="path"` to read a path, or `action="sync"` to move base.

CAUTION: `devstack base sync` restarts nothing. A running copy keeps serving the old code. It serves the old code until somebody restarts it.

An edit in your checkout does not reach base on its own. The edit reaches base after two things happen. First, the edit is on the default branch. Second, you run `devstack base sync`. To see a change run at once, put the change in a stack.

### Naming the copy

A command that starts, stops or restarts a copy must name the copy. There is no default. Name the copy in one of three ways:

- In a shell, pass `--stack <name>` or `--stack base`.
- Over MCP, pass `stack="<name>"` or `stack="base"`.
- Run the command from a working directory inside a stack worktree, or inside the replica.

If you pass no flag from a directory that is neither, the command refuses. devstack does not guess. This rule does not apply to a read-only command. A read-only command answers with no flag.

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

`devstack ws` is an alias for `devstack workspace`. devstack writes the registration to `~/.config/devstack/workspaces.json`, one time for each machine.

## Services

```bash
devstack service start   [name] --stack <name>     # and its dependencies
devstack service restart [name] --stack <name>     # the target alone
devstack service stop    [name] --stack <name>
devstack group   start|stop|restart <group>       # every service in the group
devstack status  [--all] [--stack <name>]
devstack workspace topology [service]
```

Commands are noun first: `devstack <noun> <action> [target]`. A name can be both a service and a group, so the noun says which one you mean. devstack never guesses. If you name nothing after `service`, devstack works out the service from your working directory.

`start` resolves dependencies and brings them up first. If the dev daemon is down, `start` also boots it. `restart` acts on the target alone. `status` collapses the groups and stacks that run nothing. To see them, pass `--all`.

Each of `start`, `stop` and `restart` changes what is running, so each one must name the copy. Read [Naming the copy](#naming-the-copy).

The states are `running`, `starting`, `building`, `stopped`, `erroring`, `disabled`, `down` and `unknown`. `stopped` means registered but not started. That is not the same as broken. `down` means that the copy is not registered in the daemon, because its stack is down.

If the last process of a service still holds its port, the service cannot start. The service can reclaim that port. You do not name the port:

```yaml
runtime:
  prep:
    freePorts: true              # every port this service declares
    # freePorts: [http, grpc]    # or just some
    command: "dotnet build ..."  # your own prep still runs, after
```

devstack resolves those ports as it resolves `${self.port.http}`. From that one line, base frees the port that it pins, and a stack frees the port that devstack allocated to it.

CAUTION: Never write a port as a literal in `freePorts`. The worktree of a stack copies the literal. Then `fuser -k 63290/tcp` in a stack kills base.

A copy can free only the ports that it owns. devstack names the process before it kills the process. devstack sends `SIGTERM` before `SIGKILL`, so a dev server stops its own child processes instead of orphaning them.

```bash
devstack ports check 4200      # what holds a port, IPv4 or IPv6
devstack ports free 4200       # kill it (it refuses any port less than 1024)
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

A feature stack runs a parallel version of a few services beside base. Each changed service gets a git worktree on a feature branch, and a port that devstack allocates. Both fold into the one host daemon as `<workspace>:<service>:<stack>`. Every service that the stack does not change resolves to the copy of base. So several features can run at the same time, and you copy nothing that you did not change.

```bash
devstack stack create <name> --repos <svc>[,<svc>]  # worktrees for the changed services and their callers
devstack stack up <name>                            # fold it into the host daemon
devstack stack down <name>                          # stop it, keep the worktrees
devstack stack status <name>                        # its copies: state, ports, environment
devstack stack list
devstack stack rm <name>                            # remove worktrees, release ports, deregister
devstack stack config <svc> --stack <name>          # the configuration that copy runs with
devstack stack note <name> [text]                   # what this stack is for
devstack stack note <name> --add "..."              # append where it got to
```

`devstack stack create` cuts each worktree from the default branch of the repository, as origin holds that branch where a remote exists. It never cuts from the branch that your checkout has checked out. `--from <ref>` names a different ref. If the branch already exists, devstack attaches to it with the history that it already has, and `--from` does not apply to it.

`--repos` also takes a group name. devstack expands the group to its members, and records the group on the stack. `devstack stack list` then reports how much of that group the stack covers. A group action against a stack reaches only the members that the stack overlays, and devstack names the members that stay on base.

`devstack stack add` puts a service into a stack that already exists:

```bash
devstack stack add <name> <service|group> [service|group...]
```

`stack add` stops nothing and starts nothing. The stack stays exactly as up or as down as it was. If the stack is up, the added copies become resources in the host daemon, but devstack does not start them. Start each one with `devstack service start <service> --stack <name>`.

To work on a stack, change directory into its worktree. `stack create` and `stack list` print the path. That worktree is already on the branch of the stack, so you never run `git checkout`. To reload that one copy, run `service restart --stack <name>`. That command leaves base and every other stack untouched.

`create` also takes `--branch` and `--note`. `--branch` defaults to the stack name. If that branch exists, devstack attaches to it. A branch says what changed. A note says why. A week later, the note is the part that you cannot reconstruct.

`--add` appends a dated entry, and does not replace the note. A stack that you left half-done then says where the work got to. devstack keeps the last 5 entries, each of 200 characters at most. If the new entry repeats the last one, devstack changes nothing. A log that records every step buries the line that is worth reading.

The purpose and the newest entry go into the session briefing. An agent that picks the stack up reads both before it touches anything.

```
$ devstack stack note perf
NAV-412 daily value spike
  2026-08-01  repro on dev2, recalc is 40s
  2026-08-03  traced to the FX join; parked pending a prod diff
```

> WARNING: Shared state is not isolated. A stack isolates code and ports. A stack does not isolate the database, the queues or the caches. Two stacks on one database read and write the same rows. If that matters, point one stack at another database with an [environment](#environments).

The original design record is [docs/stacks-spec.md](docs/stacks-spec.md). Read it for the model of reuse and ports. Its daemon topology is older than the change to one daemon for each host.

## Environments

An environment is a named set of configuration values in the workspace manifest. It holds database URLs, feature flags and endpoints. You repoint a service with an environment, and you touch no code. Base can run against `local` while one stack runs against `staging`.

You define an environment one time, in the workspace manifest of base, and every stack inherits it. A stack defines no environment of its own. `env use --stack <name>` points the stack at an environment that base defines. There are three scopes: stack, service and workspace. The most specific scope wins.

An environment carries a description that says what the environment is for. The name and the key list say what it sets. Only the description says why you pick it, or why the choice is dangerous:

```yaml
environments:
  fx-prod:
    description: Exchange Rate PoC against the PRODUCTION database. Read-only comparison.
    values:
      SqlConnectionString: ...
```

devstack shows the description in `env list`, in `env show` and in the session briefing. So the warning travels to every place that names the environment.

```bash
devstack env set <name> KEY=VALUE [KEY=VALUE ...]    # creates the environment if it is new
devstack env use <name> [--service <svc>] [--stack <name>]
devstack env which [--service <svc>] [--stack <name>]   # what a service resolves to, key by key
devstack env show <name>
devstack env list
devstack env remove <name>
```

`env which` names the rung that each value came from. The rungs are, in order: `.envrc`, `env.files`, manifest `env.values`, the active environment, then the values that devstack computes. Each rung overrides the rung before it. `--shadowed` lists the values that a later rung overrode.

devstack redacts credentials in place, so a connection string still shows its server and its database. `--reveal` prints them in the clear. The redaction changes the output only, and never the file. Before you put a secret in an environment, read [what to commit](#files-and-what-to-commit).

## Hooks

A hook is a shell command that devstack runs when a lifecycle event fires. A stack is disposable. That is true only if the state that a stack needs beyond this machine arrives with it and goes with it.

```bash
devstack hooks list [--stack <name>] [--event <event>]
devstack hooks run <event> [--stack <name>] [--services a,b]
```

`hooks run` fires the hooks of an event, and does not do the lifecycle action. Use it to test one hook, and to retry a hook after a failure.

Declare hooks in the workspace manifest, which the team shares:

```yaml
hooks:
  - name: auth0-callbacks
    on: [stack.up]
    services: [navexa-frontend]
    run: ./scripts/auth0.sh add
    env:
      CALLBACK_URL: ${self.url}/callback
```

devstack allocates the ports of a stack when it creates the stack, so a hook cannot hold a port as a literal. `${self.url}`, `${self.port.http}` and `${<service>.port.<key>}` resolve against the ports that the copy really got. The command also receives `DEVSTACK_*` variables, among them `DEVSTACK_STACK`, `DEVSTACK_SERVICE_URL` and `DEVSTACK_ENV`. The command receives the whole event as JSON on stdin.

`services:` scopes a hook to the services that it names. The hook then runs one time for each service, with that service as `${self}`. If you leave `services:` out, the hook runs one time for the event. A `devstack.service.yaml` file can declare hooks too. Those hooks are scoped to that service, and devstack rejects a `services:` key in them. A feature stack inherits the hooks of the workspace, as it inherits the environments.

| Event | Fires |
|---|---|
| `stack.create` | After the worktrees exist, devstack allocates the ports and writes the record |
| `stack.up` | After devstack triggers the services of the stack in the host daemon |
| `stack.down` | Before the host Tiltfile drops the resources of the stack |
| `stack.destroy` | Before devstack removes any worktree, port or record |
| `service.start` | After devstack triggers a service and its dependencies |
| `service.stop` | After devstack disables a service |
| `workspace.up` | After the daemon is up and devstack folds in the services of the workspace |
| `workspace.down` | Before devstack tears down the services of the workspace |

A failure means opposite things in the two directions.

If a setup hook fails, devstack stops the other hooks and fails the command. A half-provisioned stack that looks healthy is worse than a stack that failed loudly.

If a teardown hook fails, devstack reports the failure, skips that hook and continues the teardown. Without that rule, one broken hook leaves you a worktree, a branch and a port that you cannot reclaim.

To change either rule, set `onError: abort` or `onError: continue`. To bound a hook that calls a slow API, set `timeout: 90s`.

devstack never rolls the lifecycle action back. If a `stack.create` hook fails, the stack still exists and is not provisioned, and the error says so. Fix the hook, then run `devstack hooks run stack.create --stack <name>`. Do not create the stack again.

`devstack hooks run` fires every hook on the event again, including the hooks that already succeeded. So a hook that provisions external state must tolerate two runs.

CAUTION: You cannot retry a failed `stack.destroy` hook. Removing the stack deletes the record that its `${self...}` references resolve against. By the time that you read the failure, there is nothing left to resolve. This is the cost of one guarantee: devstack can always remove a stack. devstack prints the resolved URLs at the point of failure instead:

```
warning: hook "auth0-cleanup/api" (devstack.workspace.yaml) failed on stack.destroy. devstack continues: exit status 1
  "auth0-cleanup/api" cleans up state outside this machine. That state is probably still there.
  devstack can NOT retry this hook. When devstack removes the stack, it deletes the record that ${self...} resolves against.
  remove the state by hand. Stack "login-fix" served:
    api                      http://localhost:20016
```

Agents get the same behavior. `stack_create`, `stack_add`, `stack_up`, `stack_down`, `stack_rm`, `start` and `stop` fire their events over [MCP](#mcp), and report what ran. The `hooks` tool lists the hooks and runs them again.

## Observability

Observability is opt-in for each workspace. While it is off, no collector runs, and devstack injects nothing into the services.

```bash
devstack otel config on [--backend=openobserve|signoz|forwarding]   # writes config, starts nothing
devstack otel config off                                           # writes config, stops nothing
devstack otel start                                                # runs the collector now
devstack otel stop                                                 # kills it now
```

When observability is on, `workspace up` starts one `otelcol-contrib` and one backend for the whole machine. Every workspace shares them, as every workspace shares the one daemon. devstack pushes `OTEL_EXPORTER_OTLP_ENDPOINT` down to every service (gRPC 4317, HTTP 4318), so you never repeat it. If the collector stops while observability is on, `devstack status` warns you and tries to start the collector again.

The collector needs `otelcol-contrib` on `$PATH`, or `OTELCOL_BIN` set to its path. Without it the workspace still comes up, and `devstack otel start` fails. Install the collector from [opentelemetry-collector-releases](https://github.com/open-telemetry/opentelemetry-collector-releases/releases).

OpenObserve is the default backend. It is one container, it uses about 230 MB when idle, and its UI is on `localhost:5080`. SigNoz is available too and is much heavier: ClickHouse, Zookeeper and a UI, at about 1.5-2 GB when idle. To send telemetry to your own OTLP endpoint, and to store nothing locally, run one of these commands:

```bash
devstack otel config set --plugin=forwarding --set upstream=telemetry.example.com:443 --set protocol=grpc
devstack otel config set --plugin=forwarding --set upstream=https://otel.example.com:4318
```

devstack writes the plugin choice to the workspace manifest, and the choice travels with the project. So two workspaces can differ. When they differ, the one collector routes the telemetry of each workspace to its own backend, on the `devstack.workspace` attribute. To override this for one developer, set `OTEL_EXPORTER_OTLP_ENDPOINT` in the `.envrc` file of a service repository.

A query names no backend, no URL and no credential. devstack resolves the backend of the workspace. The `investigate` MCP tool uses the same resolution:

```bash
devstack otel services               # which copies report telemetry
devstack otel traces                 # recent root spans
devstack otel traces --service=api --since=15m
devstack otel traces --stack=feat-x
devstack otel traces <trace-id>      # full span tree
devstack otel logs --trace=<trace-id>
devstack otel status                 # collector state, ports, per-service evidence
devstack otel open                   # the UI
devstack otel plugins                # backends and their config keys
```

Every copy reports to that one backend. devstack stamps each report with `devstack.workspace`, `devstack.service`, `devstack.stack` and `devstack.env`. These attributes carry the `devstack` prefix on purpose. `deployment.environment` belongs to the owner of the destination, and the `forwarding` backend sets it for each workspace. So you run one backend, and you slice the data at query time.

A service often reports itself under a name that devstack does not know it by. A filter matches either name, and `otel services` prints both:

```
Navexa.API      (devstack: navexa-api)  stack=agent env=dev
Navexa.API      (devstack: navexa-api)  stack=base  env=dev
nxTradeImporter                         stack=base  env=dev
```

## Tunnels

A tunnel forwards the service ports of this workspace over SSH. Any host that you can reach with ssh works, including a plain alias in your ssh configuration. A tailnet address is one such host, and is not a requirement. devstack supports key-based authentication only. Run `push` on the machine where the services run. Run `pull` on the machine that you want to reach them from.

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

`--service` takes exact names, the names that `tunnel status --planned` prints. It matches no partial name. Repeat the flag, or give a comma-separated list. devstack forwards only the ports that serve traffic. With `--otel`, the remote host reads the traces of this machine at the address that you use locally.

`--stacks` and `--as-base` do different jobs, and you cannot combine them. `--stacks` gives every stack its own port on the far end. `--as-base <name>` puts one stack on the ports where base lives. The far end then reaches that stack at the address that it already knows, and you configure nothing on the far end:

```
devstack tunnel push my-box.ts.net --as-base agent
# far end :4200 → here :20006, far end :63290 → here :20005
```

`stop` and `status` read the forwards that really run, and not what discovery covers now. So devstack still reports and still stops a port whose service went away. devstack does the same for the observability UI, which was never a Tilt resource.

`restart` repeats the last push or pull: the same direction, the same services and the same stack mapping. It says what it repeats. If devstack saved no push or pull, `restart` rebuilds from the defaults. Base returns to the ports that a mapped stack was serving, and a push runs on the machine that you ran `pull` from. Any flag that you pass overrides the saved one.

CAUTION: `--reclaim` kills what already holds those ports on the far host. What it kills can belong to a colleague, or to another stack. `--reclaim` touches only the ports that devstack forwards, so `--service` narrows the blast radius. Before you pass it, run `devstack tunnel check <host>` to see who holds those ports.

## MCP

`devstack serve` is the MCP server. `.mcp.json` starts it over stdio. You do not run it yourself.

```
environment  status  topology  start  stop  restart  process_logs  service_env
configure    observability     investigate    tunnel      hooks       base
stack_create stack_add stack_up  stack_down  stack_list  stack_rm  stack_note
env_use      env_which env_set
```

`base` reads the path of the replica with `action="path"`, and moves base to the default branch tip with `action="sync"`. `stack_add` adds services to a stack that already exists.

The set of tools adapts to the workspace. `investigate` appears only when observability is on. `tunnel` appears only when the machine has an ssh client. Call `environment` first. It reports the observability state of the workspace, the stacks in flight, and the tools that exist here.

`investigate` is the trace tool, and it has three modes:

- Give it a `trace_id`, and it returns one full span tree.
- Give it `attribute` and `value`, and it finds every root span with that value, for example `portfolio.id=57835`. It then expands each trace.
- Give it neither, and it returns the most recent executions.

The `stack` parameter scopes the search. An absent `stack` searches base. A name searches that stack. `"all"` searches every copy. When OTEL logs are not available, `investigate` reads the process logs of the dev daemon instead. `investigate` matches root spans only, so each result is a separate entry point of a trace, whichever service owns the root.

`service_env` resolves and edits the environment of a service:

| | |
|---|---|
| `get` | Show each key, with the rung it came from |
| `diff` | Compare two services side by side |
| `set` | Write to the manifest or to `.envrc` |
| `check` | Audit for placeholders and missing keys |
| `drift` | Compare what devstack resolves with what the repository says it needs |

Some commands have no tool and need a shell. Among them: `workspace up`, `workspace down`, `workspace doctor`, `workspace generate`, `stack config`, `ports`, `init`, `migrate`, `prime` and `upgrade`. The otel queries need a shell too: `otel traces`, `otel logs`, `otel services` and `otel open`. The `observability` and `investigate` tools cover the rest.

## Briefing an agent

`devstack prime` prints what an agent needs to work here, and resolves it when the command runs. It prints:

- where you are
- every copy of the service, and the directory that each copy runs from
- what is running
- what each environment is for

The binary generates the briefing, so `go install` updates every workspace at one time. There is no committed file to regenerate, and no file that goes stale.

```
## WHERE YOU ARE
workspace navexa · service navexa-api · stack nvxa-1422
  purpose NVXA-1422 wrong Holdings.Name: arbitrary Companies match at import
  Your changes here go on the branch of this stack, not on base.

## THIS SERVICE — navexa-api
runs as 5 copies:
    base         :63290   running   branch (detached at master)
      /home/nick/dev/.devstack-base/navexa/navexa-api
  ▸ nvxa-1422    :20012   running   branch nick/nvxa-1422-wrong-company-name…
      /home/nick/dev/.devstack-stacks/nvxa-1422/navexa-api
  The marker ▸ shows the copy that you are in now: nvxa-1422.
  The directory under each copy is the directory that copy RUNS. base runs the replica, not your checkout.
```

`prime` also works out which stack a session is probably for. When `prime` cannot tell, it stays quiet. These are two different questions. Where you are comes from the filesystem, and `prime` marks it `▸`. What you are here for is a guess, and `prime` marks it `?`.

To brief a session without a request, wire `prime` into Claude Code:

```bash
devstack init --all --claude-hook
```

That command writes a `SessionStart` hook that runs `devstack prime --json`. The hook covers the `startup`, `resume`, `clear`, `compact` and `fork` matchers. `compact` matters most, because compaction is when the landscape drops out of context. devstack merges the hook into an existing `.claude/settings.json` and does not replace the file. It keeps the hooks that you already have, and it adds nothing on a second run.

You commit `.claude/settings.json`, so the hook runs for everyone who clones the repository. That is why the flag is opt-in. `devstack init --all` on its own refreshes `.mcp.json`, and writes no hook.

## devstack writes no instructions into your repositories

devstack once wrote a block of instructions into `AGENTS.md`, and a shorter block into `CLAUDE.md` and the files beside it. It writes neither now. `devstack prime` prints the same facts at each session start, so the agent reads them from the binary. A committed copy of a live fact goes stale, and a stale fact reads exactly like a true one.

`devstack migrate` removes what an older devstack left behind. It sweeps every workspace on this machine: the workspace root, each service repository, and the worktree of each feature stack. In each directory it removes the devstack block, it deletes a file that held devstack content only, it writes `.mcp.json`, and it wires the SessionStart hook.

```bash
devstack migrate
```

devstack removes only what devstack wrote. Your own text stays, byte for byte. Where devstack can not find the end of its own block, it changes nothing and it names the file for you. Run the command again at any time: a second run changes nothing.

devstack does not own these repositories, so read the diff in each one before you commit it.

## Per-repo setup

`devstack init` writes one `.mcp.json` file for each service repository. The file names the default service and nothing else. So the file does not depend on the machine, and you can commit it:

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

If an older file still carries `DEVSTACK_WORKSPACE` or `TILT_PORT`, refresh it with `devstack migrate`.

## Configuration

| Variable | Default | Purpose |
|----------|---------|---------|
| `DEVSTACK_WORKSPACE` | auto-detected from cwd | Workspace name or path |
| `DEVSTACK_DEFAULT_SERVICE` | — | Service used when a command or tool is given no name |
| `DEVSTACK_DAEMON_HOST` | `localhost` | Host daemon host. Legacy alias: `TILT_HOST` |
| `OTELCOL_BIN` | found on `$PATH` | Path to the `otelcol-contrib` binary |

You cannot configure the port of the daemon. One daemon serves the machine on `:10300`, and every command reaches the daemon there.

## Files, and what to commit

| Artifact | Location | Commit? | Why |
|----------|----------|---------|-----|
| `devstack.workspace.yaml` | workspace root | Yes | The source of truth: services, groups, dependencies and environments. It is portable and holds no machine paths. |
| `devstack.service.yaml` | service repo | No | Machine-local: it holds absolute tool paths. Gitignore it. |
| `.mcp.json` | service repo | Yes | Generated, and it does not depend on the machine. |
| `AGENTS.md` | service repo | Yes | Yours. devstack writes nothing in it. `devstack migrate` removes the block an older devstack wrote. |
| `CLAUDE.md` / `GEMINI.md` / `.cursorrules` / `.github/copilot-instructions.md` | service repo | Yes | Yours. devstack writes nothing in them, and it creates none of them. |
| `.devstack.json` | workspace root | No | Retired. The otel plugin configuration now lives under `observability` in the workspace manifest. Delete this file after `devstack otel status` shows what you expect. |
| `Tiltfile` | anywhere | No | A generated build artifact. Never edit it by hand. |
| `~/.config/devstack/workspaces.json` | home | n/a | Machine-local registry of workspaces and their ports. |
| `~/.local/share/devstack/**` | home | n/a | Host daemon state: pids, logs, `stacks.json` and the generated host Tiltfile. |

A `.gitignore` for each service repository:

```
devstack.service.yaml
.devstack.json
Tiltfile
```

`devstack.service.yaml` is machine-local and gitignored, so the worktree of a stack does not inherit one. For that reason `devstack stack create` copies the ignored configuration into each worktree. git does not carry it there. `devstack workspace up` and `devstack base sync` copy the same files into each replica worktree.

### Secrets and `devstack env set`

CAUTION: `env set <env> KEY=VALUE` writes the value into `devstack.workspace.yaml` as plaintext. devstack masks the value when it shows the value, and never on disk. What that means for you depends on how you treat the file.

If you commit the file, which the table above recommends, keep real secrets out of it. Declare each secret in the `env.required` list of a service. Supply the secret at runtime from `.envrc`, or from your own secret store. Use `env set` for URLs, ports and feature flags only. A secret written into a committed file is a committed secret.

If the file is machine-local, `env set` is safe for API keys. The file is machine-local when you gitignore it, or when the workspace root is not a repository. devstack built `env set` for that case. The file is still plaintext on disk.

To find out which case you are in, run `git check-ignore -v devstack.workspace.yaml` in the workspace root.

## Updating devstack

```bash
cd <devstack repo> && git pull
go install ./...              # replace the binary on your PATH
devstack migrate              # remove the instructions an older devstack wrote
                              # then restart your MCP server / agent session
devstack status               # make sure that the daemon and the services still answer
```

`devstack upgrade` runs the first step for you. It installs the current devstack, then counts the files that still hold instructions an older devstack wrote. `devstack upgrade --migrate` runs `devstack migrate` too.

Two of those steps carry a risk.

CAUTION: A stale binary does not fail on configuration that it does not understand. It falls back without a word. One older devstack read a workspace set to OpenObserve, and started SigNoz.

CAUTION: The MCP server reads its tool descriptions one time, at startup. A session that already runs keeps the old tool list. New tools do not appear, and the server rejects new parameters. Restart the MCP server.

`devstack migrate` writes files only, and restarts nothing. Expect a git diff in every service repository.

If a workspace is part-way through an upgrade, `devstack otel start` replaces the observability container when its pinned image moved.
