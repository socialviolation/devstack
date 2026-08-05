# devstack.yaml — one file for each repository

Declare every service of a repository in one `devstack.yaml` at the root of that
repository. Delete the per-directory `devstack.service.yaml`.

> **Status: DESIGN.** The numbers come from the navexa workspace on 2026-08-05:
> 16 services across 17 manifests.

## The unit is the repository, not the directory

devstack keeps one `devstack.service.yaml` for each service, in the directory of
that service. Everything else devstack does works on the repository:

- **A worktree is a repository.** `stack create` cuts one worktree for `nxOrbit`
  and runs `orbit-api` and `orbit-web` from it. devstack already treats the two
  services as one unit; nothing in the manifests says so.
- **A branch is a repository.** A stack puts one branch on each repository it
  overlays, whatever number of services that repository holds.
- **A toolchain is a repository.** `mise.toml`, `.tool-versions` and `.envrc` sit
  at the repository root and apply to every service in it.
- **Dependencies are a repository.** One `node_modules`, one `vendor`, one
  submodule checkout, shared by every service in the tree.
- **The instructions devstack used to write were a repository.** One `AGENTS.md`
  at the root.

Only `workDir` differs for each service, and that is a field.

### The bug this conflation already caused

devstack wrote `AGENTS.md` at each repository root. It removes that block by
walking each **service** path: `migrateTargets` (`cmd/migrate_cmd.go:429`) builds
its list from `cfg.ServicePaths`, and `init --all` (`cmd/init.go:264`) and
`scanResidue` do the same.

`orbit-api` is `./nxOrbit/api` and `orbit-web` is `./nxOrbit/web`, so the sweep
visits two subdirectories and never `./nxOrbit`, where the file is. The block is
still there after `devstack upgrade`, after `devstack migrate` and after
`init --all`, and `workspace doctor` reports the workspace clean because it scans
the same service paths.

Any repository whose services live below its root keeps its block for ever.

## The shape

```yaml
# nxOrbit/devstack.yaml — committed, with the code
version: 1

services:
  orbit-api:
    workDir: api/src/Navexa.Orbit.Api
    run: dotnet watch run --no-launch-profile
    ports: {http: 5100}

  orbit-web:
    workDir: web
    run: npm run dev --workspace @nxorbit/web -- --port ${self.port.http}
    ports: {http: 4201}
    dependsOn: [orbit-api]
```

A repository with one service declares one key. The file is the same shape
either way, so nothing special happens when a repository grows a second service.

`workDir` is relative to the repository root, which is where this file is. That
is what makes the same file correct in the checkout, in the replica, and in every
stack worktree — the three directories the same service runs from.

### What the repository shares with itself

A repository-level block holds what its services have in common, and each service
overrides what it needs:

```yaml
env:
  values:
    NAVEXA_ENV: dev
prep:
  command: "[ -d node_modules ] || npm ci"

services:
  orbit-api: {...}
  orbit-web: {...}
```

This is the answer to running `npm ci` once for a repository rather than once for
each service in it, and it is where the `mise trust` hook belongs too.

## What stays in the workspace manifest

`devstack.workspace.yaml` keeps only what crosses a repository boundary:

    repos           where each repository is, and optionally its git url
    groups          named sets that span repositories
    dependencies    cross-repo start order (orbit-api needs navexa-api)
    environments    named config patches
    observability   the backend for the workspace
    hooks           lifecycle actions that are not one repository's business

A dependency inside a repository goes in that repository's file (`dependsOn`
above). A dependency between repositories goes in the workspace manifest. The
rule is the same one that decides the file: does it cross a repository?

## Committed, and what has to move first

`devstack.service.yaml` must not be committed. `devstack.yaml` must be, or the
change buys nothing: today none of the 17 files is shared, so a second machine or
a fresh clone starts from nothing.

Three things stand in the way, and all three are already problems.

**Secrets. 6 of 17 files.** Azure SQL passwords, service bus keys, storage
account keys, an OpenRouter key. devstack already says what to do instead —
declare the key under `env.required` and supply the value from `.envrc` — and it
already has the guard: `config.IsCredentialKey`, which
`internal/config/manifest_edit.go:241` uses to refuse a credential in a committed
manifest. Point the same guard at `devstack.yaml`.

**A toolchain path. 8 occurrences.** `run.command` begins
`/home/nick/.local/share/mise/installs/dotnet/8.0.418/dotnet`, because the
process that runs the command has no mise applied. **This is the one hard
dependency.** Until devstack runs a service command with the toolchain its
repository pins, committing these files commits one machine's interpreter paths.
Fix it first; it is worth doing on its own, and it is the same gap that made a
stack worktree need a `mise trust` hook.

**A checkout path. 6 occurrences.** `dotnet build /home/nick/dev/navexa/...`,
`workDir: /home/nick/dev/navexa/nxAgent`. These are already bugs: base runs the
replica, so each one builds the checkout rather than the code of the copy that
devstack started. Making them relative to the repository root is the fix, and
`devstack.yaml` at that root is what makes it natural.

## The one machine-local escape

`agent-rag` runs a binary from `/home/nick/dev/tsfc/rutter`, outside the
workspace. Nothing rewrites that away.

`devstack.local.yaml` at the workspace root, ignored by git, keyed by service,
merged over the committed definition for the keys it names. One ignored file
where there are 17 now, holding only what is true of this machine alone.

The test for what belongs in it: if a value is not a path to something outside
the workspace, it is not machine-local. It is a secret, or it is a bug.

## Migration

`devstack migrate`, version 2 to 3, for each repository rather than each service:

1. Find every `devstack.service.yaml` below the repository root.
2. Write one `devstack.yaml` at the root, with one `services:` key for each,
   converting each `workDir` to a path relative to that root.
3. Refuse to copy a value whose key `IsCredentialKey` matches. Write the key
   under `env.required` instead, and report the `.envrc` it belongs in.
4. Copy a value holding an absolute path, and report it. devstack cannot tell a
   toolchain path from a bug from a real external path, and must not guess.
5. Delete each `devstack.service.yaml` it folded in, and say so.

**Walk repositories, not service paths.** The same sweep fixes the `AGENTS.md`
residue, because the repository root is finally a place devstack looks. Add
`git rev-parse --show-toplevel` for each service path, dedupe, and use that list
in `migrateTargets`, `init --all` and `scanResidue`.

## Verify

- A repository with two services in one `devstack.yaml` runs both, each from its
  own `workDir`, out of one worktree.
- A repository-level `prep` runs once for the repository, not once for each
  service in it.
- The migration strips the devstack block from `nxOrbit/AGENTS.md` — the file the
  service-path sweep never reached.
- `workspace doctor` reports residue at a repository root, not only at a service
  path.
- The migration refuses to write a credential value and names the file it
  reported.
- A `devstack.local.yaml` override applies only to the keys it names, and
  `workspace doctor` names every override.
- A workspace with no `devstack.yaml` still resolves through the per-directory
  manifests.

## Rejected

**`devstack.svc.<name>.yaml`, several to a directory.** It allows two services in
one directory and changes nothing else. The definitions stay per-directory, so
the repository stays a thing devstack works on but never names, and the
`AGENTS.md` sweep stays wrong.

**Every definition in `devstack.workspace.yaml`.** One file for the whole
workspace reads well and puts the definition of a service in a different
repository from its code, where it is not reviewed with the code and does not
arrive with a clone of it. The workspace manifest should hold what crosses
repositories and nothing else.
