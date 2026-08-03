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

// The reader has to see the size of the job before agreeing to it: a worktree
// per repository, and a dependency install per worktree. The count is a
// repository count, so a repository that holds two services is one worktree.
func TestUpgradeReportCountsRepositoriesAndBuildsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "shop")
	gitRepoWith(t, filepath.Join(root, "mono"), map[string]string{"api": "api", "web": "web"})
	ws := workspaceAt(t, root, "shop", "mono/api", "mono/web")

	plans := planReplicas([]workspace.Workspace{*ws})
	if len(plans) != 1 {
		t.Fatalf("planReplicas returned %d plans", len(plans))
	}
	p := plans[0]
	if p.Blocker != nil {
		t.Fatalf("plan blocked: %v", p.Blocker)
	}
	if p.Built {
		t.Errorf("a workspace with no replica is reported as built")
	}
	if p.Services != 2 || p.Repos != 1 {
		t.Errorf("plan = %d services in %d repositories, want 2 in 1", p.Services, p.Repos)
	}

	var b strings.Builder
	writeReplicaReport(&b, plans, false)
	got := b.String()
	for _, want := range []string{"shop", "no replica yet", "2 services in 1 repository", "dependency"} {
		if !strings.Contains(got, want) {
			t.Errorf("the report does not state %q:\n%s", want, got)
		}
	}

	if _, err := os.Stat(replica.Root(ws)); !os.IsNotExist(err) {
		t.Errorf("the report built a replica at %s", replica.Root(ws))
	}
}

// A service directory in no git repository stops the whole replica, because
// devstack cuts git worktrees. The report has to name the service and the path,
// or the reader has 16 services to search.
func TestUpgradeReportNamesAServiceThatIsInNoRepository(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "loose")
	writeAt(t, filepath.Join(root, "api", config.ServiceManifestFileName), serviceManifest("api"))
	ws := workspaceAt(t, root, "loose", "api")

	plans := planReplicas([]workspace.Workspace{*ws})
	if plans[0].Blocker == nil {
		t.Fatal("a service in no git repository must block the replica")
	}

	var b strings.Builder
	writeReplicaReport(&b, plans, false)
	got := b.String()
	for _, want := range []string{"can not build a replica", "api", filepath.Join(root, "api")} {
		if !strings.Contains(got, want) {
			t.Errorf("the report does not state %q:\n%s", want, got)
		}
	}
}

// A replica that is already built must not read as work still to do.
func TestUpgradeReportSaysWhenTheReplicaIsBuilt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, "dev", "shop")
	gitRepoWith(t, filepath.Join(root, "api"), map[string]string{".": "api"})
	ws := workspaceAt(t, root, "shop", "api")
	if err := buildBase(ws); err != nil {
		t.Fatalf("buildBase() = %v", err)
	}

	var b strings.Builder
	writeReplicaReport(&b, planReplicas([]workspace.Workspace{*ws}), false)
	if got := b.String(); !strings.Contains(got, "replica built") {
		t.Errorf("the report does not report the replica as built:\n%s", got)
	}
}

// The build runs the newly installed binary, and names the workspace. This
// process is the old build: it does not hold the code that cuts the replica the
// user is upgrading to.
func TestReplicaBuildRunsTheInstalledBinaryPerWorkspace(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	bin := stubDevstack(t, log, 0)

	err := buildReplicas(bin, []workspace.Workspace{
		{Name: "alpha", Path: t.TempDir()},
		{Name: "beta", Path: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("buildReplicas() = %v", err)
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want one invocation per workspace, got %d: %q", len(lines), lines)
	}
	for i, name := range []string{"alpha", "beta"} {
		if !strings.HasSuffix(lines[i], "base build --workspace "+name) {
			t.Errorf("invocation %d = %q, want a replica build for %s", i, lines[i], name)
		}
	}
}

// A machine with four replicas out of five is better than a machine with one,
// so one failure must not strand the workspaces after it.
func TestReplicaBuildReportsFailureAndStillAttemptsEveryWorkspace(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	bin := stubDevstack(t, log, 1)

	err := buildReplicas(bin, []workspace.Workspace{
		{Name: "alpha", Path: t.TempDir()},
		{Name: "beta", Path: t.TempDir()},
	})
	if err == nil {
		t.Fatal("a failed replica build must be reported")
	}
	for _, name := range []string{"alpha", "beta"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error names no failure for %s: %v", name, err)
		}
	}
	data, _ := os.ReadFile(log)
	if n := len(strings.Split(strings.TrimSpace(string(data)), "\n")); n != 2 {
		t.Errorf("every workspace must still be attempted, got %d", n)
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
