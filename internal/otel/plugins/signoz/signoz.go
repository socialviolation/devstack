// Package signoz provides the SigNoz OTEL plugin for devstack.
// It manages the SigNoz docker-compose stack (ClickHouse + SigNoz UI) as companion
// infrastructure, and configures the collector to export to ClickHouse directly.
package signoz

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/socialviolation/devstack/internal/observability"
	obssignoz "github.com/socialviolation/devstack/internal/observability/signoz"
	"github.com/socialviolation/devstack/internal/otel"
	"github.com/socialviolation/devstack/internal/workspace"
)

//go:embed files
var signozFiles embed.FS

// uiPort serves the SigNoz UI and query API. One SigNoz runs per machine, so it
// is fixed rather than per-workspace.
const uiPort = 3301

func init() {
	otel.Register(&SignozPlugin{})
}

// SignozPlugin is the built-in SigNoz observability plugin.
type SignozPlugin struct{}

func (p *SignozPlugin) Name() string { return "signoz" }

// Contribute returns the components and pipelines that export a workspace's
// telemetry into SigNoz's ClickHouse schema.
func (p *SignozPlugin) Contribute(ws *workspace.Workspace) (otel.Contribution, error) {
	return otel.Contribution{
		Connectors: map[string]any{
			"signozmeter": map[string]any{
				"metrics_flush_interval": "1h",
				"dimensions": []any{
					map[string]any{"name": "service.name"},
					map[string]any{"name": "deployment.environment"},
					map[string]any{"name": "host.name"},
				},
			},
		},
		Processors: map[string]any{
			"batch": map[string]any{
				"send_batch_size":     10000,
				"send_batch_max_size": 11000,
				"timeout":             "10s",
			},
			"batch/meter": map[string]any{
				"send_batch_max_size": 25000,
				"send_batch_size":     20000,
				"timeout":             "1s",
			},
			"resourcedetection": map[string]any{
				"detectors": []string{"env", "system"},
				"timeout":   "2s",
			},
			"signozspanmetrics/delta": map[string]any{
				"metrics_exporter":       "signozclickhousemetrics",
				"metrics_flush_interval": "60s",
				"latency_histogram_buckets": []string{
					"100us", "1ms", "2ms", "6ms", "10ms", "50ms", "100ms", "250ms",
					"500ms", "1000ms", "1400ms", "2000ms", "5s", "10s", "20s", "40s", "60s",
				},
				"dimensions_cache_size":   100000,
				"aggregation_temporality": "AGGREGATION_TEMPORALITY_DELTA",
				"enable_exp_histogram":    true,
				"dimensions": []any{
					map[string]any{"name": "service.namespace", "default": "default"},
					map[string]any{"name": "deployment.environment", "default": "default"},
					map[string]any{"name": "signoz.collector.id"},
					map[string]any{"name": "service.version"},
					map[string]any{"name": "host.name"},
				},
			},
		},
		Extensions: map[string]any{
			"health_check": map[string]any{"endpoint": "0.0.0.0:13133"},
			"pprof":        map[string]any{"endpoint": "0.0.0.0:1777"},
		},
		Exporters: map[string]any{
			"clickhousetraces": map[string]any{
				"datasource":                      "tcp://clickhouse:9000/signoz_traces",
				"low_cardinal_exception_grouping": false,
				"use_new_schema":                  true,
			},
			"signozclickhousemetrics": map[string]any{"dsn": "tcp://clickhouse:9000/signoz_metrics"},
			"clickhouselogsexporter": map[string]any{
				"dsn":            "tcp://clickhouse:9000/signoz_logs",
				"timeout":        "10s",
				"use_new_schema": true,
			},
			"signozclickhousemeter": map[string]any{
				"dsn":           "tcp://clickhouse:9000/signoz_meter",
				"timeout":       "45s",
				"sending_queue": map[string]any{"enabled": false},
			},
			"metadataexporter": map[string]any{
				"cache":   map[string]any{"provider": "in_memory"},
				"dsn":     "tcp://clickhouse:9000/signoz_metadata",
				"enabled": true,
				"timeout": "45s",
			},
		},
		Traces: otel.Pipeline{
			Processors: []string{"signozspanmetrics/delta", "batch"},
			Exporters:  []string{"clickhousetraces", "metadataexporter", "signozmeter"},
		},
		Metrics: otel.Pipeline{
			Processors: []string{"batch"},
			Exporters:  []string{"signozclickhousemetrics", "metadataexporter", "signozmeter"},
		},
		Logs: otel.Pipeline{
			Processors: []string{"batch"},
			Exporters:  []string{"clickhouselogsexporter", "metadataexporter", "signozmeter"},
		},
		Extra: map[string]otel.Pipeline{
			"metrics/meter": {
				Receivers:  []string{"signozmeter"},
				Processors: []string{"batch/meter"},
				Exporters:  []string{"signozclickhousemeter"},
			},
		},
		Telemetry: map[string]any{
			"logs": map[string]any{"encoding": "json"},
		},
	}, nil
}

// StartCompanion extracts config files and starts the SigNoz stack via docker compose.
func (p *SignozPlugin) StartCompanion(ws *workspace.Workspace) error {
	return startSignoz()
}

// StopCompanion stops the SigNoz docker-compose stack.
func (p *SignozPlugin) StopCompanion(ws *workspace.Workspace) error {
	return stopSignoz()
}

// CompanionRunning returns true if the SigNoz signoz container is running.
func (p *SignozPlugin) CompanionRunning(ws *workspace.Workspace) bool {
	return isSignozRunning()
}

// QueryEndpoint returns the SigNoz UI URL.
func (p *SignozPlugin) QueryEndpoint(ws *workspace.Workspace) string {
	return fmt.Sprintf("http://localhost:%d", uiPort)
}

// Backend returns a query client for the local SigNoz.
func (p *SignozPlugin) Backend(ws *workspace.Workspace) (observability.Backend, error) {
	return obssignoz.NewClient(p.QueryEndpoint(ws), ""), nil
}

// Validate checks that docker is available.
func (p *SignozPlugin) Validate(ws *workspace.Workspace) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found on PATH — required for SigNoz plugin")
	}
	return nil
}

// ConfigSchema returns empty — no plugin-specific config keys for SigNoz.
func (p *SignozPlugin) ConfigSchema() []otel.ConfigField {
	return nil
}

// --- internal helpers ---

func signozDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "devstack", "signoz"), nil
}

func signozComposePath() (string, error) {
	dir, err := signozDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "docker-compose.yml"), nil
}

// signozProject is the one compose project per machine — every workspace shares
// the stack and is told apart by its devstack.* resource attributes.
const signozProject = "devstack-signoz"

// ensureSignozFiles extracts all embedded SigNoz config files to ~/.config/devstack/signoz/.
// Files are always overwritten so updates to the binary propagate.
func ensureSignozFiles() (string, error) {
	dir, err := signozDir()
	if err != nil {
		return "", err
	}

	err = fs.WalkDir(signozFiles, "files", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Compute destination path: strip "files/" prefix, place under dir.
		rel, err := filepath.Rel("files", path)
		if err != nil {
			return err
		}
		dest := filepath.Join(dir, rel)

		if d.IsDir() {
			return os.MkdirAll(dest, 0755)
		}

		data, err := signozFiles.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0644)
	})
	if err != nil {
		return "", fmt.Errorf("failed to extract SigNoz config files: %w", err)
	}

	return filepath.Join(dir, "docker-compose.yml"), nil
}

type composePS struct {
	Name    string `json:"Name"`
	Service string `json:"Service"`
	State   string `json:"State"`
	Status  string `json:"Status"`
}

func isSignozRunning() bool {
	composePath, err := signozComposePath()
	if err != nil {
		return false
	}
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		return false
	}

	out, err := exec.Command("docker", "compose",
		"-f", composePath,
		"-p", signozProject,
		"ps", "--format", "json",
	).Output()
	if err != nil {
		return false
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" || raw == "[]" {
		return false
	}

	var services []composePS
	if err := json.Unmarshal([]byte(raw), &services); err != nil {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var svc composePS
			if err := json.Unmarshal([]byte(line), &svc); err == nil {
				services = append(services, svc)
			}
		}
	}

	for _, svc := range services {
		if svc.Service == "signoz" && svc.State == "running" {
			return true
		}
	}
	return false
}

func startSignoz() error {
	composePath, err := ensureSignozFiles()
	if err != nil {
		return err
	}

	cmd := exec.Command("docker", "compose",
		"-f", composePath,
		"-p", signozProject,
		"up", "-d",
	)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("SIGNOZ_UI_PORT=%d", uiPort),
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose up failed (see output above)")
	}
	return nil
}

func stopSignoz() error {
	composePath, err := signozComposePath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		return fmt.Errorf("signoz compose file not found at %s", composePath)
	}

	cmd := exec.Command("docker", "compose",
		"-f", composePath,
		"-p", signozProject,
		"down",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose down failed (see output above)")
	}
	return nil
}
