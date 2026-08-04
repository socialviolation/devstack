package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/migrate"
	"github.com/socialviolation/devstack/internal/selfcheck"
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Install the current devstack, migrate this machine, and restart what runs",
	Long: `Upgrade this machine in three steps.

  1. Install the current devstack from its source.
  2. Move your configuration to the version that this devstack needs, and build
     the replica of each workspace that has none. The version lives in
     devstack.workspace.yaml, which you commit, so a migration runs one time for
     the repository and not one time for each machine. The migration runs
     'devstack migrate' through the devstack that was just installed, and never
     through this one.
  3. Transform the running state. devstack regenerates the daemon's Tiltfile and
     restarts each copy that runs now, so that what runs comes from the replica
     and not from your checkout.

An upgrade is explicit on purpose. devstack manages a daemon that runs services
right now. If you replace the binary under a live MCP server, the running process
stays on the old code. So nothing upgrades on its own. The session briefing tells
you when an upgrade is worth doing.

An MCP server reads its tool descriptions once, when it starts. A session that
already runs therefore keeps the old tool list. Restart that session after the
upgrade.

CAUTION: Step 3 restarts services that serve right now. devstack restarts only
the copies that were already running. It starts no copy that is stopped, and it
starts no daemon. Each replica worktree is a new checkout, so a service can need
its own dependency install before it serves again. This step is slow.

devstack does not own your service repositories. A migration makes a real git
diff in each one. devstack neither commits nor pushes. Read each diff, and commit
it yourself.

  devstack upgrade                install, migrate, then restart what runs
  devstack upgrade --no-migrate   install only, and name each pending migration
  devstack upgrade --no-restart   install and migrate, and restart nothing
  devstack upgrade --force        install over every refusal: a build that is
                                  current, local, ahead, or unidentified

To read what a migration does, and change nothing, run: devstack migrate --list`,
	SilenceUsage: true,
	RunE:         runUpgrade,
}

func init() {
	rootCmd.AddCommand(upgradeCmd)
	upgradeCmd.Flags().Bool("no-migrate", false, "Do not run the pending migrations. devstack names them instead")
	upgradeCmd.Flags().Bool("no-restart", false, "Do not restart the copies that run. They keep serving the old code")
	upgradeCmd.Flags().Bool("force", false, "Install over every refusal: a build that is current, is local, is ahead of the published branch, or can not be identified")
}

func runUpgrade(cmd *cobra.Command, args []string) error {
	noMigrate, _ := cmd.Flags().GetBool("no-migrate")
	noRestart, _ := cmd.Flags().GetBool("no-restart")
	force, _ := cmd.Flags().GetBool("force")

	mod := modulePath()
	if mod == "" {
		return fmt.Errorf("this binary carries no module path, so devstack has nothing to install from")
	}
	writeUpgradeIntent(os.Stdout, noMigrate, noRestart)
	fmt.Println("STEP 1 of 3: install the binary")
	fmt.Printf("installed: %s\n", buildStamp())

	res := selfcheck.Refresh(mod, buildRevision())
	if err := checkUpgradeWorthDoing(res, buildVersion(), force); err != nil {
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

	fmt.Println("\nSTEP 2 of 3: run each pending migration")
	step2 := reportAndMigrate(!noMigrate)

	fmt.Println("\nSTEP 3 of 3: transform the running state")
	terr := transformStep(noMigrate, noRestart, step2)

	writeUpgradeNext(os.Stdout)
	writeDeprecations(os.Stdout)
	return errors.Join(step2.Err(), terr)
}

// step2Result is what step 2 leaves for step 3. The two failures of step 2 are
// separate, because they permit different things: a migration that failed or
// refused stops no service, and a replica that did not build is the one reason a
// restart moves a copy onto the checkout.
type step2Result struct {
	// MigrateErr is the failure of the migration, or nil.
	MigrateErr error
	// ReplicaErr is every failure to build a replica, or nil.
	ReplicaErr error
	// Unbuilt names the workspaces whose replica did not build.
	Unbuilt []string
}

func (s step2Result) Err() error { return errors.Join(s.MigrateErr, s.ReplicaErr) }

// Refused reports whether the migration stopped before it wrote. A refusal
// changes no file, so the state of the machine is the state step 3 expects.
func (s step2Result) Refused() bool { return errors.Is(s.MigrateErr, migrate.ErrRefused) }

// transformStep restarts what runs, unless the user asked devstack not to.
//
// It is skipped with step 2 as well: step 2 builds the replicas that the copies
// must serve from, so a restart before it moves a copy onto a replica that is
// not there.
//
// A migration that failed stops the restart. It wrote part of a configuration,
// and devstack can not say which part, so it changes nothing else.
//
// A migration that refused wrote nothing at all. It also stopped nothing: step 2
// still built each replica, so each copy has a replica to serve. A machine that
// installs a new binary and then keeps every copy on the old code is half
// upgraded, so the restart goes ahead.
//
// A replica that did not build stops the restart of that workspace, and of that
// workspace only. Its copies have no replica to serve, so a restart would move
// them onto the checkout. Every other workspace has its replica, and one broken
// workspace must not keep the whole machine on the old code.
func transformStep(noMigrate, noRestart bool, step step2Result) error {
	if noRestart {
		fmt.Println("You gave --no-restart, so devstack restarts nothing.")
		fmt.Println("Each copy that runs keeps serving the code it started with.")
		fmt.Println("To move one copy to the replica, run: devstack service restart <svc> --stack base")
		return nil
	}
	if noMigrate {
		fmt.Println("You gave --no-migrate, so devstack restarts nothing. A copy can only serve a replica")
		fmt.Println("that is built. Run the migrations first: devstack migrate")
		return nil
	}
	if step.MigrateErr != nil && !step.Refused() {
		fmt.Println("STEP 2 FAILED, so devstack restarts nothing. The migration did not finish, and devstack")
		fmt.Println("can not say how much of your configuration it wrote.")
		fmt.Println("Read the failure in step 2. After you fix the cause, run: devstack upgrade")
		return nil
	}
	if step.Refused() {
		fmt.Println("STEP 2 REFUSED TO MIGRATE, and it changed no file in any repository. It built each")
		fmt.Println("replica, so devstack restarts what runs, and every copy serves its replica.")
		fmt.Println("Your configuration stays on its old version, and this migration stays pending.")
		fmt.Println("Read the refusal in step 2. It names each file that holds a change nobody committed.")
		fmt.Println("Commit or stash each one, then run: devstack migrate")
	}
	if len(step.Unbuilt) > 0 {
		fmt.Printf("devstack restarts no copy of %s. The replica of each one did not build in step 2, so a\n", pluralWorkspaceList(step.Unbuilt))
		fmt.Println("restart there moves a copy onto your checkout. Each copy keeps serving the old code.")
		fmt.Println("To build a replica after you fix the cause, run in that workspace: devstack workspace up")
		fmt.Println("devstack restarts every other workspace now.")
	}
	return transformRunningState(os.Stdout, "", step.Unbuilt)
}

// pluralWorkspaceList names the workspaces, in the order they failed, and it
// keeps the sentence grammatical either way.
func pluralWorkspaceList(names []string) string {
	if len(names) == 1 {
		return "the workspace " + names[0]
	}
	return "these workspaces: " + strings.Join(names, ", ")
}

// writeUpgradeNext is the work that is left for a human. devstack does not own
// the service repositories, so it can not read a diff and decide that it is
// right to keep.
func writeUpgradeNext(w io.Writer) {
	fmt.Fprintln(w, "\nNEXT")
	fmt.Fprintln(w, "1. Read the diff in each repository a migration wrote in. Then commit it there:")
	fmt.Fprintln(w, "   "+commitCommand)
	fmt.Fprintln(w, "   devstack does not commit, and it does not push.")
	fmt.Fprintln(w, "2. Restart your agent session. It holds the tool list from before this upgrade.")
	fmt.Fprintln(w, "3. Check what runs: devstack status")
}

// checkUpgradeWorthDoing stops an install that would move the binary backwards.
//
// A build from an unpushed commit is the normal state while developing devstack
// itself, and it is always "newer" than what is published. Installing @latest
// there replaces your work with the branch, silently, from a command whose name
// promises the opposite.
//
// The comparison needs the commit, and a binary made with `go build` outside a
// git checkout carries none. The status is then "unknown", which used to
// install: the guard read the status and never the identity, so the one build
// most likely to hold unpushed work was the one build it did not protect.
//
// A build with no version tag is a build from source, so devstack refuses it
// whenever the comparison gives no answer, whatever the cause. A wrong install
// here destroys work, and a wrong refusal costs one flag. A tagged build is a
// published release, which holds no work of yours, so it installs.
func checkUpgradeWorthDoing(res selfcheck.Result, version string, force bool) error {
	if force {
		return nil
	}
	if version == devVersion && res.Status == selfcheck.StatusUnknown {
		why := "devstack can not read what the published branch holds"
		if res.Revision == "" {
			why = "this build carries no commit to compare"
		}
		return fmt.Errorf("this build carries no version tag, and %s. devstack can not tell whether an install moves it backwards\nTo install the published branch over it, run: devstack upgrade --force", why)
	}
	switch res.Status {
	case selfcheck.StatusLocal:
		return fmt.Errorf("this is a local build, and its commit is not published. An install replaces it with the published branch\nTo do that anyway, run: devstack upgrade --force")
	case selfcheck.StatusAhead:
		return fmt.Errorf("this build is %s ahead of the published branch. An install moves it backwards\nTo do that anyway, run: devstack upgrade --force", pluralCommits(res.AheadBy))
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

// reportAndMigrate moves the configuration to the version this devstack needs,
// and then builds each replica that is missing.
//
// The replica is machine state, so no migration owns it. An upgraded machine
// must still have one, so the upgrade builds it here, directly.
//
// It keeps the two failures apart, because step 3 acts on them differently.
func reportAndMigrate(doMigrate bool) step2Result {
	all, err := migrate.Workspaces()
	if err != nil {
		return step2Result{MigrateErr: err}
	}
	writePendingReport(os.Stdout, migrate.List(patches(), all), doMigrate)

	if !doMigrate {
		return step2Result{}
	}

	bin := installedBinary()
	if bin == "" {
		return step2Result{MigrateErr: fmt.Errorf("devstack can not find the installed binary to migrate with. Run 'devstack migrate' instead")}
	}

	fmt.Println()
	merr := runMigration(bin)
	perr, unbuilt := ensureReplicas(os.Stdout)
	return step2Result{MigrateErr: merr, ReplicaErr: perr, Unbuilt: unbuilt}
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
		fmt.Fprintf(w, "\n%s  %s\n", st.Name(), st.Title)
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
		fmt.Fprintln(w, "\n1 migration is pending. You gave --no-migrate, so devstack did not run it.")
	} else {
		fmt.Fprintf(w, "\n%d migrations are pending. You gave --no-migrate, so devstack did not run them.\n", pending)
	}
	fmt.Fprintln(w, "A migration writes files in your repositories. Where it does, it makes a real git diff.")
	fmt.Fprintln(w, "Read that diff before you commit it.")
	fmt.Fprintln(w, "To run the migrations, run: devstack migrate")
}

// migrateArgs runs the sweep of every workspace. `devstack migrate` is one
// command for the whole machine, so the upgrade path stays one command too.
var migrateArgs = []string{"migrate"}

// runMigration sweeps every workspace by running bin, which is the devstack that
// was just installed and not this process. This one is the old build: asking it
// to migrate would leave behind exactly what the new one removes.
//
// A migration that refused wrote nothing, and it names the files that stopped it
// on the output above. That is a different outcome from a write that failed, and
// the exit status is the only place this process can read it.
func runMigration(bin string) error {
	c := exec.Command(bin, migrateArgs...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	err := c.Run()
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == exitMigrateRefused {
		return fmt.Errorf("devstack migrate changed nothing: %w", migrate.ErrRefused)
	}
	return fmt.Errorf("devstack migrate failed: %w. To read the error, run: devstack migrate", err)
}

// writeUpgradeIntent states what the command changes, before it changes it. An
// upgrade of devstack is also an upgrade of the configuration in repositories
// devstack does not own, and of the code the running copies serve. A reader who
// learns that from the report has already had it done to them.
func writeUpgradeIntent(w io.Writer, noMigrate, noRestart bool) {
	fmt.Fprintf(w, "devstack upgrade migrates your configuration to version %d, which is the version that\nthis devstack needs. devstack writes that version in %s, and you commit that file.\n", config.WorkspaceManifestVersion, config.WorkspaceManifestFileName)
	switch {
	case noMigrate:
		fmt.Fprintln(w, "You gave --no-migrate, so devstack installs the binary and changes nothing else.")
		fmt.Fprintln(w, "Your configuration stays on its current version. To read what is pending, run: devstack migrate --list")
	case noRestart:
		fmt.Fprintln(w, "It writes files in your repositories, and it builds the replica that base runs from.")
		fmt.Fprintln(w, "You gave --no-restart, so each copy that runs keeps serving the old code.")
	default:
		fmt.Fprintln(w, "It writes files in your repositories. It builds the replica that base runs from.")
		fmt.Fprintln(w, "It then restarts each copy that runs, so that copy serves the code in the replica.")
		fmt.Fprintln(w, "To install the binary and change nothing else, run: devstack upgrade --no-migrate")
	}
	fmt.Fprintln(w)
}
