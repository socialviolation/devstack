package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func resolvedWith(root string, services map[string]string) *ResolvedWorkspace {
	rw := &ResolvedWorkspace{RootPath: root, Services: map[string]ResolvedService{}}
	for name, repoPath := range services {
		rw.Services[name] = ResolvedService{
			Name:     name,
			RepoPath: repoPath,
			Manifest: &ServiceManifest{},
		}
	}
	return rw
}

func setWorkDir(rw *ResolvedWorkspace, service, dir string) {
	rw.Services[service].Manifest.Runtime.WorkDir = dir
}

func workDir(rw *ResolvedWorkspace, service string) string {
	return rw.Services[service].Manifest.Runtime.WorkDir
}

// A service manifest is committed, so an absolute runtime.workDir in it names a
// directory of the checkout. A copy that keeps that path runs the checkout.
func TestRebaseWorkDirsPointsAWorkDirAtTheCopy(t *testing.T) {
	template := resolvedWith("/dev/tsfc", map[string]string{
		"pete-v": "/dev/tsfc/ptvmcp/.devstack/pete-v",
	})
	setWorkDir(template, "pete-v", "/dev/tsfc/ptvmcp")

	copyRoot := "/dev/.devstack-base/southfoundry"
	copyWS := resolvedWith(copyRoot, map[string]string{
		"pete-v": copyRoot + "/ptvmcp/.devstack/pete-v",
	})
	setWorkDir(copyWS, "pete-v", "/dev/tsfc/ptvmcp")

	if warnings := RebaseWorkDirs(template, copyWS, copyRoot); len(warnings) != 0 {
		t.Fatalf("RebaseWorkDirs() warnings = %v, want none for a repository the workspace holds", warnings)
	}
	if got, want := workDir(copyWS, "pete-v"), copyRoot+"/ptvmcp"; got != want {
		t.Errorf("workDir = %q, want %q", got, want)
	}
}

// A workDir below the repository root moves with the repository, and keeps the
// path below it.
func TestRebaseWorkDirsKeepsThePathBelowTheRepository(t *testing.T) {
	template := resolvedWith("/dev/tsfc", map[string]string{
		"api": "/dev/tsfc/monorepo/services/api",
	})
	setWorkDir(template, "api", "/dev/tsfc/monorepo/services/api/cmd")

	copyRoot := "/dev/.devstack-stacks/tsfc/feat"
	copyWS := resolvedWith(copyRoot, map[string]string{
		"api": copyRoot + "/monorepo/services/api",
	})
	setWorkDir(copyWS, "api", "/dev/tsfc/monorepo/services/api/cmd")

	RebaseWorkDirs(template, copyWS, copyRoot)
	if got, want := workDir(copyWS, "api"), copyRoot+"/monorepo/services/api/cmd"; got != want {
		t.Errorf("workDir = %q, want %q", got, want)
	}
}

// A workDir in a repository the workspace does not hold stays as it is. The copy
// can not hold that code, so a rewrite would name a directory that is not there.
func TestRebaseWorkDirsLeavesAnUnownedDirectoryAndWarns(t *testing.T) {
	template := resolvedWith("/dev/tsfc", map[string]string{
		"agent-rag": "/dev/tsfc/agent-rag",
	})
	setWorkDir(template, "agent-rag", "/dev/tsfc/rutter")

	copyRoot := "/dev/.devstack-base/southfoundry"
	copyWS := resolvedWith(copyRoot, map[string]string{
		"agent-rag": copyRoot + "/agent-rag",
	})
	setWorkDir(copyWS, "agent-rag", "/dev/tsfc/rutter")

	warnings := RebaseWorkDirs(template, copyWS, copyRoot)
	if got := workDir(copyWS, "agent-rag"); got != "/dev/tsfc/rutter" {
		t.Errorf("workDir = %q, want the manifest value unchanged", got)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one", warnings)
	}
	for _, want := range []string{"agent-rag", "/dev/tsfc/rutter"} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("warning %q does not name %q", warnings[0], want)
		}
	}
}

func TestRebaseWorkDirsLeavesARelativeWorkDir(t *testing.T) {
	template := resolvedWith("/dev/tsfc", map[string]string{
		"api": "/dev/tsfc/api",
	})
	setWorkDir(template, "api", "cmd/server")

	copyRoot := "/dev/.devstack-base/tsfc"
	copyWS := resolvedWith(copyRoot, map[string]string{
		"api": copyRoot + "/api",
	})
	setWorkDir(copyWS, "api", "cmd/server")

	if warnings := RebaseWorkDirs(template, copyWS, copyRoot); len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none: a relative workDir already resolves against the copy", warnings)
	}
	if got := workDir(copyWS, "api"); got != "cmd/server" {
		t.Errorf("workDir = %q, want it unchanged", got)
	}
}

// Two repositories, one below the other. The longest root wins, so each workDir
// lands in the worktree of the repository that holds it.
func TestRebaseWorkDirsPrefersTheNearestRepository(t *testing.T) {
	template := resolvedWith("/dev/tsfc", map[string]string{
		"outer": "/dev/tsfc/outer",
		"inner": "/dev/tsfc/outer/vendor/inner",
	})
	setWorkDir(template, "outer", "/dev/tsfc/outer/cmd")
	setWorkDir(template, "inner", "/dev/tsfc/outer/vendor/inner/cmd")

	copyRoot := "/dev/.devstack-base/tsfc"
	copyWS := resolvedWith(copyRoot, map[string]string{
		"outer": copyRoot + "/outer",
		"inner": copyRoot + "/inner",
	})
	setWorkDir(copyWS, "outer", "/dev/tsfc/outer/cmd")
	setWorkDir(copyWS, "inner", "/dev/tsfc/outer/vendor/inner/cmd")

	RebaseWorkDirs(template, copyWS, copyRoot)
	if got, want := workDir(copyWS, "outer"), copyRoot+"/outer/cmd"; got != want {
		t.Errorf("outer workDir = %q, want %q", got, want)
	}
	if got, want := workDir(copyWS, "inner"), filepath.Join(copyRoot, "inner", "cmd"); got != want {
		t.Errorf("inner workDir = %q, want %q", got, want)
	}
}
