package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tasks"
	"github.com/socialviolation/devstack/internal/workspace"
)

func registerTaskRunTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("task_run",
		mcp.WithDescription("Run a task that a service declares, in a feature stack's worktree of that service. A task is a named shell command under 'tasks:' in the service manifest, such as a test suite, a seed or a codegen step. "+
			"This tool runs only declared tasks. It takes no command of its own. To list the tasks a service declares, omit task.\n\n"+
			"The task runs under sh -c in the stack's worktree, never in base. A task's workDir is relative to that worktree. Its run and env take the same references a hook takes: ${self.url}, ${self.port.<key>}, ${<service>.url} and ${<service>.port.<key>} resolve to the stack's own ports. "+
			"A task stops at its timeout (10m by default), and devstack kills its whole process group. The result gives the exit code, the time it took and the last 64KB of its output."),
		mcp.WithString("stack", mcp.Required(),
			mcp.Description(requiredStackNameDesc)),
		mcp.WithString("service", mcp.Required(),
			mcp.Description("Exact service name. The stack must have a worktree of it.")),
		mcp.WithString("task",
			mcp.Description("Name of a task the service declares. Omit it to list the declared tasks.")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		out, failed, err := runTask(ctx, ws,
			strings.TrimSpace(request.GetString("stack", "")),
			strings.TrimSpace(request.GetString("service", "")),
			strings.TrimSpace(request.GetString("task", "")))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if failed {
			return mcp.NewToolResultError(out), nil
		}
		return mcp.NewToolResultText(out), nil
	})
}

func runTask(ctx context.Context, ws *workspace.Workspace, stackName, service, taskName string) (string, bool, error) {
	if stackName == "" {
		return "", false, fmt.Errorf("task_run runs in a stack's worktree. Pass stack=<name>")
	}
	if service == "" {
		return "", false, fmt.Errorf("service must not be empty")
	}
	rec, err := resolveStackRecord(ws, stackName)
	if err != nil {
		return "", false, err
	}
	rw, err := stack.ResolveWorktree(rec)
	if err != nil {
		return "", false, err
	}
	svc, ok := rw.Services[service]
	if !ok || svc.Manifest == nil {
		return "", false, fmt.Errorf("stack %q has no worktree of service %q. To add one, use stack_add", rec.Name, service)
	}
	declared := svc.Manifest.Tasks

	if taskName == "" {
		return listTasks(rec.Name, service, declared), false, nil
	}
	task, ok := declared[taskName]
	if !ok {
		return "", false, fmt.Errorf("service %q declares no task %q. Declared tasks: %s", service, taskName, taskNames(declared))
	}

	names := make([]string, 0, len(rw.Services))
	for n := range rw.Services {
		names = append(names, n)
	}
	opts, err := stack.GenerateOptions(rec, names)
	if err != nil {
		return "", false, err
	}
	resolved := config.ServiceTask{WorkDir: task.WorkDir, Timeout: task.Timeout, Env: map[string]string{}}
	if resolved.Run, err = config.ResolveStackRefs(task.Run, service, opts.Book, opts.Stack); err != nil {
		return "", false, fmt.Errorf("task %q: %w", taskName, err)
	}
	for k, v := range task.Env {
		if resolved.Env[k], err = config.ResolveStackRefs(v, service, opts.Book, opts.Stack); err != nil {
			return "", false, fmt.Errorf("task %q env %s: %w", taskName, k, err)
		}
	}

	res, err := tasks.Run(ctx, svc.RepoPath, resolved, tasks.DefaultOutputCap)
	if err != nil {
		return "", false, fmt.Errorf("task %q: %w", taskName, err)
	}

	var sb strings.Builder
	switch {
	case res.TimedOut:
		fmt.Fprintf(&sb, "task %s/%s in stack %q TIMED OUT after %s. devstack killed its process group.\n", service, taskName, rec.Name, res.Duration.Round(time.Millisecond))
	default:
		fmt.Fprintf(&sb, "task %s/%s in stack %q exited %d after %s.\n", service, taskName, rec.Name, res.Exit, res.Duration.Round(time.Millisecond))
	}
	if res.Truncated {
		fmt.Fprintf(&sb, "output (the last %d bytes):\n", tasks.DefaultOutputCap)
	} else {
		sb.WriteString("output:\n")
	}
	sb.WriteString(res.Tail)
	return sb.String(), res.TimedOut || res.Exit != 0, nil
}

func listTasks(stackName, service string, declared map[string]config.ServiceTask) string {
	if len(declared) == 0 {
		return fmt.Sprintf("service %q declares no tasks in stack %q. Declare them under 'tasks:' in its service manifest.\n", service, stackName)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "tasks of service %q in stack %q:\n", service, stackName)
	for _, name := range sortedTaskNames(declared) {
		t := declared[name]
		fmt.Fprintf(&sb, "  %s\n      run: %s\n", name, t.Run)
		if t.WorkDir != "" {
			fmt.Fprintf(&sb, "      workDir: %s\n", t.WorkDir)
		}
		if t.Timeout != "" {
			fmt.Fprintf(&sb, "      timeout: %s\n", t.Timeout)
		}
	}
	return sb.String()
}

func taskNames(declared map[string]config.ServiceTask) string {
	if len(declared) == 0 {
		return "none"
	}
	return strings.Join(sortedTaskNames(declared), ", ")
}

func sortedTaskNames(declared map[string]config.ServiceTask) []string {
	names := make([]string, 0, len(declared))
	for n := range declared {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
