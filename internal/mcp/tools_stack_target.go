package mcp

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

// stackParamDesc is the shared description for the optional `stack` parameter the
// local tools accept. Absent = the base workspace this daemon is bound to.
const stackParamDesc = "Optional feature stack name to target instead of the base workspace. " +
	"Absent (default) operates on the base workspace, unchanged. When set, the tool operates the named stack's own " +
	"instance — its daemon and its worktree config — not base's."

// resolveStackRecord looks up a feature stack by short name within the bound
// (base) workspace, returning a clear error that lists the available stack names
// when the name is unknown.
func resolveStackRecord(ws *workspace.Workspace, name string) (*stack.Record, error) {
	if ws == nil {
		return nil, fmt.Errorf("no base workspace resolved to look up stack %q", name)
	}
	rec, err := stack.FindStack(ws.Name, name)
	if err == nil {
		return rec, nil
	}
	recs, lerr := stack.LoadStore(ws.Name)
	if lerr != nil || len(recs) == 0 {
		return nil, fmt.Errorf("stack %q not found in workspace %q (it has no feature stacks — create one with: devstack stack create %s --repos <svc>)", name, ws.Name, name)
	}
	avail := make([]string, 0, len(recs))
	for _, r := range recs {
		avail = append(avail, r.Name)
	}
	return nil, fmt.Errorf("stack %q not found in workspace %q. Available stacks: %s", name, ws.Name, strings.Join(avail, ", "))
}

// serviceEnvTarget resolves the workspace path service_env reads and writes for
// an optional stack param. Empty stack → the bound (base) workspace path,
// byte-for-byte today's behavior. A named stack → the stack's synthesised root,
// whose generated manifest points at the stack's worktrees, so every read and
// write lands in the worktree and never in base.
func serviceEnvTarget(ws *workspace.Workspace, basePath, stackName string) (path, instance string, err error) {
	if stackName == "" {
		return basePath, "", nil
	}
	rec, err := resolveStackRecord(ws, stackName)
	if err != nil {
		return "", "", err
	}
	return rec.Root, fmt.Sprintf("stack %q", rec.Name), nil
}

// localTarget bundles everything a daemon-facing local tool needs to operate one
// instance: the Tilt client for its daemon, the service dirs and config for its
// manifests, its default service, and a label naming it. A zero label means the
// base workspace, so output stays byte-identical to today.
type localTarget struct {
	client      *tilt.Client
	serviceDirs map[string]string
	cfg         *config.WorkspaceConfig
	defaultSvc  string
	label       string
}

// resolveLocalTarget picks the instance a daemon-facing tool acts on. Empty
// stackName returns base unchanged. A named stack returns a target bound to the
// stack's own daemon, its worktree manifests, and a naming label — or a clear
// error (never a hang) when the stack is unknown or its daemon isn't running.
func resolveLocalTarget(ws *workspace.Workspace, base localTarget, stackName string) (localTarget, error) {
	if stackName == "" {
		return base, nil
	}
	rec, err := resolveStackRecord(ws, stackName)
	if err != nil {
		return localTarget{}, err
	}
	if !stack.DaemonReachable(rec.DaemonPort) {
		return localTarget{}, fmt.Errorf("stack %q daemon is not running on :%d — start it with: (cd %s && devstack up)", stackName, rec.DaemonPort, rec.Root)
	}
	cfg, _ := config.Load(rec.Root)
	if cfg == nil {
		cfg = &config.WorkspaceConfig{
			Deps:         map[string][]string{},
			Groups:       map[string][]string{},
			ServicePaths: map[string]string{},
		}
	}
	return localTarget{
		client:      rec.DaemonClient(),
		serviceDirs: tilt.ParseTiltfileServeDirs(filepath.Join(rec.Root, "Tiltfile")),
		cfg:         cfg,
		defaultSvc:  "",
		label:       fmt.Sprintf("stack %q (:%d)", rec.Name, rec.DaemonPort),
	}, nil
}

// targetHeader returns a one-line banner naming the instance a tool acted on, or
// "" for the base workspace so its output is byte-identical to today.
func targetHeader(label string) string {
	if label == "" {
		return ""
	}
	return "target: " + label + "\n\n"
}

// prependInstanceResult adds a banner naming the instance a service_env action
// acted on, so a set never silently edits the wrong repo. For the base workspace
// (empty instance) and for error results it returns the result unchanged, keeping
// the non-stack path byte-identical to today.
func prependInstanceResult(res *mcp.CallToolResult, instance string) *mcp.CallToolResult {
	if instance == "" || res == nil || res.IsError {
		return res
	}
	banner := mcp.NewTextContent(fmt.Sprintf("operating on %s\n\n", instance))
	res.Content = append([]mcp.Content{banner}, res.Content...)
	return res
}

// onTarget annotates a success message with the instance it acted on, or returns
// it unchanged for the base workspace (byte-identical to today).
func onTarget(label, msg string) string {
	if label == "" {
		return msg
	}
	return msg + " [" + label + "]"
}
