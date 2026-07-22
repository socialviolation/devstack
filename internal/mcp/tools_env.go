package mcp

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/svcconfig"
	"github.com/socialviolation/devstack/internal/workspace"
)

// registerEnvTools registers the config-patch environment tools, mirroring the
// CLI's `devstack env use/which/set`. These operate on the named environments
// defined in the workspace manifest, not the infra environment (see the
// environment tool for the distinction).
func registerEnvTools(mcpServer *server.MCPServer, ws *workspace.Workspace, workspacePath string) {
	registerEnvUseTool(mcpServer, ws, workspacePath)
	registerEnvWhichTool(mcpServer, ws, workspacePath)
	registerEnvSetTool(mcpServer, ws, workspacePath)
}

func registerEnvUseTool(mcpServer *server.MCPServer, ws *workspace.Workspace, workspacePath string) {
	tool := mcp.NewTool("env_use",
		mcp.WithDescription("Point a scope at one of the workspace's named config-patch environments, so its services run with that environment's config vars. Mirrors 'devstack env use'. Scope precedence is stack > service > workspace: with neither service nor stack the workspace default is set; with 'service' the single service is pointed; with 'stack' that feature stack is pointed. The env name must be one defined in the workspace manifest. Use env_which or status to see where each instance points."),
		mcp.WithString("name", mcp.Required(),
			mcp.Description("Named environment to select (must be defined in the workspace manifest, e.g. 'staging').")),
		mcp.WithString("service",
			mcp.Description("Exact service name to point at the env (service scope). Cannot be combined with stack.")),
		mcp.WithString("stack",
			mcp.Description("Feature stack short name to point at the env (stack scope). Cannot be combined with service.")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("no workspace resolved"), nil
		}
		name := strings.TrimSpace(request.GetString("name", ""))
		if name == "" {
			return mcp.NewToolResultError("name must not be empty"), nil
		}
		svcName := strings.TrimSpace(request.GetString("service", ""))
		stackName := strings.TrimSpace(request.GetString("stack", ""))
		if svcName != "" && stackName != "" {
			return mcp.NewToolResultError("specify either service or stack, not both"), nil
		}

		m, err := config.LoadWorkspaceManifest(ws.Path)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if _, ok := m.Environments[name]; !ok {
			return mcp.NewToolResultError(fmt.Sprintf("env %q is not defined in workspace %q; available: %s", name, ws.Name, envNames(m))), nil
		}

		switch {
		case stackName != "":
			rec, err := stack.Resolve(ws.Name, stackName)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if err := stack.SetEnv(ws.Name, rec.Name, name); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("stack %q now uses env %q", rec.Name, name)), nil
		case svcName != "":
			rw, err := config.ResolveWorkspace(ws.Path)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			svc, ok := rw.Services[svcName]
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("service %q not found in workspace %q; services: %s", svcName, ws.Name, strings.Join(sortedServiceNames(rw), ", "))), nil
			}
			if err := config.SetServiceEnv(svc.RepoPath, name); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("service %q now uses env %q", svcName, name)), nil
		default:
			if err := config.SetWorkspaceEnv(ws.Path, name); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("workspace %q now uses env %q", ws.Name, name)), nil
		}
	})
}

func registerEnvWhichTool(mcpServer *server.MCPServer, ws *workspace.Workspace, workspacePath string) {
	tool := mcp.NewTool("env_which",
		mcp.WithDescription("Show which named config-patch environment applies to a service at each scope (workspace/service/stack) and the merged effective config vars it would run with. Mirrors 'devstack env which'. Secret values are masked. If service is omitted it is resolved from the server's working directory."),
		mcp.WithString("service",
			mcp.Description("Exact service name to resolve. If omitted, resolved from the current working directory.")),
		mcp.WithString("stack",
			mcp.Description("Feature stack short name whose stack-scope env to include in the merge.")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("no workspace resolved"), nil
		}
		rw, err := config.ResolveWorkspace(ws.Path)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		svcName := strings.TrimSpace(request.GetString("service", ""))
		if svcName == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			identity, err := config.ResolveIdentity(cwd)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("could not detect a service from the working directory; pass service: %v", err)), nil
			}
			svcName = identity.ServiceName
		}
		if svcName == "" {
			return mcp.NewToolResultError("no service resolved; pass service"), nil
		}
		svc, ok := rw.Services[svcName]
		if !ok {
			return mcp.NewToolResultError(fmt.Sprintf("service %q not found in workspace %q; services: %s", svcName, ws.Name, strings.Join(sortedServiceNames(rw), ", "))), nil
		}

		stackEnv := ""
		if stackName := strings.TrimSpace(request.GetString("stack", "")); stackName != "" {
			rec, err := stack.Resolve(ws.Name, stackName)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			stackEnv = rec.Env
		}

		merged, err := config.ResolveEnvPatch(rw.Manifest, svc.Manifest, stackEnv)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "Active env by scope for service %q:\n", svcName)
		fmt.Fprintf(&sb, "  workspace.env  %s\n", orNone(rw.Manifest.Workspace.Env))
		fmt.Fprintf(&sb, "  service.env    %s\n", orNone(svc.Manifest.Service.Env))
		fmt.Fprintf(&sb, "  stack.env      %s\n\n", orNone(stackEnv))
		fmt.Fprintf(&sb, "Merged effective values (stack > service > workspace, secrets masked):\n")
		for _, k := range sortedStrMapKeys(merged) {
			fmt.Fprintf(&sb, "  %-32s %s\n", k, maskValue(k, merged[k]))
		}
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerEnvSetTool(mcpServer *server.MCPServer, ws *workspace.Workspace, workspacePath string) {
	tool := mcp.NewTool("env_set",
		mcp.WithDescription("Set a config-var (key=value) on one of the workspace's named config-patch environments. Mirrors 'devstack env set'. Secret values are masked in the confirmation output. Use env_use to point a scope at the environment."),
		mcp.WithString("name", mcp.Required(),
			mcp.Description("Named environment to modify (e.g. 'staging').")),
		mcp.WithString("key", mcp.Required(),
			mcp.Description("Config-var key to set.")),
		mcp.WithString("value", mcp.Required(),
			mcp.Description("Value to set.")),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("no workspace resolved"), nil
		}
		name := strings.TrimSpace(request.GetString("name", ""))
		key := strings.TrimSpace(request.GetString("key", ""))
		value := request.GetString("value", "")
		if name == "" || key == "" {
			return mcp.NewToolResultError("name and key must not be empty"), nil
		}
		if err := config.SetEnvValue(ws.Path, name, key, value); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("set %s.%s = %s", name, key, maskValue(key, value))), nil
	})
}

func maskValue(key, value string) string {
	if svcconfig.IsSecret(key, value) {
		return svcconfig.MaskedValue
	}
	return value
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func envNames(m *config.WorkspaceManifest) string {
	names := make([]string, 0, len(m.Environments))
	for k := range m.Environments {
		names = append(names, k)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

func sortedServiceNames(rw *config.ResolvedWorkspace) []string {
	names := make([]string, 0, len(rw.Services))
	for n := range rw.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func sortedStrMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
