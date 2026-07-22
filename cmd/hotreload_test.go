package cmd

import (
	"os"
	"path/filepath"
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
