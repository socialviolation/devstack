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
// defined in the workspace manifest.
func registerEnvTools(mcpServer *server.MCPServer, ws *workspace.Workspace, workspacePath string) {
	registerEnvUseTool(mcpServer, ws, workspacePath)
	registerEnvWhichTool(mcpServer, ws, workspacePath)
	registerEnvSetTool(mcpServer, ws, workspacePath)
}

func registerEnvUseTool(mcpServer *server.MCPServer, ws *workspace.Workspace, workspacePath string) {
	tool := mcp.NewTool("env_use",
		mcp.WithDescription("Point a scope at one of the config-patch environments that the BASE workspace manifest defines, so that its services run with that environment's config vars. This tool mirrors 'devstack env use'.\n"+
			"devstack defines the environments ONCE, in the base workspace. A feature stack does NOT define its own. A stack inherits the base environments, and 'stack' points a stack at one of them.\n"+
			"Scope precedence is stack first, then service, then workspace.\n"+
			"This tool changes what services run with, so it names its scope explicitly: 'service' points one service, stack='<name>' points that feature stack, and stack='base' sets the workspace default. If you omit both, that is not the workspace default. devstack then reads the scope from the server's working directory, and the call fails where that directory is neither a stack nor base's replica.\n"+
			"The env name must be one that the base workspace manifest defines. To see where each copy points, use env_which or status."),
		mcp.WithString("name", mcp.Required(),
			mcp.Description("Named environment to select. The workspace manifest must define it, for example 'staging'.")),
		mcp.WithString("service",
			mcp.Description("Exact service name to point at the env. This selects the service scope. You can not combine it with stack.")),
		mcp.WithString("stack",
			mcp.Description(mutatingStackParamDesc+" A stack name points that stack at the env, which is the stack scope. \"base\" sets the workspace default. You can not combine this with service. service selects the service scope, and it names no copy.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace"), nil
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
		if svcName == "" {
			resolved, err := stack.ResolveTarget(ws, stackName)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			stackName = resolved
		}

		m, err := config.LoadWorkspaceManifest(ws.Path)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if _, ok := m.Environments[name]; !ok {
			return mcp.NewToolResultError(fmt.Sprintf("env %q is not defined in workspace %q. Available: %s", name, ws.Name, envNames(m))), nil
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
				return mcp.NewToolResultError(fmt.Sprintf("service %q is not in workspace %q. Services: %s", svcName, ws.Name, strings.Join(sortedServiceNames(rw), ", "))), nil
			}
			if err := config.SetServiceEnv(svc.ManifestPath, name); err != nil {
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
		mcp.WithDescription("Show which base-defined config-patch environment applies to a service at each scope: workspace, service and stack. It also shows the merged config vars that the service runs with. This tool mirrors 'devstack env which'.\n"+
			"devstack defines the environments once, in the base workspace manifest, and every feature stack inherits them. The stack scope shows the base environment that a stack was pointed at.\n"+
			"devstack redacts credentials in place. The identifying parts of a value stay visible: server, database, account, endpoint and user. Only the credential is hidden. Where a value has no structure to keep, devstack masks the whole value.\n"+
			"If you omit service, devstack resolves it from the server's working directory."),
		mcp.WithString("service",
			mcp.Description("Exact service name to resolve. If you omit it, devstack resolves it from the current working directory.")),
		mcp.WithString("stack",
			mcp.Description(optionalStackNameDesc+" The merge includes that stack's stack-scope env.")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace"), nil
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
				return mcp.NewToolResultError(fmt.Sprintf("devstack can not detect a service from the working directory. Pass service: %v", err)), nil
			}
			svcName = identity.ServiceName
		}
		if svcName == "" {
			return mcp.NewToolResultError("devstack resolved no service. Pass service"), nil
		}
		svc, ok := rw.Services[svcName]
		if !ok {
			return mcp.NewToolResultError(fmt.Sprintf("service %q is not in workspace %q. Services: %s", svcName, ws.Name, strings.Join(sortedServiceNames(rw), ", "))), nil
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
		fmt.Fprintf(&sb, "Merged effective values (stack > service > workspace, credentials redacted):\n")
		for _, k := range sortedStrMapKeys(merged) {
			fmt.Fprintf(&sb, "  %-32s %s\n", k, svcconfig.RedactValue(k, merged[k]))
		}
		return mcp.NewToolResultText(sb.String()), nil
	})
}

func registerEnvSetTool(mcpServer *server.MCPServer, ws *workspace.Workspace, workspacePath string) {
	tool := mcp.NewTool("env_set",
		mcp.WithDescription("Set a config var (key and value) on one of the config-patch environments that the BASE workspace manifest defines. This tool mirrors 'devstack env set'. devstack defines the environments once, in the base workspace, and every feature stack inherits them.\n"+
			"NEVER set a secret here. This tool writes devstack.workspace.yaml, which is committed to git. The value applies to every service and every stack pointed at that environment, because it lands on the 'active env' rung, above a service's own env.values.\n"+
			"For one service's value, use service_env action=set. Use target=envrc for anything that carries a credential, and target=manifest for plain config.\n"+
			"WARNING: .envrc is the LOWEST rung of the ladder. Every other rung outranks it, and that includes the 'active env' rung this tool writes. If the active environment already sets that key, the value you put in .envrc never reaches the service. The write succeeds, the service keeps the old value, and nothing about the write says so.\n"+
			"service_env re-reads the ladder after every write and names the rung that wins. Read that line before you report success. To see the whole ladder, use env_which.\n"+
			"The output that reports the write redacts credentials in place, and it keeps the identifying parts of the value visible. To point a scope at the environment, use env_use."),
		mcp.WithString("name", mcp.Required(),
			mcp.Description("Named environment to modify (for example 'staging').")),
		mcp.WithString("key", mcp.Required(),
			mcp.Description("The config var key to set.")),
		mcp.WithString("value", mcp.Required(),
			mcp.Description("The value to set. Do not put a secret here. devstack writes it to a file that is committed to git.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace"), nil
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
		return mcp.NewToolResultText(fmt.Sprintf("set %s.%s = %s", name, key, svcconfig.RedactValue(key, value))), nil
	})
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
