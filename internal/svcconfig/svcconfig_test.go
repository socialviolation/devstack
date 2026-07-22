package svcconfig

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

const appsettingsJSON = `{
  "ConnectionStrings": {
    "NavexaDatabase": "Server=prod;Password=hunter2",
    "AuditDatabase": "Server=prod-audit"
  },
  "Auth0": { "Domain": "example.auth0.com" },
  "AsxPriceLookupUrl": "http://localhost:7071/api/Asx?code=SECRETKEY123==",
  "CacheConnection": "redis-master:6379,connectTimeout=5000,password=REDISPASS==",
  "FeatureFlags": ["import", "review"]
}`

const deploymentYAML = `apiVersion: v1
kind: ConfigMap
metadata:
  name: ignore-me
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: navexa-api
spec:
  template:
    spec:
      containers:
        - name: navexa-api
          env:
            - name: ASPNETCORE_URLS
              value: "http://+:80"
            - name: ConnectionStrings__NavexaDatabase
              valueFrom:
                secretKeyRef:
                  name: db
                  key: conn
            - name: PeerServiceUrl
              value: "https://peer.prod.example.com"
`

func TestReadAppsettingsFlattens(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "appsettings.json", appsettingsJSON)

	got, err := readSource(dir, "appsettings.json")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ConnectionStrings__NavexaDatabase": "Server=prod;Password=hunter2",
		"ConnectionStrings__AuditDatabase":  "Server=prod-audit",
		"Auth0__Domain":                     "example.auth0.com",
		"AsxPriceLookupUrl":                 "http://localhost:7071/api/Asx?code=SECRETKEY123==",
		"CacheConnection":                   "redis-master:6379,connectTimeout=5000,password=REDISPASS==",
		"FeatureFlags__0":                   "import",
		"FeatureFlags__1":                   "review",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d keys, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q = %q, want %q", k, got[k], v)
		}
	}
}

func TestReadDeploymentSurface(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "cicd/navexa.api.yml", deploymentYAML)

	got, err := readSource(dir, "cicd/navexa.api.yml")
	if err != nil {
		t.Fatal(err)
	}
	if got["ASPNETCORE_URLS"] != "http://+:80" {
		t.Errorf("ASPNETCORE_URLS = %q, want literal", got["ASPNETCORE_URLS"])
	}
	if got["ConnectionStrings__NavexaDatabase"] != externalMarker {
		t.Errorf("valueFrom key = %q, want external marker", got["ConnectionStrings__NavexaDatabase"])
	}
	if got["PeerServiceUrl"] != "https://peer.prod.example.com" {
		t.Errorf("PeerServiceUrl = %q", got["PeerServiceUrl"])
	}
}

func TestReadSourceUnknownType(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.txt", "x")
	if _, err := readSource(dir, "config.txt"); err == nil {
		t.Fatal("expected error for unrecognized source type")
	}
}

func TestReadSourceMissingFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := readSource(dir, "nope.json"); err == nil {
		t.Fatal("expected error naming the missing file")
	}
}

// withStackPorts points the workspace data root at a temp HOME and writes a
// stack's allocated port record so LoadPorts resolves it during the test.
func withStackPorts(t *testing.T, stackName, qualifiedKey string, port int) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".local", "share", "devstack", stackName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "ports.json", `{"`+qualifiedKey+`": `+strconv.Itoa(port)+`}`)
}

func newService(t *testing.T, sources []string, portEnv string) config.ResolvedService {
	dir := t.TempDir()
	writeFile(t, dir, "appsettings.json", appsettingsJSON)
	writeFile(t, dir, "cicd/navexa.api.yml", deploymentYAML)
	return config.ResolvedService{
		Name:     "navexa-api",
		RepoPath: dir,
		Manifest: &config.ServiceManifest{
			Version: 1,
			Service: config.ServiceManifestService{Name: "navexa-api"},
			Config:  config.ServiceConfig{Sources: sources, PortEnv: portEnv},
		},
	}
}

func find(entries []ConfigEntry, key string) (ConfigEntry, bool) {
	for _, e := range entries {
		if e.Key == key {
			return e, true
		}
	}
	return ConfigEntry{}, false
}

func TestEffectiveConfigSourceOrderLaterWins(t *testing.T) {
	withStackPorts(t, "navexa--demo", "navexa-api/http", 20007)
	// appsettings has a literal NavexaDatabase; k8s marks it external. k8s is
	// listed last, so the k8s provenance wins for the shared key.
	svc := newService(t, []string{"appsettings.json", "cicd/navexa.api.yml"}, "")

	entries, err := EffectiveConfig(svc, "navexa--demo")
	if err != nil {
		t.Fatal(err)
	}
	e, ok := find(entries, "ConnectionStrings__NavexaDatabase")
	if !ok {
		t.Fatal("missing ConnectionStrings__NavexaDatabase")
	}
	if e.Source != "k8s" {
		t.Errorf("provenance = %q, want k8s (later source wins)", e.Source)
	}

	// Reverse the order: appsettings now wins.
	svc = newService(t, []string{"cicd/navexa.api.yml", "appsettings.json"}, "")
	entries, err = EffectiveConfig(svc, "navexa--demo")
	if err != nil {
		t.Fatal(err)
	}
	e, _ = find(entries, "ConnectionStrings__NavexaDatabase")
	if e.Source != "appsettings" {
		t.Errorf("provenance = %q, want appsettings (later source wins)", e.Source)
	}
}

func TestEffectiveConfigPortOverride(t *testing.T) {
	withStackPorts(t, "navexa--demo", "navexa-api/http", 20007)
	svc := newService(t, []string{"appsettings.json", "cicd/navexa.api.yml"}, "ASPNETCORE_URLS")

	entries, err := EffectiveConfig(svc, "navexa--demo")
	if err != nil {
		t.Fatal(err)
	}
	e, ok := find(entries, "ASPNETCORE_URLS")
	if !ok {
		t.Fatal("missing ASPNETCORE_URLS")
	}
	if !e.Overridden || e.Source != "stack" {
		t.Errorf("ASPNETCORE_URLS not marked as stack override: %+v", e)
	}
	if e.Value != "http://localhost:20007" {
		t.Errorf("ASPNETCORE_URLS = %q, want http://localhost:20007 (URL-style render)", e.Value)
	}
	if e.Secret {
		t.Error("stack override must never be masked")
	}
}

func TestEffectiveConfigSecretMasking(t *testing.T) {
	withStackPorts(t, "navexa--demo", "navexa-api/http", 20007)
	svc := newService(t, []string{"appsettings.json", "cicd/navexa.api.yml"}, "ASPNETCORE_URLS")

	entries, err := EffectiveConfig(svc, "navexa--demo")
	if err != nil {
		t.Fatal(err)
	}

	audit, _ := find(entries, "ConnectionStrings__AuditDatabase")
	if audit.Value != maskedValue || !audit.Secret {
		t.Errorf("connection string leaked: %q", audit.Value)
	}

	port, _ := find(entries, "ASPNETCORE_URLS")
	if port.Value != "http://localhost:20007" {
		t.Errorf("stack-overridden port must be shown, got %q", port.Value)
	}

	extern, _ := find(entries, "ConnectionStrings__NavexaDatabase")
	if extern.Value != maskedValue {
		t.Errorf("external secretRef must be masked, got %q", extern.Value)
	}

	// Secrets smuggled into a value under an innocent key name must be redacted
	// in place — the exact shape that leaked on real navexa config.
	code, _ := find(entries, "AsxPriceLookupUrl")
	if strings.Contains(code.Value, "SECRETKEY123") || !strings.Contains(code.Value, "Asx") {
		t.Errorf("Azure ?code= key must be redacted, URL kept; got %q", code.Value)
	}
	cache, _ := find(entries, "CacheConnection")
	if strings.Contains(cache.Value, "REDISPASS") {
		t.Errorf("embedded password= leaked in CacheConnection: %q", cache.Value)
	}
}
