package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const groupsTestManifest = `version: 1
workspace:
  name: test-ws
  # keep me
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/api
groups:
  core: [api, frontend]
  data: [postgres]
dependencies:
  api: [postgres]
`

func TestSetGroupMembers(t *testing.T) {
	tests := []struct {
		name    string
		group   string
		members []string
		want    map[string][]string
	}{
		{
			name:    "new group",
			group:   "extras",
			members: []string{"mailhog"},
			want: map[string][]string{
				"core":   {"api", "frontend"},
				"data":   {"postgres"},
				"extras": {"mailhog"},
			},
		},
		{
			name:    "add to existing group",
			group:   "core",
			members: []string{"api", "frontend", "worker"},
			want: map[string][]string{
				"core": {"api", "frontend", "worker"},
				"data": {"postgres"},
			},
		},
		{
			name:    "remove one member",
			group:   "core",
			members: []string{"api"},
			want: map[string][]string{
				"core": {"api"},
				"data": {"postgres"},
			},
		},
		{
			name:    "empty removes the group",
			group:   "data",
			members: nil,
			want: map[string][]string{
				"core": {"api", "frontend"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, WorkspaceManifestFileName), groupsTestManifest)

			if err := SetGroupMembers(dir, tt.group, tt.members); err != nil {
				t.Fatalf("SetGroupMembers: %v", err)
			}

			m, err := LoadWorkspaceManifest(dir)
			if err != nil {
				t.Fatalf("LoadWorkspaceManifest: %v", err)
			}
			if !reflect.DeepEqual(m.Groups, tt.want) {
				t.Errorf("groups = %v, want %v", m.Groups, tt.want)
			}

			data, err := os.ReadFile(WorkspaceManifestPath(dir))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if len(tt.members) == 0 && strings.Contains(string(data), tt.group+":") {
				t.Errorf("emptied group %q still present:\n%s", tt.group, data)
			}
			if !strings.Contains(string(data), "# keep me") {
				t.Errorf("comment was dropped:\n%s", data)
			}
		})
	}
}

func TestSetServiceDependencies(t *testing.T) {
	tests := []struct {
		name    string
		service string
		deps    []string
		want    map[string][]string
	}{
		{
			name:    "new service",
			service: "worker",
			deps:    []string{"redis"},
			want: map[string][]string{
				"api":    {"postgres"},
				"worker": {"redis"},
			},
		},
		{
			name:    "add to existing service",
			service: "api",
			deps:    []string{"postgres", "redis"},
			want: map[string][]string{
				"api": {"postgres", "redis"},
			},
		},
		{
			name:    "empty removes the entry",
			service: "api",
			deps:    nil,
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, WorkspaceManifestFileName), groupsTestManifest)

			if err := SetServiceDependencies(dir, tt.service, tt.deps); err != nil {
				t.Fatalf("SetServiceDependencies: %v", err)
			}

			m, err := LoadWorkspaceManifest(dir)
			if err != nil {
				t.Fatalf("LoadWorkspaceManifest: %v", err)
			}
			if len(tt.want) == 0 {
				if len(m.Dependencies) != 0 {
					t.Errorf("dependencies = %v, want empty", m.Dependencies)
				}
			} else if !reflect.DeepEqual(m.Dependencies, tt.want) {
				t.Errorf("dependencies = %v, want %v", m.Dependencies, tt.want)
			}

			data, err := os.ReadFile(WorkspaceManifestPath(dir))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if len(tt.deps) == 0 && strings.Contains(string(data), tt.service+": [") {
				t.Errorf("emptied dependency entry %q still present:\n%s", tt.service, data)
			}
			if !strings.Contains(string(data), "# keep me") {
				t.Errorf("comment was dropped:\n%s", data)
			}
		})
	}
}

func TestSetGroupMembersRoundTripsThroughResolveWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, WorkspaceManifestFileName), groupsTestManifest)
	writeFile(t, filepath.Join(dir, "repos", "api", ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
`)

	if err := SetGroupMembers(dir, "core", []string{"api", "worker"}); err != nil {
		t.Fatalf("SetGroupMembers: %v", err)
	}
	if err := SetServiceDependencies(dir, "worker", []string{"postgres"}); err != nil {
		t.Fatalf("SetServiceDependencies: %v", err)
	}

	rw, err := ResolveWorkspace(dir)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	wantGroups := map[string][]string{"core": {"api", "worker"}, "data": {"postgres"}}
	if !reflect.DeepEqual(rw.Manifest.Groups, wantGroups) {
		t.Errorf("groups = %v, want %v", rw.Manifest.Groups, wantGroups)
	}
	wantDeps := map[string][]string{"api": {"postgres"}, "worker": {"postgres"}}
	if !reflect.DeepEqual(rw.Manifest.Dependencies, wantDeps) {
		t.Errorf("dependencies = %v, want %v", rw.Manifest.Dependencies, wantDeps)
	}
}

func TestSetGroupMembersCreatesMissingSections(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, WorkspaceManifestFileName), `version: 1
workspace:
  name: test-ws
  repoDiscovery:
    mode: explicit
    repos:
      - ./repos/api
`)

	if err := SetGroupMembers(dir, "core", []string{"api"}); err != nil {
		t.Fatalf("SetGroupMembers: %v", err)
	}
	if err := SetServiceDependencies(dir, "api", []string{"postgres"}); err != nil {
		t.Fatalf("SetServiceDependencies: %v", err)
	}

	m, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest: %v", err)
	}
	if !reflect.DeepEqual(m.Groups, map[string][]string{"core": {"api"}}) {
		t.Errorf("groups = %v", m.Groups)
	}
	if !reflect.DeepEqual(m.Dependencies, map[string][]string{"api": {"postgres"}}) {
		t.Errorf("dependencies = %v", m.Dependencies)
	}
}

func TestSetGroupMembersRequiresWorkspaceManifest(t *testing.T) {
	if err := SetGroupMembers(t.TempDir(), "core", []string{"api"}); err == nil {
		t.Fatal("expected an error when the workspace has no manifest")
	}
	if err := SetServiceDependencies(t.TempDir(), "api", []string{"postgres"}); err == nil {
		t.Fatal("expected an error when the workspace has no manifest")
	}
}
