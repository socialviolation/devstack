package cmd

import (
	"fmt"
	"os/exec"
	"strings"
)

const otelImage = "jaegertracing/all-in-one"
const otelUIPort = "16686"
const otelOTLPGRPCPort = "4317"
const otelOTLPHTTPPort = "4318"
const otelUIURL = "http://localhost:16686"

// isOtelRunning checks if the Jaeger container is running.
func isOtelRunning(containerName string) bool {
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", containerName).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// startOtel pulls (if needed) and starts the Jaeger all-in-one container.
func startOtel(containerName string) error {
	// Remove stopped container with the same name if it exists (idempotent).
	exec.Command("docker", "rm", containerName).Run() //nolint:errcheck

	cmd := exec.Command("docker", "run", "-d",
		"--name", containerName,
		"--restart", "unless-stopped",
		"-p", otelUIPort+":"+otelUIPort,
		"-p", otelOTLPGRPCPort+":"+otelOTLPGRPCPort,
		"-p", otelOTLPHTTPPort+":"+otelOTLPHTTPPort,
		"-e", "COLLECTOR_OTLP_ENABLED=true",
		otelImage,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start Jaeger: %w\n%s", err, out)
	}
	return nil
}

// stopOtel stops and removes the Jaeger container.
func stopOtel(containerName string) error {
	out, err := exec.Command("docker", "stop", containerName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker stop failed: %w\n%s", err, out)
	}
	exec.Command("docker", "rm", containerName).Run() //nolint:errcheck
	return nil
}
