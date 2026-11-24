package mcp

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

func agentQuota(workspaceName string, max int) error {
	recs, err := stack.LoadStore(workspaceName)
	if err != nil {
		return err
	}
	var held []string
	for _, r := range recs {
		if r.Owner == "" {
			continue
		}
		held = append(held, fmt.Sprintf("%s (owner %s, expires %s)", r.Name, r.Owner, r.ExpiresAt.Local().Format(time.RFC3339)))
	}
	if len(held) < max {
		return nil
	}
	return fmt.Errorf("agents hold %d of %d agent stacks, so devstack creates no more. Remove one with stack_rm first. Agent stacks: %s",
		len(held), max, strings.Join(held, "; "))
}

func registerStackExtendTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_extend",
		mcp.WithDescription(fmt.Sprintf("Push back the expiry of a feature stack that an agent created. An expired stack is removed, the same way stack_rm removes it. "+
			"The new expiry is the later of the current expiry and now, plus the hours you give. No stack lives past %d hours from its creation, so devstack caps the expiry there. "+
			"A stack that the owner created has no expiry, and this tool refuses it.", int(stack.MaxLifetime.Hours()))),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(requiredStackNameDesc)),
		mcp.WithNumber("hours", mcp.Required(),
			mcp.Description("How many hours to add. It must be more than 0.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
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
		hours := request.GetFloat("hours", 0)
		if hours <= 0 {
			return mcp.NewToolResultError("hours must be more than 0"), nil
		}
		expires, err := stack.Extend(ws.Name, name, time.Duration(hours*float64(time.Hour)), time.Now())
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Stack %q now expires at %s.", name, expires.Local().Format(time.RFC3339))), nil
	})
}

func CollectExpired(ws *workspace.Workspace, now time.Time, dryRun bool, w io.Writer) error {
	mutating.Lock()
	defer mutating.Unlock()

	expired, err := stack.Expired(ws.Name, now)
	if err != nil {
		return err
	}
	if len(expired) == 0 {
		fmt.Fprintf(w, "No expired stacks in workspace %q.\n", ws.Name)
		return nil
	}
	var failed []string
	for _, rec := range expired {
		if dryRun {
			fmt.Fprintf(w, "would remove %s (owner %s, expired %s)\n", rec.Name, rec.Owner, rec.ExpiresAt.Local().Format(time.RFC3339))
			continue
		}
		fmt.Fprintf(w, "removing %s (owner %s, expired %s)\n", rec.Name, rec.Owner, rec.ExpiresAt.Local().Format(time.RFC3339))
		if rec.Active {
			res := stackDown(ws, rec.Name)
			fmt.Fprintln(w, toolResultText(res))
			if res.IsError {
				failed = append(failed, rec.Name)
				continue
			}
		}
		res := removeStack(ws, rec.Name, rec.Owner != "")
		fmt.Fprintln(w, toolResultText(res))
		if res.IsError {
			failed = append(failed, rec.Name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("devstack did not remove these expired stacks: %s", strings.Join(failed, ", "))
	}
	return nil
}

func toolResultText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if t, ok := c.(mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}
