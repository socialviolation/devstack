package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/herdr"
	"github.com/socialviolation/devstack/internal/hostdaemon"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

func registerStackResyncTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_resync",
		mcp.WithDescription("Move a feature stack to what you pushed. For each repo of the stack that is on the stack's branch, devstack fetches origin/<branch> and fast-forwards the worktree to it. "+
			"It refuses a repo whose worktree has uncommitted changes, and names the files. It refuses a repo whose worktree and origin diverged, and names both commits. It never merges or rebases. "+
			"If the stack is up, devstack then restarts the services of each repo that moved, and every service of the stack that depends on them. "+
			"For each detached repo of the stack, devstack fetches origin's default branch and fast-forwards the worktree to it, with the same refusals. "+
			"The result lists each repo with its old and new commit, its status (updated, unchanged or refused) and the reason, then the services it restarted. "+
			"devstack does not add services. It reports each repo outside the stack that has the stack's branch on origin, and each service that a member now needs and the stack does not run. Each report names `stack add`. "+
			"If the new commits change a service's migrations directory, the result says so. A migration changes the shared database for every stack. Resync runs no migration. "+
			"This tool mirrors 'devstack stack resync'."),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(requiredStackNameDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace, so it can not manage stacks"), nil
		}
		name := strings.TrimSpace(request.GetString("name", ""))
		if name == "" {
			return mcp.NewToolResultError("name must not be empty"), nil
		}
		bindPane(ctx, ws, name)
		defer herdr.Notify()
		out, err := ResyncStack(ws, name, false)
		if err != nil {
			return mcp.NewToolResultError(out + err.Error()), nil
		}
		return mcp.NewToolResultText(out), nil
	})
}

func ResyncStack(ws *workspace.Workspace, name string, cli bool) (string, error) {
	return resyncStack(ws, name, restartStackServices(ws), cli)
}

func resyncStack(ws *workspace.Workspace, name string, restart stack.Restarter, cli bool) (string, error) {
	res, err := stack.Resync(ws, name, restart)
	if res == nil {
		return "", err
	}
	return formatResync(res, err, cli)
}

func formatResync(res *stack.ResyncResult, err error, cli bool) (string, error) {
	migrateFix := "Run the service's migration task with `task_run` when you are ready."
	if cli {
		migrateFix = "Run the service's migration task when you are ready."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Stack %q resync:\n", res.Name)
	for _, r := range res.Repos {
		fmt.Fprintf(&sb, "  %s (%s) branch %s: %s", r.Repo, strings.Join(r.Services, ", "), r.Branch, r.Status)
		if r.Old != r.New {
			fmt.Fprintf(&sb, " %s -> %s", shortSHA(r.Old), shortSHA(r.New))
		} else if r.Old != "" {
			fmt.Fprintf(&sb, " at %s", shortSHA(r.Old))
		}
		if r.Reason != "" {
			fmt.Fprintf(&sb, ": %s", r.Reason)
		}
		sb.WriteString("\n")
	}
	for _, o := range res.Outside {
		fmt.Fprintf(&sb, "Branch %s also changes %s, which this stack does not run. Run `stack add %s`.\n", res.Branch, o.Repo, strings.Join(o.Services, " "))
	}
	for _, m := range res.Missing {
		fmt.Fprintf(&sb, "%s. This stack does not run %s. Run `stack add %s`.\n", strings.Join(m.Chain, " needs "), m.Service, m.Service)
	}
	for _, svc := range res.Migrations {
		fmt.Fprintf(&sb, "The new commits change migrations in %s. A migration changes the shared database for every stack. %s\n", svc, migrateFix)
	}
	for _, n := range res.Notes {
		fmt.Fprintf(&sb, "note: %s\n", n)
	}
	switch {
	case len(res.Restart) == 0:
		sb.WriteString("No repo moved, so devstack restarted nothing.\n")
	case res.Down:
		fmt.Fprintf(&sb, "The stack is down, so devstack restarted nothing. Bring it up with stack_up to run %s.\n", strings.Join(res.Restart, ", "))
	default:
		fmt.Fprintf(&sb, "restarted: %s\n", strings.Join(res.Restarted, ", "))
	}
	if err != nil {
		sb.WriteString("\n")
	}
	return sb.String(), err
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func restartStackServices(ws *workspace.Workspace) stack.Restarter {
	return func(rec *stack.Record, services []string) ([]string, error) {
		client := tilt.NewClient("localhost", workspace.HostTiltPort)
		targets := make([]string, 0, len(services))
		for _, svc := range services {
			targets = append(targets, stack.ResourceName(ws.Name, svc, rec.Name))
		}
		hostdaemon.SyncAndReload(client, hostdaemon.ScopeNames(targets...))
		view, err := client.GetView()
		if err != nil {
			return nil, fmt.Errorf("devstack can not reach the host daemon: %w", err)
		}
		present, disabled := map[string]bool{}, map[string]bool{}
		for _, r := range view.UiResources {
			present[r.Metadata.Name] = true
			if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
				disabled[r.Metadata.Name] = true
			}
		}
		var restarted []string
		for i, svc := range services {
			rn := targets[i]
			if !present[rn] {
				continue
			}
			if disabled[rn] {
				if out, err := client.RunCLI("enable", rn); err != nil {
					return restarted, fmt.Errorf("devstack can not enable %s: %w\n%s", rn, err, out)
				}
			}
			if out, err := client.RunCLI("trigger", rn); err != nil {
				return restarted, fmt.Errorf("devstack can not restart %s: %w\n%s", rn, err, out)
			}
			restarted = append(restarted, svc)
		}
		return restarted, nil
	}
}
