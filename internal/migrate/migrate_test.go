package migrate

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

// manifest is a workspace manifest at one version, with a comment above the
// version and a comment below it. A migration writes into a file that a person
// reads, so the comments are part of what the test guards.
func manifest(name string, version int) string {
	return "# The workspace manifest of " + name + ".\n" +
		"# devstack generates the Tiltfile from this file.\n" +
		"version: " + strconv.Itoa(version) + "\n" +
		"workspace:\n" +
		"  name: " + name + "\n" +
		"  # How devstack finds the services.\n" +
		"  repoDiscovery:\n" +
		"    mode: explicit\n" +
		"    repos:\n" +
		"      - ./api\n"
}

// at builds a registered-shaped workspace whose manifest is at version.
func at(t *testing.T, name string, version int) workspace.Workspace {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, config.WorkspaceManifestFileName), manifest(name, version))
	return workspace.Workspace{Name: name, Path: dir}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func versionOf(t *testing.T, ws workspace.Workspace) int {
	t.Helper()
	v, err := config.WorkspaceVersion(ws.Path)
	if err != nil {
		t.Fatalf("WorkspaceVersion(%s) = %v", ws.Path, err)
	}
	return v
}

// counter is a patch from version 1 to 2 that counts its runs.
func counter(runs *int) Patch {
	return Patch{
		From:  1,
		To:    2,
		Title: "a patch",
		Run: func(ws *workspace.Workspace) (Result, error) {
			*runs++
			return Result{Changed: true, Lines: []string{"    did the work"}}, nil
		},
		Next: func([]Result) []string { return []string{"do the next thing"} },
	}
}

// A workspace at the from version runs the patch, and devstack writes the to
// version into the manifest. That number is the whole of the state.
func TestAPendingPatchRunsAndStampsTheNewVersion(t *testing.T) {
	ws := at(t, "navexa", 1)
	runs := 0

	var b strings.Builder
	if err := Apply(&b, []Patch{counter(&runs)}, []workspace.Workspace{ws}); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if runs != 1 {
		t.Fatalf("the patch ran %d times, want 1:\n%s", runs, b.String())
	}
	if got := versionOf(t, ws); got != 2 {
		t.Fatalf("the manifest is at version %d, want 2", got)
	}
	if !strings.Contains(b.String(), "is at version 2 now") {
		t.Errorf("the report never says the new version:\n%s", b.String())
	}
}

// The version in the manifest is what stops a second run. Nothing on this
// machine remembers anything.
func TestASecondRunDoesNothing(t *testing.T) {
	ws := at(t, "navexa", 1)
	runs := 0
	p := counter(&runs)

	if err := Apply(&strings.Builder{}, []Patch{p}, []workspace.Workspace{ws}); err != nil {
		t.Fatalf("first Apply() = %v", err)
	}

	var b strings.Builder
	if err := Apply(&b, []Patch{p}, []workspace.Workspace{ws}); err != nil {
		t.Fatalf("second Apply() = %v", err)
	}
	if runs != 1 {
		t.Fatalf("the patch ran %d times, want 1: the version must stop the second run:\n%s", runs, b.String())
	}
	if !strings.Contains(b.String(), "this workspace is at version 2") {
		t.Errorf("the report never says which version the workspace is at:\n%s", b.String())
	}
	if !strings.Contains(b.String(), "Every migration is applied") {
		t.Errorf("a run with nothing to do must say so:\n%s", b.String())
	}
}

// The stamp goes beside the version, in the file, and it survives the round trip
// through the yaml editor with every comment in place.
func TestTheRunKeepsTheCommentsAndWritesWhoMigrated(t *testing.T) {
	ws := at(t, "navexa", 1)
	Stamp = "v0.9.9 (abc1234)"
	runs := 0

	if err := Apply(&strings.Builder{}, []Patch{counter(&runs)}, []workspace.Workspace{ws}); err != nil {
		t.Fatalf("Apply() = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(ws.Path, config.WorkspaceManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"# The workspace manifest of navexa.",
		"# devstack generates the Tiltfile from this file.",
		"# How devstack finds the services.",
		"version: 2",
		"v0.9.9 (abc1234)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the manifest lost %q:\n%s", want, got)
		}
	}
}

// A migrated repository carries its version. A teammate who clones it has the
// answer already, and devstack asks them for nothing — with no record of any
// kind on their machine.
func TestAFreshCloneOfAMigratedRepositoryHasNothingPending(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	ws := at(t, "navexa", 1)
	runs := 0
	if err := Apply(&strings.Builder{}, []Patch{counter(&runs)}, []workspace.Workspace{ws}); err != nil {
		t.Fatalf("Apply() = %v", err)
	}

	clone := workspace.Workspace{Name: "clone", Path: t.TempDir()}
	data, err := os.ReadFile(filepath.Join(ws.Path, config.WorkspaceManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(clone.Path, config.WorkspaceManifestFileName), string(data))

	st := List([]Patch{counter(&runs)}, []workspace.Workspace{clone})
	if st[0].Pending() {
		t.Fatalf("a clone of a migrated repository reads as pending: %+v", st[0].Rows)
	}
	if runs != 1 {
		t.Errorf("the patch ran %d times, want 1", runs)
	}
	if found := findFile(t, home, "migrations.json"); found != "" {
		t.Errorf("devstack keeps a record of the migration on this machine: %s", found)
	}
}

func findFile(t *testing.T, root, name string) string {
	t.Helper()
	var found string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == name {
			found = path
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// A manifest that a newer devstack wrote is refused. Running a migration over a
// version this binary does not know would write an older shape into a newer file.
func TestAnUnknownFutureVersionIsRefusedAndNeverMigrated(t *testing.T) {
	ws := at(t, "navexa", 3)
	runs := 0

	var b strings.Builder
	err := Apply(&b, []Patch{counter(&runs)}, []workspace.Workspace{ws})
	if err == nil {
		t.Fatal("a manifest at an unknown version must be refused")
	}
	if runs != 0 {
		t.Fatal("a manifest at an unknown version must not be migrated")
	}
	data, err := os.ReadFile(filepath.Join(ws.Path, config.WorkspaceManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "version: 3") {
		t.Fatalf("devstack changed a manifest it does not understand:\n%s", data)
	}
	for _, want := range []string{"version 3", "version 2", "devstack upgrade"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("the report never states %q:\n%s", want, b.String())
		}
	}

	st := List([]Patch{counter(&runs)}, []workspace.Workspace{ws})
	if st[0].Pending() || st[0].Rows[0].Err == nil {
		t.Errorf("--list must report the refusal, and never a pending migration: %+v", st[0].Rows)
	}
}

// A run that fails leaves the old version in place, so the next run tries again.
// Writing the new version after a failure would call the work done.
func TestAFailedRunLeavesTheOldVersion(t *testing.T) {
	ws := at(t, "navexa", 1)
	p := Patch{
		From: 1, To: 2, Title: "a patch that fails",
		Run:  func(*workspace.Workspace) (Result, error) { return Result{}, errBoom },
		Next: func([]Result) []string { return []string{"do the next thing"} },
	}

	var b strings.Builder
	if err := Apply(&b, []Patch{p}, []workspace.Workspace{ws}); err == nil {
		t.Fatal("a failed patch must be reported as an error")
	}
	if got := versionOf(t, ws); got != 1 {
		t.Fatalf("the manifest is at version %d, want 1: a failed run must not stamp", got)
	}
	if !strings.Contains(b.String(), "failed: boom") {
		t.Errorf("the report never states the failure:\n%s", b.String())
	}
	if strings.Contains(b.String(), "do the next thing") {
		t.Errorf("a patch that changed nothing must not print its next action:\n%s", b.String())
	}
}

// One patch failing must not strand the patches after it. Both are reported.
func TestFailingPatchDoesNotStopTheNextOne(t *testing.T) {
	ws := at(t, "navexa", 1)
	runs := 0
	bad := Patch{
		From: 1, To: 2, Title: "a patch that fails",
		Run: func(*workspace.Workspace) (Result, error) { return Result{}, errBoom },
	}

	var b strings.Builder
	err := Apply(&b, []Patch{bad, counter(&runs)}, []workspace.Workspace{ws})
	if err == nil {
		t.Fatal("a failed patch must be reported as an error")
	}
	if !strings.Contains(err.Error(), "version 1 to 2") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("the error does not name the patch and the cause: %v", err)
	}
	if runs != 1 {
		t.Errorf("the patch after the failure ran %d times, want 1:\n%s", runs, b.String())
	}
}

// A patch's next action must name the workspaces it changed, so the reader knows
// where to act. A workspace that is at the version already is not one of them.
func TestNextReceivesOneResultPerMigratedWorkspace(t *testing.T) {
	var seen []string
	p := Patch{
		From: 1, To: 2, Title: "a patch",
		Run: func(*workspace.Workspace) (Result, error) { return Result{Changed: true}, nil },
		Next: func(res []Result) []string {
			for _, r := range res {
				seen = append(seen, r.Workspace)
			}
			return []string{"done"}
		},
	}

	all := []workspace.Workspace{at(t, "navexa", 1), at(t, "shop", 2), at(t, "tsfc", 1)}
	if err := Apply(&strings.Builder{}, []Patch{p}, all); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if strings.Join(seen, ",") != "navexa,tsfc" {
		t.Errorf("Next saw %v, want the two workspaces the patch migrated", seen)
	}
}

// The version moves the workspace manifest, which is a file a human commits. A
// run that changed no other file still has that one diff to finish.
func TestTheNextActionNamesTheWorkspaceRootEvenWhenNoOtherFileChanged(t *testing.T) {
	ws := at(t, "navexa", 1)
	var seen []Item
	p := Patch{
		From: 1, To: 2, Title: "a patch",
		Run: func(*workspace.Workspace) (Result, error) { return Result{}, nil },
		Next: func(res []Result) []string {
			seen = res[0].Items
			return []string{"NOW COMMIT"}
		},
	}

	var b strings.Builder
	if err := Apply(&b, []Patch{p}, []workspace.Workspace{ws}); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if len(seen) != 1 || seen[0].Path != ws.Path {
		t.Fatalf("the next action names %+v, want the workspace root %s", seen, ws.Path)
	}
	if !strings.Contains(b.String(), "NOW COMMIT") {
		t.Errorf("the note is lost when the run changed no other file:\n%s", b.String())
	}
}

// A patch that names the workspace root itself must not have it named twice.
func TestTheWorkspaceRootIsNamedOnce(t *testing.T) {
	ws := at(t, "navexa", 1)
	var seen []Item
	p := Patch{
		From: 1, To: 2, Title: "a patch",
		Run: func(w *workspace.Workspace) (Result, error) {
			return Result{Changed: true, Items: []Item{{Label: "workspace root", Path: w.Path}}}, nil
		},
		Next: func(res []Result) []string {
			seen = res[0].Items
			return nil
		},
	}

	if err := Apply(&strings.Builder{}, []Patch{p}, []workspace.Workspace{ws}); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if len(seen) != 1 {
		t.Errorf("the workspace root is named %d times: %+v", len(seen), seen)
	}
}

// --list has to show both halves of the truth: the workspace that is current,
// and the one that is not. It changes nothing.
func TestListShowsPendingAndCurrentAndChangesNothing(t *testing.T) {
	old, current := at(t, "navexa", 1), at(t, "shop", 2)
	runs := 0

	st := List([]Patch{counter(&runs)}, []workspace.Workspace{old, current})
	if len(st) != 1 || len(st[0].Rows) != 2 {
		t.Fatalf("List() = %+v, want one patch over two workspaces", st)
	}
	if !st[0].Pending() {
		t.Errorf("the workspace at version 1 does not read as pending: %+v", st[0].Rows)
	}
	if st[0].Rows[1].Pending {
		t.Errorf("the workspace at version 2 reads as pending: %+v", st[0].Rows[1])
	}
	if runs != 0 || versionOf(t, old) != 1 {
		t.Error("List must change nothing")
	}

	var b strings.Builder
	WriteList(&b, st)
	got := b.String()
	for _, want := range []string{
		"version 1 to 2",
		"navexa           pending: this workspace is at version 1, and this devstack needs version 2",
		"shop             nothing to do: this workspace is at version 2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("--list never states %q:\n%s", want, got)
		}
	}
}

// A directory with no workspace manifest holds no version. There is nothing to
// migrate and nothing to stamp, and that is not an error.
func TestAWorkspaceWithNoManifestIsNotPending(t *testing.T) {
	ws := workspace.Workspace{Name: "empty", Path: t.TempDir()}
	runs := 0

	st := List([]Patch{counter(&runs)}, []workspace.Workspace{ws})
	if st[0].Pending() || st[0].Rows[0].Err != nil {
		t.Fatalf("a workspace with no manifest reads as %+v", st[0].Rows[0])
	}
	if err := Apply(&strings.Builder{}, []Patch{counter(&runs)}, []workspace.Workspace{ws}); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if runs != 0 {
		t.Error("a workspace with no manifest must not be migrated")
	}
}

var errBoom = boom{}

type boom struct{}

func (boom) Error() string { return "boom" }

// blocked is a patch that leaves a file only a person can change. It writes no
// new version, so it stays pending.
func blocked() Patch {
	return Patch{
		From:  1,
		To:    2,
		Title: "a patch that can not finish",
		Run: func(ws *workspace.Workspace) (Result, error) {
			return Result{Lines: []string{"    1 file needs a human"}, Incomplete: true}, nil
		},
	}
}

func TestApplyClosesABlockedRunWithTheWorkThatIsLeft(t *testing.T) {
	ws := at(t, "navexa", 1)

	var b strings.Builder
	if err := Apply(&b, []Patch{blocked()}, []workspace.Workspace{ws}); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	got := b.String()
	if strings.Contains(got, "Every migration is applied") {
		t.Errorf("the report says the migration is applied, and it is still pending:\n%s", got)
	}
	if !strings.Contains(got, "NOT FINISHED") {
		t.Errorf("the report never says what is left:\n%s", got)
	}
	if v := versionOf(t, ws); v != 1 {
		t.Errorf("the manifest is at version %d, want 1: a blocked patch writes no version", v)
	}
}
