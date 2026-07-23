package cmd

import (
	"reflect"
	"testing"

	"github.com/fatih/color"

	"github.com/socialviolation/devstack/internal/tilt"
)

func runningResource() tilt.UIResource {
	var r tilt.UIResource
	r.Status.RuntimeStatus = "ok"
	return r
}

func testSections() []serviceSection {
	backend := serviceSection{
		label:     "backend",
		members:   []string{"api", "db"},
		deps:      map[string][]string{"api": {"db"}},
		resources: map[string]tilt.UIResource{"api": runningResource()},
		envs:      map[string]string{"api": "local"},
		dirs:      map[string]string{"api": "/src/api"},
		color:     color.New(color.FgCyan),
		running:   1,
		tag:       "[1/2]",
	}
	frontend := serviceSection{
		label:     "frontend",
		members:   []string{"web"},
		deps:      map[string][]string{"web": {"api"}},
		resources: map[string]tilt.UIResource{"web": runningResource()},
		color:     color.New(color.FgBlue),
		running:   1,
		tag:       "[1/1]",
	}
	idleGroup := serviceSection{
		label:   "holdings",
		members: []string{"prices", "events"},
		deps:    map[string][]string{},
		color:   color.New(color.FgGreen),
		running: 0,
		tag:     "[0/2]",
	}
	stackAgent := serviceSection{
		label:     "stack: agent",
		members:   []string{"api", "web"},
		deps:      map[string][]string{"web": {"api"}},
		resources: map[string]tilt.UIResource{"api": runningResource(), "web": runningResource()},
		color:     color.New(color.FgMagenta),
		running:   2,
		tag:       "[2/2]",
		isStack:   true,
	}
	ungrouped := serviceSection{
		label:     ungroupedLabel,
		members:   []string{"lonely"},
		deps:      map[string][]string{},
		resources: map[string]tilt.UIResource{"lonely": runningResource()},
		color:     color.New(color.Faint),
		running:   1,
		tag:       "[1/1]",
	}
	return []serviceSection{stackAgent, frontend, idleGroup, ungrouped, backend}
}

func sectionLabels(sections []serviceSection) []string {
	out := make([]string, 0, len(sections))
	for _, s := range sections {
		out = append(out, s.label)
	}
	return out
}

func TestSortSectionsBaseGroupsThenUngroupedThenStacks(t *testing.T) {
	got := sectionLabels(sortSections(testSections()))
	want := []string{"backend", "frontend", "holdings", ungroupedLabel, "stack: agent"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sortSections order = %v, want %v", got, want)
	}
}

func TestPartitionSectionsCondensesOnlyIdleSections(t *testing.T) {
	table, condensed := partitionSections(testSections(), false)
	if got, want := sectionLabels(condensed), []string{"holdings"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("condensed = %v, want %v", got, want)
	}
	want := []string{"backend", "frontend", ungroupedLabel, "stack: agent"}
	if got := sectionLabels(table); !reflect.DeepEqual(got, want) {
		t.Fatalf("table = %v, want %v", got, want)
	}
}

func TestPartitionSectionsExpandTablesEverything(t *testing.T) {
	table, condensed := partitionSections(testSections(), true)
	if len(condensed) != 0 {
		t.Fatalf("condensed = %v, want none with expand", sectionLabels(condensed))
	}
	want := []string{"backend", "frontend", "holdings", ungroupedLabel, "stack: agent"}
	if got := sectionLabels(table); !reflect.DeepEqual(got, want) {
		t.Fatalf("table = %v, want %v", got, want)
	}
}

func TestAssembleRowsGroupsContiguousBaseBeforeStacks(t *testing.T) {
	table, _ := partitionSections(testSections(), true)
	rows := assembleRows(table)

	var groups []string
	for _, r := range rows {
		if len(groups) == 0 || groups[len(groups)-1] != r.group {
			groups = append(groups, r.group)
		}
	}
	want := []string{"backend", "frontend", "holdings", ungroupedLabel, "stack: agent"}
	if !reflect.DeepEqual(groups, want) {
		t.Fatalf("group bands = %v, want each group contiguous in order %v", groups, want)
	}
}

func TestAssembleRowsDependencyOrderWithinGroup(t *testing.T) {
	table, _ := partitionSections(testSections(), true)
	rows := assembleRows(table)

	pos := map[string]int{}
	for i, r := range rows {
		pos[r.group+"/"+r.service] = i
	}
	if pos["backend/db"] >= pos["backend/api"] {
		t.Fatalf("db must precede api within backend, got %v", rowKeys(rows))
	}
	if pos["stack: agent/api"] >= pos["stack: agent/web"] {
		t.Fatalf("api must precede web within stack: agent, got %v", rowKeys(rows))
	}
}

func TestAssembleRowsAlwaysCarriesGroupAsText(t *testing.T) {
	table, _ := partitionSections(testSections(), true)
	for _, r := range assembleRows(table) {
		if r.group == "" {
			t.Fatalf("row %q has an empty GROUP cell; the group must survive colour stripping", r.service)
		}
	}
}

func TestAssembleRowsCarriesStateEnvPortsAndDir(t *testing.T) {
	table, _ := partitionSections(testSections(), true)
	rows := assembleRows(table)

	var api, prices *statusRow
	for i := range rows {
		switch {
		case rows[i].group == "backend" && rows[i].service == "api":
			api = &rows[i]
		case rows[i].service == "prices":
			prices = &rows[i]
		}
	}
	if api == nil || prices == nil {
		t.Fatalf("missing expected rows in %v", rowKeys(rows))
	}
	if api.state != "running" {
		t.Fatalf("api state = %q, want running", api.state)
	}
	if api.env != "local" {
		t.Fatalf("api env = %q, want local", api.env)
	}
	if api.dir != "/src/api" {
		t.Fatalf("api dir = %q, want /src/api", api.dir)
	}
	if !reflect.DeepEqual(api.needs, []string{"db"}) {
		t.Fatalf("api needs = %v, want [db]", api.needs)
	}
	if prices.state != "unknown" {
		t.Fatalf("prices state = %q, want unknown (absent from the daemon view)", prices.state)
	}
}

func rowKeys(rows []statusRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.group+"/"+r.service)
	}
	return out
}

func TestServiceStatusLabelsNotRunningAsStopped(t *testing.T) {
	var r tilt.UIResource
	if got := serviceStatus(r); got != "stopped" {
		t.Fatalf("serviceStatus(zero) = %q, want stopped", got)
	}
	if got := serviceStatus(runningResource()); got != "running" {
		t.Fatalf("serviceStatus(ok) = %q, want running", got)
	}

	var disabled tilt.UIResource
	disabled.Status.DisableStatus = &tilt.DisableStatus{State: "Disabled"}
	if got := serviceStatus(disabled); got != "disabled" {
		t.Fatalf("serviceStatus(disabled) = %q, want disabled", got)
	}
}

func TestCountRunningCountsOnlyRunning(t *testing.T) {
	resources := map[string]tilt.UIResource{"api": runningResource()}
	var pending tilt.UIResource
	pending.Status.RuntimeStatus = "pending"
	resources["web"] = pending

	if got := countRunning([]string{"api", "web", "absent"}, resources); got != 1 {
		t.Fatalf("countRunning = %d, want 1", got)
	}
}
