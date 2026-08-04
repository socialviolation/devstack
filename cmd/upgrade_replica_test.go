package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
)

// The reader has to see the size of the job before agreeing to it: a worktree
// per repository, and a dependency install per worktree. The count is a
// repository count, so a repository that holds two services is one worktree.
func TestReplicaDetectorCountsRepositoriesAndBuildsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "shop")
	gitRepoWith(t, filepath.Join(root, "mono"), map[string]string{"api": "api", "web": "web"})
	ws := workspaceAt(t, root, "shop", "mono/api", "mono/web")

	pending, why, err := detectReplica(ws)
	if err != nil {
		t.Fatalf("detectReplica() = %v", err)
	}
	if !pending {
		t.Error("a workspace with no replica must read as pending")
	}
	for _, want := range []string{"no replica yet", "2 services in 1 repository", "dependency install"} {
		if !strings.Contains(why, want) {
			t.Errorf("the detector says %q, which does not state %q", why, want)
		}
	}
	if _, err := os.Stat(replica.Root(ws)); !os.IsNotExist(err) {
		t.Errorf("the detector built a replica at %s", replica.Root(ws))
	}
}

// A service directory in no git repository stops the whole replica, because
// devstack cuts git worktrees. The report has to name the service and the path,
// or the reader has 16 services to search.
func TestReplicaDetectorNamesAServiceThatIsInNoRepository(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "loose")
	writeAt(t, filepath.Join(root, "api", config.ServiceManifestFileName), serviceManifest("api"))
	ws := workspaceAt(t, root, "loose", "api")

	_, _, err := detectReplica(ws)
	if err == nil {
		t.Fatal("a service in no git repository must block the replica")
	}
	for _, want := range []string{"api", filepath.Join(root, "api")} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the blocker does not state %q: %v", want, err)
		}
	}
}

// A replica that is already built must not read as work still to do, even where
// no record says devstack built it.
func TestReplicaDetectorSaysWhenTheReplicaIsBuilt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "shop")
	gitRepoWith(t, filepath.Join(root, "api"), map[string]string{".": "api"})
	ws := workspaceAt(t, root, "shop", "api")
	if err := buildBase(ws); err != nil {
		t.Fatalf("buildBase() = %v", err)
	}

	pending, why, err := detectReplica(ws)
	if err != nil {
		t.Fatalf("detectReplica() = %v", err)
	}
	if pending {
		t.Errorf("a built replica reads as pending: %q", why)
	}
	if !strings.Contains(why, "the replica is built") {
		t.Errorf("the detector says %q", why)
	}
}

// The patch builds the replica in this process. `upgrade` runs `devstack
// migrate` through the binary it just installed, so the code that runs here is
// already the new one.
func TestReplicaPatchBuildsTheReplicaAndNamesIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "shop")
	gitRepoWith(t, filepath.Join(root, "api"), map[string]string{".": "api"})
	ws := workspaceAt(t, root, "shop", "api")

	res, err := runReplicaPatch(ws)
	if err != nil {
		t.Fatalf("runReplicaPatch() = %v", err)
	}
	if !res.Changed {
		t.Fatalf("the build reports no change:\n%s", strings.Join(res.Lines, "\n"))
	}
	if len(res.Items) != 1 || res.Items[0].Path != replica.Root(ws) {
		t.Errorf("the result does not name the replica: %+v", res.Items)
	}
	if !config.HasWorkspaceManifest(replica.Root(ws)) {
		t.Errorf("no replica at %s", replica.Root(ws))
	}
}

// Every one of these still parses and no longer does what it did. Nothing else
// reports them, so upgrade is where the reader learns.
func TestDeprecationsNameTheReplacementForEveryChangedHabit(t *testing.T) {
	var b strings.Builder
	writeDeprecations(&b)
	got := b.String()

	for _, want := range []string{
		"devstack base sync",
		"--stack base",
		"devstack service start|stop|restart",
		"devstack env use",
		"git pull",
		"Restart the session",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the deprecations do not state %q:\n%s", want, got)
		}
	}
}
