package config

import (
	"path/filepath"
	"testing"
)

func TestServiceManifestDecodesTasks(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, ServiceManifestFileName), `version: 1
service:
  name: api
runtime:
  run:
    command: go run .
tasks:
  test:
    run: go test ./...
    workDir: src
    timeout: 2m
    env:
      API: ${self.url}
`)
	m, err := LoadServiceManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := m.Tasks["test"]
	if got.Run != "go test ./..." || got.WorkDir != "src" || got.Timeout != "2m" || got.Env["API"] != "${self.url}" {
		t.Errorf("tasks.test = %+v", got)
	}
}

func TestResolvedTimeout(t *testing.T) {
	if d, err := (ServiceTask{}).ResolvedTimeout(); err != nil || d != DefaultTaskTimeout {
		t.Errorf("empty timeout = %s, %v, want the default", d, err)
	}
	for _, bad := range []string{"soon", "0s", "-1m"} {
		if _, err := (ServiceTask{Timeout: bad}).ResolvedTimeout(); err == nil {
			t.Errorf("timeout %q: want an error", bad)
		}
	}
}
