package telemetry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatusClassifiesHealthyAndDegradedCases(t *testing.T) {
	workspaceDir := t.TempDir()
	mustWrite(t, filepath.Join(workspaceDir, "devstack.workspace.yaml"), `version: 1
workspace:
  name: playground
  repoDiscovery:
    mode: explicit
    repos:
      - ./services/telemetry-good
      - ./services/telemetry-bad
      - ./services/logs-only
`)
	mustWrite(t, filepath.Join(workspaceDir, "services", "telemetry-good", "devstack.service.yaml"), `version: 1
service:
  name: telemetry-good
runtime:
  run:
    command: python3 app.py
telemetry:
  traces:
    expected: true
  logs:
    expected: true
`)
	mustWrite(t, filepath.Join(workspaceDir, "services", "telemetry-bad", "devstack.service.yaml"), `version: 1
service:
  name: telemetry-bad
runtime:
  run:
    command: python3 app.py
telemetry:
  traces:
    expected: true
  logs:
    expected: true
`)
	mustWrite(t, filepath.Join(workspaceDir, "services", "logs-only", "devstack.service.yaml"), `version: 1
service:
  name: logs-only
runtime:
  run:
    command: python3 app.py
telemetry:
  traces:
    expected: true
  logs:
    expected: true
`)
	mustWrite(t, filepath.Join(workspaceDir, "logs", "collector.jsonl"), "{\"body\":{\"service\":\"telemetry-good\"}}\n")
	mustWrite(t, filepath.Join(workspaceDir, "logs", "telemetry-good.log"), "trace export succeeded\n")
	mustWrite(t, filepath.Join(workspaceDir, "logs", "telemetry-bad.log"), "trace export failed\n")
	mustWrite(t, filepath.Join(workspaceDir, "logs", "logs-only.log"), "application log line\n")
	mustWrite(t, filepath.Join(workspaceDir, "state", "telemetry-bad.mode"), "collector-down\n")
	mustWrite(t, filepath.Join(workspaceDir, "state", "logs-only.mode"), "logs-only\n")

	statuses, err := Status(workspaceDir)
	if err != nil {
		t.Fatalf("Status(): %v", err)
	}
	byService := map[string]ServiceStatus{}
	for _, status := range statuses {
		byService[status.Service] = status
	}
	if byService["telemetry-good"].Confidence != "high" {
		t.Fatalf("telemetry-good confidence = %q", byService["telemetry-good"].Confidence)
	}
	if byService["telemetry-bad"].Confidence != "inconclusive" {
		t.Fatalf("telemetry-bad confidence = %q", byService["telemetry-bad"].Confidence)
	}
	if byService["logs-only"].Confidence != "partial" {
		t.Fatalf("logs-only confidence = %q", byService["logs-only"].Confidence)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
