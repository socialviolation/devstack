package mcp

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/herdr"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

func registerStackExtendTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("stack_extend",
		mcp.WithDescription(fmt.Sprintf("Push back the stop time of a feature stack. When the stop time passes, devstack stops the stack, the same way stack_down stops it. devstack never removes it. "+
			"%s "+
			"stack_up also sets a new stop time: %s after the up. "+
			"This tool mirrors 'devstack stack extend'.", stack.ExtendRule(), WindowText(stack.StopAfter))),
		mcp.WithString("name", mcp.Required(),
			mcp.Description(requiredStackNameDesc)),
		mcp.WithNumber("hours", mcp.Required(),
			mcp.Description(fmt.Sprintf("How many hours to add. It must be more than 0 and %d or less.", int(stack.MaxExtend.Hours())))),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if ws == nil {
			return mcp.NewToolResultError("devstack resolved no workspace, so it can not manage stacks"), nil
		}
		name := strings.TrimSpace(request.GetString("name", ""))
		if name == "" {
			return mcp.NewToolResultError("name must not be empty"), nil
		}
		out, err := ExtendStack(ctx, ws, name, request.GetFloat("hours", 0))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(out), nil
	})
}

func ExtendStack(ctx context.Context, ws *workspace.Workspace, name string, hours float64) (string, error) {
	if hours <= 0 {
		return "", fmt.Errorf("hours must be more than 0")
	}
	stopAt, err := stack.Extend(ws.Name, name, time.Duration(hours*float64(time.Hour)), stack.Now())
	if err != nil {
		return "", err
	}
	bindPane(ctx, ws, name)
	herdr.Notify()
	return fmt.Sprintf("Stack %q now stops at %s.", name, stopAt.Local().Format(time.RFC3339)), nil
}

func Sweep(dryRun bool, w io.Writer) (int, error) {
	mutating.Lock()
	defer mutating.Unlock()

	all, err := workspace.All()
	if err != nil {
		return 0, err
	}
	now := stack.Now()
	var failed []string
	found := 0
	for i := range all {
		ws := &all[i]
		due, err := stack.Due(ws.Name, now)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", ws.Name, err))
			continue
		}
		for _, rec := range due {
			found++
			stopAt := rec.StopAt.Local().Format(time.RFC3339)
			if dryRun {
				fmt.Fprintf(w, "would stop %s (stop time %s)\n", rec.FullName(), stopAt)
				continue
			}
			fmt.Fprintf(w, "stopping %s (stop time %s)\n", rec.FullName(), stopAt)
			res, stopped := stackDown(ws, rec.Name, stack.StoppedBySweep, now)
			if !stopped {
				found--
				fmt.Fprintf(w, "devstack does not stop %s. It is down now, or its stop time moved.\n", rec.FullName())
				continue
			}
			fmt.Fprintln(w, toolResultText(res))
			if res.IsError {
				failed = append(failed, rec.FullName())
				continue
			}
			if _, _, err := stack.AppendNote(ws.Name, rec.Name, "stopped by devstack: "+WindowText(stack.StopAfter)+" since up"); err != nil {
				fmt.Fprintf(w, "devstack can not add the log entry to %s: %v\n", rec.FullName(), err)
			}
		}
	}
	if found == 0 && len(failed) == 0 {
		fmt.Fprintln(w, "No stack that is up is past its stop time.")
	}
	if found > 0 && !dryRun {
		herdr.Notify()
	}
	if len(failed) > 0 {
		return found, fmt.Errorf("devstack did not stop these stacks: %s", strings.Join(failed, ", "))
	}
	return found, nil
}

func WindowText(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
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
