package cmd

import (
	"fmt"
	"os/exec"
	"strings"
)

// Aspire Dashboard all-in-one container.
const otelImage = "mcr.microsoft.com/dotnet/aspire-dashboard"

// otelUIPort is the Aspire Dashboard web UI port.
const otelUIPort = "18888"

// otelOTLPGRPCPort is the OTLP gRPC ingestion port.
const otelOTLPGRPCPort = "4317"

// otelOTLPHTTPPort is the OTLP HTTP ingestion port.
const otelOTLPHTTPPort = "4318"

// otelUIURL is the local URL for the Aspire Dashboard UI.
const otelUIURL = "http://localhost:18888"

// isOtelRunning checks if the Aspire Dashboard container is running.
func isOtelRunning(containerName string) bool {
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", containerName).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// startOtel pulls (if needed) and starts the Aspire Dashboard all-in-one container.
func startOtel(containerName string) error {
	// Remove stopped container with the same name if it exists (idempotent).
	exec.Command("docker", "rm", containerName).Run() //nolint:errcheck

	cmd := exec.Command("docker", "run", "-d",
		"--name", containerName,
		"--restart", "unless-stopped",
		"-p", otelUIPort+":"+otelUIPort,
		"-p", otelOTLPGRPCPort+":"+otelOTLPGRPCPort,
		"-p", otelOTLPHTTPPort+":"+otelOTLPHTTPPort,
		// Disable authentication so services can push without API keys.
		"-e", "ASPIRE_DASHBOARD_UNSECURED_ALLOW_ANONYMOUS=true",
		// Enable the telemetry HTTP query API (/api/telemetry/*).
		"-e", "Dashboard__Api__Enabled=true",
		"-e", "Dashboard__Api__AuthMode=Unsecured",
		otelImage,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start Aspire Dashboard: %w\n%s", err, out)
	}
	return nil
}

// stopOtel stops and removes the Aspire Dashboard container.
func stopOtel(containerName string) error {
	out, err := exec.Command("docker", "stop", containerName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker stop failed: %w\n%s", err, out)
	}
	exec.Command("docker", "rm", containerName).Run() //nolint:errcheck
	return nil
}
