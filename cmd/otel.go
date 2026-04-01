package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SigNoz docker-compose stack constants.
const otelUIPort = "3301"       // SigNoz frontend
const otelQueryPort = "8080"    // SigNoz query-service
const otelOTLPGRPCPort = "4317" // OTLP gRPC
const otelOTLPHTTPPort = "4318" // OTLP HTTP

const otelUIURL = "http://localhost:3301"
const otelQueryURL = "http://localhost:8080"

// signozComposeTemplate is the docker-compose.yml written to disk for SigNoz.
const signozComposeTemplate = `version: "3.8"

services:
  zookeeper-1:
    image: bitnami/zookeeper:3.7.1
    container_name: ${COMPOSE_PROJECT_NAME}-zookeeper
    hostname: zookeeper-1
    user: root
    volumes:
      - signoz-zookeeper:/bitnami/zookeeper
    environment:
      - ALLOW_ANONYMOUS_LOGIN=yes
      - ZOO_SERVER_ID=1
      - ZOO_PORT_NUMBER=2181
      - ZOO_TICK_TIME=2000
    restart: unless-stopped

  clickhouse:
    image: clickhouse/clickhouse-server:24.1.2-alpine
    container_name: ${COMPOSE_PROJECT_NAME}-clickhouse
    hostname: clickhouse
    depends_on:
      - zookeeper-1
    volumes:
      - signoz-clickhouse:/var/lib/clickhouse
      - signoz-clickhouse-logs:/var/log/clickhouse-server
    environment:
      - CLICKHOUSE_DB=signoz_traces
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "--spider", "-q", "http://localhost:8123/ping"]
      interval: 30s
      timeout: 5s
      retries: 3

  schema-migrator:
    image: signoz/schema-migrator:latest
    container_name: ${COMPOSE_PROJECT_NAME}-schema-migrator
    command: ["--dsn=tcp://clickhouse:9000"]
    depends_on:
      clickhouse:
        condition: service_healthy
    restart: on-failure

  query-service:
    image: signoz/query-service:latest
    container_name: ${COMPOSE_PROJECT_NAME}-query-service
    command:
      - "--config=/root/config/prometheus.yml"
    ports:
      - "8080:8080"
      - "8085:8085"
    environment:
      - ClickHouseUrl=tcp://clickhouse:9000
      - ALERTMANAGER_API_PREFIX=http://alertmanager:9093/api/
      - SIGNOZ_LOCAL_DB_PATH=/var/lib/signoz/signoz.db
      - DASHBOARDS_PATH=/root/config/dashboards
      - STORAGE=clickhouse
      - GODEBUG=netdns=go
      - TELEMETRY_ENABLED=false
      - DEPLOYMENT_TYPE=docker-standalone-amd
    depends_on:
      schema-migrator:
        condition: service_completed_successfully
    volumes:
      - signoz-sqlite:/var/lib/signoz
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "--spider", "-q", "http://localhost:8080/api/v1/health"]
      interval: 30s
      timeout: 5s
      retries: 3

  frontend:
    image: signoz/frontend:latest
    container_name: ${COMPOSE_PROJECT_NAME}-frontend
    ports:
      - "3301:3301"
    depends_on:
      - query-service
    environment:
      - FRONTEND_API_ENDPOINT=http://query-service:8080
    restart: unless-stopped

  otel-collector:
    image: signoz/signoz-otel-collector:latest
    container_name: ${COMPOSE_PROJECT_NAME}-otel-collector
    command:
      - "--config=/etc/otel-collector-config.yaml"
      - "--feature-gates=-pkg.translator.prometheus.NormalizeName"
    ports:
      - "4317:4317"
      - "4318:4318"
    environment:
      - OTEL_RESOURCE_ATTRIBUTES=host.name=signoz-host,os.type=linux
      - DOCKER_MULTI_NODE_CLUSTER=false
      - LOW_CARDINAL_EXCEPTION_GROUPING=false
    depends_on:
      clickhouse:
        condition: service_healthy
    restart: unless-stopped

  alertmanager:
    image: signoz/alertmanager:latest
    container_name: ${COMPOSE_PROJECT_NAME}-alertmanager
    ports:
      - "9093:9093"
    depends_on:
      - query-service
    restart: unless-stopped

volumes:
  signoz-zookeeper:
  signoz-clickhouse:
  signoz-clickhouse-logs:
  signoz-sqlite:
`

// signozComposePath returns the path where the SigNoz compose file is written.
func signozComposePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "devstack", "signoz", "docker-compose.yml"), nil
}

// signozProjectName returns the docker-compose project name for a workspace.
func signozProjectName(workspaceName string) string {
	return "devstack-signoz-" + workspaceName
}

// ensureSignozComposeFile writes the compose file to disk.
func ensureSignozComposeFile() (string, error) {
	composePath, err := signozComposePath()
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(composePath), 0755); err != nil {
		return "", fmt.Errorf("failed to create signoz config directory: %w", err)
	}

	if err := os.WriteFile(composePath, []byte(signozComposeTemplate), 0644); err != nil {
		return "", fmt.Errorf("failed to write signoz compose file: %w", err)
	}

	return composePath, nil
}

// composePS holds the subset of fields we parse from `docker compose ps --format json`.
type composePS struct {
	Name    string `json:"Name"`
	Service string `json:"Service"`
	State   string `json:"State"`
	Status  string `json:"Status"`
}

// isOtelRunning checks whether the SigNoz query-service container is running.
func isOtelRunning(workspaceName string) bool {
	composePath, err := signozComposePath()
	if err != nil {
		return false
	}

	// If the compose file doesn't exist yet, clearly not running.
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

	// Output may be one JSON object per line (NDJSON) or a JSON array.
	raw := strings.TrimSpace(string(out))
	if raw == "" || raw == "[]" {
		return false
	}

	// Try array first.
	var services []composePS
	if err := json.Unmarshal([]byte(raw), &services); err != nil {
		// Try NDJSON.
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
		if svc.Service == "query-service" && svc.State == "running" {
			return true
		}
	}
	return false
}

// startOtel writes the compose file and starts the SigNoz stack via docker compose.
func startOtel(workspaceName string) error {
	composePath, err := ensureSignozComposeFile()
	if err != nil {
		return err
	}

	project := signozProjectName(workspaceName)
	cmd := exec.Command("docker", "compose",
		"-f", composePath,
		"-p", project,
		"up", "-d",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start SigNoz: %w\n%s", err, out)
	}
	return nil
}

// stopOtel stops the SigNoz stack via docker compose.
func stopOtel(workspaceName string) error {
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
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose down failed: %w\n%s", err, out)
	}
	return nil
}
