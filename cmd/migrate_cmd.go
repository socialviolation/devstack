package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/migrate"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Run every migration this devstack needs, and print what to do next",
	Long: `Run each migration that this machine still needs.

A migration is a patch: one versioned unit of work, with an id, a detector and
its own next action. devstack runs every pending patch, over every workspace, in
declared order. A patch that applies to nothing reports that and stops. A patch
that fails does not stop the patches after it.

THE PATCHES
  0.2.0-agent-files  Remove the instructions that an older devstack wrote into
                     AGENTS.md, CLAUDE.md, GEMINI.md, .cursorrules and
                     .github/copilot-instructions.md. Delete a file that held
                     nothing but that block. Write .mcp.json, which connects
                     an agent to the devstack MCP server. Write the Claude Code
                     SessionStart hook, so each session runs 'devstack prime'.
                     It sweeps the workspace root, each service repository, and
                     the worktree of each feature stack.
  0.2.0-replica      Build the replica that base runs from: one git worktree for
                     each repository of the workspace.

WHAT DEVSTACK RECORDS
  This binary records each patch it applied, and the workspace it applied it in,
  under ~/.local/share/devstack/migrations.json. A patch whose work is cheap to
  repeat reads the filesystem instead, and runs again whenever the filesystem
  needs it. The record never hides a repository that still holds a devstack
  block.

devstack removes only what devstack wrote. Where a file holds text of your own,
that text stays, byte for byte. Where devstack can not find the end of its own
block, it changes nothing and it names the file for you.

CAUTION: devstack does not own these repositories. Read the diff in each one
before you commit it.

  devstack migrate          run every pending patch
  devstack migrate --list   print every patch, applied or pending

Run this command again at any time. A second run changes nothing.`,
	SilenceUsage: true,
	RunE:         runMigrate,
}

func init() {
	rootCmd.AddCommand(migrateCmd)
	migrateCmd.Flags().Bool("list", false, "Print every patch, applied or pending, and change nothing")
}

// patches is every migration devstack knows, in the order it runs them. To add
// one, write a migrate.Patch and put it in this list.
func patches() []migrate.Patch {
	return []migrate.Patch{agentFilesPatch(), replicaPatch()}
}

func runMigrate(cmd *cobra.Command, args []string) error {
	list, _ := cmd.Flags().GetBool("list")
	all := registeredWorkspaces()
	if len(all) == 0 {
		fmt.Println("No workspace is registered on this machine, so devstack migrates nothing.")
		return nil
	}

	if list {
		st, err := migrate.List(patches(), all)
		if err != nil {
			return err
		}
		writePatchList(os.Stdout, st)
		return nil
	}

	fmt.Printf("devstack runs %d migrations over %s.\n", len(patches()), pluralWorkspaces(len(all)))
	return migrate.Apply(os.Stdout, patches(), all)
}

// writePatchList prints every patch, applied or pending. It changes nothing.
func writePatchList(w io.Writer, statuses []migrate.Status) {
	for _, st := range statuses {
		fmt.Fprintf(w, "\n%s  %s\n", st.ID, st.Title)
		for _, row := range st.Rows {
			switch {
			case row.Err != nil:
				fmt.Fprintf(w, "  %-16s blocked: %v\n", row.Name, row.Err)
			case row.Pending && !row.AppliedAt.IsZero():
				fmt.Fprintf(w, "  %-16s pending again (applied on %s): %s\n", row.Name, row.AppliedAt.Local().Format("2006-01-02 15:04"), row.Why)
			case row.Pending:
				fmt.Fprintf(w, "  %-16s pending: %s\n", row.Name, row.Why)
			case !row.AppliedAt.IsZero():
				fmt.Fprintf(w, "  %-16s applied on %s\n", row.Name, row.AppliedAt.Local().Format("2006-01-02 15:04"))
			default:
				fmt.Fprintf(w, "  %-16s nothing to do: %s\n", row.Name, row.Why)
			}
		}
	}
	fmt.Fprintln(w, "\ndevstack migrate runs each pending patch. This command changes nothing.")
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
// It rescans: the sweep is cheap and idempotent, so the filesystem decides and
// not the record. A repository cloned after the record was written still holds
// the old block, and a record must never be the thing that hides it.
func agentFilesPatch() migrate.Patch {
	return migrate.Patch{
		ID:     "0.2.0-agent-files",
		Title:  "Remove the devstack instructions from every repository, and wire each one for the live briefing",
		Rescan: true,
		Detect: detectAgentFiles,
		Run:    runAgentFiles,
		Next:   nextAgentFiles,
	}
}

func detectAgentFiles(ws *workspace.Workspace) (bool, string, error) {
	targets, _ := migrateTargets(ws)
	files, repos := 0, 0
	for _, t := range targets {
		files += len(scanResidue(t.Dir))
		if wiringPending(t) {
			repos++
		}
	}
	if files == 0 && repos == 0 {
		return false, "no file holds a devstack block, and every repository is wired", nil
	}
	return true, agentFilesPhrase(files, repos), nil
}

func agentFilesPhrase(files, repos int) string {
	var parts []string
	if files == 1 {
		parts = append(parts, "1 file holds a devstack block")
	} else if files > 1 {
		parts = append(parts, fmt.Sprintf("%d files hold a devstack block", files))
	}
	if repos == 1 {
		parts = append(parts, "1 repository is not wired to devstack")
	} else if repos > 1 {
		parts = append(parts, fmt.Sprintf("%d repositories are not wired to devstack", repos))
	}
	return strings.Join(parts, ", and ")
}

// wiringPending reads the two files the patch writes, and reports whether
// either one is missing or out of date. It writes nothing: 'upgrade' and
// '--list' both call it, and a report must change no file.
func wiringPending(t migrateTarget) bool {
	if t.Service != "" && mcpPending(t.Dir, t.Service) {
		return true
	}
	return hookPending(t.Dir)
}

func mcpPending(dir, service string) bool {
	want, err := mcpJSONContent(dir, service)
	if err != nil {
		return true
	}
	got, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	return err != nil || !bytes.Equal(got, want)
}

func hookPending(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, claudeSettingsRel))
	if err != nil {
		return true
	}
	settings := map[string]any{}
	if json.Unmarshal(data, &settings) != nil {
		return true
	}
	hooks, _ := settings["hooks"].(map[string]any)
	sessionStart, _ := hooks["SessionStart"].([]any)
	_, changed := mergePrimeHook(sessionStart)
	return changed
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

	out := migrate.Result{Changed: res.Changed() > 0, Lines: lines}
	for _, t := range res.Repos {
		out.Items = append(out.Items, migrate.Item{Label: t.Label, Path: t.Dir})
	}
	return out, errors.Join(errs...)
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
		out = append(out, "    1 file: devstack deleted it. It held nothing but the devstack block")
	} else if res.Deleted > 1 {
		out = append(out, fmt.Sprintf("    %s: devstack deleted them. They held nothing but the devstack block", pluralFiles(res.Deleted)))
	}
	if res.MCP > 0 {
		out = append(out, fmt.Sprintf("    %s: devstack wrote .mcp.json, which connects an agent to the MCP server", pluralRepos(res.MCP)))
	}
	if res.Hooks > 0 {
		out = append(out, fmt.Sprintf("    %s: devstack wired the SessionStart hook, so each session runs `devstack prime`", pluralRepos(res.Hooks)))
	}
	return out
}

// commitCommand is what the reader runs in each repository. It stages and
// commits, and it pushes nothing: devstack does not decide when work leaves this
// machine.
const commitCommand = `git add -A && git commit -m "chore: devstack migrate"`

// nextAgentFiles is the instruction after the sweep changed something. Each
// change is a real git diff in a repository devstack does not own, so the run is
// not finished until a human reads that diff and commits it.
func nextAgentFiles(results []migrate.Result) []string {
	out := []string{"NOW COMMIT. These repositories hold uncommitted changes:"}
	for _, r := range results {
		out = append(out, "  "+r.Workspace)
		for _, it := range r.Items {
			out = append(out, fmt.Sprintf("    %-24s %s", it.Label, it.Path))
		}
	}
	return append(out,
		"Read the diff in each repository. Then commit it there:",
		"  "+commitCommand,
		"devstack does not push. Push it yourself, or leave it.",
		"WHY: until you commit the diff, the next clone of that repository still carries",
		"the old instructions, and an agent that reads them acts on text that is not true.")
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

func pluralWorkspaces(n int) string {
	if n == 1 {
		return "1 workspace"
	}
	return fmt.Sprintf("%d workspaces", n)
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
