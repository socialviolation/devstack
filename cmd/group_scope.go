package cmd

import (
	"fmt"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

// resolveInstanceTarget is resolveTargetKind for a command acting on one
// instance, with the two answers a stack makes wrong put right.
//
// A stack's generated manifest lists only the group members that made it into
// the overlay. So a group half in a stack silently resolves to that half, and a
// group not in it at all is reported as unknown — while `devstack group list`,
// which reads base, shows it. Both readings are true of the stack and false of
// the workspace, and the caller asked about the workspace.
func resolveInstanceTarget(cmd *cobra.Command, ws *workspace.Workspace, wsPath, name string, cfg *config.WorkspaceConfig, stackName string) ([]string, error) {
	kind := targetKindOf(cmd)
	services, err := resolveTargetKind(wsPath, name, cfg, kind)
	if stackName == "" || kind != targetGroup || name == "" {
		return services, err
	}

	baseGroups := baseGroupMembers(ws)
	members, isBaseGroup := baseGroups[name]
	if err != nil {
		if !isBaseGroup {
			return nil, err
		}
		return nil, fmt.Errorf("group %q has no services in stack %q. It runs entirely on base (%s).\nAct on base's copies: devstack group %s %s --stack base",
			name, stackName, joinServices(members), cmd.Name(), name)
	}

	for _, cov := range stack.CoverageOf([]string{name}, services, baseGroups) {
		if !cov.Complete() {
			color.New(color.Faint).Printf("group %s: %d of %d in stack %q. %s %s on base\n",
				cov.Group, len(cov.In), len(cov.In)+len(cov.Missing), stackName,
				joinServices(cov.Missing), isAre(len(cov.Missing)))
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

func isAre(n int) string {
	if n == 1 {
		return "stays"
	}
	return "stay"
}
