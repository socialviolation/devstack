# devstack reference

Every command, flag and file that devstack reads or writes. To learn what
devstack is, and how to install it, read the [README](README.md).

- [The shared set and the replica](#the-shared-set-and-the-replica)
- [Workspaces](#workspaces)
- [Services](#services)
- [Feature stacks](#feature-stacks)
- [Environments](#environments)
- [Hooks](#hooks)
- [Observability](#observability)
- [Tunnels](#tunnels)
- [Addresses](#addresses)
- [The panel](#the-panel)
- [MCP](#mcp)
- [Briefing an agent](#briefing-an-agent)
- [devstack writes no instructions into your repositories](#devstack-writes-no-instructions-into-your-repositories)
- [Per-repo setup](#per-repo-setup)
- [Configuration](#configuration)
- [Files, and what to commit](#files-and-what-to-commit)
- [Updating devstack](#updating-devstack)

## The shared set and the replica

A shared service is a service in the `shared:` list of the workspace manifest. It runs once for the workspace, and never in a stack. Every stack uses it. Databases and stores are shared, because they are too heavy to run once for each stack. All the shared services of one workspace are the shared set.

```yaml
shared:
  - navexa-sql
  - azurite
```

Each name in `shared:` must be a declared service, or devstack refuses to load the manifest. A shared service keeps the ports that its manifest declares. devstack allocates no port for it. A shared service can not `need` a service that is not shared.

Every other service is a stack service. A stack service runs only inside a [feature stack](#feature-stacks). To run the default branch of a stack service, create a stack with `--detach`.

The shared set runs from the replica. The replica is one git worktree for each repository that holds a shared service, under `<parent>/.devstack-base/<workspace>/`. devstack detaches each worktree at the default branch tip of its repository.

Your checkout is the source that devstack cuts worktrees from. It holds the git objects, the workspace manifest and the machine-local gitignored configuration. The source runs nothing.

You maintain nothing here. `devstack workspace up` does four things, in this order:

1. It builds the replica, if the replica is absent.
2. It moves each worktree to the current default branch tip.
3. It restarts each shared service that runs now and that step 2 moved.
4. It starts each shared service that does not run.

CAUTION: Step 3 restarts services that serve right now. Every stack uses them.

The shared set stays up until `devstack workspace down`. Once an hour, the `devstack-sweep` timer runs the same refresh as `devstack shared sync` for each active workspace. If a fetch fails, devstack keeps the current commit and writes a warning. A failed fetch never stops a shared service.

```bash
devstack workspace up         # start the daemon and the shared set
devstack shared status        # each shared service and its replica commit
devstack shared sync          # move the replica to the default branch tip now
devstack shared path [svc]    # the replica root, or one shared service's worktree
devstack status --all         # the DIR column: the directory each copy runs from
```

The MCP `shared` tool has the same three actions: `action="status"`, `action="path"` and `action="sync"`. An agent can call only `action="path"`. `devstack base` is a hidden old name for `devstack shared`. It prints a warning.

Every stack reads and writes the same shared data. A migration that one stack runs changes the database for every stack.

### Naming the copy

A shared service has one copy. A stack service has one copy in each stack that runs it. A command that starts, stops or restarts a stack service acts on one copy. Name the copy in one of three ways:

- In a shell, pass `--stack <name>`.
- Over MCP, pass `stack="<name>"`.
- Run the command from a working directory inside a stack worktree. That directory names the copy.

A shared service needs no stack. Name it with no `--stack`. An agent can read the status and the logs of a shared service. An agent can not start, stop or restart one, because every stack uses it.

No stack can carry the name `shared` or `base`. Each command names the copy it changed, so read that line. A shared copy has no `:stack` suffix.

## Workspaces

```bash
devstack workspace            # list registered workspaces
devstack workspace add [path]
devstack workspace remove <name>
devstack workspace up         # start the daemon and the shared set
devstack workspace down       # stop the shared set and every stack; the daemon stops if nothing else needs it
devstack workspace doctor     # check manifests and topology integrity
devstack workspace topology   # the service graph: groups, needs, calls
devstack workspace generate   # rebuild the daemon's Tiltfile from the manifests
devstack workspace open       # dev daemon dashboard
```

`devstack ws` is an alias for `devstack workspace`. devstack writes the registration to `~/.config/devstack/workspaces.json`, one time for each machine.

## Services

```bash
devstack service start   [name] --stack <name>     # and the services it needs
devstack service restart [name] --stack <name>     # the target alone
devstack service stop    [name] --stack <name>
devstack group   start|stop|restart <group>       # every service in the group
devstack status  [--all] [--stack <name>]
devstack workspace topology [service]
devstack wiring [service] [--stack <name>]
```

Commands are noun first: `devstack <noun> <action> [target]`. A name can be both a service and a group, so the noun says which one you mean. devstack never guesses. If you name nothing after `service`, devstack works out the service from your working directory.

`start` brings up first the services that the target `needs`. If the dev daemon is down, `start` also boots it. `restart` acts on the target alone. `status` collapses the groups and stacks that run nothing. To see them, pass `--all`.

Each of `start`, `stop` and `restart` changes what is running, so each one acts on one copy. Read [Naming the copy](#naming-the-copy).

The states are `running`, `starting`, `building`, `stopped`, `erroring`, `disabled`, `down` and `unknown`. `stopped` means registered but not started. That is not the same as broken. `down` means that the copy is not registered in the daemon, because its stack is down.

If the last process of a service still holds its port, the service cannot start. The service can reclaim that port. You do not name the port:

```yaml
runtime:
  prep:
    freePorts: true              # every port this service declares
    # freePorts: [http, grpc]    # or just some
    command: "dotnet build ..."  # your own prep still runs, after
```

devstack resolves those ports as it resolves `${self.port.http}`. From that one line, a shared service frees the port that it declares, and a stack frees the port that devstack allocated to it.

CAUTION: Never write a port as a literal in `freePorts`. The worktree of a stack copies the literal. Then `fuser -k 63290/tcp` in a stack kills the process that serves the declared port.

A copy can free only the ports that it owns. devstack names the process before it kills the process. devstack sends `SIGTERM` before `SIGKILL`, so a dev server stops its own child processes instead of orphaning them.

```bash
devstack ports check 4200      # what holds a port, IPv4 or IPv6
devstack ports free 4200       # kill it (it refuses any port less than 1024)
```

On Linux with a systemd user session, each service runs in its own systemd scope, with its own memory limit. When a service passes its limit, the kernel kills that service alone. The terminal and the other services keep running. To set the limits, use `runtime.limits`:

```yaml
runtime:
  limits:
    memory: 6G     # MemoryMax. The default is 4G
    cpu: 200%      # CPUQuota. 100% is one core. With no value, there is no CPU limit
```

Write `memory` as a systemd size, such as `512M`, `4G` or `infinity`, and `cpu` as a percentage, such as `200%`. devstack refuses any other value when it loads the manifest. Swap is fixed at 1G for each service. Every scope is in `devstack.slice`, and `devstack workspace down` stops the slice with the daemon. To list the scopes, run `systemctl --user status devstack.slice`. On a host without a systemd user session (macOS, WSL without systemd, a container), the services run without scopes, and devstack ignores `runtime.limits` with one warning.

Groups, and the old workspace `dependencies:` map. The `dependencies` commands edit that map. devstack loads its entries as `calls` edges, so nothing orders start on them. `service start`, `group start` and `stack up` start what each service needs. For a real start order, declare `needs` in the service manifest (see [Service edges](#service-edges-needs-and-calls)):

```bash
devstack dependencies add <service> <dep>       # adds <dep> to the old map
devstack dependencies remove <service> <dep>
devstack dependencies list <service>            # the order that the old map gives
devstack group list
devstack group add <group> <service> [service...]
devstack group remove <group> <service> [service...]
```

### Registering a service

To register a service that the workspace manifest does not hold yet, run this command:

```bash
devstack init --name=api --path=~/dev/my-workspace/api --cmd="go run ." --port=8080
```

devstack detects the language from `go.mod`, `package.json`, `requirements.txt` or `*.csproj`. To name the language yourself, pass `--language`.

One directory declares as many services as it runs. Name the files for what the directory holds:

- One service in the directory: call the file `devstack.service.yaml`.
- Several services in the directory: name each file after its service, `devstack.<name>.yaml`. Then every file says which service it declares.

`devstack init` writes `devstack.service.yaml` for the first service in a directory, and `devstack.<name>.yaml` for each service after that. Run it once for each service, with the same `--path`. If a directory grows a second service, rename the first file to `devstack.<name>.yaml` yourself. devstack reads both names, so the rename is safe.

### Service edges: `needs` and `calls`

A service declares its edges in its own `devstack.service.yaml`. There are two lists:

```yaml
needs:                   # the service can not work without these
  - navexa-sql
  - service: navexa-api
    via: http
calls:                   # the service talks to these, and works without them
  - service: nxTradeImporter
    via: queue
    entities: [stage-trades-queue]
migrations: db/migrations   # optional: a path relative to the service directory
```

An entry is a service name, or a map with `service`, an optional `via` and optional `entities`.

| Edge | Pulls into a stack | Orders start | Shown in `wiring` and `topology` |
|---|---|---|---|
| `needs` | yes, transitively | yes | yes |
| `calls` | no | no | yes |

- The daemon's start order comes from `needs` only.
- A `needs` edge to a shared service orders start, and pulls nothing into a stack.
- A `needs` cycle stops the load. The error names the cycle. To break it, change one edge to `calls`.
- An edge to a service that the workspace does not declare stops the load.
- `wiring` and `topology` show `via`. devstack reads `entities` and shows it nowhere. devstack acts on neither. For `via`, use the nxAlmanac values: `http`, `sql`, `redis`, `mcp`, `queue`, `blob`, `webhook`, `otlp`, `oidc`.
- `migrations` names the migrations directory of the service. If `stack resync` moves commits that change it, resync says so.

The workspace maps `dependencies:`, `calls:` and `startsAfter:` are the old form. devstack loads each entry of them as a `calls` edge, not as `needs`. To move them onto each service, run `devstack migrate shared-stacks`.

`${stack.suffix:<svc>[,<svc>...]}` resolves to `-<stack>` when the stack runs its own copy of every listed service. It resolves to an empty string in any other case, and for a shared service. Use it in a queue, topic or bucket name, so that each stack gets its own name inside a shared store.

## Feature stacks

A feature stack runs the services that one feature needs. Each member gets a git worktree, and a port that devstack allocates. The stack folds into the one host daemon as `<workspace>:<service>:<stack>`. The stack uses the shared set for databases and stores. So several features run at the same time, and each stack stays small.

```bash
devstack stack create <name> --repos <svc>[,<svc>]  # the named services and what they need
devstack stack create main --repos <svc> --detach   # run the default branch, with no branch
devstack stack create <name> --template <t>         # the services and environment of a template
devstack stack templates                            # each template of the workspace
devstack stack up <name>                            # fold it into the host daemon
devstack stack down <name>                          # stop it, keep the worktrees
devstack stack status <name>                        # its copies: state, ports, environment
devstack stack list                                 # each stack, its state and its stop time
devstack stack rm <name>                            # remove worktrees, release ports, deregister
devstack stack config <svc> --stack <name>          # the configuration that copy runs with
devstack stack note <name> [text]                   # what this stack is for
devstack stack note <name> --add "..."              # append where it got to
devstack stack resync <name>                        # fast-forward the worktrees to origin
devstack stack adopt <name>                         # take over an agent's stack
devstack stack sweep [--dry-run]                    # stop each stack past its stop time
devstack task run <svc> [task] --stack <name>       # run a task the service declares, in the stack's worktree
```

### Members

The members of a stack are the services that you name in `--repos`, and every service that they `need`, transitively. A shared service is never a member. A service that a member only `calls` does not come in. A caller of a member does not come in either. If a stack names only `api`, it gets no frontend. If the feature needs the frontend, name it too.

`stack create` prints the members. For each service that it added, it prints the `needs` chain that pulled it in. `stack create` and `stack add` refuse a shared service, and name the shared set.

If a member calls or references a service that is neither a member nor shared, `stack create` and `stack up` warn, and name `stack add`. They never refuse. The reference resolves to the declared port of that service, where nothing serves. You decide whether the change needs that service.

`--repos` also takes a group name. devstack expands the group to its members, and records the group on the stack. `devstack stack list` then reports how much of that group the stack covers. A group action against a stack reaches only the members of the stack. devstack names the services of the group that are not in the stack.

### Templates

A template is a named recipe for a new stack, in the `templates:` key of the workspace manifest. A template never runs anything. It is not in the host daemon, in `status`, or in a group command.

```yaml
templates:
  agent:
    purpose: An nx-agent stack that uses prod RAG.
    services: [navexa-api, navexa-frontend, nx-agent, nxmcp, agent-rag]
    env: dev-agent-prod
```

- `services` names services, groups, or both. A group expands to its members.
- `env` is optional. It must name an environment in `environments:`.
- `purpose` says what the template is for. `stack templates` and the `environment` tool show it.

devstack refuses to load the manifest if a template names an undeclared service, group or environment. It also refuses a template with the same name as a service or a group. devstack skips a shared service in `services`, as it does in a group.

`devstack stack create <name> --template <t>` uses the template's services as the named services. `--repos` adds more. devstack then adds the services that they `need`, as for any stack. If the template has `env`, the stack uses that environment, as `devstack env use <env> --stack <name>` does. The stack record keeps the name of the template, and `stack list` shows it. If the template does not exist, devstack lists the templates.

`devstack stack templates` lists each template with its purpose and services. Over MCP, `stack_create` takes `template`, and the `environment` tool lists the templates.

### Branches

A named service gets the stack's branch. `--branch` names it, and defaults to the stack name. If that branch exists, devstack attaches to it with the history that it already has. A service that comes in through `needs` is detached at the default branch tip. With `--detach`, every worktree is detached, and the stack has no branch.

`devstack stack create` cuts each worktree from the default branch of the repository, as origin holds that branch where a remote exists. It never cuts from the branch that your checkout has checked out. `--from <ref>` names a different ref. `--from` does not apply to a branch that already exists.

`devstack stack resync` moves a stack to what you pushed. It fetches origin, and fast-forwards each worktree on the stack's branch to `origin/<branch>`. It fast-forwards each detached worktree to the default branch tip. It refuses a worktree with uncommitted changes, or one that diverged from origin. It never merges or rebases. If the stack is up, devstack restarts the services that moved, and the members that depend on them.

Resync adds no service. It reports each repository outside the stack that has the branch on origin. It reports each service that a member now needs and the stack does not run. Each report names `stack add`. If the new commits change the `migrations:` directory of a service, resync says so, and names `task_run`. Resync runs no migration.

`stack up` checks each worktree against origin. If a worktree is behind, `stack up` names `stack resync`. It does not resync by itself.

### Adding and removing services

`devstack stack add` puts a service into a stack that already exists:

```bash
devstack stack add <name> <service|group> [service|group...]
```

`stack add` also adds the services that the new members need. It stops nothing and starts nothing. The stack stays exactly as up or as down as it was. If the stack is up, the added copies become resources in the host daemon, but devstack does not start them. Start each one with `devstack service start <service> --stack <name>`.

`devstack stack remove` takes a service out of a stack, and keeps the rest of the stack as it is:

```bash
devstack stack remove <name> <service> [service...]
```

`stack remove` removes the services from the stack record and the stack manifest, and releases only their ports. It refuses to remove a service that another member needs, and names that member. It keeps the worktrees on disk, and prints the `git worktree remove` command for each one. If the stack is up, the host daemon drops the removed copies. Never edit `stacks.json`, `ports.json` or a stack manifest by hand. Use `stack add` and `stack remove`.

`stack remove` fires the `stack.destroy` hooks for the removed services first, while their ports and record still exist; a hook failure does not stop the removal.

### The stop time

Every stack has a stop time. `stack create` sets it to the create time plus 24 hours. `stack up` sets it to now plus 24 hours. `devstack stack extend` (MCP: `stack_extend`) pushes it back: to the later of the stop time and now, plus the hours you give. One call adds 24 hours at most. `DEVSTACK_STOP_AFTER` changes the 24-hour window.

`devstack stack sweep` stops each stack that is up and past its stop time, in every workspace of this machine. A stop is the same as `stack down`: the `stack.down` hooks fire, and the host daemon drops the stack. devstack keeps the worktrees, the ports and the record. The sweep never removes a stack. `stack list` shows the stop time of each stack that is up, and when the sweep stopped a stack.

`devstack workspace up` installs a systemd user timer, `devstack-sweep.timer`. Every 10 minutes it runs `devstack tick`. Each tick runs the sweep and publishes the [herdr pane tokens](#the-stack-of-a-herdr-pane). Once an hour, the tick also refreshes the shared set. On a host without systemd, devstack runs the sweep before a command, at most once in 10 minutes. `devstack stack gc` is a hidden old name for `stack sweep`.

### Agents

An agent acts only on its own `agent-*` stacks. devstack does not limit how many stacks an agent creates. `devstack stack adopt <name>` hands an agent's stack to you. devstack clears the owner, so no agent can act on it. The stop time stays as it is.

### Working in a stack

To work on a stack, change directory into its worktree. `stack create` and `stack list` print the path. That worktree is already on the branch of the stack, so you never run `git checkout`. To reload that one copy, run `service restart --stack <name>`. That command leaves the shared set and every other stack untouched.

`create` also takes `--note`. A branch says what changed. A note says why. A week later, the note is the part that you cannot reconstruct.

`--add` appends a dated entry, and does not replace the purpose. A stack that you left half-done then says where the work got to. devstack keeps the last 5 entries, each of 200 characters at most. If the new entry repeats the last one, devstack changes nothing. A log that records every step buries the line that is worth reading.

The purpose and the newest entry go into the session briefing. An agent that picks the stack up reads both before it touches anything.

```
$ devstack stack note perf
NAV-412 daily value spike
  2026-08-01  repro on dev2, recalc is 40s
  2026-08-03  traced to the FX join; parked pending a prod diff
```

> WARNING: Shared data is not isolated. A stack isolates code and ports. Every stack reads and writes the same rows in the shared databases, queues and caches. To give a stack its own queue or bucket name, use [`${stack.suffix:...}`](#service-edges-needs-and-calls) and the `stack.create` and `stack.destroy` [hooks](#hooks). To point one stack at another database, use an [environment](#environments).

## Environments

An environment is a named set of configuration values in the workspace manifest. It holds database URLs, feature flags and endpoints. You repoint a service with an environment, and you touch no code. One stack can run against `local` while another stack runs against `staging`.

You define an environment one time, in the workspace manifest, and every stack inherits it. A stack defines no environment of its own. `env use --stack <name>` points the stack at an environment that the workspace manifest defines. There are three scopes: stack, service and workspace default. The most specific scope wins. `env use --workspace-default` points the workspace default. With no flag, the working directory decides: a stack worktree selects that stack, and every other directory selects the workspace default. The MCP tools take `scope="workspace"` for the workspace default.

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
devstack env use <name> [--service <svc> | --stack <name> | --workspace-default]
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

`services:` scopes a hook to the services that it names. The hook then runs one time for each service, with that service as `${self}`. If you leave `services:` out, the hook runs one time for the event. A service manifest can declare hooks too. Those hooks are scoped to that service, and devstack rejects a `services:` key in them. A feature stack inherits the hooks of the workspace, as it inherits the environments.

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
devstack otel traces --stack=shared  # the shared set alone
devstack otel traces --stack=all     # every stack and the shared set (the default)
devstack otel traces <trace-id>      # full span tree
devstack otel logs --trace=<trace-id>
devstack otel status                 # collector state, ports, per-service evidence
devstack otel open                   # the UI
devstack otel plugins                # backends and their config keys
```

A query with no `--stack` covers every stack and the shared set. Pass a stack's short name for that stack alone. Pass `shared` for the shared set alone. `--stack all` is the same as no `--stack`. The `investigate` MCP tool has the same default, so the two surfaces cover the same copies.

Every copy reports to that one backend. devstack stamps each report with `devstack.workspace`, `devstack.service`, `devstack.stack` and `devstack.env`. These attributes carry the `devstack` prefix on purpose. `deployment.environment` belongs to the owner of the destination, and the `forwarding` backend sets it for each workspace. So you run one backend, and you slice the data at query time.

A service often reports itself under a name that devstack does not know it by. A filter matches either name, and `otel services` prints both:

```
Navexa.API      (devstack: navexa-api)  stack=agent      env=dev
Navexa.API      (devstack: navexa-api)  stack=nvxa-1422  env=dev
```

A shared service reports `devstack.stack=shared`. That value is the label of the shared set, not a stack. Hooks get the same label in `DEVSTACK_STACK` when they run with no stack. Older spans carry `devstack.stack=base`. `--stack shared` matches both values, and `--stack base` is a deprecated alias of `--stack shared`.

## Tunnels

A tunnel forwards the service ports of this workspace over SSH. Any host that you can reach with ssh works, including a plain alias in your ssh configuration. A tailnet address is one such host, and is not a requirement. devstack supports key-based authentication only. Run `push` on the machine where the services run. Run `pull` on the machine that you want to reach them from.

```bash
devstack tunnel push my-box.ts.net --user alice        # first run saves the remote
devstack tunnel push                                   # later runs reuse it
devstack tunnel push --service navexa-api,navexa-frontend
devstack tunnel push --stacks                          # every stack that is up, each on its own port
devstack tunnel push --as-declared-ports agent         # one stack, on its declared ports
devstack tunnel push --otel                            # also the observability UI
devstack tunnel pull <host>
devstack tunnel status                                 # what is forwarded right now
devstack tunnel status --planned [--stacks]            # what a push would forward, no SSH
devstack tunnel check <host>                           # what already holds those ports on the far end
devstack tunnel stop [--service navexa-api]
devstack tunnel restart [--mode push|pull]
```

`--service` takes exact names, the names that `tunnel status --planned` prints. It matches no partial name. Repeat the flag, or give a comma-separated list. devstack forwards only the ports that serve traffic. With `--otel`, the remote host reads the traces of this machine at the address that you use locally.

With no stack flag, devstack forwards the ports of the shared set. `--stacks` and `--as-declared-ports` do different jobs, and you cannot combine them. `--stacks` gives every stack its own port on the far end. `--as-declared-ports <name>` puts one stack on the ports that its service manifests declare. The far end then reaches that stack at the address that it already knows, and you configure nothing on the far end. `--as-base` is a deprecated old name of `--as-declared-ports`:

```
devstack tunnel push my-box.ts.net --as-declared-ports agent
# far end :4200 → here :20006, far end :63290 → here :20005
```

`stop` and `status` read the forwards that really run, and not what discovery covers now. So devstack still reports and still stops a port whose service went away. devstack does the same for the observability UI, which was never a Tilt resource.

`restart` repeats the last push or pull: the same direction, the same services and the same stack mapping. It says what it repeats. If devstack saved no push or pull, `restart` rebuilds from the defaults. A stack that was on its declared ports then drops off them, and a push runs on the machine that you ran `pull` from. Any flag that you pass overrides the saved one.

CAUTION: `--reclaim` kills what already holds those ports on the far host. What it kills can belong to a colleague, or to another stack. `--reclaim` touches only the ports that devstack forwards, so `--service` narrows the blast radius. Before you pass it, run `devstack tunnel check <host>` to see who holds those ports.

## Addresses

A service of this machine often answers on the tailnet too. The request crosses three hops: the tailnet address, then the local proxy, then the service. devstack reads the map of every hop, joins them, and prints the address at the front of the chain. Give that address to a person, or open it on your phone.

```bash
devstack urls                     # the shared set and every stack of this workspace
devstack urls --stack orbit       # one feature stack
devstack urls --all               # every service, published or not
devstack urls --json              # the same list, for a script or an agent
```

devstack reads the tailnet map from `tailscale serve status`, and the proxy map from the caddy admin API on `:2019`. If a machine has no tailnet map or no proxy map, devstack prints fewer addresses. It reports no error. A service with no address still runs, and it still answers on its own port on this machine.

The `urls` MCP tool reports the same list, so an agent can tell you where to see its work. To reach a service that has no address, forward its port with [a tunnel](#tunnels) instead.

## The panel

`devstack panel` watches the machine, and acts on one service. It lists what the workspace runs. For each copy it gives the state, the group, the branch, the port and the address. It reads the machine again every few seconds.

```bash
devstack panel                    # the workspace of the working directory
devstack panel --workspace navexa # one named workspace
devstack panel --jump             # open the address picker at once
```

| Key | What it does |
| --- | --- |
| `↑` `↓`, or `j` `k` | move |
| `enter` | open the address in the browser |
| `y` | copy the address |
| `O` | find an address by name, then open it |
| `s` `r` `x` | start, restart, stop the service. On a stack line, `s` brings the stack up and `x` takes it down |
| `l` | read the process log |
| `a` | show every service, and not only what is up |
| `?` | the keys |
| `q` | quit |

The panel shows the workspace of the directory it opens in. From a directory outside every workspace, the panel shows all the workspaces.

The `machine` group at the top holds the host daemon and the collector. One of each serves every workspace, so the panel does not start or stop them. A `containers` group holds the docker containers of one workspace. The panel does not start or stop these containers either.

### The herdr plugin

[herdr](https://herdr.dev) opens the panel in a pane beside your work. The plugin is a launcher: it runs the `devstack` binary on your `$PATH`. As a result, the panel and the CLI stay the same version, and `devstack upgrade` updates both.

Install it from your checkout, or from GitHub:

```bash
herdr plugin link ~/dev/devstack/herdr                 # the checkout you work in
herdr plugin install socialviolation/devstack/herdr    # the published manifest
```

Then bind the two actions in `~/.config/herdr/config.toml`:

```toml
[[keys.command]]
key = "prefix+l"
type = "plugin_action"
command = "devstack.open-panel"
description = "open the devstack panel in split"

[[keys.command]]
key = "prefix+shift+l"
type = "plugin_action"
command = "devstack.open-links"
description = "open a service address in the browser"
```

Run `herdr server reload-config` to apply it. The plugin gives four actions:

| Action | What it does |
| --- | --- |
| `devstack.open-panel` | Open the panel beside the current work. The same key focuses it, then closes it |
| `devstack.open-panel-tab` | Open the panel in its own tab, or switch to the tab that holds it |
| `devstack.open-links` | Open the address picker over the current work. Type, then press enter |
| `devstack.open-pane-stack` | Open the tailnet address of the stack that the focused pane binds |

The panel shows the workspace of the pane where you press the key. From a directory outside every workspace, the panel shows all the workspaces. The panel draws with the palette of your herdr theme, and it follows `[theme]` and `[theme.custom]` in the herdr configuration.

### The stack of a herdr pane

A herdr pane binds a stack when you run `stack create`, `stack up`, `stack resync`, `stack adopt` or `stack extend` in it. This includes an agent in the pane that calls the stdio MCP tools. A call over HTTP binds no pane. The last pane wins. One pane can bind more than one stack.

After each stack change, devstack sets these tokens on the pane, with the source `devstack`:

| Token | Value |
| --- | --- |
| `stack` | The stack name. If the pane binds more stacks, `name +N`. The name is the stack that was up or created last |
| `stack_state` | `up`, `stopped`, or `stopped-auto` (the sweep stopped it) |
| `stack_url` | The tailnet address of the first member that has one. Only while the stack is up |
| `stack_stop` | The local stop time, for example `14:20`. Only while the stack is up |

`devstack herdr publish` sets the tokens again. The plugin runs it when herdr starts, because herdr loses tokens when it restarts. The plugin action `devstack.open-pane-stack` opens `stack_url` of the focused pane. If herdr is not installed or does not run, devstack does nothing.

devstack never edits your herdr configuration. To show the tokens, add rows to `~/.config/herdr/config.toml`:

```toml
[ui.sidebar.agents]
rows = [
  ["state_icon", "machine", "workspace", "tab"],
  ["agent"],
  ["$stack", { token = "$stack_state", rules = [
    { equals = "up", fg = "#a6e3a1" },
    { equals = "stopped-auto", dim = true },
  ] }, "$stack_stop"],
  ["$stack_url"],
]
```

## MCP

`devstack serve` is the MCP server. `.mcp.json` starts it over stdio. You do not run it yourself.

```
environment   status        urls          topology      wiring
start         stop          restart       process_logs  service_env
configure     observability investigate   tunnel        hooks
shared        doctor        cleanup       migrate       task_run
stack_create  stack_add     stack_remove  stack_up      stack_down
stack_list    stack_rm      stack_note    stack_config  stack_extend
stack_resync  stack_adopt   env_use       env_which     env_set
```

| Tool | What it does |
|---|---|
| `shared` | Reads the shared set with `action="status"` or `action="path"`. `action="sync"` moves the replica to the default branch tip, and restarts the shared services that moved. |
| `stack_create` | Creates a stack. `template` names a template, and `repos` adds services. Give `repos`, `template`, or both. |
| `stack_extend` | Pushes back the stop time of a stack, by 24 hours at most in one call. |
| `stack_adopt` | Hands an agent's stack to the owner. It clears the owner, and keeps the stop time. |
| `stack_resync` | Fast-forwards the worktrees of a stack to origin, and restarts what moved. |
| `task_run` | Runs a task that a service declares under `tasks:`, in a stack's worktree of that service. It takes no command of its own. |
| `doctor` | Finds what is broken in the workspace, and names the fix for each problem. It changes nothing. |
| `cleanup` | Removes orphaned port allocations, worktrees and stack records. `dry_run` defaults to true. |
| `urls` | Reports the tailnet address of each service. |
| `wiring` | Shows how each service reaches the services it talks to: the env key, the resolved value, and the `needs` or `calls` edge. |

`stack_add` adds services to a stack that already exists, and `stack_remove` takes them out. `migrate` lists the migrations of this machine with `action="list"`, and runs the pending ones with `action="run"`. A run writes and deletes files in the service repositories, so read the git diff it makes and commit it yourself. `action="shared-stacks"` runs `devstack migrate shared-stacks`. It changes nothing until you set `apply=true`.

The set of tools adapts to the workspace. `investigate` appears only when observability is on. `tunnel` appears only when the machine has an ssh client. Call `environment` first. It reports the observability state of the workspace, the stacks in flight, and the tools that exist here.

`investigate` is the trace tool, and it has three modes:

- Give it a `trace_id`, and it returns one full span tree.
- Give it `attribute` and `value`, and it finds every root span with that value, for example `portfolio.id=57835`. It then expands each trace.
- Give it neither, and it returns the most recent executions.

The `stack` parameter scopes the search. An absent `stack`, or `"all"`, searches every stack and the shared set. A name searches that stack alone. When OTEL logs are not available, `investigate` reads the process logs of the dev daemon instead. `investigate` matches root spans only, so each result is a separate entry point of a trace, whichever service owns the root.

`service_env` resolves and edits the environment of a service:

| | |
|---|---|
| `get` | Show each key, with the rung it came from |
| `diff` | Compare two services side by side |
| `set` | Write to the manifest or to `.envrc` |
| `check` | Audit for placeholders and missing keys |
| `drift` | Compare what devstack resolves with what the repository says it needs |

Some commands have no tool and need a shell. They are: `upgrade`, `init`, `prime`, `serve`, `panel`, `ports`, `dependencies`, `group add`, `group remove`, `group list`, `env list`, `env show`, `env remove`, `otel start`, `otel stop`, `otel open`, `otel plugins`, `stack sweep`, `herdr publish`, `herdr open`, and every `workspace` command but `topology`.

The remaining otel commands have a tool. `investigate` covers `otel traces` and `otel logs`. The `observability` tool covers `otel status` with `action="status"`, `otel services` with `action="variants"`, and `otel config` with the three `config_*` actions.

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
  Your changes here go on the branch of this stack.

## THIS SERVICE — navexa-api
runs as 2 copies:
    agent-fx     :20006   stopped   branch agent/fx-rates
      /home/nick/dev/.devstack-stacks/agent-fx/navexa-api
  ▸ nvxa-1422    :20012   running   branch nick/nvxa-1422-wrong-company-name…
      /home/nick/dev/.devstack-stacks/nvxa-1422/navexa-api
  The marker ▸ shows the copy that you are in now: nvxa-1422.
  The directory under each copy is the directory that copy RUNS.
```

The sample is a short form. The briefing also names the shared set, the stop time of each stack, and how to extend it.

`prime` also names the stack that a session is probably for. That is a different question from where you are. Where you are comes from the filesystem, and `prime` marks it `▸`. What you are here for is a guess, and `prime` marks it `?`.

When `prime` cannot name one stack, it lists the stacks in flight as ranked candidates, each marked `?`. It ranks a stack that is up first, then the newest note entry, then the newest stack. It shows 5 rows at most, and it names the count it left out. A candidate is a guess about intent, and never a fact. Ask the user before you work on one.

To brief a session without a request, wire `prime` into Claude Code:

```bash
devstack init --all --claude-hook
```

That command writes a `SessionStart` hook that runs `devstack prime --json`. The hook covers the `startup`, `resume`, `clear`, `compact` and `fork` matchers. `compact` matters most, because compaction is when the landscape drops out of context. devstack merges the hook into an existing `.claude/settings.json` and does not replace the file. It keeps the hooks that you already have, and it adds nothing on a second run.

You commit `.claude/settings.json`, so the hook runs for everyone who clones the repository. That is why the flag is opt-in. `devstack init --all` on its own refreshes `.mcp.json`, and writes no hook.

## devstack writes no instructions into your repositories

devstack once wrote a block of instructions into `AGENTS.md`, and a shorter block into `CLAUDE.md` and the files beside it. It writes neither now. `devstack prime` prints the same facts at each session start, so the agent reads them from the binary. A committed copy of a live fact goes stale, and a stale fact reads exactly like a true one.

`devstack migrate` removes what an older devstack left behind. It acts on every workspace on this machine: the workspace root, each service repository, and the worktree of each feature stack. In each directory it removes the devstack block, it deletes a file that holds devstack content and nothing else, it writes `.mcp.json`, and it writes the SessionStart hook.

```bash
devstack migrate
```

That file work is the migration that `devstack migrate` runs today. The command runs every pending migration. Building the replica that the shared set runs from is not a migration: `devstack upgrade` builds it, and so does `devstack workspace up`. To read what is pending without changing anything, run `devstack migrate --list`. An agent does the same with the `migrate` tool: `action="list"` to read, `action="run"` to apply.

A migration runs one time in each workspace, and then it is done forever. `devstack upgrade` runs the pending ones for you. Run `devstack migrate` yourself when you clone a repository that still holds a block an older devstack committed, because nothing else finds that block.

A migration does not watch this machine. To find a repository devstack is not connected to, a devstack file that nobody committed, or a workspace with no replica, run `devstack workspace doctor`. The doctor reports these, and it fixes none of them. Each report names the command that does fix it.

devstack removes only what devstack wrote. Your own text stays, byte for byte. Where devstack can not find the end of its own block, it changes nothing and it names the file for you. Run the command again at any time: a second run changes nothing.

devstack does not own these repositories, so read the diff in each one before you commit it.

### Moving a workspace to the shared set

`devstack migrate shared-stacks` converts one workspace to the shared set and small stacks. Without `--apply`, it prints what it changes, and it changes nothing. The MCP tool `migrate` runs it with `action="shared-stacks"`. Only the owner can call that tool.

```bash
devstack migrate shared-stacks                                   # dry run
devstack migrate shared-stacks --apply --shared navexa-sql,azurite
devstack migrate shared-stacks --apply --needs navexa-api=navexa-sql
```

- **shared.** If the manifest has no `shared:` list, devstack proposes one: each service that starts a container, and each store. devstack writes the list only from `--shared`.
- **edges.** devstack moves the workspace maps `dependencies:`, `startsAfter:` and `calls:` onto each service, as `calls` in its `devstack.service.yaml`. It then removes the maps. `--needs svc=dep` makes that edge a `needs` edge.
- **groups.** devstack removes the shared services from each group.
- **stacks.** devstack removes the shared services from the members of each stack, and releases their ports. It removes no worktree, record or branch.
- **refs.** devstack names each `${svc.*}` reference to a stack service. It changes nothing for these.

Before it writes, devstack copies `stacks.json` and each file it edits to `<data dir>/migrate-backup-<time>/`, and prints that path. A second run changes nothing.

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
| `DEVSTACK_STOP_AFTER` | `24h` | Time from `stack create` or `stack up` to the stop time. `~/.devstack/config.json` takes `stop_after` too |

You cannot configure the port of the daemon. One daemon serves the machine on `:10300`, and every command reaches the daemon there.

## Files, and what to commit

| Artifact | Location | Commit? | Why |
|----------|----------|---------|-----|
| `devstack.workspace.yaml` | workspace root | Yes | The source of truth: services, groups, the shared set, environments and templates. It is portable and holds no machine paths. |
| `devstack.service.yaml`, `devstack.<name>.yaml` | service repo | No | The service manifests of that directory. Machine-local: each one holds absolute tool paths. Gitignore them. |
| `.mcp.json` | service repo | Yes | Generated, and it does not depend on the machine. |
| `AGENTS.md` | service repo | Yes | Yours. devstack writes nothing in it. `devstack migrate` removes the block an older devstack wrote. |
| `CLAUDE.md` / `GEMINI.md` / `.cursorrules` / `.github/copilot-instructions.md` | service repo | Yes | Yours. devstack writes nothing in them, and it creates none of them. |
| `.devstack.json` | workspace root | No | Retired. The otel plugin configuration now lives under `observability` in the workspace manifest. Delete this file after `devstack otel status` shows what you expect. |
| `Tiltfile` | anywhere | No | A generated build artifact. Never edit it by hand. |
| `~/.config/devstack/workspaces.json` | home | n/a | Machine-local registry of workspaces and their ports. |
| `~/.local/share/devstack/**` | home | n/a | Host daemon state: pids, `stacks.json` and the generated host Tiltfile. Without systemd, also `tilt.log` and `tilt.log.1`. |

A `.gitignore` for each service repository:

```
devstack.*.yaml
!devstack.workspace.yaml
.devstack.json
Tiltfile
```

A service manifest is machine-local and gitignored, so the worktree of a stack does not inherit one. For that reason `devstack stack create` copies the ignored configuration into each worktree. It copies every `devstack.*.yaml` beside the service. A directory that declares several services then keeps all of its manifests. git carries none of these files there. `devstack workspace up` copies the same files into each replica worktree.

`devstack stack create` never copies `devstack.workspace.yaml`. A worktree runs from the generated manifest of its stack, not from the manifest of the source.

### Secrets and `devstack env set`

CAUTION: `env set <env> KEY=VALUE` writes the value into `devstack.workspace.yaml` as plaintext. devstack masks the value when it shows the value, and never on disk. What that means for you depends on how you treat the file.

If you commit the file, which the table above recommends, keep real secrets out of it. Declare each secret in the `env.required` list of a service. Supply the secret at runtime from `.envrc`, or from your own secret store. Use `env set` for URLs, ports and feature flags only. A secret written into a committed file is a committed secret.

If the file is machine-local, `env set` is safe for API keys. The file is machine-local when you gitignore it, or when the workspace root is not a repository. devstack built `env set` for that case. The file is still plaintext on disk.

To find out which case you are in, run `git check-ignore -v devstack.workspace.yaml` in the workspace root.

## Updating devstack

```bash
devstack upgrade              # install, migrate, then restart what runs
                              # then restart your MCP server / agent session
devstack status               # make sure that the daemon and the services still answer
```

`devstack upgrade` does three things, in order.

1. It installs the current devstack. It refuses to move a local build backwards. To install anyway, give `--force`.
2. It runs each pending migration. A migration is one-off: devstack records it, and it never runs a second time. To skip this step, give `--no-migrate`.
3. It transforms the running state. devstack regenerates the daemon's Tiltfile and restarts each shared service that runs now, so that it runs from the replica. It restarts no copy in a stack. To skip this step, give `--no-restart`.

CAUTION: Step 3 restarts services that serve right now. devstack restarts only the copies that were already running. It starts no copy that is stopped, and it starts no daemon. Each replica worktree is a new checkout, so a service can need its own dependency install before it serves again. This step is slow.

devstack leaves one job for you: read the git diff that a migration made in each service repository, and commit it there. devstack does not commit, and it does not push.

Two of those steps carry a risk.

CAUTION: A stale binary does not fail on configuration that it does not understand. It falls back without a word. One older devstack read a workspace set to OpenObserve, and started SigNoz.

CAUTION: The MCP server reads its tool descriptions one time, at startup. A session that already runs keeps the old tool list. New tools do not appear, and the server rejects new parameters. Restart the MCP server.

`devstack migrate` writes files and cuts git worktrees. It restarts nothing. Expect a git diff in each service repository that it writes into.

If a workspace is part-way through an upgrade, `devstack otel start` replaces the observability container when its pinned image moved.
