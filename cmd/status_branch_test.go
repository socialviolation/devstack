package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fatih/color"

	"github.com/socialviolation/devstack/internal/tilt"
)

// gitRepoOn creates a checkout on branch, with one commit so HEAD resolves.
func gitRepoOn(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-b", branch)
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	run("commit", "-m", "initial")
	return dir
}

func TestReadBranchLabelsReportsBranchAndUncommittedWork(t *testing.T) {
	repo := gitRepoOn(t, "NAV-412-import")
	rows := []statusRow{{service: "api", dir: repo}, {service: "worker", dir: repo}, {service: "no-repo"}}

	labels := readBranchLabels(rows)
	if got := labels[repo]; got != "NAV-412-import" {
		t.Fatalf("branch label = %q, want NAV-412-import", got)
	}
	if got := labels[""]; got != "" {
		t.Fatalf("a service with no source directory got label %q, want empty", got)
	}

	if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readBranchLabels(rows)[repo]; got != "NAV-412-import*" {
		t.Fatalf("dirty checkout label = %q, want NAV-412-import*", got)
	}
}

func TestFitCellFitsTheColumnAndKeepsTheDirtyMarker(t *testing.T) {
	if got := fitCell("main", 10); got != "main" {
		t.Fatalf("short value was altered: %q", got)
	}
	got := fitCell("nvxa-1331-holding-local-currency*", 20)
	if len(got) != 20 {
		t.Fatalf("truncated branch = %q (%d chars), want 20", got, len(got))
	}
	if !strings.HasSuffix(got, "..*") {
		t.Fatalf("truncated branch = %q, want the uncommitted-work marker kept", got)
	}
	if got := fitCell("nx-holding-price-functions-and-then-some", colServiceMax); len(got) != colServiceMax {
		t.Fatalf("truncated service = %q (%d chars), want %d", got, len(got), colServiceMax)
	}
}

func TestStatusTableCapsTheServiceColumn(t *testing.T) {
	long := "nx-holding-price-functions-that-will-not-fit"
	rows := []statusRow{
		{service: long, group: "core", state: "running", ports: ":8080",
			rowColor: color.New(color.FgCyan), stateColor: color.New(color.FgGreen)},
	}

	buf := captureFaint(t)
	renderStatusTable(rows, false)

	if strings.Contains(buf.String(), long) {
		t.Errorf("a %d-char service name printed in full and pushed every later column out:\n%s", len(long), buf.String())
	}
}

func TestStatusTableShowsBranchWithoutExpand(t *testing.T) {
	base := gitRepoOn(t, "main")
	worktree := gitRepoOn(t, "NAV-412-import")

	rows := []statusRow{
		{service: "api", group: "core", state: "running", ports: ":8080", dir: base,
			rowColor: color.New(color.FgCyan), stateColor: color.New(color.FgGreen)},
		{service: "api", group: "stack: agent", state: "running", ports: ":20011", dir: worktree,
			rowColor: color.New(color.FgMagenta), stateColor: color.New(color.FgGreen)},
	}

	buf := captureFaint(t)
	renderStatusTable(rows, false)

	got := buf.String()
	if !strings.Contains(got, "BRANCH") {
		t.Errorf("no BRANCH column in the default table:\n%s", got)
	}
	for _, want := range []string{"main", "NAV-412-import"} {
		if !strings.Contains(got, want) {
			t.Errorf("branch %q missing from the table, so the running copy is unidentified:\n%s", want, got)
		}
	}
}

func runningNamed(name string) tilt.UIResource {
	r := runningResource()
	r.Metadata.Name = name
	return r
}

func TestStackInstancesRunningExcludesBaseAndOtherWorkspaces(t *testing.T) {
	view := &tilt.TiltView{UiResources: []tilt.UIResource{
		runningNamed("navexa:navexa-api"),
		runningNamed("navexa:navexa-api:fx-rates"),
		runningNamed("navexa:nxPriceService:fx-rates"),
		runningNamed("navexa:navexa-api:agent"),
		runningNamed("other:api:agent"),
		resourceNamed("navexa:navexa-frontend:fx-rates"),
	}}

	running, stacks := stackInstancesRunning(view, "navexa")
	if running != 3 {
		t.Errorf("running stack instances = %d, want 3 (base, another workspace and a stopped instance excluded)", running)
	}
	if !reflect.DeepEqual(stacks, []string{"agent", "fx-rates"}) {
		t.Errorf("stacks = %v, want [agent fx-rates]", stacks)
	}
}

func TestStackRunningSummaryNamesTheOnlyStack(t *testing.T) {
	if got := stackRunningSummary(0, nil); got != "" {
		t.Errorf("no stack instances should add nothing to the header, got %q", got)
	}
	if got := stackRunningSummary(3, []string{"fx-rates"}); got != "3 more in stack fx-rates" {
		t.Errorf("summary = %q, want 3 more in stack fx-rates", got)
	}
	// "across 2 stacks" was read as the workspace having 2 stacks. It counts
	// only the ones with something running, so the phrasing says so and points
	// at the command that lists them all.
	want := "4 more running, in 2 stacks — devstack stack list"
	if got := stackRunningSummary(4, []string{"agent", "fx-rates"}); got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}
