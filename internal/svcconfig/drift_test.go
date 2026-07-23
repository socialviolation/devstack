package svcconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

const deployment = `apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
        - name: app
          env:
            - name: OPENAI_MODEL
              value: gpt-small
            - name: OPENAI_MODEL_SPEC
              value: gpt-large
            - name: MIXED_IMPORT
              value: "off"
            - name: PORT
              value: "8080"
            - name: AZURE_SQL_CONNECTION_STRING
              valueFrom:
                secretKeyRef:
                  name: sql
                  key: conn
`

func driftService(t *testing.T, portEnv string) config.ResolvedService {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cicd"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cicd", "deployment.yml"), []byte(deployment), 0644); err != nil {
		t.Fatal(err)
	}
	return config.ResolvedService{
		Name:     "nxFileProcessor",
		RepoPath: dir,
		Manifest: &config.ServiceManifest{
			Service: config.ServiceManifestService{Name: "nxFileProcessor"},
			Config:  config.ServiceConfig{Sources: []string{"cicd/deployment.yml"}, PortEnv: portEnv},
		},
	}
}

func byKey(entries []DriftEntry) map[string]DriftEntry {
	out := make(map[string]DriftEntry, len(entries))
	for _, e := range entries {
		out[e.Key] = e
	}
	return out
}

// The reported failure: a key the deployment sets is absent locally, so the
// service silently runs on its code default instead of erroring.
func TestDriftReportsDeclaredKeyMissingLocally(t *testing.T) {
	svc := driftService(t, "PORT")
	entries, err := Drift(svc, map[string]string{
		"OPENAI_MODEL":                "gpt-small",
		"MIXED_IMPORT":                "off",
		"PORT":                        "5178",
		"AZURE_SQL_CONNECTION_STRING": "Server=localhost",
	})
	if err != nil {
		t.Fatal(err)
	}

	got := byKey(entries)
	spec, ok := got["OPENAI_MODEL_SPEC"]
	if !ok {
		t.Fatalf("Drift() did not report the missing key; got %+v", entries)
	}
	if spec.Kind != DriftMissing {
		t.Errorf("OPENAI_MODEL_SPEC kind = %q, want %q", spec.Kind, DriftMissing)
	}
	if spec.Declared != "gpt-large" {
		t.Errorf("OPENAI_MODEL_SPEC declared = %q, want gpt-large", spec.Declared)
	}
	if _, reported := got["OPENAI_MODEL"]; reported {
		t.Errorf("a key that matches the deployment must not be reported: %+v", got["OPENAI_MODEL"])
	}
}

// The allocated local port is meant to differ from the deployment's, so the key
// named by config.portEnv is never drift.
func TestDriftIgnoresThePortEnv(t *testing.T) {
	svc := driftService(t, "PORT")
	entries, err := Drift(svc, map[string]string{"PORT": "5178"})
	if err != nil {
		t.Fatal(err)
	}
	if _, reported := byKey(entries)["PORT"]; reported {
		t.Errorf("portEnv must not be reported as drift; got %+v", entries)
	}
}

// Without a portEnv declaration the port is an ordinary key and does drift.
func TestDriftReportsPortWhenNotDeclaredAsPortEnv(t *testing.T) {
	svc := driftService(t, "")
	entries, err := Drift(svc, map[string]string{"PORT": "5178"})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := byKey(entries)["PORT"]
	if !ok || got.Kind != DriftDiffers {
		t.Errorf("PORT = %+v (present=%v), want a %q entry", got, ok, DriftDiffers)
	}
}

// A secret-backed key with no local value needs a credential before the code
// path that reads it can run — distinct from a plain missing value.
func TestDriftSeparatesSecretBackedKeys(t *testing.T) {
	svc := driftService(t, "PORT")
	entries, err := Drift(svc, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	got := byKey(entries)
	if got["AZURE_SQL_CONNECTION_STRING"].Kind != DriftSecretMissing {
		t.Errorf("secret key kind = %q, want %q", got["AZURE_SQL_CONNECTION_STRING"].Kind, DriftSecretMissing)
	}
	if got["OPENAI_MODEL"].Kind != DriftMissing {
		t.Errorf("literal key kind = %q, want %q", got["OPENAI_MODEL"].Kind, DriftMissing)
	}
}

// A locally supplied secret is satisfied, and its value never reaches the report.
func TestDriftSuppliedSecretIsNotReportedAndStaysRedacted(t *testing.T) {
	svc := driftService(t, "PORT")
	entries, err := Drift(svc, map[string]string{
		"AZURE_SQL_CONNECTION_STRING": "Server=localhost;Password=hunter2",
		"OPENAI_MODEL":                "gpt-small",
		"OPENAI_MODEL_SPEC":           "gpt-large",
		"MIXED_IMPORT":                "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Drift() = %+v, want none — every declared key is satisfied", entries)
	}
}

// Missing entries sort ahead of informational ones so the actionable finding
// leads the report.
func TestDriftOrdersMissingFirst(t *testing.T) {
	svc := driftService(t, "PORT")
	entries, err := Drift(svc, map[string]string{
		"OPENAI_MODEL":                "gpt-different",
		"MIXED_IMPORT":                "off",
		"AZURE_SQL_CONNECTION_STRING": "Server=localhost",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 || entries[0].Kind != DriftMissing {
		t.Fatalf("first entry = %+v, want a %q entry; all: %+v", entries[0], DriftMissing, entries)
	}
	if last := entries[len(entries)-1]; last.Kind != DriftDiffers {
		t.Errorf("last entry = %+v, want a %q entry", last, DriftDiffers)
	}
}

// A service that declares no config sources has nothing to compare against.
func TestDriftWithoutConfigSources(t *testing.T) {
	svc := config.ResolvedService{
		Name:     "frontend",
		RepoPath: t.TempDir(),
		Manifest: &config.ServiceManifest{Service: config.ServiceManifestService{Name: "frontend"}},
	}
	entries, err := Drift(svc, map[string]string{"ANY": "thing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("Drift() = %+v, want none", entries)
	}
}

func TestRenderNamesTheServiceAndSummarises(t *testing.T) {
	svc := driftService(t, "PORT")
	entries, err := Drift(svc, map[string]string{"AZURE_SQL_CONNECTION_STRING": "x"})
	if err != nil {
		t.Fatal(err)
	}
	out := Render("nxFileProcessor", entries)
	for _, want := range []string{"nxFileProcessor", "OPENAI_MODEL_SPEC", string(DriftMissing)} {
		if !contains(out, want) {
			t.Errorf("Render() missing %q:\n%s", want, out)
		}
	}
	if clean := Render("frontend", nil); !contains(clean, "no drift") {
		t.Errorf("Render() with no entries = %q, want a no-drift line", clean)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
