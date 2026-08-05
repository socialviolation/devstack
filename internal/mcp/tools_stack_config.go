package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/svcconfig"
	"github.com/socialviolation/devstack/internal/tiltgen"
	"github.com/socialviolation/devstack/internal/workspace"
)

func registerStackConfigTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_config",
		mcp.WithDescription("Show the configuration that one copy of a service runs with. "+baseTermDesc+
			"The tool prints two tables. The first is the effective service configuration, and it names the source of each value. The second is the environment ladder, which is what the process receives.\n"+
			"A star marks a value that the stack overrides, and devstack computes that value. devstack shows a secret value as "+svcconfig.MaskedValue+".\n"+
			"This tool reads files. It does not ask the daemon what runs. If the copy is down, the tool says so, and it prints the configuration that the copy would run with.\n"+
			"For base, devstack reads the replica that base runs from, and not your checkout. If devstack has built no replica, run 'devstack workspace up' first.\n"+
			"This tool mirrors 'devstack stack config'."),
		mcp.WithString("service", mcp.Required(),
			mcp.Description("Exact service name, for example 'api-service'. The tool reads that service's copy. Other tools reject a partial match, and so does this one.")),
		mcp.WithString("stack",
			mcp.Description(stackParamDesc)),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace, so there is no configuration to read"), nil
		}
		service := strings.TrimSpace(request.GetString("service", ""))
		if service == "" {
			return mcp.NewToolResultError("service is required. Name the service whose configuration you want to read"), nil
		}
		stackName := strings.TrimSpace(request.GetString("stack", ""))
		if stackName == "" || stackName == "base" {
			return stackConfigForBase(ws, service), nil
		}
		rec, err := resolveStackRecord(ws, stackName)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return stackConfigForStack(ws, rec, service), nil
	})
}

// stackConfigForStack reads one stack copy's configuration out of that stack's
// worktree. The stack does not have to be up: a copy that is down still has the
// configuration it would run with, and that is the answer to "why did it start
// like that".
func stackConfigForStack(ws *workspace.Workspace, rec *stack.Record, service string) *mcp.CallToolResult {
	rw, err := stack.ResolveWorktree(rec)
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}
	svc, ok := rw.Services[service]
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("service %q is not in stack %q. Its services: %s", service, rec.FullName(), strings.Join(sortedServiceNames(rw), ", ")))
	}
	entries, err := svcconfig.EffectiveConfig(svc, rec.RuntimeKey())
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}

	var sb strings.Builder
	tense := "the configuration it runs with"
	if !rec.Active {
		tense = "the configuration it would run with"
		fmt.Fprintf(&sb, "Stack %s is down. Nothing runs with this configuration now. To start the stack, use the stack_up tool (CLI: devstack stack up %s).\n\n", rec.FullName(), rec.Name)
	}
	stackConfigTable(&sb, service, "stack "+rec.FullName(), tense, entries)
	fmt.Fprintf(&sb, "\n* = the stack overrides this value, and devstack computes it. devstack shows a secret value as %s.\n", svcconfig.MaskedValue)
	stackConfigLadder(&sb, ws, rw, svc, rec)
	return mcp.NewToolResultText(sb.String())
}

// stackConfigForBase reads base's copy out of the replica. base runs the replica
// worktrees, and the checkout is only the template they were built from, so the
// checkout is the wrong file to read.
func stackConfigForBase(ws *workspace.Workspace, service string) *mcp.CallToolResult {
	rw, err := replica.Resolve(ws)
	if errors.Is(err, replica.ErrNotBuilt) {
		return mcp.NewToolResultError(fmt.Sprintf("devstack has not built the replica of workspace %q, and base runs from it. There is no configuration to read yet. To build the replica, run: devstack workspace up", ws.Name))
	}
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("base runs from the replica of workspace %q. The replica is stale or incomplete. To rebuild it, run: devstack workspace up. devstack could not read it: %v", ws.Name, err))
	}
	svc, ok := rw.Services[service]
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("service %q is not in workspace %q. Its services: %s", service, ws.Name, strings.Join(sortedServiceNames(rw), ", ")))
	}
	entries, err := svcconfig.EffectiveConfig(svc, ws.Name)
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}

	var sb strings.Builder
	tense := "the configuration it runs with"
	if !ws.Active {
		tense = "the configuration it would run with"
		sb.WriteString("Base is down. Nothing runs with this configuration now. To start base, run: devstack workspace up\n\n")
	}
	stackConfigTable(&sb, service, "base", tense, entries)
	fmt.Fprintf(&sb, "\ndevstack reads this from the replica at %s. To change it, put the change on the default branch, then run: devstack workspace up\n", svc.RepoPath)
	fmt.Fprintf(&sb, "A secret value appears as %s.\n", svcconfig.MaskedValue)
	stackConfigLadder(&sb, ws, rw, svc, nil)
	return mcp.NewToolResultText(sb.String())
}

func stackConfigTable(sb *strings.Builder, service, scope, tense string, entries []svcconfig.ConfigEntry) {
	fmt.Fprintf(sb, "Effective configuration for %s in %s (read-only: %s)\n", service, scope, tense)
	fmt.Fprintf(sb, "  %-42s %-12s %s\n", "KEY", "SOURCE", "VALUE")
	sb.WriteString(strings.Repeat("-", 90) + "\n")
	for _, e := range entries {
		marker := "  "
		if e.Overridden {
			marker = "* "
		}
		fmt.Fprintf(sb, "%s%-42s %-12s %s\n", marker, e.Key, e.Source, e.Value)
	}
}

// stackConfigLadder appends the environment the process receives, rung by rung.
// A ladder that devstack can not resolve is a note and not an error: the
// effective configuration above it is still the answer to most questions.
func stackConfigLadder(sb *strings.Builder, ws *workspace.Workspace, rw *config.ResolvedWorkspace, svc config.ResolvedService, rec *stack.Record) {
	names := sortedServiceNames(rw)
	var managed map[string]string
	var book config.PortBook
	stackEnv := ""
	if rec != nil {
		stackEnv = rec.Env
		if opts, err := stack.GenerateOptions(rec, names); err == nil {
			managed = opts.ManagedEnv[svc.Name]
			book = opts.Book
		}
	} else {
		managed = workspace.ManagedEnv(ws, names, workspace.ActiveEnvNames(rw, stackEnv))[svc.Name]
		book = config.BuildPortBook(rw)
	}

	layers, err := config.EnvLadder(svc.EnvDir(), rw.Manifest, svc.Manifest, stackEnv, managed, svc.ManifestPath)
	if err != nil {
		fmt.Fprintf(sb, "\nEnvironment (serve_env ladder): unavailable: %v\n", err)
		return
	}
	if book != nil {
		if err := tiltgen.ResolveLayerRefs(layers, svc.Name, book); err != nil {
			fmt.Fprintf(sb, "\nEnvironment (serve_env ladder): unavailable: %v\n", err)
			return
		}
	}

	merged := config.MergeEnvLadder(layers)
	source := map[string]config.EnvRung{}
	for _, l := range layers {
		for k := range l.Values {
			source[k] = l.Rung
		}
	}
	sb.WriteString("\nEnvironment (serve_env ladder — what the process receives):\n")
	fmt.Fprintf(sb, "  %-42s %-22s %s\n", "KEY", "SOURCE", "VALUE")
	sb.WriteString(strings.Repeat("-", 90) + "\n")
	for _, k := range sortedKeys(merged) {
		fmt.Fprintf(sb, "  %-42s %-22s %s\n", k, string(source[k]), svcconfig.RedactValue(k, merged[k]))
	}
}
