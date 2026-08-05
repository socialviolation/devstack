package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/socialviolation/devstack/internal/config"
)

// workspaceManifestTemplate is an educational devstack.workspace.yaml scaffold.
// %d = configuration version, %s = workspace name. It teaches an agent (or
// human) the whole model in comments.
const workspaceManifestTemplate = `# devstack workspace manifest. This file is the SINGLE SOURCE OF TRUTH for this
# workspace.
#
# devstack GENERATES the Tiltfile of the dev daemon from this file, and from the
# manifest of each service. Never edit the Tiltfile by hand. Edit these
# manifests, then run 'devstack workspace up' or 'devstack workspace generate' to
# write the Tiltfile again.
#
# This directory is the TEMPLATE. Nothing runs here. 'devstack workspace up'
# builds a REPLICA from it: one git worktree per repository at its default branch
# tip, in a .devstack-base directory beside this one. The base workspace runs
# there. To see that directory, run 'devstack status --all' and read the DIR
# column. Work that you park in a checkout does not run, and it blocks nothing.
#
# version is the version of this configuration. 'devstack migrate' moves it to
# the version that your devstack needs, and it writes the new number here.
version: %d

workspace:
  name: %s
  repoDiscovery:
    # How devstack finds the services:
    #   explicit  list each service repo directory. This mode is deterministic
    #   scan      give root directories. devstack walks them for a service
    #             manifest: devstack.service.yaml, or devstack.<name>.yaml for
    #             each service after the first one in that directory
    mode: explicit
    repos: []
      # - ./my-api
      # - ./my-worker

# Shared values that reach every service. They have the LOWEST precedence: the
# env.values of a service overrides them. devstack also injects the local OTEL
# endpoint itself, so you never write it twice. Change the OTEL values and the
# database values once, here.
env:
  values: {}
    # DATABASE_HOST: localhost
    # DATABASE_PORT: "5432"

# groups BIND services into one unit that you operate together. start and stop
# act on one copy: --stack base, or --stack <name> for a feature stack. With no
# flag, they act on the stack that holds the current directory. Anywhere else
# they act on base.
#   devstack group start <group> --stack base   devstack status
# The daemon also labels its UI from the groups. A service can belong to many
# groups.
groups: {}
  # backend: [my-api]
  # workers: [my-worker]

# dependencies define the START ORDER, and not the grouping. "A: [B]" means that
# B must be up before A. If you start A, devstack starts B first. devstack writes
# these as the resource_deps of the daemon.
dependencies: {}
  # my-worker: [my-api]
`

// serviceManifestTemplate is an educational devstack.service.yaml scaffold.
// Args in order: name, name (OTEL), name (link label).
const serviceManifestTemplate = `# devstack service manifest. This file says how devstack runs THIS service.
# The grouping and the start order are in the workspace manifest, not here.
version: 1

service:
  name: %s
  # aliases: [api]              # other names that devstack and agents can use

runtime:
  workDir: .                    # the run directory, relative to this repo (default ".")
  run:
    command: ""                 # REQUIRED: the long command that serves this service
  # prep:
  #   command: ""               # one command that runs before the service: free a port, or build
  # triggerMode: manual         # manual = you start it with devstack (default). auto = it starts with the daemon
  # autoStart: false            # start it when the daemon comes up
  # watch: []                   # files and directories that start the service again on a change
  healthcheck:
    type: http                  # http or exec
    port: 8080                  # http: the port to probe
    path: /health               # http: the path to probe (default "/")
    # command: ""               # exec: a shell command. Exit code 0 means healthy
    periodSecs: 5
    failureThreshold: 10

ports:
  http: 8080                    # the named ports that this service serves

env:
  # Values for THIS service. They override the workspace env.values.
  # NOTE: devstack reads a .envrc in the run directory itself. Do not list it here.
  values: {}
    # OTEL_SERVICE_NAME: %s

links:
  - url: http://localhost:8080
    label: %s
`

// scaffoldWorkspaceManifest writes an educational devstack.workspace.yaml at path
// unless one already exists. Returns whether it wrote the file.
func scaffoldWorkspaceManifest(path, name string) (bool, error) {
	target := config.WorkspaceManifestPath(path)
	if _, err := os.Stat(target); err == nil {
		return false, nil // already exists — never clobber
	}
	content := fmt.Sprintf(workspaceManifestTemplate, config.WorkspaceManifestVersion, name)
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		return false, err
	}
	return true, nil
}

// scaffoldServiceManifest writes an educational devstack.service.yaml in dir.
// It refuses to overwrite an existing file unless force is set.
func scaffoldServiceManifest(dir, name string, force bool) (string, error) {
	target := config.ServiceManifestPath(dir)
	if _, err := os.Stat(target); err == nil && !force {
		return "", fmt.Errorf("%s exists already. To overwrite it, give --force", target)
	}
	content := fmt.Sprintf(serviceManifestTemplate, name, name, name)
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		return "", err
	}
	return target, nil
}

// hasLegacyConfig reports whether a legacy .devstack.json exists at path.
func hasLegacyConfig(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".devstack.json"))
	return err == nil
}
