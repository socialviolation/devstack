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

	"devstack/internal/otel"
	"devstack/internal/workspace"
)

//go:embed files
var signozFiles embed.FS

func init() {
	otel.Register(&SignozPlugin{})
}

// SignozPlugin is the built-in SigNoz observability plugin.
type SignozPlugin struct{}

func (p *SignozPlugin) Name() string { return "signoz" }

// CollectorConfig returns the processor/exporter/pipeline YAML for the SigNoz ClickHouse backend.
// The OTLP receiver block is injected by the collector core before this is written.
func (p *SignozPlugin) CollectorConfig(ws *workspace.Workspace) ([]byte, error) {
	cfg := `connectors:
  signozmeter:
    metrics_flush_interval: 1h
    dimensions:
      - name: service.name
      - name: deployment.environment
      - name: host.name

processors:
  batch:
    send_batch_size: 10000
    send_batch_max_size: 11000
    timeout: 10s
  batch/meter:
    send_batch_max_size: 25000
    send_batch_size: 20000
    timeout: 1s
  resourcedetection:
    detectors: [env, system]
    timeout: 2s
  signozspanmetrics/delta:
    metrics_exporter: signozclickhousemetrics
    metrics_flush_interval: 60s
    latency_histogram_buckets: [100us, 1ms, 2ms, 6ms, 10ms, 50ms, 100ms, 250ms, 500ms, 1000ms, 1400ms, 2000ms, 5s, 10s, 20s, 40s, 60s]
    dimensions_cache_size: 100000
    aggregation_temporality: AGGREGATION_TEMPORALITY_DELTA
    enable_exp_histogram: true
    dimensions:
      - name: service.namespace
        default: default
      - name: deployment.environment
        default: default
      - name: signoz.collector.id
      - name: service.version
      - name: host.name

extensions:
  health_check:
    endpoint: 0.0.0.0:13133
  pprof:
    endpoint: 0.0.0.0:1777

exporters:
  clickhousetraces:
    datasource: tcp://clickhouse:9000/signoz_traces
    low_cardinal_exception_grouping: false
    use_new_schema: true
  signozclickhousemetrics:
    dsn: tcp://clickhouse:9000/signoz_metrics
  clickhouselogsexporter:
    dsn: tcp://clickhouse:9000/signoz_logs
    timeout: 10s
    use_new_schema: true
  signozclickhousemeter:
    dsn: tcp://clickhouse:9000/signoz_meter
    timeout: 45s
    sending_queue:
      enabled: false
  metadataexporter:
    cache:
      provider: in_memory
    dsn: tcp://clickhouse:9000/signoz_metadata
    enabled: true
    timeout: 45s

service:
  telemetry:
    logs:
      encoding: json
  extensions:
    - health_check
    - pprof
  pipelines:
    traces:
      receivers: [otlp]
      processors: [signozspanmetrics/delta, batch]
      exporters: [clickhousetraces, metadataexporter, signozmeter]
    metrics:
      receivers: [otlp]
      processors: [batch]
      exporters: [signozclickhousemetrics, metadataexporter, signozmeter]
    logs:
      receivers: [otlp]
      processors: [batch]
      exporters: [clickhouselogsexporter, metadataexporter, signozmeter]
    metrics/meter:
      receivers: [signozmeter]
      processors: [batch/meter]
      exporters: [signozclickhousemeter]
`
	return []byte(cfg), nil
}

// StartCompanion extracts config files and starts the SigNoz stack via docker compose.
func (p *SignozPlugin) StartCompanion(ws *workspace.Workspace) error {
	return startSignoz(ws)
}

// StopCompanion stops the SigNoz docker-compose stack.
func (p *SignozPlugin) StopCompanion(ws *workspace.Workspace) error {
	return stopSignoz(ws.Name)
}

// CompanionRunning returns true if the SigNoz signoz container is running.
func (p *SignozPlugin) CompanionRunning(ws *workspace.Workspace) bool {
	return isSignozRunning(ws.Name)
}

// QueryEndpoint returns the SigNoz UI URL for the workspace.
func (p *SignozPlugin) QueryEndpoint(ws *workspace.Workspace) string {
	return fmt.Sprintf("http://localhost:%d", ws.UIPort())
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

func signozProjectName(workspaceName string) string {
	return "devstack-signoz-" + workspaceName
}

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

func isSignozRunning(workspaceName string) bool {
	composePath, err := signozComposePath()
	if err != nil {
		return false
	}
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		return false
	}

	project := signozProjectName(workspaceName)
	out, err := exec.Command("docker", "compose",
		"-f", composePath,
		"-p", project,
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

func startSignoz(ws *workspace.Workspace) error {
	composePath, err := ensureSignozFiles()
	if err != nil {
		return err
	}

	project := signozProjectName(ws.Name)
	cmd := exec.Command("docker", "compose",
		"-f", composePath,
		"-p", project,
		"up", "-d",
	)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("SIGNOZ_UI_PORT=%d", ws.UIPort()),
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose up failed (see output above)")
	}
	return nil
}

func stopSignoz(workspaceName string) error {
	composePath, err := signozComposePath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		return fmt.Errorf("signoz compose file not found at %s", composePath)
	}

	project := signozProjectName(workspaceName)
	cmd := exec.Command("docker", "compose",
		"-f", composePath,
		"-p", project,
		"down",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose down failed (see output above)")
	}
	return nil
}
