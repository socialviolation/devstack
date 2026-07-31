package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ctxWorkspace(t *testing.T, ctx WorkspaceContext, servicePaths map[string]string) *ResolvedWorkspace {
	t.Helper()
	rw := &ResolvedWorkspace{
		RootPath: t.TempDir(),
		Manifest: &WorkspaceManifest{Version: 1, Context: ctx},
		Services: map[string]ResolvedService{},
	}
	for name, p := range servicePaths {
		rw.Services[name] = ResolvedService{Name: name, RepoPath: p}
	}
	return rw
}

func writeDoc(t *testing.T, dir, rel, body string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Workspace docs resolve against the workspace root; a service's resolve against
// that service's own repo, so the document sits with the code it describes.
func TestResolveContextDocsScopesPathsToTheirOwner(t *testing.T) {
	svcDir := t.TempDir()
	rw := ctxWorkspace(t, WorkspaceContext{
		Workspace: ContextFiles{"docs/overview.md"},
		Service:   map[string]ContextFiles{"api": {"NOTES.md"}},
	}, map[string]string{"api": svcDir})

	writeDoc(t, rw.RootPath, "docs/overview.md", "# Overview\nworkspace prose")
	writeDoc(t, svcDir, "NOTES.md", "# API\nservice prose")

	docs := ResolveContextDocs(rw, "api")
	if len(docs) != 2 {
		t.Fatalf("docs = %d, want workspace + service", len(docs))
	}
	if docs[0].Scope != "workspace" || !strings.Contains(docs[0].Body, "workspace prose") {
		t.Errorf("first doc = %+v", docs[0])
	}
	if docs[1].Scope != "api" || !strings.Contains(docs[1].Body, "service prose") {
		t.Errorf("second doc = %+v", docs[1])
	}
}

// Standing in one service must not pull in another service's notes.
func TestResolveContextDocsExcludesOtherServices(t *testing.T) {
	apiDir, webDir := t.TempDir(), t.TempDir()
	rw := ctxWorkspace(t, WorkspaceContext{
		Service: map[string]ContextFiles{"api": {"A.md"}, "web": {"B.md"}},
	}, map[string]string{"api": apiDir, "web": webDir})
	writeDoc(t, apiDir, "A.md", "api only")
	writeDoc(t, webDir, "B.md", "web only")

	docs := ResolveContextDocs(rw, "api")
	if len(docs) != 1 || docs[0].Scope != "api" {
		t.Fatalf("docs = %+v, want only the api doc", docs)
	}
}

// An empty service name is the workspace-root case: workspace scope alone.
func TestResolveContextDocsWithNoServiceReturnsEveryService(t *testing.T) {
	apiDir := t.TempDir()
	rw := ctxWorkspace(t, WorkspaceContext{
		Workspace: ContextFiles{"W.md"},
		Service:   map[string]ContextFiles{"api": {"A.md"}},
	}, map[string]string{"api": apiDir})
	writeDoc(t, rw.RootPath, "W.md", "workspace")
	writeDoc(t, apiDir, "A.md", "api")

	if got := len(ResolveContextDocs(rw, "")); got != 2 {
		t.Fatalf("docs = %d, want every scope when no service is named", got)
	}
}

// A declared file that is not on disk is reported, never skipped: silently
// omitting it looks the same as having nothing to say.
func TestResolveContextDocsReportsAMissingFile(t *testing.T) {
	rw := ctxWorkspace(t, WorkspaceContext{Workspace: ContextFiles{"gone.md"}}, nil)
	docs := ResolveContextDocs(rw, "")
	if len(docs) != 1 || !docs[0].Missing {
		t.Fatalf("docs = %+v, want one missing doc", docs)
	}
}

func TestContextFilesAcceptsAStringOrAList(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, WorkspaceManifestFileName), `version: 1
workspace:
  name: playground
  repoDiscovery:
    mode: explicit
    repos: [./api]
context:
  workspace: docs/one.md
  service:
    api: [a.md, b.md]
`)
	m, err := LoadWorkspaceManifest(dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceManifest(): %v", err)
	}
	if got := m.Context.Workspace; len(got) != 1 || got[0] != "docs/one.md" {
		t.Errorf("workspace = %v", got)
	}
	if got := m.Context.Service["api"]; len(got) != 2 || got[1] != "b.md" {
		t.Errorf("service = %v", got)
	}
}

func TestDocTitleReadsTheHeading(t *testing.T) {
	d := ContextDoc{Body: "\n# FX rate testing\n\nbody text"}
	if got := d.DocTitle(); got != "FX rate testing" {
		t.Fatalf("DocTitle() = %q", got)
	}
}

// Team prose is unbounded, and the briefing has a hard host limit, so a long
// document is clipped and told where the rest is.
func TestClipBoundsALongDocAndNamesItsPath(t *testing.T) {
	d := ContextDoc{Path: "/w/notes.md", Body: strings.Repeat("line of prose\n", 200)}
	got := d.Clip(100)
	if len(got) > 200 {
		t.Errorf("Clip() = %d chars, want it bounded", len(got))
	}
	if !strings.Contains(got, "/w/notes.md") {
		t.Errorf("clipped doc must name its path so the rest is findable: %q", got)
	}
}

func TestClipLeavesAShortDocIntact(t *testing.T) {
	d := ContextDoc{Path: "/w/n.md", Body: "short"}
	if got := d.Clip(4000); got != "short" {
		t.Fatalf("Clip() = %q", got)
	}
}
