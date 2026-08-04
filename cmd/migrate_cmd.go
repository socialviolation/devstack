package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Remove the devstack instructions from every repository, and wire each one for the live briefing",
	Long: `Remove the instructions that an older devstack wrote into your repositories, and
leave each repository connected to devstack.

devstack wrote a block of instructions into AGENTS.md, and a shorter block into
CLAUDE.md and the files beside it. It writes neither now. 'devstack prime' prints
the same facts at each session start, so the agent gets them from the binary and
never from a committed file. A committed copy of a live fact goes stale, and a
stale fact reads exactly like a true one.

WHAT IT SWEEPS
  Every workspace on this machine. In each one: the workspace root, each service
  repository, and the worktree of each feature stack.

WHAT IT DOES IN EACH DIRECTORY
  1. It removes the devstack block from AGENTS.md, CLAUDE.md, GEMINI.md,
     .cursorrules and .github/copilot-instructions.md
  2. It deletes a file that held devstack content only
  3. It writes .mcp.json, which connects an AI agent to the devstack MCP server
  4. It writes the Claude Code SessionStart hook into .claude/settings.json, so
     each session runs 'devstack prime'

devstack removes only what devstack wrote. Where a file holds text of your own,
that text stays, byte for byte. Where devstack can not find the end of its own
block, it changes nothing and it names the file for you.

CAUTION: devstack does not own these repositories. Read the diff in each one
before you commit it.

Run this command again at any time. A second run changes nothing.`,
	SilenceUsage: true,
	RunE:         runMigrate,
}

func init() {
	rootCmd.AddCommand(migrateCmd)
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

func runMigrate(cmd *cobra.Command, args []string) error {
	all := registeredWorkspaces()
	if len(all) == 0 {
		fmt.Println("No workspace is registered on this machine, so devstack migrates nothing.")
		return nil
	}

	fmt.Println("devstack removes its instructions from each repository.")
	fmt.Println("An agent gets the same facts from `devstack prime`, at each session start.")

	var total migrateResult
	var errs []error
	for i := range all {
		targets, terrs := migrateTargets(&all[i])
		errs = append(errs, terrs...)
		total.add(migrateWorkspace(os.Stdout, all[i].Name, targets))
	}

	writeMigrateSummary(os.Stdout, total)
	return errors.Join(errs...)
}

// migrateWorkspace migrates each target of one workspace, and prints the name of
// the workspace only where something happened. A run that changes nothing must
// print nothing, or nobody reads the run that does.
func migrateWorkspace(w io.Writer, name string, targets []migrateTarget) migrateResult {
	var res migrateResult
	named := false
	for _, t := range targets {
		lines, r := migrateOne(t)
		res.add(r)
		if len(lines) == 0 {
			continue
		}
		if !named {
			fmt.Fprintf(w, "\n%s\n", name)
			named = true
		}
		fmt.Fprintf(w, "  %-24s %s\n", t.Label, t.Dir)
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
	}
	return res
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

// commitCommand is what the reader runs in each repository. It stages and
// commits, and it pushes nothing: devstack does not decide when work leaves this
// machine.
const commitCommand = `git add -A && git commit -m "chore: devstack migrate"`

// writeMigrateSummary is the last thing the command prints, so it is what an
// agent reads before it decides what to do next. It states what changed, it
// names each repository that changed, and it gives the command that finishes the
// job. A run that changed nothing prints none of it: an instruction to commit an
// empty diff teaches a reader to ignore the whole report.
func writeMigrateSummary(w io.Writer, res migrateResult) {
	if res.Changed() == 0 && res.NeedsHuman == 0 {
		fmt.Fprintln(w, "\ndevstack changed no file. Every repository is migrated already.")
		return
	}

	if res.NeedsHuman == 1 {
		fmt.Fprintln(w, "\n1 file needs a human. devstack changed nothing in it.")
		fmt.Fprintln(w, "Remove the devstack block by hand.")
	} else if res.NeedsHuman > 1 {
		fmt.Fprintf(w, "\n%s need a human. devstack changed nothing in them.\n", pluralFiles(res.NeedsHuman))
		fmt.Fprintln(w, "Remove the devstack block from each one by hand.")
	}
	if res.Changed() == 0 {
		fmt.Fprintln(w, "\ndevstack changed no file, so there is nothing to commit.")
		return
	}

	fmt.Fprintf(w, "\nDONE. devstack changed %s in %s:\n", pluralFiles(res.Changed()), pluralRepos(len(res.Repos)))
	if res.Removed > 0 {
		fmt.Fprintf(w, "  %s: devstack removed its block. Your own text stays\n", pluralFiles(res.Removed))
	}
	if res.Deleted > 0 {
		fmt.Fprintf(w, "  %s: devstack deleted them. They held devstack content only\n", pluralFiles(res.Deleted))
	}
	if res.MCP > 0 {
		fmt.Fprintf(w, "  %s: devstack wrote .mcp.json, which connects an agent to the MCP server\n", pluralRepos(res.MCP))
	}
	if res.Hooks > 0 {
		fmt.Fprintf(w, "  %s: devstack wired the SessionStart hook, so each session runs `devstack prime`\n", pluralRepos(res.Hooks))
	}

	fmt.Fprintln(w, "\nNOW COMMIT. These repositories hold uncommitted changes:")
	for _, t := range res.Repos {
		fmt.Fprintf(w, "  %-24s %s\n", t.Label, t.Dir)
	}
	fmt.Fprintln(w, "\nRead the diff in each repository. Then commit it there:")
	fmt.Fprintf(w, "  %s\n", commitCommand)
	fmt.Fprintln(w, "devstack does not push. Push it yourself, or leave it.")
	fmt.Fprintln(w, "\nWHY: until you commit the diff, the next clone of that repository still carries")
	fmt.Fprintln(w, "the old instructions, and an agent that reads them acts on text that is not true.")
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

// residueByWorkspace reports every registered workspace that still holds
// devstack content, so `upgrade` and `workspace doctor` agree.
func residueByWorkspace() (map[string][]residueFile, []workspace.Workspace) {
	all := registeredWorkspaces()
	found := map[string][]residueFile{}
	var order []workspace.Workspace
	for i := range all {
		files := workspaceResidue(&all[i])
		if len(files) == 0 {
			continue
		}
		found[all[i].Name] = files
		order = append(order, all[i])
	}
	return found, order
}
