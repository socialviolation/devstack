package infra

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"devstack/internal/config"
)

type ComposeSpec struct {
	WorkspacePath string
	ProjectName   string
	Files         []string
}

func ResolveComposeSpec(workspacePath string) (*ComposeSpec, error) {
	if !config.HasWorkspaceManifest(workspacePath) {
		return nil, nil
	}
	manifest, err := config.LoadWorkspaceManifest(workspacePath)
	if err != nil {
		return nil, err
	}
	if manifest.Runtime.Infra.Provider != "compose" || len(manifest.Runtime.Infra.ComposeFiles) == 0 {
		return nil, nil
	}
	files := make([]string, 0, len(manifest.Runtime.Infra.ComposeFiles))
	for _, file := range manifest.Runtime.Infra.ComposeFiles {
		files = append(files, filepath.Clean(filepath.Join(workspacePath, file)))
	}
	projectName := fmt.Sprintf("devstack-%s-infra", strings.ReplaceAll(manifest.Workspace.Name, "_", "-"))
	return &ComposeSpec{WorkspacePath: workspacePath, ProjectName: projectName, Files: files}, nil
}

func Up(spec *ComposeSpec) error {
	if spec == nil {
		return nil
	}
	args := composeArgs(spec, "up", "-d")
	cmd := exec.Command("docker", args...)
	cmd.Dir = spec.WorkspacePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose up failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func Down(spec *ComposeSpec) error {
	if spec == nil {
		return nil
	}
	args := composeArgs(spec, "down")
	cmd := exec.Command("docker", args...)
	cmd.Dir = spec.WorkspacePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose down failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func RunningServices(spec *ComposeSpec) ([]string, error) {
	if spec == nil {
		return nil, nil
	}
	args := composeArgs(spec, "ps", "--services", "--status", "running")
	cmd := exec.Command("docker", args...)
	cmd.Dir = spec.WorkspacePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker compose ps failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	var services []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			services = append(services, line)
		}
	}
	return services, nil
}

func composeArgs(spec *ComposeSpec, extra ...string) []string {
	args := []string{"compose", "-p", spec.ProjectName}
	for _, file := range spec.Files {
		args = append(args, "-f", file)
	}
	args = append(args, extra...)
	return args
}
