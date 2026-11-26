package doctor

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

type Drift struct {
	Name   string
	Detail string
}

func ReplicaBuilt(ws *workspace.Workspace) bool {
	return config.HasWorkspaceManifest(replica.Root(ws))
}

func ReplicaDrift(ws *workspace.Workspace) []Drift {
	template, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return nil
	}

	var out []Drift
	for _, dir := range replicaRepoDirs(replica.Root(ws)) {
		if len(config.ServiceManifestFiles(dir)) == 0 {
			out = append(out, Drift{filepath.Base(dir), "the replica lists this repository, and it holds no service manifest: " + dir})
		}
	}
	rw, err := replica.Resolve(ws)
	if err != nil && len(out) == 0 {
		out = append(out, Drift{ws.Name, fmt.Sprintf("devstack can not read the replica: %v", err)})
	}
	if err == nil {
		for _, name := range resolvedServiceNames(rw.Services) {
			if _, ok := template.Services[name]; !ok {
				out = append(out, Drift{name, "the replica declares this service, and the workspace does not: " + rw.Services[name].RepoPath})
			}
		}
		for _, name := range template.SharedNames() {
			if _, ok := rw.Services[name]; !ok {
				out = append(out, Drift{name, "the workspace declares this service, and the replica does not: " + template.Services[name].RepoPath})
			}
		}
	}
	return out
}

func replicaRepoDirs(root string) []string {
	manifest, err := config.LoadWorkspaceManifest(root)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, repo := range manifest.Workspace.RepoDiscovery.Repos {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		if !filepath.IsAbs(repo) {
			repo = filepath.Join(root, repo)
		}
		dirs = append(dirs, filepath.Clean(repo))
	}
	sort.Strings(dirs)
	return dirs
}

func resolvedServiceNames(services map[string]config.ResolvedService) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
