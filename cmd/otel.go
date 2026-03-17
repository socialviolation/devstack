package cmd

import (
	"fmt"
	"os/exec"
	"strings"
)

const otelImage = "mcr.microsoft.com/dotnet/aspire-dashboard:latest"
const otelUIPort = "18888"
const otelGRPCPort = "18889"
const otelHTTPPort = "18890"
const otelUIURL = "http://localhost:18888"

// isOtelRunning checks if the Aspire Dashboard container is running.
func isOtelRunning(containerName string) bool {
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", containerName).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// startOtel pulls (if needed) and starts the Aspire Dashboard container.
func startOtel(containerName string) error {
	// Remove stopped container with the same name if it exists (idempotent).
	exec.Command("docker", "rm", containerName).Run() //nolint:errcheck

	cmd := exec.Command("docker", "run", "-d",
		"--name", containerName,
		"--restart", "unless-stopped",
		"-p", otelUIPort+":"+otelUIPort,
		"-p", otelGRPCPort+":"+otelGRPCPort,
		"-p", otelHTTPPort+":"+otelHTTPPort,
		"-e", "DOTNET_DASHBOARD_UNSECURED_ALLOW_ANONYMOUS=true",
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
