package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/socialviolation/devstack/internal/config"
)

// workspaceManifestTemplate is an educational devstack.workspace.yaml scaffold.
// %s = workspace name. It teaches an agent (or human) the whole model in comments.
const workspaceManifestTemplate = `# devstack workspace manifest — the SINGLE SOURCE OF TRUTH for this workspace.
#
# The dev daemon's Tiltfile is GENERATED from this file + each service's
# devstack.service.yaml. Never edit the Tiltfile by hand — edit these manifests
# and run 'devstack workspace up' (or 'devstack workspace generate') to regenerate it.
version: 1

workspace:
  name: %s
  repoDiscovery:
    # How devstack finds services:
    #   explicit — list each service repo directory (recommended, deterministic)
    #   scan     — give root dirs; devstack walks them for devstack.service.yaml
    mode: explicit
    repos: []
      # - ./my-api
      # - ./my-worker

# Shared environment that TRICKLES DOWN to every service (lowest precedence — a
# service's own env.values override these). devstack also auto-injects the local
# OTEL endpoint, so you never repeat it. Change OTEL/DB config once, here.
env:
  values: {}
    # DATABASE_HOST: localhost
    # DATABASE_PORT: "5432"

# groups BIND services into a unit you operate on together:
#   devstack group start <group>   devstack group stop <group>   devstack status
# Groups also drive the daemon UI labels. A service may belong to many groups.
groups: {}
  # backend: [my-api]
  # workers: [my-worker]

# dependencies define START ORDER (not grouping): "A: [B]" means B must be up
# before A, and starting A pulls in B first. Emitted as the daemon resource_deps.
dependencies: {}
  # my-worker: [my-api]
`

// serviceManifestTemplate is an educational devstack.service.yaml scaffold.
// Args in order: name, name (OTEL), name (link label).
const serviceManifestTemplate = `# devstack service manifest — how devstack runs THIS service.
# Grouping and start-order live in the workspace manifest, not here.
version: 1

service:
  name: %s
  # aliases: [api]              # alternate names devstack/agents may use

runtime:
  workDir: .                    # run dir, relative to this repo (default ".")
  run:
    command: ""                 # REQUIRED: the long-running command to serve this service
  # prep:
  #   command: ""               # optional one-shot before serve (free a port, build, etc.)
  # triggerMode: manual         # manual = start via devstack (default) | auto = start with daemon
  # autoStart: false            # start automatically when the daemon comes up
  # watch: []                   # file/dir paths that re-trigger the service on change
  healthcheck:
    type: http                  # http | exec
    port: 8080                  # http: port to probe
    path: /health               # http: path to probe (default "/")
    # command: ""               # exec: shell command; success (exit 0) = healthy
    periodSecs: 5
    failureThreshold: 10

ports:
  http: 8080                    # named ports this service exposes

env:
  # Inline env for THIS service (overrides workspace env.values).
  # NOTE: a .envrc in the run dir is sourced automatically — no need to list it.
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
	content := fmt.Sprintf(workspaceManifestTemplate, name)
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
		return "", fmt.Errorf("%s already exists (use --force to overwrite)", target)
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
