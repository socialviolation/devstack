package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/selfcheck"
	"github.com/socialviolation/devstack/internal/workspace"
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Install the current devstack, and report what it leaves out of date",
	Long: `Install the current devstack from its source. Then report the generated files
that an older devstack wrote.

An upgrade is explicit on purpose. devstack manages a daemon that runs services
right now. If you replace the binary under a live MCP server, the running process
stays on the old code while every new run is on the new code. That is worse than
the stale build it corrects. So nothing upgrades on its own. The session briefing
tells you when an upgrade is worth doing.

An MCP server reads its tool descriptions once, when it starts. A session that
already runs therefore keeps the old tool list. Restart that session after the
upgrade.

A migration is a second and separate decision. Every service repo commits its
AGENTS.md, so a regeneration produces a real git diff in repos that devstack does
not own. A replica is one git worktree for each repository, and each worktree
needs its own dependency install. This command reports both, and then it stops.
Pass --migrate to regenerate the files and to build the replicas. The migration
runs the devstack that was just installed, and never this one.

  devstack upgrade             install, then report what is now out of date
  devstack upgrade --migrate   also regenerate the files, and build each replica
  devstack upgrade --force     install even when this build is current or local`,
	SilenceUsage: true,
	RunE:         runUpgrade,
}

func init() {
	rootCmd.AddCommand(upgradeCmd)
	upgradeCmd.Flags().Bool("migrate", false, "Also regenerate the files that an older devstack wrote, and build each workspace's replica")
	upgradeCmd.Flags().Bool("force", false, "Install even when this build is already current, or is a local build")
}

func runUpgrade(cmd *cobra.Command, args []string) error {
	migrate, _ := cmd.Flags().GetBool("migrate")
	force, _ := cmd.Flags().GetBool("force")

	mod := modulePath()
	if mod == "" {
		return fmt.Errorf("this binary carries no module path, so devstack has nothing to install from")
	}
	fmt.Printf("installed: %s\n", buildStamp())

	res := selfcheck.Refresh(mod, buildRevision())
	if err := checkUpgradeWorthDoing(res, force); err != nil {
		return err
	}

	if res.Status != selfcheck.StatusCurrent || force {
		if err := goInstallLatest(mod); err != nil {
			return err
		}
	}

	newStamp := installedStamp()
	if newStamp != "" && newStamp != buildStamp() {
		fmt.Printf("now:       %s\n", newStamp)
	}
	// The stamp to compare against is the one the NEW binary writes, and it must
	// be the whole stamp: this is compared against what is written into every
	// generated file, so anything less matches nothing. Comparing against this
	// process's stamp would call every file stale the moment the install landed,
	// and fresh again once it was restarted.
	target := newStamp
	if target == "" {
		target = buildStamp()
	}
	return reportAndMigrate(target, migrate)
}

// checkUpgradeWorthDoing stops an install that would move the binary backwards.
//
// A build from an unpushed commit is the normal state while developing devstack
// itself, and it is always "newer" than what is published. Installing @latest
// there replaces your work with the branch, silently, from a command whose name
// promises the opposite.
func checkUpgradeWorthDoing(res selfcheck.Result, force bool) error {
	if force {
		return nil
	}
	switch res.Status {
	case selfcheck.StatusLocal:
		return fmt.Errorf("this is a local build, and its commit is not published. An install replaces it with the published branch\nTo do that anyway, run: devstack upgrade --force")
	case selfcheck.StatusAhead:
		return fmt.Errorf("this build is %d commit(s) ahead of the published branch. An install moves it backwards\nTo do that anyway, run: devstack upgrade --force", res.AheadBy)
	case selfcheck.StatusCurrent:
		fmt.Println("This is the current build. devstack has nothing to install.")
		return nil
	}
	return nil
}

func goInstallLatest(mod string) error {
	target := mod + "@latest"
	fmt.Printf("devstack installs %s ...\n", target)
	c := exec.Command("go", "install", target)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("go install %s: %w", target, err)
	}
	return nil
}

// installedBinary is the devstack `go install` just wrote, which is not this
// process. Every migration runs through it: this process is the old build, and
// asking it to regenerate would write exactly the content being replaced.
func installedBinary() string {
	dir := ""
	if out, err := exec.Command("go", "env", "GOBIN").Output(); err == nil {
		dir = strings.TrimSpace(string(out))
	}
	if dir == "" {
		out, err := exec.Command("go", "env", "GOPATH").Output()
		if err != nil {
			return ""
		}
		if p := strings.TrimSpace(string(out)); p != "" {
			dir = filepath.Join(p, "bin")
		}
	}
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, "devstack")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// installedStamp asks the newly installed binary what it is, rather than
// assuming the install produced what was asked for.
func installedStamp() string {
	bin := installedBinary()
	if bin == "" {
		return ""
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return ""
	}
	return parseVersionOutput(string(out))
}

// parseVersionOutput reads a stamp back out of `devstack --version`.
//
// It takes the whole of the first line after the program name, because the stamp
// has spaces in it: "v0.1.1 (f1969ec)". Taking the second field instead dropped
// the commit, so the value compared against the stamp in every generated file
// could never equal it, and `upgrade` reported every file in every workspace as
// written by an older devstack — while `workspace doctor`, which compares
// buildStamp directly, reported the same files as current.
func parseVersionOutput(out string) string {
	first := strings.SplitN(out, "\n", 2)[0]
	rest := strings.TrimPrefix(strings.TrimSpace(first), "devstack")
	return strings.TrimSpace(rest)
}

func reportAndMigrate(version string, migrate bool) error {
	stale, order := staleByWorkspace(version)
	writeStaleReport(os.Stdout, stale, order, migrate)

	all := registeredWorkspaces()
	writeReplicaReport(os.Stdout, planReplicas(all), migrate)

	if !migrate {
		writeDeprecations(os.Stdout)
		return nil
	}

	bin := installedBinary()
	if bin == "" {
		return fmt.Errorf("devstack can not find the installed binary to migrate with. Run 'devstack init --all' in each workspace instead")
	}

	// The generated files come first. They tell an agent that a bare restart acts
	// on base, which this release makes false, and a build takes long enough for
	// an agent to read the old text and act on it.
	var failures []error
	if len(order) > 0 {
		fmt.Println()
		if err := migrateWorkspaces(bin, order); err != nil {
			failures = append(failures, err)
		}
	}
	if err := buildReplicas(bin, all); err != nil {
		failures = append(failures, err)
	}
	writeDeprecations(os.Stdout)
	return errors.Join(failures...)
}

func registeredWorkspaces() []workspace.Workspace {
	all, err := workspace.All()
	if err != nil {
		return nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all
}

func writeStaleReport(w io.Writer, stale map[string][]generatedFile, order []workspace.Workspace, migrate bool) {
	if len(order) == 0 {
		fmt.Fprintln(w, "The generated files of every workspace match this build.")
		return
	}

	fmt.Fprintf(w, "\n%d workspace(s) hold generated files that differ from what this devstack writes:\n", len(order))
	for _, ws := range order {
		files := stale[ws.Name]
		fmt.Fprintf(w, "  %-16s %d file(s)\n", ws.Name, len(files))
		for _, f := range files {
			fmt.Fprintf(w, "      %-24s %s\n", f.Service, describeStamp(f.Version))
		}
	}
	if migrate {
		return
	}
	fmt.Fprintln(w, "\ndevstack upgrade --migrate brings these up to date. In every service above,")
	fmt.Fprintln(w, "and in each stack worktree, it writes:")
	fmt.Fprintln(w, "  AGENTS.md              the devstack block, which replaces what an older one left")
	fmt.Fprintln(w, "  .mcp.json              the MCP server entry")
	fmt.Fprintln(w, "  CLAUDE.md and friends  a pointer block, in the files a repo already has")
	fmt.Fprintln(w, "  .claude/settings.json  the SessionStart hook, so every session runs devstack prime")
	fmt.Fprintln(w, "\nEvery repo commits these files, so this is a real diff in each one. devstack")
	fmt.Fprintln(w, "touches nothing else in them, and it creates no file that is not already there.")
}

// migrateArgs is the full refresh: every generated file this devstack owns,
// brought to what this devstack writes.
//
// --claude-hook is included here and nowhere else by default. The flag is
// opt-in on `init` because .claude/settings.json is committed, and adding a hook
// to somebody's repo unasked is not a side effect a refresh should have. A
// migration is the one place it is asked for: the report above names every file
// before anything is written, and running it is a separate decision from
// upgrading.
var migrateArgs = []string{"init", "--all", "--claude-hook"}

// migrateWorkspaces regenerates each workspace's files by running bin, which is
// the devstack that was just installed and not this process. This one is the old
// build: asking it to regenerate would write exactly the content being replaced.
//
// One workspace failing does not stop the rest — a half-migrated machine is
// worse than a fully-attempted one, and the failures are named at the end.
func migrateWorkspaces(bin string, order []workspace.Workspace) error {
	var failed []string
	for _, ws := range order {
		c := exec.Command(bin, migrateArgs...)
		c.Dir = ws.Path
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			failed = append(failed, ws.Name)
			fmt.Fprintf(os.Stderr, "✗ %s: %v\n", ws.Name, err)
			continue
		}
		fmt.Printf("✓ %s regenerated\n", ws.Name)
	}
	if len(failed) > 0 {
		return fmt.Errorf("the migration failed for: %s", strings.Join(failed, ", "))
	}
	fmt.Println("\nRead the diff in each repo before you commit.")
	return nil
}
