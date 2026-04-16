package cmd

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"devstack/internal/workspace"
)

//go:embed signoz
var signozFiles embed.FS


// signozDir returns the path where SigNoz config files are written.
func signozDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "devstack", "signoz"), nil
}

// signozComposePath returns the path to the docker-compose.yml on disk.
func signozComposePath() (string, error) {
	dir, err := signozDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "docker-compose.yml"), nil
}

// signozProjectName returns the docker-compose project name for a workspace.
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

	err = fs.WalkDir(signozFiles, "signoz", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Compute destination path: strip "signoz/" prefix, place under dir.
		rel, err := filepath.Rel("signoz", path)
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

// composePS holds the subset of fields we parse from `docker compose ps --format json`.
type composePS struct {
	Name    string `json:"Name"`
	Service string `json:"Service"`
	State   string `json:"State"`
	Status  string `json:"Status"`
}

// isOtelRunning checks whether the SigNoz signoz container is running.
func isOtelRunning(workspaceName string) bool {
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

// startOtel extracts config files and starts the SigNoz stack via docker compose.
// Ports are passed as environment variables so the compose file substitutes them.
func startOtel(ws *workspace.Workspace) error {
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
		fmt.Sprintf("SIGNOZ_OTLP_GRPC_PORT=%d", ws.GRPCPort()),
		fmt.Sprintf("SIGNOZ_OTLP_HTTP_PORT=%d", ws.HTTPPort()),
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose up failed (see output above)")
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
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose down failed (see output above)")
	}
	return nil
}
