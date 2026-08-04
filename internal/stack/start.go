package stack

import (
	"fmt"
	"sort"
	"time"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/tilt"
)

// StartServices enables and triggers a stack's own resources, in dependency
// order, and reports the services it started. Folding a stack into the host
// Tiltfile only registers its resources; without a trigger they sit at runtime
// "none" and the stack is up in name only. Services the stack reuses from base
// are not present under its namespace and are left running where they are.
func StartServices(client *tilt.Client, baseName string, rec *Record) ([]string, error) {
	order := StartOrder(rec)
	wanted := make([]string, 0, len(order))
	for _, svc := range order {
		wanted = append(wanted, ResourceName(baseName, svc, rec.Name))
	}

	view, err := waitForResources(client, wanted)
	if err != nil {
		return nil, fmt.Errorf("devstack can not reach the host daemon: %w", err)
	}
	present, disabled := map[string]bool{}, map[string]bool{}
	for _, r := range view.UiResources {
		present[r.Metadata.Name] = true
		if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
			disabled[r.Metadata.Name] = true
		}
	}

	var started []string
	for _, svc := range order {
		rn := ResourceName(baseName, svc, rec.Name)
		if !present[rn] {
			continue
		}
		if disabled[rn] {
			if out, err := client.RunCLI("enable", rn); err != nil {
				return nil, fmt.Errorf("devstack can not enable %s: %w\n%s", rn, err, out)
			}
		}
		if out, err := client.RunCLI("trigger", rn); err != nil {
			return nil, fmt.Errorf("devstack can not trigger %s: %w\n%s", rn, err, out)
		}
		started = append(started, svc)
	}
	return started, nil
}

// ResourceName is the host daemon's name for one service of one stack.
func ResourceName(baseName, svc, stackName string) string {
	if stackName == "" {
		return baseName + ":" + svc
	}
	return baseName + ":" + svc + ":" + stackName
}

// resourceWait bounds how long a caller waits for the daemon to load resources
// it has just written into the host Tiltfile.
var resourceWait = 30 * time.Second

// waitForResources polls until every named resource has been loaded from the
// regenerated Tiltfile, returning the last view read either way. Writing the
// Tiltfile does not make the daemon read it: triggering straight afterwards
// names resources the daemon has never heard of, and the stack silently never
// starts.
func waitForResources(client *tilt.Client, wanted []string) (*tilt.TiltView, error) {
	deadline := time.Now().Add(resourceWait)
	for {
		view, err := client.GetView()
		if err != nil {
			return nil, err
		}
		loaded := map[string]bool{}
		for _, r := range view.UiResources {
			loaded[r.Metadata.Name] = true
		}
		missing := false
		for _, w := range wanted {
			if !loaded[w] {
				missing = true
				break
			}
		}
		if !missing || time.Now().After(deadline) {
			return view, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// StartOrder lists a stack's services with each one's dependencies ahead of it,
// falling back to name order when the stack's manifest cannot be read.
func StartOrder(rec *Record) []string {
	names := append([]string(nil), rec.Overlay...)
	sort.Strings(names)

	cfg, err := config.Load(rec.Root)
	if err != nil {
		return names
	}
	var ordered []string
	seen := map[string]bool{}
	for _, svc := range names {
		resolved, err := config.ResolveDeps(cfg, svc)
		if err != nil {
			resolved = []string{svc}
		}
		for _, r := range resolved {
			if !seen[r] {
				seen[r] = true
				ordered = append(ordered, r)
			}
		}
	}
	return ordered
}
