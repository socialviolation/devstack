package mcp

import (
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

// For the tools that do not start, stop or restart anything: absent = the base
// workspace this daemon is bound to.
const stackParamDesc = "Optional. Name a feature stack to act on instead of base. " + baseTermDesc +
	"Absent (or the literal \"base\") acts on base's service copies. A stack name acts on that stack's copies. " +
	"Those copies run in the one host daemon as <workspace>:<service>:<stack> resources. The tool also reads that stack's worktree config. " +
	"On THIS tool, absent means base. On the tools that start, stop or restart a service, absent does not mean base. There, devstack reads the copy from the working directory, or the call fails."

// Separate from stackParamDesc because absent no longer means base: base runs
// from a replica, not from the checkouts, so a call that silently defaulted to
// base would act on code the caller is not looking at.
const mutatingStackParamDesc = "Which copy to act on: a feature stack's SHORT name (for example 'import-review'), or the literal \"base\". " + baseTermDesc +
	"This tool changes what runs, so it has NO implicit default. If you omit this parameter, devstack reads the copy from the server's working directory. " +
	"That directory must be a stack worktree, or base's replica. If the directory is neither, the call fails and lists the stacks available. " +
	"To act on base, write \"base\". An omitted parameter is never a safe way to mean base."

// resolveStackRecord looks up a feature stack by short name within the bound
// (base) workspace, returning a clear error that lists the available stack names
// when the name is unknown.
func resolveStackRecord(ws *workspace.Workspace, name string) (*stack.Record, error) {
	if ws == nil {
		return nil, fmt.Errorf("devstack found no base workspace, so it can not look up stack %q", name)
	}
	rec, err := stack.FindStack(ws.Name, name)
	if err == nil {
		return rec, nil
	}
	recs, lerr := stack.LoadStore(ws.Name)
	if lerr != nil || len(recs) == 0 {
		return nil, fmt.Errorf("stack %q is not in workspace %q. That workspace has no feature stacks. To create one, run: devstack stack create %s --repos <svc>", name, ws.Name, name)
	}
	avail := make([]string, 0, len(recs))
	for _, r := range recs {
		avail = append(avail, r.Name)
	}
	return nil, fmt.Errorf("stack %q is not in workspace %q. Available stacks: %s", name, ws.Name, strings.Join(avail, ", "))
}

// serviceEnvTarget resolves the workspace path service_env reads and writes for
// an optional stack param. Empty stack → the bound (base) workspace path,
// byte-for-byte today's behavior. A named stack → the stack's synthesised root,
// whose generated manifest points at the stack's worktrees, so every read and
// write lands in the worktree and never in base.
func serviceEnvTarget(ws *workspace.Workspace, basePath, stackName string) (path, instance, stackEnv string, err error) {
	if stackName == "" || stackName == "base" {
		return basePath, "", "", nil
	}
	rec, err := resolveStackRecord(ws, stackName)
	if err != nil {
		return "", "", "", err
	}
	return rec.Root, fmt.Sprintf("stack %q", rec.Name), rec.Env, nil
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
		return localTarget{}, fmt.Errorf("stack %q is not up. Its worktrees and its record exist, and none of its services run. There is nothing here to act on. To bring it up, use the stack_up tool (CLI: devstack stack up %s). Then call this tool again", stackName, rec.Name)
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
		serviceDirs: cfg.ServicePaths,
		cfg:         cfg,
		defaultSvc:  "",
		namespace:   rec.Name,
		label:       fmt.Sprintf("stack %q (host :%d)", rec.Name, workspace.HostTiltPort),
	}, nil
}

// replicaServiceDirs is where base's copies actually run: the replica worktrees,
// not the checkouts they were built from. PATH and BRANCH are read to answer "is
// my work what is running", and the checkout answers it wrongly — it can sit on
// any branch, dirty, while base runs a detached worktree at the default branch
// tip. Falls back to the checkouts, which is then the truth, when no replica is
// built.
func replicaServiceDirs(ws *workspace.Workspace, checkouts map[string]string) map[string]string {
	rw, err := replica.Resolve(ws)
	if err != nil {
		return checkouts
	}
	dirs := make(map[string]string, len(rw.Services))
	for name, svc := range rw.Services {
		dirs[name] = svc.RepoPath
	}
	return dirs
}

func resolveMutatingTarget(ws *workspace.Workspace, base localTarget, stackParam string) (localTarget, error) {
	name, err := stack.ResolveTarget(ws, stackParam)
	if err != nil {
		return localTarget{}, err
	}
	return resolveLocalTarget(ws, base, name)
}

// targetGroupMembers resolves a group name against the instance a tool acts on,
// with the two answers a stack makes wrong put right.
//
// A stack's generated manifest lists only the group members that made it into
// the overlay. So a group half in a stack silently resolves to that half, and a
// group not in it at all reads as a name that does not exist — while the group
// does exist, on base. Both readings are true of the stack and false of the
// workspace, and the caller asked about the workspace. The returned note is
// empty unless the group falls short, and belongs in the tool's result text.
func targetGroupMembers(ws *workspace.Workspace, t localTarget, groupName string) (members []string, note string, err error) {
	members, ok := t.cfg.Groups[groupName]
	if t.namespace == "" {
		if !ok {
			return nil, "", fmt.Errorf("group %q not found — available groups: %s", groupName, availableGroups(t.cfg))
		}
		return members, "", nil
	}

	baseGroups := map[string][]string{}
	if ws != nil {
		if cfg, cerr := config.Load(ws.Path); cerr == nil && cfg != nil {
			baseGroups = cfg.Groups
		}
	}
	if !ok {
		onBase, isBaseGroup := baseGroups[groupName]
		if !isBaseGroup {
			return nil, "", fmt.Errorf("group %q not found — available groups: %s", groupName, availableGroups(t.cfg))
		}
		return nil, "", fmt.Errorf("group %q has no services in stack %q. It runs entirely on base (%s). There is nothing of it here to act on. To act on base's copies, call this tool again with stack=\"base\"",
			groupName, t.namespace, strings.Join(onBase, ", "))
	}
	for _, cov := range stack.CoverageOf([]string{groupName}, members, baseGroups) {
		if cov.Complete() {
			continue
		}
		note = fmt.Sprintf("stack %s %s, and this call does not touch them. To act on base's copies, use stack=\"base\".\n",
			t.namespace, cov.Sentence())
	}
	return members, note, nil
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
	banner := mcp.NewTextContent(fmt.Sprintf("target: %s\n\n", instance))
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
