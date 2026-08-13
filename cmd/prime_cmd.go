package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/gitinfo"
	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/selfcheck"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

// primeCharBudget is the ceiling a SessionStart hook will accept before Claude
// Code truncates the output to a file and shows a preview instead. Everything
// here is generated per session, so it earns its place against that budget or it
// belongs behind `devstack <command> --help`.
const primeCharBudget = 9000

var primeCmd = &cobra.Command{
	Use:    "prime",
	Hidden: true,
	Short:  "Brief an agent on where it is, what runs, and which stack it is probably here for",
	Long: `Print the session-start briefing for this directory: the workspace and the
service you are in, the feature stack the checkout belongs to, what runs now,
and what this workspace uses.

The binary generates this briefing at each session start, so it can not go stale
the way a committed AGENTS.md does. Install a new devstack, and every workspace
is briefed correctly on its next session.

To install it in Claude Code as a SessionStart hook, put this in
.claude/settings.json:

  {
    "hooks": {
      "SessionStart": [
        {
          "matcher": "startup",
          "hooks": [{"type": "command", "command": "devstack prime --json"}]
        }
      ]
    }
  }

With --json, devstack writes the hookSpecificOutput envelope that the hook
expects. The plain output is for you to read.`,
	SilenceUsage: true,
	RunE:         runPrime,
}

func init() {
	rootCmd.AddCommand(primeCmd)
	primeCmd.Flags().Bool("json", false, "Write the Claude Code SessionStart hookSpecificOutput envelope")
}

type workingStack struct {
	Rec     *stack.Record
	Reason  string
	Certain bool
}

func inferWorkingStack(ws *workspace.Workspace, service, branch string) *workingStack {
	if _, rec, err := stack.DetectFromCwd(); err == nil && rec != nil {
		return &workingStack{Rec: rec, Reason: "you are in the worktree of this stack", Certain: true}
	}

	recs, err := stack.LoadStore(ws.Name)
	if err != nil || len(recs) == 0 {
		return nil
	}

	if branch != "" {
		bare := strings.TrimSuffix(branch, "*")
		for i := range recs {
			if recs[i].Branch != "" && recs[i].Branch == bare {
				return &workingStack{Rec: &recs[i], Reason: "the branch of this stack is checked out here", Certain: true}
			}
		}
	}

	if service == "" {
		return nil
	}
	var candidates []*stack.Record
	for i := range recs {
		if containsString(recs[i].Overlay, service) {
			candidates = append(candidates, &recs[i])
		}
	}
	if len(candidates) == 1 {
		return &workingStack{Rec: candidates[0], Reason: "this is the only stack that runs " + service}
	}
	return nil
}

func runPrime(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")

	body, err := buildPrime()
	if err != nil {
		// A briefing that cannot resolve must not fail the session it is briefing.
		body = fmt.Sprintf("devstack: can not brief this directory: %v", err)
	}
	if len(body) > primeCharBudget {
		body = body[:primeCharBudget] + "\n… truncated. Run: devstack status"
	}

	if !asJSON {
		fmt.Println(body)
		return nil
	}
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": body,
		},
	}
	enc := json.NewEncoder(os.Stdout)
	return enc.Encode(out)
}

func buildPrime() (string, error) {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return "", err
	}
	rw, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return "", err
	}

	cwd, _ := os.Getwd()
	service := ""
	if cwd != "" {
		if id, ierr := config.ResolveIdentity(cwd); ierr == nil {
			service = id.ServiceName
		}
	}

	repo := ""
	if svc, ok := rw.Services[service]; ok && service != "" {
		repo = svc.RepoPath
	}
	branch := ""
	if repo != "" {
		branch = gitinfo.ReadAll(map[string]string{repo: repo})[repo].Label()
	}

	here := "base"
	inReplica := false
	var hereRec *stack.Record
	siblings := map[string]string{}
	if _, rec, derr := stack.DetectFromCwd(); derr == nil && rec != nil {
		here = rec.Name
		hereRec = rec
		siblings = stackSiblings(rw, rec)
		if wt, ok := rec.Worktrees[service]; ok && wt != "" {
			repo = wt
			branch = gitinfo.ReadAll(map[string]string{wt: wt})[wt].Label()
		}
	} else if rws, rerr := replica.DetectFromCwd(); rerr == nil && rws != nil && strings.EqualFold(rws.Name, ws.Name) {
		inReplica = true
	}

	working := inferWorkingStack(ws, service, branch)

	var candidates []stack.Record
	if working == nil {
		candidates, _ = stack.LoadStore(ws.Name)
	}

	var b strings.Builder
	writePrimeTask(&b, service, here, working, siblings, rw.Manifest.Observability.IsEnabled(), inReplica, candidates)
	writePrimeWhatThisIs(&b)
	writePrimeTerms(&b)

	section(&b, "WHERE YOU ARE")
	writePrimeIdentity(&b, ws, service, here, inReplica)

	switch {
	case service != "":
		section(&b, "THIS SERVICE — "+service)
		writePrimeInstances(&b, ws, rw, service, here, working, inTemplateCheckout(ws, here, inReplica))
		writePrimeReload(&b, rw, service, here, inReplica, config.HasWorkspaceManifest(replica.Root(ws)), working)
	case hereRec != nil:
		section(&b, "THIS DIRECTORY")
		writePrimeStackDirectory(&b, hereRec, here, cwd, siblings)
	}

	section(&b, "THIS WORKSPACE — "+ws.Name)
	writePrimeLiveCount(&b, ws)
	writePrimeApplies(&b, ws, rw)

	writePrimeSafety(&b)

	section(&b, "REFERENCE")
	b.WriteString("devstack status · devstack urls · devstack <command> --help · devstack stack list · devstack hooks list\n")
	return strings.TrimRight(b.String(), "\n"), nil
}

func writePrimeTask(b *strings.Builder, service, here string, working *workingStack, siblings map[string]string, telemetry, inReplica bool, candidates []stack.Record) {
	b.WriteString("## YOUR TASK\n")
	switch {
	case working == nil || working.Rec == nil:
		writePrimeNoStackTask(b, inReplica, candidates)
	case working.Rec.Name == here:
		writePrimeStackTask(b, working.Rec, service, siblings, telemetry)
	default:
		writePrimeOtherStackTask(b, working, service)
	}
}

func writePrimeStackTask(b *strings.Builder, rec *stack.Record, service string, siblings map[string]string, telemetry bool) {
	fmt.Fprintf(b, "stack %s", rec.Name)
	if len(rec.Overlay) > 0 {
		fmt.Fprintf(b, " · %s", strings.Join(rec.Overlay, ", "))
	}
	b.WriteString("\n")
	writePrimeStackNote(b, rec)

	b.WriteString("\n")
	if len(rec.Overlay) == 0 {
		fmt.Fprintf(b, "1. This stack runs no service yet. Add the one this feature changes: devstack stack add %s <service>\n", rec.Name)
	} else {
		b.WriteString("1. Change code in these directories, and in no others:\n")
		writePrimeOverlayDirs(b, rec)
		writePrimeSiblingCaution(b, siblings)
	}
	fmt.Fprintf(b, "2. Restart the copy you changed: devstack service restart %s --stack %s\n", taskService(rec, service), rec.Name)
	writePrimeReadStep(b, rec.Name, telemetry)
	fmt.Fprintf(b, "4. Record where you got to: devstack stack note %s --add \"what you found\"\n", rec.Name)
	writePrimeCloseOut(b, rec.Branch)
	b.WriteString("Every commit you make here goes on the branch of this stack, and not on base.\n")
	b.WriteString("Everything else runs from base. The user and every other stack share base, and devstack\n")
	b.WriteString("keeps base current. Do not change base to finish this feature.\n")
	fmt.Fprintf(b, "To make this stack run one more service, add it: devstack stack add %s <service>\n", rec.Name)
}

func writePrimeOtherStackTask(b *strings.Builder, working *workingStack, service string) {
	rec := working.Rec
	fmt.Fprintf(b, "stack %s · a guess: %s\n", rec.Name, working.Reason)
	if len(rec.Overlay) > 0 {
		fmt.Fprintf(b, "services %s\n", strings.Join(rec.Overlay, ", "))
	}
	writePrimeStackNote(b, rec)

	fmt.Fprintf(b, "\n1. Ask the user: is this session for %s? Change no code until you have the answer.\n", rec.Name)
	if dir := rec.Worktrees[taskService(rec, service)]; dir != "" {
		fmt.Fprintf(b, "2. Work in the directory of that stack: %s\n", dir)
		b.WriteString("   You are not in it now. A change you make here does not reach that stack.\n")
	} else {
		b.WriteString("2. Work in the directory of that stack. You are not in it now, and a change you make here\n   does not reach it.\n")
	}
	fmt.Fprintf(b, "3. Restart the copy you changed: devstack service restart %s --stack %s\n", taskService(rec, service), rec.Name)
	fmt.Fprintf(b, "4. Record where you got to: devstack stack note %s --add \"what you found\"\n", rec.Name)
	writePrimeCloseOut(b, rec.Branch)
	b.WriteString("Everything else runs from base. The user and every other stack share base, and devstack\n")
	b.WriteString("keeps base current. Do not change base to finish this feature.\n")
}

func writePrimeNoStackTask(b *strings.Builder, inReplica bool, candidates []stack.Record) {
	b.WriteString("no stack. Nothing in this directory belongs to one, and devstack can not guess which feature\n")
	b.WriteString("this session is for.\n")
	writePrimeCandidates(b, candidates)
	b.WriteString("\n1. Ask the user which feature this session is for.\n")
	b.WriteString("2. To see a change run, cut a stack for it: devstack stack create <name> --repos <service>\n")
	b.WriteString("   Then work in the directory that command prints.\n")
	b.WriteString("3. See what runs now, and where: devstack status\n")
	if inReplica {
		b.WriteString("This directory is devstack's own copy of base, and `devstack workspace up` overwrites it. Do not edit here.\n")
		return
	}
	b.WriteString("A change you make here reaches base only on the default branch, after `devstack workspace up`.\n")
}

const primeCandidateRows = 5

func writePrimeCandidates(b *strings.Builder, recs []stack.Record) {
	if len(recs) == 0 {
		return
	}
	ranked := rankStackCandidates(recs)
	shown := ranked
	if len(shown) > primeCandidateRows {
		shown = shown[:primeCandidateRows]
	}

	width := 0
	for _, r := range shown {
		if len(r.Name) > width {
			width = len(r.Name)
		}
	}

	fmt.Fprintf(b, "\n%s in flight. devstack ranks them: a stack that is up first, then the newest note\n", pluralStack(len(ranked)))
	b.WriteString("entry, then the newest stack.\n")
	for _, r := range shown {
		state := "down"
		if r.Active {
			state = "up"
		}
		fmt.Fprintf(b, "  ? %-*s %-4s %s\n", width, r.Name, state, orDash(firstLine(r.Note, 60)))
	}
	if n := len(ranked) - len(shown); n > 0 {
		fmt.Fprintf(b, "  %d more. To see every one, run: devstack stack list\n", n)
	}
	b.WriteString("  The marker ? shows a guess about intent, and never a fact. Ask the user before you work on one.\n")
}

func rankStackCandidates(recs []stack.Record) []stack.Record {
	out := append([]stack.Record(nil), recs...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		ei, iok := out[i].LatestEntry()
		ej, jok := out[j].LatestEntry()
		if iok != jok {
			return iok
		}
		if iok && !ei.At.Equal(ej.At) {
			return ei.At.After(ej.At)
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func pluralStack(n int) string {
	if n == 1 {
		return "1 stack is"
	}
	return fmt.Sprintf("%d stacks are", n)
}

// writePrimeReadStep names the surfaces that say what a copy did. The trace tool
// is registered only where the workspace has observability, so a briefing that
// always names it sends half the workspaces to a tool that is not there.
func writePrimeReadStep(b *strings.Builder, name string, telemetry bool) {
	if telemetry {
		fmt.Fprintf(b, "3. Read what broke: the process_logs and investigate tools, or `devstack otel traces --stack %s`\n", name)
		return
	}
	b.WriteString("3. Read what broke: the process_logs tool, or `devstack status`\n")
}

func writePrimeStackNote(b *strings.Builder, rec *stack.Record) {
	if note := firstLine(rec.Note, 110); note != "" {
		fmt.Fprintf(b, "purpose %s\n", note)
	}
	if e, ok := rec.LatestEntry(); ok {
		fmt.Fprintf(b, "latest  %s  %s\n", e.At.Format("2006-01-02"), firstLine(e.Text, 100))
	}
}

func writePrimeOverlayDirs(b *strings.Builder, rec *stack.Record) {
	width := 0
	for _, svc := range rec.Overlay {
		if len(svc) > width {
			width = len(svc)
		}
	}
	for _, svc := range rec.Overlay {
		if path := rec.Worktrees[svc]; path != "" {
			fmt.Fprintf(b, "     %-*s  %s\n", width, svc, path)
		}
	}
}

func writePrimeSiblingCaution(b *strings.Builder, siblings map[string]string) {
	names := sortedServices(siblings)
	if len(names) == 0 {
		return
	}
	fmt.Fprintf(b, "   The worktrees also hold the code of: %s. This stack runs no copy of them.\n", strings.Join(names, ", "))
	b.WriteString("   base runs its own copy of each, from its own directory elsewhere. A change you make to\n")
	b.WriteString("   one here goes on the branch of this stack, and no process runs it.\n")
}

func taskService(rec *stack.Record, service string) string {
	if service != "" {
		return service
	}
	if len(rec.Overlay) > 0 {
		return rec.Overlay[0]
	}
	return "<service>"
}

// git cuts a worktree of a whole repository and never of a subdirectory, so a
// stack that overlays one service of a repository gets the code of every other
// service in that repository too. Nothing runs that code: base runs its own copy
// from its own directory.
func stackSiblings(rw *config.ResolvedWorkspace, rec *stack.Record) map[string]string {
	overlay := map[string]bool{}
	for _, s := range rec.Overlay {
		overlay[s] = true
	}

	out := map[string]string{}
	for _, s := range rec.Overlay {
		repoDir, top := splitStackWorktree(rec.Root, rec.Worktrees[s], rw.Services[s].RepoPath)
		if repoDir == "" {
			continue
		}
		for name, svc := range rw.Services {
			if overlay[name] || out[name] != "" {
				continue
			}
			rel, err := filepath.Rel(top, svc.RepoPath)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			path := filepath.Join(repoDir, rel)
			if fi, err := os.Stat(path); err == nil && fi.IsDir() {
				out[name] = path
			}
		}
	}
	return out
}

// devstack builds the worktree path as <stack root>/<repository directory>/<path
// of the service below its repository>. The repository directory is always one
// element, so the same suffix cuts the base path and gives its repository root.
func splitStackWorktree(stackRoot, worktreePath, basePath string) (repoDir, toplevel string) {
	if stackRoot == "" || worktreePath == "" || basePath == "" {
		return "", ""
	}
	rel, err := filepath.Rel(stackRoot, worktreePath)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", ""
	}
	parts := strings.Split(rel, string(filepath.Separator))
	repoDir = filepath.Join(stackRoot, parts[0])
	sub := filepath.Join(parts[1:]...)
	if sub == "" {
		return repoDir, basePath
	}
	suffix := string(filepath.Separator) + sub
	if !strings.HasSuffix(basePath, suffix) {
		return "", ""
	}
	return repoDir, strings.TrimSuffix(basePath, suffix)
}

func sortedServices(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func writePrimeStackDirectory(b *strings.Builder, rec *stack.Record, here, cwd string, siblings map[string]string) {
	svc := siblingAt(siblings, cwd)
	if svc == "" {
		fmt.Fprintf(b, "This directory is in the worktree of stack %s. devstack can not name a service in it.\n", here)
		fmt.Fprintf(b, "The stack runs its own copy of these services only: %s.\n", strings.Join(rec.Overlay, ", "))
		b.WriteString("A change you make here goes on the branch of this stack. Before you change code here,\n")
		b.WriteString("run `devstack status`. It names the copy that runs this code.\n")
		return
	}
	fmt.Fprintf(b, "This directory holds the code of %s, on the branch of stack %s.\n", svc, here)
	fmt.Fprintf(b, "This stack runs no copy of %s. It runs its own copy of: %s.\n", svc, strings.Join(rec.Overlay, ", "))
	fmt.Fprintf(b, "base runs its own copy of %s, from its own directory elsewhere.\n", svc)
	b.WriteString("A change you make here runs nowhere. No copy serves it, and no test executes it.\n")
	fmt.Fprintf(b, "To make this stack run %s, run: devstack stack add %s %s\n", svc, here, svc)
}

func siblingAt(siblings map[string]string, cwd string) string {
	if cwd == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	for _, name := range sortedServices(siblings) {
		dir := siblings[name]
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			dir = resolved
		}
		if cwd == dir || strings.HasPrefix(cwd, dir+string(filepath.Separator)) {
			return name
		}
	}
	return ""
}

func inTemplateCheckout(ws *workspace.Workspace, here string, inReplica bool) bool {
	return here == "base" && !inReplica && config.HasWorkspaceManifest(replica.Root(ws))
}

func writePrimeIdentity(b *strings.Builder, ws *workspace.Workspace, service, here string, inReplica bool) {
	fmt.Fprintf(b, "workspace %s", ws.Name)
	if service != "" {
		fmt.Fprintf(b, " · service %s", service)
	}
	switch {
	case here != "base":
		fmt.Fprintf(b, " · stack %s\n", here)
	case inReplica:
		b.WriteString(" · base replica (not a stack)\n")
		b.WriteString("  devstack generates this directory. `devstack workspace up` overwrites it, so do not edit here.\n")
	case inTemplateCheckout(ws, here, inReplica):
		b.WriteString(" · template checkout (not a stack, and not what base runs)\n")
		fmt.Fprintf(b, "  Nothing runs here. base runs a replica of this workspace at %s.\n", replica.Root(ws))
	default:
		b.WriteString(" · base (not a stack)\n")
		fmt.Fprintf(b, "  No replica is built yet, so base runs this checkout. `devstack workspace up` builds one\n  at %s, and after that nothing here runs.\n", replica.Root(ws))
	}
}

func writePrimeCloseOut(b *strings.Builder, branch string) {
	if branch == "" {
		branch = "<branch>"
	}
	b.WriteString("5. If this feature is finished, ask the user: merge this branch, or discard it?\n")
	b.WriteString("   Never merge it without an answer.\n")
	fmt.Fprintf(b, "   After a merge, delete the branch: git branch -d %s\n", branch)
}

func writePrimeSafety(b *strings.Builder) {
	section(b, "BEFORE YOU COMMIT")
	b.WriteString("  Never commit `devstack.service.yaml`, or the `devstack.<name>.yaml` of each service after the first one\n")
	b.WriteString("  in that directory. Each one holds absolute paths of this machine. Add them to `.gitignore`.\n")
	b.WriteString("  If it is committed already, keep every real secret out of it, and out of `devstack.workspace.yaml`.\n")
	b.WriteString("  For a value that must come from the environment, declare the key under `env.required`, and supply the\n")
	b.WriteString("  value from `.envrc`. devstack then reads the value at run time, and no manifest holds it.\n")
}

func writePrimeApplies(b *strings.Builder, ws *workspace.Workspace, rw *config.ResolvedWorkspace) {
	var lines []string
	if envs := sortedEnvNames(rw); len(envs) > 0 {
		lines = append(lines, fmt.Sprintf("  environments  active: %s. Each environment sets different configuration values.", orDash(rw.Manifest.Workspace.Env)))
		lines = append(lines, fmt.Sprintf("                %-10s %s", "NAME", "PURPOSE"))
		for _, n := range envs {
			marker := " "
			if n == rw.Manifest.Workspace.Env {
				marker = "▸"
			}
			desc := firstLine(rw.Manifest.Environments[n].Description, 84)
			if desc == "" {
				desc = "(no purpose recorded)"
			}
			lines = append(lines, fmt.Sprintf("              %s %-10s %s", marker, n, desc))
		}
		lines = append(lines, "                Every environment sets values. A blank PURPOSE means only that nobody wrote down why.")
		lines = append(lines, "                To see the values one sets, run: devstack env which --service <svc>")
	}

	if n := countHooks(ws, rw); n > 0 {
		lines = append(lines, fmt.Sprintf("  hooks         %d declared. They run automatically on the stack and service lifecycle. See: devstack hooks list", n))
	}

	if rw.Manifest.Observability.IsEnabled() {
		lines = append(lines,
			"  telemetry     every copy sends traces and logs. Query them with `devstack otel traces` and `devstack otel logs`,",
			"                or with the investigate tool over MCP. The attribute devstack.stack identifies each copy.",
			"                `devstack otel traces` with no --stack returns base alone. To get one stack, give `--stack <name>`.",
			"                To get base and every stack together, give `--stack all`. `devstack otel logs` has no --stack:",
			"                use `--trace <id>` to get the logs of one execution.")
	}

	if len(lines) == 0 {
		return
	}
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
}

func sortedEnvNames(rw *config.ResolvedWorkspace) []string {
	names := make([]string, 0, len(rw.Manifest.Environments))
	for n := range rw.Manifest.Environments {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func countHooks(ws *workspace.Workspace, rw *config.ResolvedWorkspace) int {
	n := len(rw.Manifest.Hooks)
	for _, svc := range rw.Services {
		if svc.Manifest != nil {
			n += len(svc.Manifest.Hooks)
		}
	}
	return n
}

func writePrimeWhatThisIs(b *strings.Builder) {
	section(b, "DEVSTACK")
	b.WriteString("devstack runs the local development services of this machine, and nothing else. Never point it at a\n")
	b.WriteString("staging or a production system.\n")
	b.WriteString("devstack is a CLI and an MCP server. The tools share the names of the commands. This briefing already\n")
	b.WriteString("says where you are and what runs. Call the `environment` tool for the list of tools this workspace\n")
	b.WriteString("has, or later, when this briefing is old. Among the commands with no tool: upgrade, init,\n")
	b.WriteString("panel, ports, dependencies, group add and remove, env list, show and remove, and every\n")
	b.WriteString("workspace command but topology. Run those in the shell.\n")
	b.WriteString("To tell the user where to see a copy, call the `urls` tool, or run `devstack urls`.\n")
	b.WriteString("It reports the address that reaches each service from another machine.\n")
}

func writePrimeTerms(b *strings.Builder) {
	section(b, "TERMS")
	b.WriteString("  stack  a group of services you cut for one feature. It has its own branch, its own directories and\n")
	b.WriteString("         its own ports. It is local, and it does not last: you stand it up, and you tear it down\n")
	b.WriteString("  copy   one running process of a service. base runs one copy, and each stack runs another copy. Each\n")
	b.WriteString("         copy has a different port. Run `devstack status` before you decide that a service is down\n")
	b.WriteString("  base   every service no stack replaces. base runs a replica: one worktree per service at the default\n")
	b.WriteString("         branch tip. Your own checkout is a template, and it runs nothing. An edit there reaches base\n")
	b.WriteString("         only on the default branch, after `devstack workspace up`\n")
	b.WriteString("A command that starts, stops or restarts a copy names it with `--stack <name>`, or `--stack base` for base.\n")
	b.WriteString("With no flag it acts on the stack or replica your directory is in, and the default is base anywhere else.\n")
	b.WriteString("Each command names the copy it changed. A base copy has no :stack suffix, so read that line to confirm it.\n")
	writePrimeStates(b)
}

func writePrimeStates(b *strings.Builder) {
	b.WriteString("\nSTATES. A stack is up or down. A copy has one of these states:\n")
	b.WriteString("  running    the process is up and healthy\n")
	b.WriteString("  starting   devstack started it and it is not ready yet\n")
	b.WriteString("  building   its build step runs now\n")
	b.WriteString("  erroring   it failed. Read its logs\n")
	b.WriteString("  stopped    it is registered but not started. This is not a fault\n")
	b.WriteString("  disabled   somebody stopped it on purpose\n")
	b.WriteString("  down       the copy is not registered in the daemon. Usually its stack is down: run `devstack stack up <name>`\n")
	b.WriteString("  unknown    the daemon does not answer. Run `devstack workspace up`\n")
}

func writePrimeLiveCount(b *strings.Builder, ws *workspace.Workspace) {
	view, err := tilt.NewClient("localhost", workspace.HostTiltPort).GetView()
	if err != nil {
		b.WriteString("  daemon        not started. To start it, run: `devstack workspace up`\n")
		return
	}
	base, stacked := 0, 0
	for _, r := range view.UiResources {
		name := r.Metadata.Name
		if !strings.HasPrefix(name, ws.Name+":") || serviceStatus(r) != "running" {
			continue
		}
		if strings.Count(name, ":") > 1 {
			stacked++
		} else {
			base++
		}
	}
	fmt.Fprintf(b, "  live          %s now, on daemon port %d: %d in base, %d in stacks\n",
		pluralCopyRunning(base+stacked), workspace.HostTiltPort, base, stacked)
	writePrimeBuild(b)
}

func writePrimeBuild(b *strings.Builder) {
	rev := buildRevision()
	if rev == "" {
		return
	}
	line := selfcheck.Refresh(modulePath(), rev).Describe(modulePath())
	if line == "" {
		return
	}
	fmt.Fprintf(b, "  build         %s\n", buildStamp())
	fmt.Fprintf(b, "                %s\n", line)
}

func writePrimeReload(b *strings.Builder, rw *config.ResolvedWorkspace, service, here string, inReplica, replicaBuilt bool, working *workingStack) {
	if service == "" {
		return
	}
	svc, ok := rw.Services[service]
	if !ok || svc.Manifest == nil {
		return
	}
	runCmd := strings.TrimSpace(svc.Manifest.Runtime.Run.Command)
	if runCmd == "" {
		return
	}

	target := service + " --stack base"
	switch {
	case here != "base":
		target = service + " --stack " + here
	case working != nil && working.Certain:
		target = service + " --stack " + working.Rec.Name
	}

	switch {
	case looksHotReloading(runCmd) || looksHotReloading(resolveRunScript(runCmd, svc.RepoPath)):
		fmt.Fprintf(b, "\n  reload        automatic (run command: `%s`). An edit in the directory a copy runs from applies\n", runCmd)
		b.WriteString("                to that copy without a restart.\n")
	case len(svc.Manifest.Runtime.Watch) > 0:
		b.WriteString("\n  reload        automatic, because runtime.watch is set. An edit in the directory a copy runs from\n")
		b.WriteString("                applies to that copy without a restart.\n")
	default:
		fmt.Fprintf(b, "\n  reload        MANUAL (run command: `%s`). After you change the code, it runs the old code.\n", runCmd)
		fmt.Fprintf(b, "                To load your changes, run: devstack service restart %s\n", target)
		fmt.Fprintf(b, "                If the table above shows that copy as stopped, disabled or down, restart does not\n")
		fmt.Fprintf(b, "                apply. Start it: devstack service start %s\n", target)
	}
	if here == "base" && !inReplica && replicaBuilt {
		b.WriteString("                Neither applies to an edit you make here: base runs the replica, so your change\n")
		b.WriteString("                reaches it only via the default branch and `devstack workspace up`.\n")
	}
	b.WriteString("                If you change the configuration or an environment variable, you must restart the service.\n")
}

func writePrimeInstances(b *strings.Builder, ws *workspace.Workspace, rw *config.ResolvedWorkspace, service, here string, working *workingStack, template bool) {
	view, err := tilt.NewClient("localhost", workspace.HostTiltPort).GetView()
	if err != nil {
		b.WriteString("\nThe daemon is not started. To start it, run: `devstack workspace up`\n")
		return
	}

	states := map[string]string{}
	total := 0
	for _, r := range view.UiResources {
		name := r.Metadata.Name
		if !strings.HasPrefix(name, ws.Name+":") {
			continue
		}
		st := serviceStatus(r)
		if st == "running" {
			total++
		}
		states[strings.TrimPrefix(name, ws.Name+":")] = st
	}
	if service == "" {
		return
	}

	type instance struct{ name, port, state, branch, dir, note string }

	baseState := states[service]
	if baseState == "" {
		baseState = "down"
	}
	rows := []instance{{name: "base", port: "-", state: baseState}}
	replicaBuilt := false
	if svc, ok := rw.Services[service]; ok {
		rows[0].dir, replicaBuilt = replicaDir(ws, service, svc.RepoPath)
		if svc.Manifest != nil {
			if p, ok := svc.Manifest.Ports["http"]; ok {
				rows[0].port = fmt.Sprintf(":%d", p)
			}
		}
	}

	recs, _ := stack.LoadStore(ws.Name)
	sort.Slice(recs, func(i, j int) bool { return recs[i].Name < recs[j].Name })
	for _, rec := range recs {
		if !containsString(rec.Overlay, service) {
			continue
		}
		row := instance{name: rec.Name, port: "-", state: "down", note: firstLine(rec.Note, 74)}
		if p, ok := rec.Ports[stack.QualifyPortKey(service, "http")]; ok {
			row.port = fmt.Sprintf(":%d", p)
		}
		if st, ok := states[service+":"+rec.Name]; ok {
			row.state = st
		}
		row.dir = rec.Worktrees[service]
		rows = append(rows, row)
	}

	// The branch is read from each checkout rather than taken from the stack
	// record: a service pulled into an overlay because it calls a changed one is
	// detached at HEAD, not on the branch of the stack, and telling an agent
	// otherwise sends it to commit somewhere that does not exist.
	dirs := map[string]string{}
	for _, r := range rows {
		if r.dir != "" {
			dirs[r.dir] = r.dir
		}
	}
	labels := gitinfo.ReadAll(dirs)
	for i := range rows {
		if rows[i].dir != "" {
			rows[i].branch = labels[rows[i].dir].Label()
		}
	}

	fmt.Fprintf(b, "runs as %s:\n", pluralCopy(len(rows)))
	suggested := ""
	if working != nil && working.Rec != nil && working.Rec.Name != here {
		suggested = working.Rec.Name
	}

	for _, r := range rows {
		marker := " "
		switch {
		case r.name == here && !template:
			marker = "▸"
		case r.name == suggested:
			marker = "?"
		}
		fmt.Fprintf(b, "  %s %-12s %-8s %-9s branch %s\n", marker, r.name, r.port, r.state, orDash(truncateCell(r.branch, 46)))
		if r.dir != "" {
			fmt.Fprintf(b, "      %s\n", r.dir)
		}
		if r.note != "" {
			fmt.Fprintf(b, "      %s\n", r.note)
		}
	}
	if template {
		b.WriteString("  No marker ▸ is on this table. You are in the template checkout, which is no copy.\n")
	} else {
		fmt.Fprintf(b, "  The marker ▸ shows the copy that you are in now: %s.\n", here)
	}
	if suggested != "" {
		fmt.Fprintf(b, "  The marker ? shows a guess. %s is the only stack that runs %s, but you are not in it.\n  Ask the user before you work on it.\n", suggested, service)
	}
	if replicaBuilt {
		b.WriteString("  The directory under each copy is the directory that copy RUNS. base runs the replica, not your checkout.\n")
	} else {
		b.WriteString("  The directory under each copy is the directory that copy RUNS. base has no replica built yet, so for now\n")
		b.WriteString("  it runs your checkout. `devstack workspace up` builds one, and after that your checkout runs nothing.\n")
	}
	b.WriteString("  To start, stop or restart one, name it: `--stack <name>`, or `--stack base`.\n")
	b.WriteString("  With no flag the command uses the copy whose directory you are in: a stack worktree, or the replica.\n")
	b.WriteString("  In a plain checkout it uses base. Each command names the copy it changed, so read that line.\n")
	b.WriteString("  To reach a copy over the network, use its port.\n")
}

func replicaDir(ws *workspace.Workspace, service, fallback string) (string, bool) {
	rw, err := replica.Resolve(ws)
	if err != nil {
		return fallback, false
	}
	if svc, ok := rw.Services[service]; ok && svc.RepoPath != "" {
		return svc.RepoPath, true
	}
	return fallback, false
}

func pluralCopyRunning(n int) string {
	if n == 1 {
		return "1 copy runs"
	}
	return fmt.Sprintf("%d copies run", n)
}

func pluralCopy(n int) string {
	if n == 1 {
		return "1 copy"
	}
	return fmt.Sprintf("%d copies", n)
}

func section(b *strings.Builder, title string) {
	fmt.Fprintf(b, "\n## %s\n", title)
}
