package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

const sharedFixtureWorkspace = `version: 2
workspace:
  name: shop
  # keep this workspace comment
  repoDiscovery:
    mode: explicit
    repos:
      - ./api
      - ./web
      - ./pg
      - ./cache
groups:
  core: [api, web]
  data: [pg, redis-cache]
# dependencies define start order
dependencies:
  api: [pg, web]
  # web waits for the api
  web: [api]
startsAfter:
  web: [redis-cache]
# keep this environments comment
environments:
  dev:
    values:
      API_BASE: ${api.url}
`

const sharedFixtureAPI = `version: 1
service:
  name: api
# keep this api comment
runtime:
  run:
    command: go run .
ports:
  http: 8080
env:
  values:
    DB_PORT: ${pg.port.db}
    WEB: ${web.url}
calls:
  - web
`

type sharedFixture struct {
	ws        *workspace.Workspace
	root      string
	stackRoot string
	apiPath   string
	webPath   string
}

func setupSharedFixture(t *testing.T) sharedFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "dev", "shop")
	stackRoot := filepath.Join(home, "dev", ".devstack-stacks", "shop", "feat")
	files := map[string]string{
		filepath.Join(root, "devstack.workspace.yaml"):           sharedFixtureWorkspace,
		filepath.Join(root, "api", "devstack.service.yaml"):      sharedFixtureAPI,
		filepath.Join(root, "web", "devstack.service.yaml"):      "version: 1\nservice:\n  name: web\nruntime:\n  run:\n    command: npm run dev\nports:\n  http: 3000\n",
		filepath.Join(root, "pg", "devstack.service.yaml"):       "version: 1\nservice:\n  name: pg\nruntime:\n  run:\n    command: cd db && docker compose up pg\nports:\n  db: 5432\n",
		filepath.Join(root, "cache", "devstack.service.yaml"):    "version: 1\nservice:\n  name: redis-cache\nruntime:\n  run:\n    command: redis-server\n",
		filepath.Join(stackRoot, "devstack.workspace.yaml"):      "version: 2\nworkspace:\n  name: shop--feat\n",
		filepath.Join(stackRoot, "api", "devstack.service.yaml"): sharedFixtureAPI,
		filepath.Join(stackRoot, "pg", "devstack.service.yaml"):  "version: 1\nservice:\n  name: pg\nruntime:\n  run:\n    command: docker compose up pg\n",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, content)
	}
	ports, err := workspace.AllocatePorts("shop--feat", []string{"api/http", "pg/db"})
	if err != nil {
		t.Fatal(err)
	}
	legacyStore := `[{"name":"feat","base":"shop","root":"` + stackRoot + `","branch":"nick/feat",
"overlay":["api","pg"],
"worktrees":{"api":"` + filepath.Join(stackRoot, "api") + `","pg":"` + filepath.Join(stackRoot, "pg") + `"},
"ports":{"api/http":` + strconv.Itoa(ports["api/http"]) + `,"pg/db":` + strconv.Itoa(ports["pg/db"]) + `},
"created_at":"2026-11-20T10:00:00Z"}]`
	if err := os.MkdirAll(workspace.DataDir("shop"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, stack.StoreFile("shop"), legacyStore)
	return sharedFixture{
		ws:        &workspace.Workspace{Name: "shop", Path: root},
		root:      root,
		stackRoot: stackRoot,
		apiPath:   filepath.Join(root, "api", "devstack.service.yaml"),
		webPath:   filepath.Join(root, "web", "devstack.service.yaml"),
	}
}

func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || strings.HasSuffix(path, "stacks.lock") {
			return err
		}
		data, err := os.ReadFile(path)
		out[path] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func runSharedMigration(t *testing.T, f sharedFixture, opts sharedStacksOptions) string {
	t.Helper()
	var buf bytes.Buffer
	if err := migrateSharedStacks(&buf, f.ws, opts); err != nil {
		t.Fatalf("migrateSharedStacks: %v\n%s", err, buf.String())
	}
	return buf.String()
}

func backupDirs(t *testing.T) []string {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(workspace.DataDir("shop"), "migrate-backup-*"))
	return matches
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSharedStacksDryRunProposesAndChangesNothing(t *testing.T) {
	f := setupSharedFixture(t)
	before := snapshotTree(t, os.Getenv("HOME"))

	out := runSharedMigration(t, f, sharedStacksOptions{})

	for _, want := range []string{
		"pg                           its run command starts a container",
		"redis-cache                  its name names a store",
		"--apply --shared pg,redis-cache",
		"feat                         api, pg -> api",
		"This was a dry run",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"api                          its", "web                          its"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("dry run proposes a stack service (%q):\n%s", unwanted, out)
		}
	}
	after := snapshotTree(t, os.Getenv("HOME"))
	if len(before) != len(after) {
		t.Fatalf("the dry run made or removed files: %d -> %d", len(before), len(after))
	}
	for path, content := range before {
		if after[path] != content {
			t.Errorf("the dry run changed %s", path)
		}
	}
}

func TestSharedStacksApplyWithoutSharedWritesNoSharedList(t *testing.T) {
	f := setupSharedFixture(t)

	out := runSharedMigration(t, f, sharedStacksOptions{Apply: true})

	if !strings.Contains(out, "devstack writes no shared list without --shared") {
		t.Errorf("the output does not name --shared:\n%s", out)
	}
	m, err := config.LoadWorkspaceManifest(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Shared) != 0 {
		t.Errorf("shared = %v, want none without --shared", m.Shared)
	}
	if len(m.Dependencies) != 0 {
		t.Errorf("the edges did not move: dependencies = %v", m.Dependencies)
	}
	if got := m.Groups["data"]; !slices.Equal(got, []string{"pg", "redis-cache"}) {
		t.Errorf("groups.data = %v, want it unchanged with no shared list", got)
	}
}

func TestSharedStacksApplyConvertsTheWorkspace(t *testing.T) {
	f := setupSharedFixture(t)
	origStore := readText(t, stack.StoreFile("shop"))
	origWorkspace := readText(t, config.WorkspaceManifestPath(f.root))

	out := runSharedMigration(t, f, sharedStacksOptions{
		Apply:  true,
		Shared: []string{"pg", "redis-cache"},
		Needs:  map[string][]string{"api": {"pg"}},
	})

	wsText := readText(t, config.WorkspaceManifestPath(f.root))
	for _, want := range []string{"# keep this workspace comment", "# keep this environments comment", "shared: [pg, redis-cache]"} {
		if !strings.Contains(wsText, want) {
			t.Errorf("the workspace manifest lacks %q:\n%s", want, wsText)
		}
	}
	m, err := config.LoadWorkspaceManifest(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Dependencies)+len(m.StartsAfter)+len(m.Calls) != 0 {
		t.Errorf("the old maps are still there:\n%s", wsText)
	}
	if _, ok := m.Groups["data"]; ok {
		t.Errorf("groups.data holds only shared services, and it is still there: %v", m.Groups)
	}
	if !slices.Equal(m.Groups["core"], []string{"api", "web"}) {
		t.Errorf("groups.core = %v, want it unchanged", m.Groups["core"])
	}

	api, err := config.LoadServiceManifestFile(f.apiPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := edgeNames(api.Needs); !slices.Equal(got, []string{"pg"}) {
		t.Errorf("api needs = %v, want [pg]", got)
	}
	if got := edgeNames(api.Calls); !slices.Equal(got, []string{"web"}) {
		t.Errorf("api calls = %v, want [web] with no duplicate", got)
	}
	if !strings.Contains(readText(t, f.apiPath), "# keep this api comment") {
		t.Errorf("the api manifest lost its comment:\n%s", readText(t, f.apiPath))
	}
	web, err := config.LoadServiceManifestFile(f.webPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := edgeNames(web.Calls); !slices.Equal(got, []string{"api", "redis-cache"}) {
		t.Errorf("web calls = %v, want [api redis-cache]", got)
	}

	recs, err := stack.LoadStore("shop")
	if err != nil {
		t.Fatal(err)
	}
	rec := recs[0]
	if !slices.Equal(rec.Members, []string{"api"}) {
		t.Errorf("members = %v, want [api]", rec.Members)
	}
	if rec.Worktrees["pg"] == "" || rec.Ports["api/http"] == 0 || rec.Branch != "nick/feat" {
		t.Errorf("the record lost a worktree, a member port or the branch: %+v", rec)
	}
	if _, ok := rec.Ports["pg/db"]; ok {
		t.Errorf("the record still holds the port of the shared service pg: %v", rec.Ports)
	}
	ledger, err := workspace.LoadPorts("shop--feat")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ledger["pg/db"]; ok || ledger["api/http"] == 0 {
		t.Errorf("ledger = %v, want api/http kept and pg/db released", ledger)
	}
	if _, err := os.Stat(filepath.Join(f.stackRoot, "pg")); err != nil {
		t.Errorf("the pg worktree is gone: %v", err)
	}
	storeText := readText(t, stack.StoreFile("shop"))
	if strings.Contains(storeText, `"overlay"`) || !strings.Contains(storeText, `"stop_at"`) {
		t.Errorf("the store is not in the current schema:\n%s", storeText)
	}
	stackManifest, err := config.LoadWorkspaceManifest(f.stackRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !stackManifest.Partial || !slices.Equal(stackManifest.Workspace.RepoDiscovery.Repos, []string{filepath.Join(f.stackRoot, "api")}) {
		t.Errorf("the stack manifest is not regenerated: %+v", stackManifest)
	}

	dirs := backupDirs(t)
	if len(dirs) != 1 {
		t.Fatalf("backup dirs = %v, want one", dirs)
	}
	if !strings.Contains(out, dirs[0]) {
		t.Errorf("the output does not print the backup path %s:\n%s", dirs[0], out)
	}
	if got := readText(t, filepath.Join(dirs[0], stack.StoreFile("shop"))); got != origStore {
		t.Errorf("the backup of stacks.json is not the file before the write:\n%s", got)
	}
	if got := readText(t, filepath.Join(dirs[0], config.WorkspaceManifestPath(f.root))); got != origWorkspace {
		t.Errorf("the backup of the workspace manifest is not the file before the write:\n%s", got)
	}

	before := snapshotTree(t, os.Getenv("HOME"))
	again := runSharedMigration(t, f, sharedStacksOptions{
		Apply:  true,
		Shared: []string{"pg", "redis-cache"},
		Needs:  map[string][]string{"api": {"pg"}},
	})
	if !strings.Contains(again, "Nothing to change") {
		t.Errorf("the second run found work:\n%s", again)
	}
	after := snapshotTree(t, os.Getenv("HOME"))
	if len(before) != len(after) {
		t.Errorf("the second run made or removed files: %d -> %d", len(before), len(after))
	}
	for path, content := range before {
		if after[path] != content {
			t.Errorf("the second run changed %s", path)
		}
	}
}

func TestSharedStacksKeepsTheCommentAfterTheRemovedMaps(t *testing.T) {
	f := setupSharedFixture(t)

	runSharedMigration(t, f, sharedStacksOptions{Apply: true, Shared: []string{"pg", "redis-cache"}})

	wsText := readText(t, config.WorkspaceManifestPath(f.root))
	envAt := strings.Index(wsText, "environments:")
	commentAt := strings.Index(wsText, "# keep this environments comment")
	if commentAt < 0 || commentAt > envAt {
		t.Errorf("the comment above environments moved or is gone:\n%s", wsText)
	}
}

func TestSharedStacksReportsRefsToStackServices(t *testing.T) {
	f := setupSharedFixture(t)

	out := runSharedMigration(t, f, sharedStacksOptions{Shared: []string{"pg", "redis-cache"}})

	if !strings.Contains(out, "api env.values.WEB") || !strings.Contains(out, "-> web.") {
		t.Errorf("the scan does not report api's ref to web:\n%s", out)
	}
	if !strings.Contains(out, "workspace environments.dev.values.API_BASE") {
		t.Errorf("the scan does not report the environment ref to api:\n%s", out)
	}
	if strings.Contains(out, "env.values.DB_PORT") {
		t.Errorf("the scan reports a ref to a shared service:\n%s", out)
	}
}

func TestSharedStacksRefusesASharedServiceThatNeedsAStackService(t *testing.T) {
	f := setupSharedFixture(t)

	var buf bytes.Buffer
	err := migrateSharedStacks(&buf, f.ws, sharedStacksOptions{Apply: true, Shared: []string{"pg"}, Needs: map[string][]string{"pg": {"api"}}})
	if err == nil || !strings.Contains(err.Error(), "can only need a shared service") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if len(backupDirs(t)) != 0 {
		t.Errorf("a refused run made a backup")
	}
}

func TestStartsContainer(t *testing.T) {
	for cmd, want := range map[string]bool{
		"docker compose up pg":      true,
		"cd db && docker run x":     true,
		"FOO=1 podman run x":        true,
		"exec docker-compose up":    true,
		"go run .":                  false,
		"npm run dockerize":         false,
		"./scripts/start-docker.sh": false,
	} {
		if got := startsContainer(cmd); got != want {
			t.Errorf("startsContainer(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestParseNeeds(t *testing.T) {
	got, err := parseNeeds([]string{"api=pg", "api=web", "web=api"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"api": {"pg", "web"}, "web": {"api"}}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("parseNeeds = %s, want %s", gotJSON, wantJSON)
	}
	if _, err := parseNeeds([]string{"api"}); err == nil {
		t.Error("parseNeeds accepts a pair with no =")
	}
}

func TestSharedStacksKeepsTheOldMapsWhenAnEdgeNamesAnUnknownService(t *testing.T) {
	f := setupSharedFixture(t)
	wsPath := config.WorkspaceManifestPath(f.root)
	writeFile(t, wsPath, strings.Replace(sharedFixtureWorkspace, "web: [api]", "web: [api, ghost]", 1))

	out := runSharedMigration(t, f, sharedStacksOptions{Apply: true, Shared: []string{"pg", "redis-cache"}})

	if !strings.Contains(out, "web -> ghost: the workspace does not declare ghost") {
		t.Errorf("the output does not name the unknown edge:\n%s", out)
	}
	m, err := config.LoadWorkspaceManifest(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Dependencies) == 0 {
		t.Errorf("devstack removed the old maps, and it could not move every edge")
	}
}
