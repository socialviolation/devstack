package cmd

import "testing"

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
