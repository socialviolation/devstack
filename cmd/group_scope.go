package cmd

import (
	"fmt"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

func resolveInstanceTarget(cmd *cobra.Command, ws *workspace.Workspace, wsPath, name string, cfg *config.WorkspaceConfig, stackName string) ([]string, error) {
	kind := targetKindOf(cmd)
	services, err := resolveTargetKind(wsPath, name, cfg, kind)
	if stackName == "" || kind != targetGroup || name == "" {
		return services, err
	}

	baseGroups := baseGroupMembers(ws)
	members, isWorkspaceGroup := baseGroups[name]
	if err != nil {
		if !isWorkspaceGroup {
			return nil, err
		}
		return nil, fmt.Errorf("group %q has no services in stack %q. Its services are %s.\nTo add them to the stack, run: devstack stack add %s %s",
			name, stackName, joinServices(members), stackName, name)
	}

	for _, cov := range stack.CoverageOf([]string{name}, services, baseGroups) {
		if !cov.Complete() {
			color.New(color.Faint).Printf("stack %s %s\n", stackName, cov.Sentence())
		}
	}
	return services, nil
}

// baseGroupMembers reads the groups from the base workspace, which is the only
// place they are declared in full.
func baseGroupMembers(ws *workspace.Workspace) map[string][]string {
	if ws == nil {
		return nil
	}
	cfg, err := config.Load(ws.Path)
	if err != nil || cfg == nil {
		return nil
	}
	return cfg.Groups
}

func joinServices(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return fmt.Sprintf("%s and %s", joinComma(names[:len(names)-1]), names[len(names)-1])
}

func joinComma(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
