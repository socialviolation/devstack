# Upgrading

This guide covers the upgrade to the release after `v0.1.2`. That release moves
base off your checkouts, and it makes every command that starts or stops a copy
name the copy.

Read [What breaks](#what-breaks) first. Two of the changes stop a command that
worked yesterday.

## What changes

**Base runs from a replica.** Before this release, the daemon ran your own
checkouts. Now `devstack workspace up` builds one git worktree for each
repository, under `<parent>/.devstack-base/<workspace>/`, and the daemon runs
those. Your checkout becomes the template. devstack builds the replica from it,
and nothing runs in it.

Your checkout is now yours alone. Park a half-finished branch there. Leave it
dirty for a week. Base keeps serving the default branch.

**A command that starts, stops or restarts a copy must name the copy.** There is
no default. Before, a bare `devstack service restart api` acted on base.

## Migrate

Do this once for each machine, and then once for each workspace.

### 1. Install

```bash
devstack upgrade
```

The command installs the new binary. Then it reports the generated files that an
older devstack wrote. It stops there, because a regeneration writes a git diff
into repositories that devstack does not own.

### 2. Refresh the generated files

```bash
devstack upgrade --migrate
```

This rewrites each service's `AGENTS.md` and the managed block in `CLAUDE.md`.
The old text tells an agent that a bare restart acts on base. That is now false,
so an agent that reads the old text gets a refusal it cannot explain.

Commit the diff in each service repository.

### 3. Restart every agent session

An MCP server reads its tool descriptions one time, when it starts. A session
that already runs keeps the old tool list, and the old list does not hold the
`base` tool or the `stack_add` tool.

### 4. Build the replica, one workspace at a time

```bash
cd ~/dev/my-workspace
devstack workspace up
```

`workspace up` cuts one worktree for each repository in the workspace. **Budget
time for this.** Each worktree is a fresh checkout, so each one needs its own
`npm install`, `dotnet restore` or virtual environment before its service
starts. A workspace of 15 repositories pays that 15 times.

Pick a moment when you are not mid-task.

### 5. Check the result

```bash
devstack base path            # the replica root
devstack status --all         # the DIR column, for every copy
```

Every base copy must show a directory under `.devstack-base`. The BRANCH column
shows `detached@<sha>`, because a replica worktree sits at the default branch
tip and not on a branch.

## What breaks

### An edit in your checkout no longer reaches base

Base runs the replica. An edit in your checkout reaches base after two things
happen. First, the edit is on the default branch. Second, you run `devstack base
sync`.

```bash
devstack base sync            # move base to the current default branch tip
```

`devstack base sync` restarts nothing. A copy that runs keeps the old code until
somebody restarts it.

If you want to see a change run now, put the change in a stack. That is what
stacks are for.

### A mutating command needs a target

```bash
devstack service restart api                 # refused
devstack service restart api --stack base    # base's copy
devstack service restart api --stack perf    # the copy of stack perf
```

Three things name the copy:

- In a shell, `--stack <name>` or `--stack base`.
- Over MCP, `stack="<name>"` or `stack="base"`.
- A working directory inside a stack worktree, or inside the replica. Then the
  command needs no flag.

This applies to `service start|stop|restart`, `group start|stop|restart` and
`env use`. It does not apply to a read-only command. `devstack status`,
`devstack stack list` and `devstack env which` all still answer with no flag.

`env set` needs no target either. It defines an environment for the whole
workspace, so there is no copy to choose.

**Check your aliases and your scripts.** A script that runs
`devstack service restart api` now fails with an error that names the copies
available. Add `--stack base`.

### An agent that omits the stack parameter now fails

Over MCP, `start`, `stop`, `restart` and `env_use` refuse a call that names no
stack from a directory that is neither a stack worktree nor the replica. Pass
`stack="base"`.

### A new stack starts from the default branch

`devstack stack create` cuts the worktrees from each repository's default
branch, as origin holds it. Before, it cut them from whatever your checkout had
checked out.

To cut from something else, name it:

```bash
devstack stack create fix --repos api --from origin/release-2
devstack stack create fix --repos api --from 8aa7285
```

A `--from` ref applies only where devstack creates the branch. devstack attaches
an existing branch with the history that branch already holds.

### A new stack lives under its workspace

A stack root is now `<parent>/.devstack-stacks/<workspace>/<name>`. Before, the
workspace name was absent, so two workspaces that share a parent directory
fought over one stack name.

**Your existing stacks keep working.** Each record holds an absolute path, and
every command reads that path. Only a new stack takes the new layout.

### A replica worktree is named after the repository

A repository can hold more than one service. git cuts a worktree of a whole
repository, so the replica cuts one worktree for each repository, and each
service resolves to its directory inside that worktree.

Where a repository's directory name differs from the service name, the next
`devstack workspace up` removes the old directory and cuts a new one. Nothing
you own lives in the replica, so this costs you the rebuild and nothing else.

## Requirements

Every service must sit inside a git repository. A service can be the repository
root, or a directory below it. devstack refuses a service directory that is in
no repository, because the replica runs git worktrees.

## Roll back

```bash
go install github.com/socialviolation/devstack@v0.1.2
```

Then remove the replica of each workspace:

```bash
devstack base path                        # print the root, then remove it
git -C <repo> worktree prune              # for each repository
```

The old devstack runs your checkouts again. It ignores a `.devstack-base`
directory that it did not build, so remove the directory to reclaim the disk.

Your stacks survive a roll back. The old devstack reads the same records.
