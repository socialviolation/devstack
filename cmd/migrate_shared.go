package cmd

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

var migrateSharedStacksCmd = &cobra.Command{
	Use:   "shared-stacks",
	Short: "Convert a workspace to the shared set and small stacks. Without --apply, change nothing",
	Long: `Convert one workspace to the shared set and small stacks.

Without --apply, devstack prints what it changes, and it changes nothing.

THE STEPS
  shared    If devstack.workspace.yaml has no shared: list, devstack proposes
            one. It proposes each service that starts a container, and each
            store. devstack writes the list only from --shared. You confirm the
            list.
  edges     devstack moves the workspace maps dependencies, startsAfter and
            calls onto each service, as calls in its devstack.service.yaml.
            Then it removes the maps from devstack.workspace.yaml. --needs
            makes an edge a needs edge.
  groups    devstack removes the shared services from each group.
  stacks    devstack removes the shared services from the members of each
            stack. It writes each record in the current schema, and it writes
            each stack manifest again. It removes no worktree, record, branch
            or port.
  refs      devstack names each ${svc.*} reference to a stack service. It
            changes nothing for these.

BEFORE IT WRITES
  It copies stacks.json and each file it edits to
  <data dir>/migrate-backup-<time>/, and it prints that path.

Run this command again at any time. A second run changes nothing.

  devstack migrate shared-stacks
  devstack migrate shared-stacks --apply --shared navexa-sql,azurite
  devstack migrate shared-stacks --apply --needs navexa-api=navexa-sql`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runMigrateSharedStacks,
}

func init() {
	migrateCmd.AddCommand(migrateSharedStacksCmd)
	migrateSharedStacksCmd.Flags().Bool("apply", false, "Write the changes. Without it, devstack changes nothing")
	migrateSharedStacksCmd.Flags().String("shared", "", "The shared services, comma separated. devstack writes this list to shared: with --apply")
	migrateSharedStacksCmd.Flags().StringSlice("needs", nil, "Make an edge a needs edge, as svc=dep. Give it more than once, or comma separated")
}

type sharedStacksOptions struct {
	Apply  bool
	Shared []string
	Needs  map[string][]string
}

func runMigrateSharedStacks(cmd *cobra.Command, args []string) error {
	ws, err := resolveWorkspace(viper.GetString("workspace"))
	if err != nil {
		return err
	}
	opts := sharedStacksOptions{}
	opts.Apply, _ = cmd.Flags().GetBool("apply")
	if cmd.Flags().Changed("shared") {
		raw, _ := cmd.Flags().GetString("shared")
		opts.Shared = splitNames(raw)
	}
	pairs, _ := cmd.Flags().GetStringSlice("needs")
	if opts.Needs, err = parseNeeds(pairs); err != nil {
		return err
	}
	return migrateSharedStacks(os.Stdout, ws, opts)
}

func splitNames(raw string) []string {
	out := []string{}
	for _, n := range strings.Split(raw, ",") {
		if n = strings.TrimSpace(n); n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func parseNeeds(pairs []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, p := range pairs {
		svc, dep, ok := strings.Cut(p, "=")
		svc, dep = strings.TrimSpace(svc), strings.TrimSpace(dep)
		if !ok || svc == "" || dep == "" {
			return nil, fmt.Errorf("--needs takes svc=dep, not %q", p)
		}
		if !slices.Contains(out[svc], dep) {
			out[svc] = append(out[svc], dep)
		}
	}
	return out, nil
}

var containerCommands = []string{"docker", "docker-compose", "podman", "podman-compose"}

var storeNameParts = []string{"sql", "psql", "postgres", "redis", "azurite", "clickhouse", "mongo"}

func proposeShared(rw *config.ResolvedWorkspace) map[string]string {
	out := map[string]string{}
	for name, svc := range rw.Services {
		if svc.Manifest != nil && startsContainer(svc.Manifest.Runtime.Run.Command) {
			out[name] = "its run command starts a container"
			continue
		}
		lower := strings.ToLower(name)
		for _, part := range storeNameParts {
			if strings.Contains(lower, part) {
				out[name] = "its name names a store"
				break
			}
		}
	}
	return out
}

func startsContainer(command string) bool {
	f := func(r rune) bool { return r == '&' || r == ';' || r == '|' || r == '\n' }
	for _, seg := range strings.FieldsFunc(command, f) {
		words := strings.Fields(seg)
		for len(words) > 0 && (words[0] == "exec" || strings.Contains(words[0], "=")) {
			words = words[1:]
		}
		if len(words) > 0 && slices.Contains(containerCommands, filepath.Base(words[0])) {
			return true
		}
	}
	return false
}

type edgeMove struct {
	Service string
	Path    string
	Calls   []string
	Needs   []string
}

func edgeNames(edges []config.ServiceEdge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.Service)
	}
	return out
}

func planEdgeMoves(rw *config.ResolvedWorkspace, needs map[string][]string, shared map[string]bool) ([]edgeMove, []string, error) {
	m := rw.Manifest
	legacy := map[string][]string{}
	for _, src := range []map[string][]string{m.Dependencies, m.StartsAfter, m.Calls} {
		for svc, deps := range src {
			for _, d := range deps {
				if !slices.Contains(legacy[svc], d) {
					legacy[svc] = append(legacy[svc], d)
				}
			}
		}
	}
	for svc, deps := range needs {
		if _, ok := rw.Services[svc]; !ok {
			return nil, nil, fmt.Errorf("--needs names the service %q. The workspace does not declare %q", svc, svc)
		}
		for _, d := range deps {
			if _, ok := rw.Services[d]; !ok {
				return nil, nil, fmt.Errorf("--needs names the service %q. The workspace does not declare %q", d, d)
			}
			if shared[svc] && !shared[d] {
				return nil, nil, fmt.Errorf("--needs %s=%s: the shared service %q can only need a shared service, and %q is not shared", svc, d, svc, d)
			}
		}
	}

	names := map[string]bool{}
	for svc := range legacy {
		names[svc] = true
	}
	for svc := range needs {
		names[svc] = true
	}
	var moves []edgeMove
	var stuck []string
	for _, svc := range slices.Sorted(maps.Keys(names)) {
		s, ok := rw.Services[svc]
		if !ok || s.ManifestPath == "" || s.Manifest == nil {
			stuck = append(stuck, fmt.Sprintf("%s: it has no service manifest", svc))
			continue
		}
		haveNeeds, haveCalls := edgeNames(s.Manifest.Needs), edgeNames(s.Manifest.Calls)
		mv := edgeMove{Service: svc, Path: s.ManifestPath}
		for _, d := range needs[svc] {
			if !slices.Contains(haveNeeds, d) {
				mv.Needs = append(mv.Needs, d)
			}
		}
		for _, d := range legacy[svc] {
			if _, ok := rw.Services[d]; !ok {
				stuck = append(stuck, fmt.Sprintf("%s -> %s: the workspace does not declare %s", svc, d, d))
				continue
			}
			if !slices.Contains(haveNeeds, d) && !slices.Contains(haveCalls, d) && !slices.Contains(needs[svc], d) {
				mv.Calls = append(mv.Calls, d)
			}
		}
		sort.Strings(mv.Calls)
		sort.Strings(mv.Needs)
		if len(mv.Calls)+len(mv.Needs) > 0 {
			moves = append(moves, mv)
		}
	}
	return moves, stuck, nil
}

type groupChange struct {
	Name   string
	Before []string
	After  []string
}

func planGroupChanges(groups map[string][]string, shared map[string]bool) []groupChange {
	var out []groupChange
	for _, g := range slices.Sorted(maps.Keys(groups)) {
		var kept []string
		for _, s := range groups[g] {
			if !shared[s] {
				kept = append(kept, s)
			}
		}
		if len(kept) != len(groups[g]) {
			out = append(out, groupChange{Name: g, Before: groups[g], After: kept})
		}
	}
	return out
}

type refHint struct {
	Where  string
	Target string
}

func scanStackRefs(rw *config.ResolvedWorkspace, shared map[string]bool, needs map[string][]string) []refHint {
	var out []refHint
	seen := map[refHint]bool{}
	add := func(where, self, text string, haveNeeds []string) {
		for _, t := range config.RefServices(text) {
			if _, ok := rw.Services[t]; !ok || t == self || shared[t] || slices.Contains(haveNeeds, t) {
				continue
			}
			h := refHint{Where: where, Target: t}
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(rw.Services)) {
		s := rw.Services[name]
		if s.Manifest == nil {
			continue
		}
		mf := s.Manifest
		haveNeeds := append(edgeNames(mf.Needs), needs[name]...)
		add(name+" runtime.run.command", name, mf.Runtime.Run.Command, haveNeeds)
		add(name+" runtime.prep.command", name, mf.Runtime.Prep.Command, haveNeeds)
		add(name+" runtime.healthcheck", name, mf.Runtime.Healthcheck.Command+" "+mf.Runtime.Healthcheck.URL, haveNeeds)
		for _, k := range slices.Sorted(maps.Keys(mf.Env.Values)) {
			add(name+" env.values."+k, name, mf.Env.Values[k], haveNeeds)
		}
		for _, l := range mf.Links {
			add(name+" links", name, l.URL, haveNeeds)
		}
		for _, h := range mf.Hooks {
			add(name+" hook "+h.Name, name, hookText(h), haveNeeds)
		}
	}
	m := rw.Manifest
	for _, k := range slices.Sorted(maps.Keys(m.Env.Values)) {
		add("workspace env.values."+k, "", m.Env.Values[k], nil)
	}
	for _, e := range slices.Sorted(maps.Keys(m.Environments)) {
		vals := m.Environments[e].Values
		for _, k := range slices.Sorted(maps.Keys(vals)) {
			add("workspace environments."+e+".values."+k, "", vals[k], nil)
		}
	}
	for _, h := range m.Hooks {
		add("workspace hook "+h.Name, "", hookText(h), nil)
	}
	return out
}

func hookText(h config.Hook) string {
	parts := []string{h.Run, h.WorkDir}
	for _, k := range slices.Sorted(maps.Keys(h.Env)) {
		parts = append(parts, h.Env[k])
	}
	return strings.Join(parts, " ")
}

func listOrDash(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

func migrateSharedStacks(w io.Writer, ws *workspace.Workspace, opts sharedStacksOptions) error {
	rw, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return err
	}
	if rw.Source != config.WorkspaceManifestFileName {
		return fmt.Errorf("workspace %q has no %s. This migration needs one", ws.Name, config.WorkspaceManifestFileName)
	}
	m := rw.Manifest
	mode := "DRY RUN. devstack changes nothing. To write the changes, add --apply."
	if opts.Apply {
		mode = "APPLY. devstack writes the changes below."
	}
	fmt.Fprintf(w, "Workspace %s (%s)\n%s\n\n", ws.Name, ws.Path, mode)

	fmt.Fprintln(w, "SHARED")
	var shared []string
	writeShared := false
	current := append([]string(nil), m.Shared...)
	sort.Strings(current)
	switch {
	case opts.Shared != nil:
		for _, n := range opts.Shared {
			if _, ok := rw.Services[n]; !ok {
				return fmt.Errorf("--shared names the service %q. The workspace does not declare %q", n, n)
			}
		}
		shared = opts.Shared
		writeShared = !slices.Equal(current, shared)
		if writeShared {
			fmt.Fprintf(w, "  shared: %s -> %s\n", listOrDash(current), listOrDash(shared))
		} else {
			fmt.Fprintf(w, "  shared: already lists %s\n", listOrDash(shared))
		}
	case len(current) > 0:
		shared = current
		fmt.Fprintf(w, "  shared: already lists %s\n", listOrDash(shared))
	default:
		proposal := proposeShared(rw)
		names := slices.Sorted(maps.Keys(proposal))
		fmt.Fprintln(w, "  devstack proposes these shared services:")
		for _, n := range names {
			fmt.Fprintf(w, "    %-28s %s\n", n, proposal[n])
		}
		if len(names) == 0 {
			fmt.Fprintln(w, "    (none)")
		}
		fmt.Fprintf(w, "  To confirm the list, run: devstack migrate shared-stacks --apply --shared %s\n", strings.Join(names, ","))
		if opts.Apply {
			fmt.Fprintln(w, "  devstack writes no shared list without --shared. The steps below use no shared service.")
		} else {
			shared = names
			fmt.Fprintln(w, "  The steps below use this proposal.")
		}
	}
	sharedSet := map[string]bool{}
	for _, n := range shared {
		sharedSet[n] = true
	}

	fmt.Fprintln(w, "\nEDGES")
	moves, stuck, err := planEdgeMoves(rw, opts.Needs, sharedSet)
	if err != nil {
		return err
	}
	for _, mv := range moves {
		var parts []string
		if len(mv.Needs) > 0 {
			parts = append(parts, "needs "+strings.Join(mv.Needs, ", "))
		}
		if len(mv.Calls) > 0 {
			parts = append(parts, "calls "+strings.Join(mv.Calls, ", "))
		}
		fmt.Fprintf(w, "  %-28s %s  (%s)\n", mv.Service, strings.Join(parts, "; "), mv.Path)
	}
	legacyPresent := len(m.Dependencies)+len(m.StartsAfter)+len(m.Calls) > 0
	removeLegacy := legacyPresent && len(stuck) == 0
	for _, s := range stuck {
		fmt.Fprintf(w, "  devstack can not move this edge. %s\n", s)
	}
	if len(stuck) > 0 && legacyPresent {
		fmt.Fprintf(w, "  devstack keeps the old workspace maps, because it can not move every edge.\n")
	}
	if removeLegacy {
		fmt.Fprintf(w, "  devstack removes dependencies, startsAfter and calls from %s.\n", config.WorkspaceManifestFileName)
	}
	if len(moves) == 0 && !legacyPresent {
		fmt.Fprintln(w, "  Nothing to move.")
	}

	fmt.Fprintln(w, "\nGROUPS")
	groups := planGroupChanges(m.Groups, sharedSet)
	for _, g := range groups {
		if len(g.After) == 0 {
			fmt.Fprintf(w, "  %-28s %s -> devstack removes the group. Every member is shared\n", g.Name, listOrDash(g.Before))
			continue
		}
		fmt.Fprintf(w, "  %-28s %s -> %s\n", g.Name, listOrDash(g.Before), listOrDash(g.After))
	}
	if len(groups) == 0 {
		fmt.Fprintln(w, "  No group holds a shared service.")
	}

	fmt.Fprintln(w, "\nREFERENCES TO STACK SERVICES (report only)")
	hints := scanStackRefs(rw, sharedSet, opts.Needs)
	for _, h := range hints {
		fmt.Fprintf(w, "  %-52s -> %s. If it can not work without %s, consider needs\n", h.Where, h.Target, h.Target)
	}
	if len(hints) == 0 {
		fmt.Fprintln(w, "  None.")
	}

	plan, err := stack.PlanMigration(ws.Name, sharedSet)
	if err != nil {
		return err
	}

	if !opts.Apply {
		printStackSummary(w, plan)
		fmt.Fprintln(w, "\nThis was a dry run. devstack changed nothing.")
		return nil
	}

	workspaceChanges := writeShared || len(moves) > 0 || removeLegacy || len(groups) > 0
	if !workspaceChanges {
		pending, storeChanges, err := stack.ApplyMigration(ws.Name, rw, false)
		if err != nil {
			return err
		}
		manifestChanges := false
		for _, p := range pending {
			manifestChanges = manifestChanges || p.Wrote
		}
		if !storeChanges && !manifestChanges {
			printStackSummary(w, plan)
			fmt.Fprintln(w, "\nNothing to change. devstack wrote no file.")
			return nil
		}
	}

	var files []string
	if writeShared || removeLegacy || len(groups) > 0 {
		files = append(files, config.WorkspaceManifestPath(ws.Path))
	}
	for _, mv := range moves {
		files = append(files, mv.Path)
	}
	edited := slices.Clone(files)
	files = append(files, stack.StoreFile(ws.Name))
	for _, p := range plan {
		files = append(files, p.ManifestPath)
	}
	backup, err := backupFiles(workspace.DataDir(ws.Name), files)
	if err != nil {
		return fmt.Errorf("devstack can not make the backup, so it changed nothing: %w", err)
	}
	fmt.Fprintf(w, "\nBACKUP\n  devstack copied each file it edits to %s\n\nWRITES\n", backup)

	if writeShared {
		if err := config.SetShared(ws.Path, shared); err != nil {
			return err
		}
	}
	for _, mv := range moves {
		if err := config.AddServiceEdges(mv.Path, mv.Calls, mv.Needs); err != nil {
			return fmt.Errorf("%s: %w", mv.Path, err)
		}
	}
	if removeLegacy {
		if err := config.RemoveLegacyEdgeMaps(ws.Path); err != nil {
			return err
		}
	}
	for _, g := range groups {
		if err := config.SetGroupMembers(ws.Path, g.Name, g.After); err != nil {
			return err
		}
	}
	for _, f := range edited {
		fmt.Fprintf(w, "  devstack wrote %s\n", f)
	}

	after, err := config.ResolveWorkspace(ws.Path)
	if err != nil {
		return fmt.Errorf("the workspace does not load after the edits, so devstack changed no stack. The backup is in %s: %w", backup, err)
	}
	done, storeWrote, err := stack.ApplyMigration(ws.Name, after, true)
	if err != nil {
		return err
	}
	if storeWrote {
		fmt.Fprintf(w, "  devstack wrote %s\n", stack.StoreFile(ws.Name))
	}
	for _, d := range done {
		switch {
		case d.Err != nil:
			fmt.Fprintf(w, "  stack %s: %v\n", d.Name, d.Err)
		case d.Wrote:
			fmt.Fprintf(w, "  stack %s: devstack wrote %s\n", d.Name, d.ManifestPath)
		}
	}
	printStackSummary(w, done)
	return nil
}

func printStackSummary(w io.Writer, stacks []stack.MigratedStack) {
	fmt.Fprintln(w, "\nSTACKS (members before -> after)")
	if len(stacks) == 0 {
		fmt.Fprintln(w, "  No stacks.")
		return
	}
	for _, s := range stacks {
		if slices.Equal(s.Before, s.After) {
			fmt.Fprintf(w, "  %-28s %s (no change)\n", s.Name, listOrDash(s.Before))
			continue
		}
		fmt.Fprintf(w, "  %-28s %s -> %s\n", s.Name, listOrDash(s.Before), listOrDash(s.After))
	}
}

func backupFiles(dataDir string, files []string) (string, error) {
	dir := filepath.Join(dataDir, "migrate-backup-"+time.Now().Format("20060102-150405"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		dst := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return "", err
		}
	}
	return dir, nil
}
