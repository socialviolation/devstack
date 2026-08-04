package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/migrate"
	"github.com/socialviolation/devstack/internal/selfcheck"
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Install the current devstack, and report what it leaves out of date",
	Long: `Install the current devstack from its source. Then report each migration that
this machine still needs.

An upgrade is explicit on purpose. devstack manages a daemon that runs services
right now. If you replace the binary under a live MCP server, the running process
stays on the old code while every new run is on the new code. That is worse than
the stale build it corrects. So nothing upgrades on its own. The session briefing
tells you when an upgrade is worth doing.

An MCP server reads its tool descriptions once, when it starts. A session that
already runs therefore keeps the old tool list. Restart that session after the
upgrade.

A migration is a second and separate decision. The file sweep writes files that a
repository commits, so it produces a real git diff in repos that devstack does not
own. A replica is one git worktree for each repository, and each worktree needs
its own dependency install. This command names each pending patch, and then it
stops. Pass --migrate to run them. The migration runs 'devstack migrate' through
the devstack that was just installed, and never through this one.

  devstack upgrade             install, then report each pending migration
  devstack upgrade --migrate   also run devstack migrate
  devstack upgrade --force     install even when this build is current or local

To read the same report without installing anything, run: devstack migrate --list`,
	SilenceUsage: true,
	RunE:         runUpgrade,
}

func init() {
	rootCmd.AddCommand(upgradeCmd)
	upgradeCmd.Flags().Bool("migrate", false, "Also remove the instructions that an older devstack wrote, and build each workspace's replica")
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
	return reportAndMigrate(migrate)
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

func reportAndMigrate(doMigrate bool) error {
	all := migrate.Workspaces()
	statuses, err := migrate.List(patches(), all)
	if err != nil {
		return err
	}
	writePendingReport(os.Stdout, statuses, doMigrate)

	if !doMigrate {
		writeDeprecations(os.Stdout)
		return nil
	}

	bin := installedBinary()
	if bin == "" {
		return fmt.Errorf("devstack can not find the installed binary to migrate with. Run 'devstack migrate' instead")
	}

	fmt.Println()
	merr := runMigration(bin)
	writeDeprecations(os.Stdout)
	return merr
}

// writePendingReport names each patch that still has work, and the workspaces it
// has work in. It reads only, and it changes nothing: a user who runs `upgrade`
// to see the damage must not have already taken it.
func writePendingReport(w io.Writer, statuses []migrate.Status, doMigrate bool) {
	pending := 0
	for _, st := range statuses {
		if !st.Pending() {
			continue
		}
		pending++
		fmt.Fprintf(w, "\n%s  %s\n", st.ID, st.Title)
		for _, row := range st.Rows {
			switch {
			case row.Err != nil:
				fmt.Fprintf(w, "  %-16s blocked: %v\n", row.Name, row.Err)
			case row.Pending:
				fmt.Fprintf(w, "  %-16s %s\n", row.Name, row.Why)
			}
		}
	}

	if pending == 0 {
		fmt.Fprintln(w, "\nEvery migration is applied. devstack has nothing to migrate.")
		return
	}
	if doMigrate {
		return
	}
	if pending == 1 {
		fmt.Fprintln(w, "\n1 migration is pending. devstack upgrade --migrate runs it, through the binary")
	} else {
		fmt.Fprintf(w, "\n%d migrations are pending. devstack upgrade --migrate runs them, through the binary\n", pending)
	}
	fmt.Fprintln(w, "it just installed. This command changes nothing.")
	fmt.Fprintln(w, "The file sweep writes files that a repository commits, so where it has work it makes")
	fmt.Fprintln(w, "a real git diff. Read the diff before you commit it. Do this when you are not mid-task.")
}

// migrateArgs runs the sweep of every workspace. `devstack migrate` is one
// command for the whole machine, so the upgrade path stays one command too.
var migrateArgs = []string{"migrate"}

// runMigration sweeps every workspace by running bin, which is the devstack that
// was just installed and not this process. This one is the old build: asking it
// to migrate would leave behind exactly what the new one removes.
func runMigration(bin string) error {
	c := exec.Command(bin, migrateArgs...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("devstack migrate failed: %w. To read the error, run: devstack migrate", err)
	}
	return nil
}
