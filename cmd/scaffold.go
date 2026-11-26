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
# This directory is the SOURCE. Nothing runs here. 'devstack workspace up'
# builds a REPLICA from it: one git worktree for each repository that holds a
# shared service, at its default branch tip, in a .devstack-base directory beside
# this one. The shared set runs there. Every other service runs only in a stack.
# To see that directory, run 'devstack status --all' and read the DIR column.
# Work that you park in a checkout does not run, and it blocks nothing.
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

# shared names the services that run once for the whole workspace, from the
# replica: databases and stores. 'devstack workspace up' starts them. Every stack
# uses them. A shared service can only need shared services.
shared: []
  # - my-db

# groups BIND services into one unit that you operate together. start and stop
# act on one stack: --stack <name>. With no flag, they act on the stack that
# holds the current directory. Anywhere else they act on the shared set.
#   devstack group start <group> --stack <name>   devstack status
# The daemon also labels its UI from the groups. A service can belong to many
# groups.
groups: {}
  # backend: [my-api]
  # workers: [my-worker]

# templates are recipes for a new stack: services, groups and an optional
# environment. A template runs nothing. To use one, run:
#   devstack stack create <name> --template <template>
# templates:
#   api-work:
#     purpose: Work on the API and its workers.
#     services: [my-api, workers]
#     env: staging

# Each service declares its edges in its own devstack.service.yaml, with needs
# and calls. This file has no edge map.
`

// serviceManifestTemplate is an educational devstack.service.yaml scaffold.
// Args in order: name, name (OTEL), name (link label).
const serviceManifestTemplate = `# devstack service manifest. This file says how devstack runs THIS service,
# and the services it needs and calls. The groups are in the workspace manifest.
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

# needs: the services that this service can not work without. devstack starts
# them first, and a stack pulls them in. An entry is a name, or a map with
# service and via.
needs: []
  # - my-db
  # - service: my-api
  #   via: http

# calls: the services that this service talks to, but works without. A calls
# edge starts nothing, and pulls nothing into a stack. wiring shows it.
calls: []
  # - service: my-worker
  #   via: queue

# migrations: the directory of this service's database migrations, relative to
# this service's directory. If a stack resync changes files in it, devstack says
# so. devstack runs no migration itself.
# migrations: db/migrations

# tasks: named commands that 'devstack task run' and the task_run tool run in a
# stack's worktree of this service. run is required. workDir, timeout (default
# 10m) and env are optional. run and env take references such as ${self.url}.
# tasks:
#   test:
#     run: go test ./...
#   seed:
#     run: ./scripts/seed.sh
#     timeout: 5m
#     env:
#       API_URL: ${self.url}
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
