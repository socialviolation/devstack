package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"devstack/internal/config"
)

type ServiceStatus struct {
	Service            string
	ExpectedTraces     bool
	ExpectedLogs       bool
	Mode               string
	CollectorReachable bool
	TraceCount         int
	LogEvidence        bool
	Confidence         string
	Interpretation     string
}

func Status(workspacePath string) ([]ServiceStatus, error) {
	resolved, err := config.ResolveWorkspace(workspacePath)
	if err != nil {
		return nil, err
	}
	entries := collectorEntries(workspacePath)
	var statuses []ServiceStatus
	for _, name := range sortedServiceNames(resolved.Services) {
		service := resolved.Services[name]
		manifest := service.Manifest
		if manifest == nil {
			continue
		}
		mode := readMode(workspacePath, name)
		traceCount := 0
		for _, entry := range entries {
			serviceName, _ := entry["service"].(string)
			if serviceName == name || serviceName == name+"-mismatch" {
				traceCount++
			}
		}
		logs := readLog(workspacePath, name)
		logEvidence := strings.TrimSpace(logs) != ""
		collectorReachable := !strings.Contains(logs, "trace export failed") || traceCount > 0
		confidence, interpretation := classify(manifest.Telemetry.Traces.Expected, manifest.Telemetry.Logs.Expected, mode, collectorReachable, traceCount, logEvidence)
		statuses = append(statuses, ServiceStatus{
			Service:            name,
			ExpectedTraces:     manifest.Telemetry.Traces.Expected,
			ExpectedLogs:       manifest.Telemetry.Logs.Expected,
			Mode:               mode,
			CollectorReachable: collectorReachable,
			TraceCount:         traceCount,
			LogEvidence:        logEvidence,
			Confidence:         confidence,
			Interpretation:     interpretation,
		})
	}
	return statuses, nil
}

func collectorEntries(workspacePath string) []map[string]any {
	file := filepath.Join(workspacePath, "logs", "collector.jsonl")
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var result []map[string]any
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			continue
		}
		if body, ok := payload["body"].(map[string]any); ok {
			result = append(result, body)
		}
	}
	return result
}

func sortedServiceNames(services map[string]config.ResolvedService) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func readMode(workspacePath, service string) string {
	data, err := os.ReadFile(filepath.Join(workspacePath, "state", service+".mode"))
	if err != nil {
		return "healthy"
	}
	mode := strings.TrimSpace(string(data))
	if mode == "" {
		return "healthy"
	}
	return mode
}

func readLog(workspacePath, service string) string {
	data, err := os.ReadFile(filepath.Join(workspacePath, "logs", service+".log"))
	if err != nil {
		return ""
	}
	return string(data)
}

func classify(expectedTraces, expectedLogs bool, mode string, collectorReachable bool, traceCount int, logEvidence bool) (string, string) {
	if !expectedTraces && !expectedLogs {
		return "low", "No telemetry expectations configured."
	}
	if mode == "collector-down" {
		return "inconclusive", "Telemetry is inconclusive because export is intentionally degraded."
	}
	if mode == "no-traces" || mode == "logs-only" {
		if logEvidence {
			return "partial", fmt.Sprintf("Scenario mode %s intentionally suppresses traces.", mode)
		}
		return "low", fmt.Sprintf("Scenario mode %s intentionally suppresses traces.", mode)
	}
	if traceCount > 0 && logEvidence {
		return "high", "Observed traces and logs for the current service."
	}
	if (traceCount > 0 || logEvidence) && collectorReachable {
		return "partial", "Observed some telemetry evidence, but coverage is incomplete."
	}
	if !collectorReachable {
		return "inconclusive", "Telemetry is inconclusive because collector reachability is degraded."
	}
	return "low", "Expected telemetry was not observed in the current artifacts."
}
