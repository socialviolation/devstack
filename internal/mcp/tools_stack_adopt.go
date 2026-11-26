package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/panel"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
	"github.com/socialviolation/devstack/internal/worktree"
)

func registerStackAdoptTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_adopt",
		mcp.WithDescription("Hand an agent's feature stack to the owner. devstack clears the stack's owner, so no agent may act on it. The stop time stays as it is. "+
			"The record keeps the agent's name as adopted-from, and stack_list shows it. "+
			"The result names the stack, each worktree with its branch, whether the stack is up, and its tailnet addresses. "+
			"A stack that already belongs to the owner is left as it is. "+
			"This tool mirrors 'devstack stack adopt'."),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(requiredStackNameDesc)),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("no workspace resolved for stacks"), nil
		}
		name := strings.TrimSpace(request.GetString("name", ""))
		if name == "" {
			return mcp.NewToolResultError("name must not be empty"), nil
		}
		out, err := AdoptStack(ctx, ws, name)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(out), nil
	})
}

func AdoptStack(ctx context.Context, ws *workspace.Workspace, name string) (string, error) {
	rec, adopted, err := stack.Adopt(ws.Name, name, time.Now())
	if err != nil {
		return "", err
	}
	if !adopted {
		return fmt.Sprintf("Stack %q already belongs to the owner. devstack changed nothing.\n", rec.Name), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Adopted stack %q from %s. Agents may not act on it.\n", rec.Name, rec.AdoptedFrom)
	status := stack.StatusDown
	if rec.Active {
		status = stack.StatusUp
	}
	fmt.Fprintf(&sb, "status: %s\n", status)

	services := map[string][]string{}
	for svc, path := range rec.Worktrees {
		services[path] = append(services[path], svc)
	}
	paths := make([]string, 0, len(services))
	for p := range services {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		sort.Strings(services[p])
		branch, err := worktree.CurrentBranch(p)
		if err != nil {
			branch = "unknown"
		}
		fmt.Fprintf(&sb, "  %s (%s) branch %s\n", p, strings.Join(services[p], ", "), branch)
	}

	var urls []string
	for _, w := range panel.Take(ctx).Workspaces {
		if w.Name != ws.Name {
			continue
		}
		for _, st := range w.Stacks {
			if !strings.EqualFold(st.Name, rec.Name) {
				continue
			}
			for _, svc := range st.Services {
				for _, u := range svc.URLs {
					urls = append(urls, fmt.Sprintf("%s=%s", svc.Name, u))
				}
			}
		}
	}
	if len(urls) > 0 {
		fmt.Fprintf(&sb, "urls: %s\n", strings.Join(urls, " "))
	} else {
		sb.WriteString("urls: none. The stack is down, or the machine publishes none of its services.\n")
	}
	return sb.String(), nil
}
