package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/selfcheck"
	"github.com/socialviolation/devstack/internal/workspace"
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Install the current devstack, and report what it leaves out of date",
	Long: `Install the current devstack from its source, then report the generated files
an older devstack wrote.

Upgrading is explicit on purpose. devstack manages a daemon that is running
services right now, and replacing the binary underneath a live MCP server leaves
the running process on the old code while every new invocation is on the new
one — which is worse than the stale build it fixes. So nothing upgrades on its
own; the session briefing tells you when it is worth doing.

Migrating is a second, separate decision. AGENTS.md is committed into every
service repo, so regenerating it produces a real git diff in repos devstack does
not own. This command reports what is out of date and stops. Pass --migrate to
regenerate it, which runs the newly installed devstack, never this one.

  devstack upgrade             install, then report what is now out of date
  devstack upgrade --migrate   also regenerate the files an older devstack wrote
  devstack upgrade --force     install even when this build is current or local`,
	SilenceUsage: true,
	RunE:         runUpgrade,
}

func init() {
	rootCmd.AddCommand(upgradeCmd)
	upgradeCmd.Flags().Bool("migrate", false, "Regenerate the generated files an older devstack wrote")
	upgradeCmd.Flags().Bool("force", false, "Install even when this build is already current, or is a local build")
}

func runUpgrade(cmd *cobra.Command, args []string) error {
	migrate, _ := cmd.Flags().GetBool("migrate")
	force, _ := cmd.Flags().GetBool("force")

	mod := modulePath()
	if mod == "" {
		return fmt.Errorf("this binary carries no module path, so there is nothing to install from")
	}
	fmt.Printf("installed: %s\n", buildVersion())

	res := selfcheck.Refresh(mod, buildRevision())
	if err := checkUpgradeWorthDoing(res, force); err != nil {
		return err
	}

	if res.Status != selfcheck.StatusCurrent || force {
		if err := goInstallLatest(mod); err != nil {
			return err
		}
	}

	newVersion := installedVersion()
	if newVersion != "" && newVersion != buildVersion() {
		fmt.Printf("now:       %s\n", newVersion)
	}
	// The stamp to compare against is the one the NEW binary writes. Comparing
	// against this process's version would call every file stale the moment the
	// install landed, and call them fresh again once it was restarted.
	target := newVersion
	if target == "" {
		target = buildVersion()
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
		return fmt.Errorf("this is a local build, and its commit is not published — installing would replace it with the published branch\nTo do that anyway: devstack upgrade --force")
	case selfcheck.StatusAhead:
		return fmt.Errorf("this build is %d commit(s) ahead of the published branch — installing would move it backwards\nTo do that anyway: devstack upgrade --force", res.AheadBy)
	case selfcheck.StatusCurrent:
		fmt.Println("This is the current build; nothing to install.")
		return nil
	}
	return nil
}

func goInstallLatest(mod string) error {
	target := mod + "@latest"
	fmt.Printf("installing %s ...\n", target)
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

// installedVersion asks the newly installed binary what it is, rather than
// assuming the install produced what was asked for.
func installedVersion() string {
	bin := installedBinary()
	if bin == "" {
		return ""
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(strings.SplitN(string(out), "\n", 2)[0])
	if len(fields) < 2 {
		return ""
	}
	return fields[1]
}

func reportAndMigrate(version string, migrate bool) error {
	stale, order := staleByWorkspace(version)
	if len(order) == 0 {
		fmt.Println("Every workspace's generated files match this build.")
		return nil
	}

	fmt.Printf("\n%d workspace(s) hold generated files an older devstack wrote:\n", len(order))
	for _, ws := range order {
		files := stale[ws.Name]
		fmt.Printf("  %-16s %d file(s)\n", ws.Name, len(files))
		for _, f := range files {
			fmt.Printf("      %-24s %s\n", f.Service, describeStamp(f.Version))
		}
	}

	if !migrate {
		fmt.Println("\nThese are committed into their repos, so regenerating them is a real diff.")
		fmt.Println("To regenerate: devstack upgrade --migrate")
		return nil
	}

	bin := installedBinary()
	if bin == "" {
		return fmt.Errorf("can not find the installed devstack to migrate with; run 'devstack init --all' in each workspace instead")
	}
	fmt.Println()
	return migrateWorkspaces(bin, order)
}

// migrateWorkspaces regenerates each workspace's files by running bin, which is
// the devstack that was just installed and not this process. This one is the old
// build: asking it to regenerate would write exactly the content being replaced.
//
// One workspace failing does not stop the rest — a half-migrated machine is
// worse than a fully-attempted one, and the failures are named at the end.
func migrateWorkspaces(bin string, order []workspace.Workspace) error {
	var failed []string
	for _, ws := range order {
		c := exec.Command(bin, "init", "--all")
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
		return fmt.Errorf("migration failed for: %s", strings.Join(failed, ", "))
	}
	fmt.Println("\nReview the diff in each repo before committing.")
	return nil
}
