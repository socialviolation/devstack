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

const stackParamDesc = "Optional. The SHORT name of the stack to read. " + stackTermDesc +
	"If you omit it, the tool reads the shared set only, and a service that is not shared fails with an error that asks for this parameter. " +
	"A stack name reads that stack's services. They run in the one host daemon as <workspace>:<service>:<stack> resources."

const mutatingStackParamDesc = "Which stack to act on: the stack's SHORT name (for example 'import-review'). " +
	"If you omit this parameter, the server's working directory decides. A stack worktree selects that stack. " +
	"Every other directory selects no stack. Then only a shared service resolves: a service in the workspace \"shared:\" list. " +
	"A service that is not shared runs only in a stack, so the call fails and asks for this parameter. " +
	"Every stack uses a shared service. If you restart or stop one, you restart or stop the copy that every stack uses. " +
	"An agent can not start, restart or stop a shared service. devstack refuses the call."

func resolveStackRecord(ws *workspace.Workspace, name string) (*stack.Record, error) {
	if ws == nil {
		return nil, fmt.Errorf("devstack resolved no workspace, so it can not look up stack %q", name)
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

func serviceEnvTarget(ws *workspace.Workspace, basePath, stackName string) (path, instance, stackEnv string, err error) {
	if stackName == "" {
		return basePath, "", "", nil
	}
	rec, err := resolveStackRecord(ws, stackName)
	if err != nil {
		return "", "", "", err
	}
	return rec.Root, fmt.Sprintf("stack %q", rec.Name), rec.Env, nil
}

type localTarget struct {
	client      *tilt.Client
	serviceDirs map[string]string
	cfg         *config.WorkspaceConfig
	defaultSvc  string
	namespace   string
	label       string
}

func resolveLocalTarget(ws *workspace.Workspace, base localTarget, stackName string) (localTarget, error) {
	if stackName == "" {
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
		inWorkspace, isWorkspaceGroup := baseGroups[groupName]
		if !isWorkspaceGroup {
			return nil, "", fmt.Errorf("group %q not found — available groups: %s", groupName, availableGroups(t.cfg))
		}
		return nil, "", fmt.Errorf("group %q has no services in stack %q. Its services are %s. There is nothing of it here to act on. To add them to the stack, use stack_add",
			groupName, t.namespace, strings.Join(inWorkspace, ", "))
	}
	for _, cov := range stack.CoverageOf([]string{groupName}, members, baseGroups) {
		if cov.Complete() {
			continue
		}
		note = fmt.Sprintf("stack %s %s, and this call does not touch them. To add them to the stack, use stack_add.\n",
			t.namespace, cov.Sentence())
	}
	return members, note, nil
}

func resourceName(wsName, svc, namespace string) string {
	if namespace == "" {
		return wsName + ":" + svc
	}
	return wsName + ":" + svc + ":" + namespace
}

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

func targetHeader(label string) string {
	if label == "" {
		return ""
	}
	return "target: " + label + "\n\n"
}

func prependInstanceResult(res *mcp.CallToolResult, instance string) *mcp.CallToolResult {
	if instance == "" || res == nil || res.IsError {
		return res
	}
	banner := mcp.NewTextContent(fmt.Sprintf("target: %s\n\n", instance))
	res.Content = append([]mcp.Content{banner}, res.Content...)
	return res
}

func onTarget(label, msg string) string {
	if label == "" {
		return msg
	}
	return msg + " [" + label + "]"
}

func requireSharedNoStack(ws *workspace.Workspace, t localTarget, services []string) error {
	if t.namespace != "" || ws == nil {
		return nil
	}
	rw, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return nil
	}
	return stack.RequireShared(rw, services, `stack="<name>"`)
}
