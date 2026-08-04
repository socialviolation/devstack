package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/migrate"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
	"github.com/socialviolation/devstack/internal/worktree"
)

var migrateCmd = &cobra.Command{
	Use:    "migrate",
	Hidden: true,
	Short:  "Run every migration this devstack needs, and print what to do next",
	Long: `Run each migration that your configuration still needs.

A migration moves the workspace configuration from one version to the next. The
version is the number at the top of devstack.workspace.yaml. A migration runs
when that number is at the version it moves from, and devstack writes the new
number into the manifest after the migration succeeds.

That manifest is committed. So the version travels with the repository: a
teammate who clones a repository that somebody migrated already gets a manifest
that is current, and devstack asks them for nothing.

'devstack upgrade' runs these migrations for you. Run this command when you clone
a repository that still holds a block an older devstack committed, because
nothing else finds that block.

THE MIGRATIONS
  version 1 to 2  It removes the instructions that an older devstack wrote into
                  AGENTS.md, CLAUDE.md, GEMINI.md, .cursorrules and
                  .github/copilot-instructions.md. It deletes a file that holds
                  that block and nothing else. It writes .mcp.json, which
                  connects an agent to the devstack MCP server. It writes the
                  Claude Code SessionStart hook, so that each session runs
                  'devstack prime'. It acts in the workspace root, in each
                  service repository, and in the worktree of each feature stack.

WHAT A MIGRATION IS NOT
  A migration does not watch this machine, and it holds no machine state. To find
  a repository devstack is not connected to, a devstack file that nobody
  committed, or a workspace with no replica, run 'devstack workspace doctor'.

devstack removes only what devstack wrote. If a file holds text of your own, that
text stays, byte for byte. If devstack can not find the end of its own block, it
changes nothing, and it names the file for you.

BEFORE IT WRITES
  devstack checks each file that it removes the devstack block from, and each
  file that it deletes: AGENTS.md, CLAUDE.md, GEMINI.md, .cursorrules and
  .github/copilot-instructions.md. If one of them holds a change that nobody
  committed, devstack refuses, and it changes no file in any workspace. git holds
  no copy of that change, so the change can not come back. Commit or stash the
  file, or give --force.

CAUTION: devstack does not own these repositories. Read the diff in each one
before you commit it.

  devstack migrate          run every pending migration
  devstack migrate --list   print the version of each workspace, and what is pending
  devstack migrate --force  migrate over the refusal above, and lose each
                            uncommitted change in those files

Run this command again at any time. A second run changes nothing.`,
	SilenceUsage: true,
	RunE:         runMigrate,
}

func init() {
	rootCmd.AddCommand(migrateCmd)
	migrateCmd.Flags().Bool("list", false, "Print the version of each workspace, and what is pending. Change nothing")
	migrateCmd.Flags().Bool("force", false, "Migrate a file that holds a change nobody committed. devstack removes its block from that file, or deletes the file, and the change is lost")
	migrate.Stamp = buildStamp()
}

// patches is every migration devstack knows, in the order it runs them. To add
// one, write a migrate.Patch that moves the version one step, and put it in this
// list.
func patches() []migrate.Patch {
	return []migrate.Patch{agentFilesPatch()}
}

func runMigrate(cmd *cobra.Command, args []string) error {
	list, _ := cmd.Flags().GetBool("list")
	force, _ := cmd.Flags().GetBool("force")
	all, err := migrate.Workspaces()
	if err != nil {
		return err
	}
	return migrate.Sweep(os.Stdout, patches(), all, !list, force)
}

// agentFilesPatch removes the instructions that an older devstack wrote into
// each repository, and leaves each repository connected to devstack.
//
// devstack wrote a block of instructions into AGENTS.md, and a shorter block
// into CLAUDE.md and the files beside it. It writes neither now. 'devstack
// prime' prints the same facts at each session start, so the agent gets them
// from the binary and never from a committed file. A committed copy of a live
// fact goes stale, and a stale fact reads exactly like a true one.
//
// A repository that devstack has not connected, and a devstack file that nobody
// committed, are states that change each time somebody adds a service. 'devstack
// workspace doctor' reports those. This patch is the one-off removal of what an
// older devstack wrote, and the version in the manifest says when it is done.
func agentFilesPatch() migrate.Patch {
	return migrate.Patch{
		From:      1,
		To:        2,
		Title:     "Remove the devstack instructions from every repository, and connect each one to devstack",
		Run:       runAgentFiles,
		Next:      nextAgentFiles,
		Preflight: preflightAgentFiles,
	}
}

// preflightAgentFiles names each file this patch destroys work in.
//
// The patch removes the devstack block from the instruction files, and it
// deletes a file that holds the block and nothing else. Where such a file holds
// a change nobody committed, that change goes with the block, and git can not
// give it back.
//
// It names no other file. devstack writes .mcp.json and .claude/settings.json,
// and it only adds to them: .mcp.json is generated whole, and the hook is merged
// into settings that keep every other key. Both leave a diff a reader can read
// and revert, so neither one is a reason to stop.
//
// It reads the workspace root, each service repository and each stack worktree,
// because the patch writes in all three.
func preflightAgentFiles(all []workspace.Workspace) []migrate.Block {
	var out []migrate.Block
	for i := range all {
		targets, _ := migrateTargets(&all[i])
		for _, t := range targets {
			for _, rel := range uncommittedStripTargets(t.Dir) {
				out = append(out, migrate.Block{Label: t.Label, Dir: t.Dir, File: rel})
			}
		}
	}
	return out
}

// uncommittedStripTargets names the files of dir that this patch strips or
// deletes, and that git reports as changed and not committed.
//
// A file devstack leaves alone is not named. A file with a marker that has no
// pair is one of those. Nothing in it changes, so nothing in it is lost, and a
// refusal about it is a refusal nobody can act on.
func uncommittedStripTargets(dir string) []string {
	var out []string
	for _, f := range contentFiles(dir) {
		data, err := os.ReadFile(f.Path)
		if err != nil || !hasDevstackContent(string(data), f.Legacy) {
			continue
		}
		if !sentinelPairsAreSound(string(data)) {
			continue
		}
		if fileIsUncommitted(dir, f.Rel) {
			out = append(out, f.Rel)
		}
	}
	return out
}

// fileIsUncommitted reports whether git holds no copy of what rel holds now. It
// reads the index and the working tree, and it reaches no network.
//
// A directory that no git repository holds gets false. There is nothing to
// compare the file against, and neither remedy this check offers — commit it, or
// stash it — can run there. The migration report names such a directory on its
// own.
func fileIsUncommitted(dir, rel string) bool {
	cmd := exec.Command("git", "status", "--porcelain", "--", rel)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(bytes.TrimSpace(out)) > 0
}

// devstackOwnedFiles are the files this patch writes, strips or deletes. The
// report and the commit instruction read one list, so that both mean the same
// set of files.
func devstackOwnedFiles() []string {
	out := make([]string, 0, len(aiInstructionFiles)+3)
	out = append(out, agentsFileName)
	out = append(out, aiInstructionFiles...)
	return append(out, ".mcp.json", claudeSettingsRel)
}

// agentFilesPresent names the files devstack owns that dir holds now. A
// directory that can not be committed in is reported with the files that are
// stranded there, because "commit this" is no use without "commit what".
func agentFilesPresent(dir string) []string {
	var out []string
	for _, rel := range devstackOwnedFiles() {
		if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			out = append(out, rel)
		}
	}
	return out
}

func runAgentFiles(ws *workspace.Workspace) (migrate.Result, error) {
	targets, errs := migrateTargets(ws)

	var res migrateResult
	var lines []string
	for _, t := range targets {
		l, r := migrateOne(t)
		res.add(r)
		if len(l) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("    %-24s %s", t.Label, t.Dir))
		lines = append(lines, l...)
	}
	lines = append(lines, agentFilesCounts(res)...)

	out := migrate.Result{Changed: res.Changed() > 0, Lines: lines, Items: commitItems(res.Repos), Incomplete: res.NeedsHuman > 0}
	return out, errors.Join(errs...)
}

// commitItems names each directory this run wrote in. Every one of them holds a
// git diff that a human has to read and commit. A directory this run did not
// change is not this migration's business: 'devstack workspace doctor' reports a
// devstack file that stays uncommitted.
func commitItems(changed []migrateTarget) []migrate.Item {
	out := make([]migrate.Item, 0, len(changed))
	for _, t := range changed {
		out = append(out, migrate.Item{Label: t.Label, Path: t.Dir})
	}
	return out
}

// agentFilesCounts states what the sweep did in one workspace. A file devstack
// can not migrate is named first: it is the only part a reader must act on
// before anything else.
func agentFilesCounts(res migrateResult) []string {
	var out []string
	if res.NeedsHuman == 1 {
		out = append(out, "    1 file needs a human. devstack changed nothing in it. Remove the devstack block by hand")
	} else if res.NeedsHuman > 1 {
		out = append(out, fmt.Sprintf("    %s need a human. devstack changed nothing in them. Remove the devstack block by hand", pluralFiles(res.NeedsHuman)))
	}
	if res.Removed > 0 {
		out = append(out, fmt.Sprintf("    %s: devstack removed its block. Your own text stays", pluralFiles(res.Removed)))
	}
	if res.Deleted == 1 {
		out = append(out, "    1 file: devstack deleted it. It held the devstack block and nothing else")
	} else if res.Deleted > 1 {
		out = append(out, fmt.Sprintf("    %s: devstack deleted them. Each one held the devstack block and nothing else", pluralFiles(res.Deleted)))
	}
	if res.MCP > 0 {
		out = append(out, fmt.Sprintf("    %s: devstack wrote .mcp.json, which connects an agent to the MCP server", pluralRepos(res.MCP)))
	}
	if res.Hooks > 0 {
		out = append(out, fmt.Sprintf("    %s: devstack wrote the SessionStart hook, so that each session runs `devstack prime`", pluralRepos(res.Hooks)))
	}
	return out
}

// commitCommand is what the reader runs in each repository. It stages and
// commits, and it pushes nothing: devstack does not decide when work leaves this
// machine.
const commitCommand = `git add -A && git commit -m "chore: devstack migrate"`

// nextAgentFiles is the instruction the state of the disk earns. Each devstack
// file is a real git diff in a repository devstack does not own, so the work is
// not finished until a human reads that diff and commits it.
//
// A directory that is not the root of a git repository gets its own list. The
// commit command stages a whole repository: below another repository's root it
// stages that repository, and outside every repository it fails.
func nextAgentFiles(results []migrate.Result) []string {
	var commit, loose []string
	for _, r := range results {
		roots, elsewhere := splitByRepoRoot(r.Items)
		if len(roots) > 0 {
			commit = append(append(commit, "  "+r.Workspace), roots...)
		}
		if len(elsewhere) > 0 {
			loose = append(append(loose, "  "+r.Workspace), elsewhere...)
		}
	}

	var out []string
	if len(commit) > 0 {
		out = append(out, "NOW COMMIT. These repositories hold an uncommitted devstack file:")
		out = append(out, commit...)
		out = append(out,
			"Read the diff in each repository. Then commit it there:",
			"  "+commitCommand,
			"devstack does not push. Push it yourself, or leave it.",
			"This session does not have the tools that .mcp.json connects. An MCP client reads its",
			"server list at session start only. To get those tools, restart the session.",
			"WHY: if you do not commit the diff, the next clone of that repository does not get it.",
			"That clone still carries any old instructions that devstack wrote there. An agent that",
			"reads them acts on text that is not true. That clone also connects no agent to the MCP",
			"server, and no session to `devstack prime`. The workspace manifest carries the version",
			"of your configuration, so a clone without it reports this migration as pending again.")
	}
	if len(loose) > 0 {
		out = append(out, "COMMIT THESE ELSEWHERE. None of these directories is the root of a git repository.")
		out = append(out, "Do not run `git add -A` in them:")
		out = append(out, loose...)
	}
	return out
}

// splitByRepoRoot sorts the directories into the ones a commit can happen in
// and the ones it can not. A directory below another repository's root joins
// the group of that root, so one repository gets one instruction and not one
// for each of its services.
func splitByRepoRoot(items []migrate.Item) (roots, elsewhere []string) {
	byTop := map[string][]migrate.Item{}
	var tops []string
	for _, it := range items {
		isRoot, top := worktree.IsRoot(it.Path)
		switch {
		case isRoot:
			roots = append(roots, fmt.Sprintf("    %-24s %s", it.Label, it.Path))
		case top == "":
			elsewhere = append(elsewhere, orphanLines(it)...)
		default:
			if _, seen := byTop[top]; !seen {
				tops = append(tops, top)
			}
			byTop[top] = append(byTop[top], it)
		}
	}
	for _, top := range tops {
		elsewhere = append(elsewhere, looseLines(top, byTop[top])...)
	}
	return roots, elsewhere
}

// looseLines say what the directories below one repository root hold, and where
// their files have to go instead.
func looseLines(top string, items []migrate.Item) []string {
	labels := make([]string, 0, len(items))
	var paths []string
	for _, it := range items {
		labels = append(labels, it.Label)
		for _, f := range agentFilesPresent(it.Path) {
			paths = append(paths, repoRelative(top, filepath.Join(it.Path, f)))
		}
	}
	return []string{
		fmt.Sprintf("    %-24s in the repository %s", strings.Join(labels, ", "), top),
		fmt.Sprintf("      It holds: %s. If you run `git add -A` here, git stages that whole repository.", listOrNone(paths)),
		"      To commit these files only, run:",
		fmt.Sprintf("        git -C %s add %s && git -C %s commit -m \"chore: devstack migrate\"", top, strings.Join(paths, " "), top),
	}
}

// orphanLines are for a directory that no git repository holds. Nothing commits
// its files, and the reader has to hear that rather than run a command that
// exits with an error.
func orphanLines(it migrate.Item) []string {
	return []string{
		fmt.Sprintf("    %-24s %s", it.Label, it.Path),
		fmt.Sprintf("      No git repository holds this directory. This directory holds: %s.", listOrNone(agentFilesPresent(it.Path))),
		"      No commit reaches these files. They stay on this machine, and no clone gets them.",
	}
}

func listOrNone(files []string) string {
	if len(files) == 0 {
		return "no devstack file"
	}
	return strings.Join(files, ", ")
}

// repoRelative is the path of a file below a repository root, which is what git
// takes after -C. A file that resolves outside the root keeps its full path.
func repoRelative(top, abs string) string {
	rel, err := filepath.Rel(top, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return abs
	}
	return rel
}

// migrateTarget is one directory devstack migrates: the root of a workspace, a
// service repository, or the worktree of a feature stack.
type migrateTarget struct {
	Label string
	Dir   string
	// Service is the service this directory holds. It is empty for a workspace
	// root, which gets no .mcp.json.
	Service string
}

// migrateTargets lists every directory of one workspace that can hold devstack
// content. A stack worktree lives outside the service paths of the workspace, so
// nothing else reaches it.
//
// It returns the targets it found and the failures it met. One broken stack must
// not hide the repositories that are readable.
func migrateTargets(ws *workspace.Workspace) ([]migrateTarget, []error) {
	out := []migrateTarget{{Label: "workspace root", Dir: ws.Path}}
	var errs []error

	if cfg, err := config.Load(ws.Path); err != nil {
		errs = append(errs, fmt.Errorf("%s: can not load the workspace configuration: %w", ws.Name, err))
	} else {
		names := make([]string, 0, len(cfg.ServicePaths))
		for name := range cfg.ServicePaths {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			out = append(out, migrateTarget{Label: name, Dir: cfg.ServicePaths[name], Service: name})
		}
	}

	recs, err := stack.LoadStore(ws.Name)
	if err != nil {
		errs = append(errs, fmt.Errorf("%s: can not load the stacks: %w", ws.Name, err))
		return out, errs
	}
	for i := range recs {
		rw, err := stack.ResolveWorktree(&recs[i])
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: stack %s: %w", ws.Name, recs[i].Name, err))
			continue
		}
		names := make([]string, 0, len(rw.Services))
		for name := range rw.Services {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			out = append(out, migrateTarget{
				Label:   fmt.Sprintf("%s (stack %s)", name, recs[i].Name),
				Dir:     rw.Services[name].RepoPath,
				Service: name,
			})
		}
	}
	return out, errs
}

// migrateResult counts what one sweep did, and names the repositories it
// changed. The next action happens in each repository on its own, so a count
// with no paths beside it tells a reader nothing they can act on.
type migrateResult struct {
	Removed    int
	Deleted    int
	MCP        int
	Hooks      int
	NeedsHuman int
	Repos      []migrateTarget
}

func (r migrateResult) Changed() int { return r.Removed + r.Deleted + r.MCP + r.Hooks }

func (r *migrateResult) add(other migrateResult) {
	r.Removed += other.Removed
	r.Deleted += other.Deleted
	r.MCP += other.MCP
	r.Hooks += other.Hooks
	r.NeedsHuman += other.NeedsHuman
	r.Repos = append(r.Repos, other.Repos...)
}

// migrateOne migrates one directory and returns the report lines it earned.
func migrateOne(t migrateTarget) ([]string, migrateResult) {
	var lines []string
	var res migrateResult

	for _, c := range stripDir(t.Dir) {
		lines = append(lines, describeChange(c))
		switch c.Action {
		case actionRemoved:
			res.Removed++
		case actionDeleted:
			res.Deleted++
		default:
			res.NeedsHuman++
		}
	}

	if t.Service != "" {
		switch changed, err := ensureMCPJson(t.Dir, t.Service); {
		case err != nil:
			lines = append(lines, describeChange(fileChange{Rel: ".mcp.json", Action: actionLeftAlone, Reason: err.Error()}))
			res.NeedsHuman++
		case changed:
			lines = append(lines, fmt.Sprintf("      %-24s %s", ".mcp.json", "written"))
			res.MCP++
		}
	}

	switch changed, err := ensureClaudeSessionHook(t.Dir); {
	case err != nil:
		lines = append(lines, describeChange(fileChange{Rel: claudeSettingsRel, Action: actionLeftAlone, Reason: err.Error()}))
		res.NeedsHuman++
	case changed:
		lines = append(lines, fmt.Sprintf("      %-24s %s", claudeSettingsRel, "the SessionStart hook now runs `devstack prime`"))
		res.Hooks++
	}

	if res.Changed() > 0 {
		res.Repos = []migrateTarget{t}
	}
	return lines, res
}

func pluralFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

func pluralRepos(n int) string {
	if n == 1 {
		return "1 repository"
	}
	return fmt.Sprintf("%d repositories", n)
}

// workspaceResidue reports the files of one workspace that still hold devstack
// content. It reads only, so `upgrade` and `workspace doctor` can say what
// `devstack migrate` will clean before anybody runs it.
func workspaceResidue(ws *workspace.Workspace) []residueFile {
	targets, _ := migrateTargets(ws)
	var out []residueFile
	for _, t := range targets {
		out = append(out, scanResidue(t.Dir)...)
	}
	return out
}
