package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/herdr"
	"github.com/socialviolation/devstack/internal/hostdaemon"
	nvxmcp "github.com/socialviolation/devstack/internal/mcp"
	"github.com/socialviolation/devstack/internal/workspace"
)

var tickCmd = &cobra.Command{
	Use:          "tick",
	Hidden:       true,
	Short:        "Run the periodic devstack jobs once. The devstack-sweep systemd timer runs this command",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runTick,
}

func init() {
	rootCmd.AddCommand(tickCmd)
}

var tickJobs = []func() error{
	tickSweep,
	tickHerdrPublish,
	tickSharedSync,
}

func runTick(cmd *cobra.Command, args []string) error {
	var errs []error
	for _, job := range tickJobs {
		if err := job(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func tickHerdrPublish() error {
	return herdr.Publish(context.Background())
}

func tickSweep() error {
	_, err := nvxmcp.Sweep(false, os.Stdout)
	return err
}

const directSweepEvery = 10 * time.Minute

var (
	systemdHost = hostdaemon.SystemdUserSession
	directSweep = func() {
		var out bytes.Buffer
		n, err := nvxmcp.Sweep(false, &out)
		if n > 0 || err != nil {
			fmt.Fprint(os.Stderr, out.String())
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		}
	}
)

func sweepStampPath() string {
	return filepath.Join(workspace.DataRoot(), "sweep.stamp")
}

func sweepIfDirect(now time.Time) {
	if !sweepDue(sweepStampPath(), now) {
		return
	}
	if systemdHost() {
		return
	}
	directSweep()
}

func sweepDue(path string, now time.Time) bool {
	return stampDue(path, now, directSweepEvery)
}

const sharedSyncEvery = time.Hour

func sharedSyncStampPath() string {
	return filepath.Join(workspace.DataRoot(), "shared-sync.stamp")
}

func tickSharedSync() error {
	if !stampDue(sharedSyncStampPath(), time.Now(), sharedSyncEvery) {
		return nil
	}
	return syncAllShared(os.Stdout)
}

var syncAllShared = func(w io.Writer) error {
	active, err := workspace.ActiveWorkspaces()
	if err != nil {
		return err
	}
	var errs []error
	for i := range active {
		if err := syncShared(&active[i], w); err != nil {
			errs = append(errs, fmt.Errorf("workspace %q: %w", active[i].Name, err))
		}
	}
	return errors.Join(errs...)
}

func stampDue(path string, now time.Time, every time.Duration) bool {
	if data, err := os.ReadFile(path); err == nil {
		if last, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data))); err == nil && now.Sub(last) < every && !now.Before(last) {
			return false
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false
	}
	return os.WriteFile(path, []byte(now.Format(time.RFC3339Nano)), 0644) == nil
}

func installSweepTimer() {
	if !systemdHost() {
		return
	}
	bin, err := os.Executable()
	if err == nil {
		bin, err = filepath.EvalSymlinks(bin)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: devstack can not find its own binary, so it does not install the %s timer: %v\n", hostdaemon.SweepUnit, err)
		return
	}
	installed, err := hostdaemon.InstallSweepTimer(bin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: devstack can not install the %s timer: %v\n", hostdaemon.SweepUnit, err)
		return
	}
	if installed {
		fmt.Printf("Installed the systemd user timer %s.timer. Every 10 minutes it stops the stacks that are past their stop time.\n", hostdaemon.SweepUnit)
	}
}
