package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

func inferenceWorkspace(t *testing.T) (root string, cfg *config.WorkspaceConfig) {
	t.Helper()
	root = t.TempDir()
	api := filepath.Join(root, "repos", "api")
	web := filepath.Join(root, "repos", "web")
	for _, d := range []string{api, web} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	return root, &config.WorkspaceConfig{
		ServicePaths: map[string]string{"api": api, "web": web},
		Groups:       map[string][]string{"core": {"api", "web"}, "api": {"web"}},
		Deps:         map[string][]string{},
	}
}

func inDir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

// Naming no service is the common case: an agent or a developer runs
// `devstack service restart` in the repo they are editing. The noun-first move
// made the noun required; the target must stay optional.
func TestServiceActionInfersTheServiceFromTheWorkingDirectory(t *testing.T) {
	root, cfg := inferenceWorkspace(t)
	inDir(t, cfg.ServicePaths["web"])

	got, err := resolveTargetKind(root, "", cfg, targetService)
	if err != nil {
		t.Fatalf("resolveTargetKind(): %v", err)
	}
	if len(got) != 1 || got[0] != "web" {
		t.Fatalf("resolved %v, want [web] from its own directory", got)
	}
}

// A subdirectory of the service repo is still that service — nobody runs
// commands from the repo root every time.
func TestServiceInferenceWorksFromASubdirectory(t *testing.T) {
	root, cfg := inferenceWorkspace(t)
	sub := filepath.Join(cfg.ServicePaths["api"], "src", "handlers")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	inDir(t, sub)

	got, err := resolveTargetKind(root, "", cfg, targetService)
	if err != nil {
		t.Fatalf("resolveTargetKind(): %v", err)
	}
	if len(got) != 1 || got[0] != "api" {
		t.Fatalf("resolved %v, want [api] from a subdirectory", got)
	}
}

// A group cannot be inferred from a directory — directories map to services.
// Guessing one would act on services the caller never named.
func TestGroupActionRefusesToInferFromTheWorkingDirectory(t *testing.T) {
	root, cfg := inferenceWorkspace(t)
	inDir(t, cfg.ServicePaths["api"])

	_, err := resolveTargetKind(root, "", cfg, targetGroup)
	if err == nil {
		t.Fatal("resolveTargetKind() = nil, want a group action to demand a name")
	}
	if !strings.Contains(err.Error(), "name a group") {
		t.Fatalf("error = %v, want it to ask for a group name", err)
	}
}

// "api" is both a service and a group here. The noun decides, which is the
// whole reason the surface became noun-first: navexa has a group "roi" and a
// service aliased "roi", and the verb-first form silently picked one.
func TestTheNounDecidesWhenANameIsBothAServiceAndAGroup(t *testing.T) {
	root, cfg := inferenceWorkspace(t)

	asService, err := resolveTargetKind(root, "api", cfg, targetService)
	if err != nil {
		t.Fatalf("service kind: %v", err)
	}
	if len(asService) != 1 || asService[0] != "api" {
		t.Errorf("as a service = %v, want [api]", asService)
	}

	asGroup, err := resolveTargetKind(root, "api", cfg, targetGroup)
	if err != nil {
		t.Fatalf("group kind: %v", err)
	}
	if len(asGroup) != 1 || asGroup[0] != "web" {
		t.Errorf("as a group = %v, want its member [web]", asGroup)
	}

	if _, err := resolveTargetKind(root, "api", cfg, targetAny); err == nil {
		t.Error("with no noun devstack must refuse rather than pick one silently")
	}
}
