package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
	"os"
	"strings"

	"github.com/socialviolation/devstack/internal/config"
)

// detectServicesFromCwd resolves services from the current directory using the new
// manifest-aware resolver, falling back to service path matching for legacy workspaces.
func detectServicesFromCwd(workspacePath string, cfg *config.WorkspaceConfig) ([]string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current directory: %w", err)
	}

	ctx, err := config.ResolveContext(config.ResolveOptions{StartPath: cwd, WorkspacePath: workspacePath})
	if err == nil && ctx.CurrentService.Value != "" {
		return []string{ctx.CurrentService.Value}, nil
	}

	var matches []string
	for name, path := range cfg.ServicePaths {
		if cwd == path || strings.HasPrefix(cwd, path+"/") {
			matches = append(matches, name)
		}
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("must specify a service name or group name\nUsage: devstack service start <service>\n       devstack service start <group>")
	}
	return matches, nil
}

// detectServiceFromCwd returns the single service matching the cwd.
// Errors if multiple match — use detectServicesFromCwd for that case.
func detectServiceFromCwd(workspacePath string, cfg *config.WorkspaceConfig) (string, error) {
	matches, err := detectServicesFromCwd(workspacePath, cfg)
	if err != nil {
		return "", err
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("multiple services match (%s); please specify explicitly", strings.Join(matches, ", "))
	}
	return matches[0], nil
}

// resolveTarget resolves a name to a list of services using this priority:
// 1. Exact match in cfg.ServicePaths → returns []string{name}
// 2. Exact match in cfg.Groups → returns the group's member list
// 3. Returns error with helpful message
// If name is empty, falls back to cwd auto-detection (detectServicesFromCwd).
func resolveTarget(workspacePath, name string, cfg *config.WorkspaceConfig) ([]string, error) {
	return resolveTargetKind(workspacePath, name, cfg, targetAny)
}

// A target kind says whether a name must be read as a service, as a group, or as
// whichever matches. It exists because one name can legitimately be both: navexa
// has a group "roi" and a service aliased "roi", and the inferring form silently
// picks one. `devstack group stop roi` and `devstack service stop roi` say which
// you meant.
type targetKind string

const (
	targetAny     targetKind = ""
	targetService targetKind = "service"
	targetGroup   targetKind = "group"
)

// targetKindOf reads the kind a command fixes, set as an annotation by the
// noun-first commands. The verb-first shortcuts set none and keep inferring.
func targetKindOf(cmd *cobra.Command) targetKind {
	if cmd == nil {
		return targetAny
	}
	return targetKind(cmd.Annotations["targetKind"])
}

func resolveTargetKind(workspacePath, name string, cfg *config.WorkspaceConfig, kind targetKind) ([]string, error) {
	if name == "" {
		if kind == targetGroup {
			return nil, fmt.Errorf("name a group: devstack group list")
		}
		return detectServicesFromCwd(workspacePath, cfg)
	}

	_, isService := cfg.ServicePaths[name]
	members, isGroup := cfg.Groups[name]

	switch kind {
	case targetService:
		if isService {
			return []string{name}, nil
		}
		if isGroup {
			return nil, fmt.Errorf("%q is a group, not a service — did you mean: devstack group <action> %s", name, name)
		}
		return nil, fmt.Errorf("'%s' is not a known service\nRun 'devstack status' to see available services", name)
	case targetGroup:
		if isGroup {
			return members, nil
		}
		if isService {
			return nil, fmt.Errorf("%q is a service, not a group — did you mean: devstack service <action> %s", name, name)
		}
		return nil, fmt.Errorf("'%s' is not a known group\nRun 'devstack group list' to see groups", name)
	}

	if isService {
		if isGroup {
			// Both exist under one name. Acting on either choice silently is the
			// bug the noun-first form was added to remove, so say so instead.
			return nil, fmt.Errorf("%q is both a service and a group. Say which:\n  devstack service <action> %s\n  devstack group <action> %s", name, name, name)
		}
		return []string{name}, nil
	}
	if isGroup {
		return members, nil
	}

	return nil, fmt.Errorf("'%s' is not a known service or group\nRun 'devstack status' to see available services or 'devstack group list' to see groups", name)
}
