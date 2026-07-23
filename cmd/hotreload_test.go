package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveRunScriptExpandsNpmScript(t *testing.T) {
	dir := t.TempDir()
	pkg := `{"scripts":{"start":"npm run prebuild && ng serve --configuration=development","build":"ng build"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatal(err)
	}
	if got := resolveRunScript("npm run start", dir); !looksHotReloading(got) {
		t.Errorf("resolveRunScript(npm run start) = %q, expected it to resolve to a hot-reloading script", got)
	}
	if got := resolveRunScript("npm run build", dir); looksHotReloading(got) {
		t.Errorf("resolveRunScript(npm run build) = %q, should not be hot-reloading", got)
	}
	if got := resolveRunScript("dotnet run", dir); got != "dotnet run" {
		t.Errorf("resolveRunScript(dotnet run) = %q, want unchanged", got)
	}
	if got := resolveRunScript("npm run start", t.TempDir()); got != "npm run start" {
		t.Errorf("resolveRunScript with no package.json = %q, want unchanged", got)
	}
}

func TestHotReloadInstructionsQualifiesRestartWithStack(t *testing.T) {
	dir := t.TempDir()
	manifest := "version: 1\n\nservice:\n  name: api\n\nruntime:\n  run:\n    command: \"go run .\"\n"
	if err := os.WriteFile(filepath.Join(dir, "devstack.service.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	base := hotReloadInstructions("api", dir, "")
	if !strings.Contains(base, "does NOT hot-reload") {
		t.Fatalf("expected the non-reloading verdict:\n%s", base)
	}
	if !strings.Contains(base, "`devstack restart api`") {
		t.Fatalf("base restart hint missing:\n%s", base)
	}
	if strings.Contains(base, "devstack restart api --stack") {
		t.Fatalf("base restart hint should not name a stack:\n%s", base)
	}

	stacked := hotReloadInstructions("api", dir, "import-review")
	if !strings.Contains(stacked, "`devstack restart api --stack import-review`") {
		t.Fatalf("stack restart hint missing:\n%s", stacked)
	}
}

func TestBuildAgentInstructionsAnnouncesStackWorktree(t *testing.T) {
	stacked := buildAgentInstructions("api", t.TempDir(), "/home/dev/navexa", "import-review")
	if !strings.Contains(stacked, "feature stack `import-review`'s worktree") {
		t.Fatalf("missing stack worktree announcement:\n%s", stacked)
	}
	if !strings.Contains(stacked, "--stack import-review") {
		t.Fatalf("missing stack targeting hint:\n%s", stacked)
	}

	base := buildAgentInstructions("api", t.TempDir(), "/home/dev/navexa", "")
	if strings.Contains(base, "worktree.**") {
		t.Fatalf("base block should not announce a stack worktree:\n%s", base)
	}
}

func TestLooksHotReloading(t *testing.T) {
	reloading := []string{
		"dotnet watch run",
		"dotnet watch --project src/Api",
		"air",
		"air -c .air.toml",
		"reflex -r '\\.go$' -s -- go run .",
		"next dev",
		"npm run dev",
		"pnpm dev",
		"vite",
		"uvicorn app:app --reload",
		"gunicorn app --reload",
		"node --watch index.js",
		"ng serve",
		"watchexec -- go run .",
		"cargo watch -x run",
	}
	for _, c := range reloading {
		if !looksHotReloading(c) {
			t.Errorf("looksHotReloading(%q) = false, want true", c)
		}
	}

	static := []string{
		"dotnet run",
		"dotnet run --no-launch-profile --urls http://localhost:${self.port.http}",
		"go run .",
		"go run ./cmd/api",
		"python app.py",
		"node index.js",
		"./server",
		"java -jar app.jar",
		"npm start",
	}
	for _, c := range static {
		if looksHotReloading(c) {
			t.Errorf("looksHotReloading(%q) = true, want false", c)
		}
	}
}
