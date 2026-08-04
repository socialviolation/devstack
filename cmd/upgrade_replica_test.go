package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/workspace"
)

// The replica is not a migration, so the upgrade builds it. A machine that ran
// an upgrade must have one, and nothing else in step 2 builds it.
func TestEnsureReplicasBuildsTheReplicaThatIsMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "shop")
	gitRepoWith(t, filepath.Join(root, "api"), map[string]string{".": "api"})
	ws := workspaceAt(t, root, "shop", "api")
	if err := workspace.Register(*ws); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	if err := ensureReplicas(&b); err != nil {
		t.Fatalf("ensureReplicas() = %v", err)
	}
	if !config.HasWorkspaceManifest(replica.Root(ws)) {
		t.Fatalf("no replica at %s:\n%s", replica.Root(ws), b.String())
	}
	for _, want := range []string{"shop", "dependency install"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("the report never states %q:\n%s", want, b.String())
		}
	}
}

// A replica that is built already is left alone. A build cuts a worktree for
// every repository, and each one needs its own dependency install.
func TestEnsureReplicasLeavesABuiltReplicaAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "shop")
	gitRepoWith(t, filepath.Join(root, "api"), map[string]string{".": "api"})
	ws := workspaceAt(t, root, "shop", "api")
	if err := workspace.Register(*ws); err != nil {
		t.Fatal(err)
	}
	if err := buildBase(ws); err != nil {
		t.Fatalf("buildBase() = %v", err)
	}

	var b strings.Builder
	if err := ensureReplicas(&b); err != nil {
		t.Fatalf("ensureReplicas() = %v", err)
	}
	if b.String() != "" {
		t.Errorf("a built replica must produce no report:\n%s", b.String())
	}
}

// A service directory in no git repository stops that one replica, because
// devstack cuts git worktrees. The report has to name the service and the path,
// and the workspaces after it still get theirs.
func TestEnsureReplicasWarnsAndKeepsGoing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	loose := filepath.Join(home, "dev", "aloose")
	writeAt(t, filepath.Join(loose, "api", config.ServiceManifestFileName), serviceManifest("api"))
	if err := workspace.Register(*workspaceAt(t, loose, "aloose", "api")); err != nil {
		t.Fatal(err)
	}

	good := filepath.Join(home, "dev", "shop")
	gitRepoWith(t, filepath.Join(good, "api"), map[string]string{".": "api"})
	shop := workspaceAt(t, good, "shop", "api")
	if err := workspace.Register(*shop); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	err := ensureReplicas(&b)
	if err == nil {
		t.Fatal("a replica that devstack can not build must be reported as an error")
	}
	for _, want := range []string{"api", filepath.Join(loose, "api")} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the blocker does not state %q: %v", want, err)
		}
	}
	if !strings.Contains(b.String(), "devstack base sync") {
		t.Errorf("the report never names the command that builds it later:\n%s", b.String())
	}
	if !config.HasWorkspaceManifest(replica.Root(shop)) {
		t.Errorf("one blocked workspace stopped the workspaces after it:\n%s", b.String())
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
