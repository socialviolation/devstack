package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

// The doctor is where a reader meets this residue, and it used to name only
// `devstack migrate` — the command that changes files in repositories devstack
// does not own. The read-only preview has to come first, so a reader can see
// what goes before anything goes.
func TestDoctorPointsAtThePreviewBeforeTheMigration(t *testing.T) {
	ws, _ := migrateToolWorkspace(t)

	var b strings.Builder
	if n := reportDevstackResidue(&b, ws.Path); n != 2 {
		t.Fatalf("reportDevstackResidue() = %d files, want 2:\n%s", n, b.String())
	}
	got := b.String()

	preview := strings.Index(got, "devstack migrate --list")
	if preview < 0 {
		t.Fatalf("the doctor never names the read-only preview:\n%s", got)
	}
	run := strings.Index(got, "Then remove it: devstack migrate")
	if run < 0 {
		t.Fatalf("the doctor never names the command that removes the residue:\n%s", got)
	}
	if preview > run {
		t.Errorf("the doctor names the migration before the preview:\n%s", got)
	}
}

// writeManifestTree lays down one workspace manifest and one service manifest
// per name, so a test can state what a checkout or a replica declares.
func writeManifestTree(t *testing.T, root, name string, services []string) {
	t.Helper()
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := "version: 1\nworkspace:\n  name: " + name + "\n  repoDiscovery:\n    mode: explicit\n    repos:\n"
	for _, s := range services {
		manifest += "      - ./" + s + "\n"
		dir := filepath.Join(root, s)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "devstack.service.yaml"),
			"version: 1\nservice:\n  name: "+s+"\nruntime:\n  run:\n    command: go run .\n")
	}
	writeFile(t, filepath.Join(root, config.WorkspaceManifestFileName), manifest)
}

// doctorDriftWorkspace registers a workspace whose replica declares its own set
// of services, so a test can make the two disagree. A nil replicaServices builds
// no replica at all.
func doctorDriftWorkspace(t *testing.T, templateServices, replicaServices []string) *workspace.Workspace {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "navexa")
	writeManifestTree(t, root, "navexa", templateServices)

	ws := &workspace.Workspace{Name: "navexa", Path: root, TiltPort: 10350}
	if replicaServices != nil {
		writeManifestTree(t, replica.Root(ws), "navexa", replicaServices)
	}
	if err := workspace.Register(*ws); err != nil {
		t.Fatalf("register: %v", err)
	}
	return ws
}

// The failure that started this: a repository was split, its root manifest was
// deleted, and the replica kept the copy. The daemon ran a service the
// workspace no longer declares, and `devstack service stop` could not name it.
func TestDoctorNamesAServiceOnlyTheReplicaDeclares(t *testing.T) {
	ws := doctorDriftWorkspace(t, []string{"api"}, []string{"api", "ghost"})

	var b strings.Builder
	reportWorkspaceDrift(&b, ws.Path)
	got := b.String()

	if !strings.Contains(got, "replica drift") {
		t.Fatalf("the doctor reports no replica drift:\n%s", got)
	}
	if !strings.Contains(got, "ghost") || !strings.Contains(got, "the replica declares this service, and the workspace does not") {
		t.Errorf("the doctor never names the service only the replica declares:\n%s", got)
	}
	if !strings.Contains(got, "devstack workspace up") || !strings.Contains(got, "devstack base sync") {
		t.Errorf("the doctor names no repair for the drift:\n%s", got)
	}
}

// The other direction: somebody added a service to the workspace, and nobody
// built the replica again. Base runs without it.
func TestDoctorNamesAServiceTheReplicaDoesNotHave(t *testing.T) {
	ws := doctorDriftWorkspace(t, []string{"api", "web"}, []string{"api"})

	var b strings.Builder
	reportWorkspaceDrift(&b, ws.Path)
	got := b.String()

	if !strings.Contains(got, "replica drift") {
		t.Fatalf("the doctor reports no replica drift:\n%s", got)
	}
	if !strings.Contains(got, "web") || !strings.Contains(got, "the workspace declares this service, and the replica does not") {
		t.Errorf("the doctor never names the service the replica is missing:\n%s", got)
	}
}

// The replica manifest lists a repository that holds no service manifest, so the
// replica does not resolve at all. The doctor has to name that repository: a
// replica that fails to resolve is the one a reader can least diagnose.
func TestDoctorNamesAReplicaRepositoryWithNoServiceManifest(t *testing.T) {
	ws := doctorDriftWorkspace(t, []string{"api"}, []string{"api"})
	gone := filepath.Join(replica.Root(ws), "api", "devstack.service.yaml")
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	reportWorkspaceDrift(&b, ws.Path)
	got := b.String()

	if !strings.Contains(got, "replica drift") {
		t.Fatalf("the doctor reports no replica drift:\n%s", got)
	}
	if !strings.Contains(got, "the replica lists this repository, and it holds no service manifest") {
		t.Errorf("the doctor never names the repository the replica can not resolve:\n%s", got)
	}
	if !strings.Contains(got, filepath.Join(replica.Root(ws), "api")) {
		t.Errorf("the doctor never names the path of that repository:\n%s", got)
	}
}

// No replica is not a stale replica. A workspace that has never built one keeps
// the message that tells the reader to build it, and gets no drift report.
func TestDoctorReportsNoDriftWhenThereIsNoReplica(t *testing.T) {
	ws := doctorDriftWorkspace(t, []string{"api"}, nil)

	var b strings.Builder
	reportWorkspaceDrift(&b, ws.Path)
	got := b.String()

	if !strings.Contains(got, "devstack has built no replica for this workspace") {
		t.Fatalf("the doctor lost the message for a workspace with no replica:\n%s", got)
	}
	if strings.Contains(got, "replica drift") {
		t.Errorf("the doctor reports drift for a replica that does not exist:\n%s", got)
	}
}
