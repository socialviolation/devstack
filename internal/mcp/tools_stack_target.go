package mcp

import (
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

// stackParamDesc is the shared description for the optional `stack` parameter the
// local tools accept. Absent = the base workspace this daemon is bound to.
const stackParamDesc = "Optional feature stack name to target instead of base. " +
	"Absent (or the literal \"base\") operates on the base workspace's service instances. When set to a stack name, the tool " +
	"operates on that stack's instances, which run in the one host daemon as <workspace>:<service>:<stack> resources, plus its worktree config."

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
	if stackName == "" || stackName == "base" {
		return basePath, "", nil
	}
	rec, err := resolveStackRecord(ws, stackName)
	if err != nil {
		return "", "", err
	}
	return rec.Root, fmt.Sprintf("stack %q", rec.Name), nil
}

// localTarget bundles everything a daemon-facing local tool needs to operate one
// instance: the Tilt client for the base daemon, the service dirs and config for
// its manifests, its default service, the namespace selecting a stack's resources,
// and a label naming it. A zero namespace and label mean the base workspace, so
// output stays byte-identical to today.
type localTarget struct {
	client      *tilt.Client
	serviceDirs map[string]string
	cfg         *config.WorkspaceConfig
	defaultSvc  string
	namespace   string
	label       string
}

// resolveLocalTarget picks the instance a daemon-facing tool acts on. Empty
// stackName returns base unchanged. A named stack's services run in the base
// workspace's one daemon as <service>:<stack> resources, so the returned target
// keeps base's client and carries the stack's namespace and worktree config — or a
// clear error (never a hang) when the stack is unknown, base's daemon is down, or
// the stack is not active.
func resolveLocalTarget(ws *workspace.Workspace, base localTarget, stackName string) (localTarget, error) {
	if stackName == "" || stackName == "base" {
		return base, nil
	}
	rec, err := resolveStackRecord(ws, stackName)
	if err != nil {
		return localTarget{}, err
	}
	if !stack.DaemonReachable(workspace.HostTiltPort) || !rec.Active {
		return localTarget{}, fmt.Errorf("stack %q is not up — run: devstack stack up %s", stackName, rec.Name)
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
		client:      base.client,
		serviceDirs: base.serviceDirs,
		cfg:         cfg,
		defaultSvc:  "",
		namespace:   rec.Name,
		label:       fmt.Sprintf("stack %q (host :%d)", rec.Name, workspace.HostTiltPort),
	}, nil
}

// resourceName is the host-daemon resource name for a service, matching tiltgen's
// hostName scheme: <workspace>:<service> for a base-workspace service, or
// <workspace>:<service>:<stack> for a feature stack folded into the host Tiltfile.
func resourceName(wsName, svc, namespace string) string {
	if namespace == "" {
		return wsName + ":" + svc
	}
	return wsName + ":" + svc + ":" + namespace
}

// splitHostResource decomposes a host-daemon resource name under prefix
// (<workspace>:) into its bare service and stack namespace. ok is false when the
// name belongs to a different workspace. A base-workspace resource yields an empty
// stack namespace.
func splitHostResource(name, prefix string) (svc, stackNS string, ok bool) {
	if !strings.HasPrefix(name, prefix) {
		return "", "", false
	}
	rest := name[len(prefix):]
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		return rest[:i], rest[i+1:], true
	}
	return rest, "", true
}

// stackResourceNames returns the full resource names in view that belong to the
// given workspace and namespace: <ws>:<svc> for the base (empty namespace), or
// <ws>:<svc>:<stack> for a stack. Other workspaces' resources are excluded.
func stackResourceNames(view *tilt.TiltView, wsName, namespace string) []string {
	prefix := wsName + ":"
	names := make([]string, 0, len(view.UiResources))
	for _, r := range view.UiResources {
		if _, ns, ok := splitHostResource(r.Metadata.Name, prefix); ok && ns == namespace {
			names = append(names, r.Metadata.Name)
		}
	}
	return names
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
