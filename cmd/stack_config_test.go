package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/replica"
	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/workspace"
)

func stackConfigCmdFor(t *testing.T, stackName string) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "config"}
	c.Flags().String("stack", "", "")
	if err := c.Flags().Set("stack", stackName); err != nil {
		t.Fatalf("set --stack: %v", err)
	}
	return c
}

// stack config makes no daemon call, so it can not report what runs. It reads
// the stack's files. For a stack that is down it must say the stack is down,
// and it must not describe the table as the configuration in use.
func TestStackConfigSaysAStackIsDown(t *testing.T) {
	rec, _ := buildStackScenario(t)
	viper.Set("workspace", "navexa")
	t.Cleanup(func() { viper.Set("workspace", "") })

	out := captureStdout(t, func() {
		if err := runStackConfig(stackConfigCmdFor(t, rec.Name), []string{"backend"}); err != nil {
			t.Fatalf("runStackConfig: %v", err)
		}
	})
	for _, want := range []string{"is down", "the configuration it starts with", "devstack stack up feat"} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "the configuration it runs with") {
		t.Errorf("a down stack must not be described as running:\n%s", out)
	}
}

// An up stack keeps the plain wording, so the two states read differently.
func TestStackConfigSaysAnUpStackRuns(t *testing.T) {
	rec, _ := buildStackScenario(t)
	viper.Set("workspace", "navexa")
	t.Cleanup(func() { viper.Set("workspace", "") })
	if err := stack.SetActive("navexa", rec.Name, true); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runStackConfig(stackConfigCmdFor(t, rec.Name), []string{"backend"}); err != nil {
			t.Fatalf("runStackConfig: %v", err)
		}
	})
	if !strings.Contains(out, "the configuration it runs with") {
		t.Errorf("an up stack must keep the plain wording, got:\n%s", out)
	}
	if strings.Contains(out, "is down") {
		t.Errorf("an up stack must not be reported as down:\n%s", out)
	}
}

func TestStackConfigReadsASharedServiceFromTheReplica(t *testing.T) {
	buildStackScenario(t)
	useWorkspaceKey(t, "navexa")
	ws, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("FindByName: %v", err)
	}
	if _, err := replica.Ensure(ws); err != nil {
		t.Fatalf("replica.Ensure: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runStackConfig(stackConfigCmdFor(t, ""), []string{"db"}); err != nil {
			t.Fatalf("runStackConfig db: %v", err)
		}
	})

	for _, want := range []string{"in the shared set", replica.Root(ws), "PGPORT", "5432", "the shared set does not run"} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "in stack") {
		t.Errorf("the shared set is not a stack, and the report must not call it one:\n%s", out)
	}
}

func TestStackConfigRefusesAStackServiceWithNoStack(t *testing.T) {
	buildStackScenario(t)
	useWorkspaceKey(t, "navexa")

	err := runStackConfig(stackConfigCmdFor(t, ""), []string{"backend"})
	if err == nil || !strings.Contains(err.Error(), "not a shared service") || !strings.Contains(err.Error(), "--stack <name>") {
		t.Errorf("a stack service with no --stack must be refused and --stack named; got %v", err)
	}
}

func TestStackConfigRefusesStackBase(t *testing.T) {
	buildStackScenario(t)
	useWorkspaceKey(t, "navexa")

	err := runStackConfig(stackConfigCmdFor(t, "base"), []string{"db"})
	if err == nil || !strings.Contains(err.Error(), `"base"`) {
		t.Errorf("--stack base names no stack and must be refused; got %v", err)
	}
}

func TestStackConfigSaysWhenTheSharedSetHasNoReplica(t *testing.T) {
	buildStackScenario(t)
	useWorkspaceKey(t, "navexa")

	err := runStackConfig(stackConfigCmdFor(t, ""), []string{"db"})
	if err == nil {
		t.Fatal("a shared service without a replica must fail")
	}
	for _, want := range []string{"has not built the replica", "devstack workspace up"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must state %q; got %v", want, err)
		}
	}
}

func TestStackConfigSaysWhenTheReplicaIsStale(t *testing.T) {
	buildStackScenario(t)
	useWorkspaceKey(t, "navexa")
	ws, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("FindByName: %v", err)
	}
	if _, err := replica.Ensure(ws); err != nil {
		t.Fatalf("replica.Ensure: %v", err)
	}
	stale := filepath.Join(replica.Root(ws), "db", "devstack.service.yaml")
	if err := os.Remove(stale); err != nil {
		t.Fatalf("remove the replica's db manifest: %v", err)
	}

	err = runStackConfig(stackConfigCmdFor(t, ""), []string{"db"})
	if err == nil {
		t.Fatal("a replica that does not resolve must fail")
	}
	for _, want := range []string{"stale or incomplete", "devstack workspace up", stale} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must state %q; got %v", want, err)
		}
	}
}

func TestStackConfigRefusesAnUnknownServiceWithNoStack(t *testing.T) {
	buildStackScenario(t)
	useWorkspaceKey(t, "navexa")

	err := runStackConfig(stackConfigCmdFor(t, ""), []string{"nope"})
	if err == nil || !strings.Contains(err.Error(), "backend") || !strings.Contains(err.Error(), "db") {
		t.Errorf("an unknown service must be refused and the services listed; got %v", err)
	}
}

func TestStackFlagRecordRefusesBase(t *testing.T) {
	rec, _ := buildStackScenario(t)

	got, err := stackFlagRecord("navexa", "")
	if err != nil || got != nil {
		t.Errorf("stackFlagRecord(\"\") = %v, %v; want no stack and no error", got, err)
	}
	got, err = stackFlagRecord("navexa", rec.Name)
	if err != nil || got == nil || got.Name != rec.Name {
		t.Fatalf("stackFlagRecord(%q) = %v, %v; want the stack record", rec.Name, got, err)
	}
	for _, name := range []string{"base", "nope"} {
		if _, err := stackFlagRecord("navexa", name); err == nil {
			t.Errorf("stackFlagRecord(%q) must refuse a name that is not a stack", name)
		}
	}
}

func TestEnvWhichRefusesStackBase(t *testing.T) {
	buildStackScenario(t)
	useWorkspaceKey(t, "navexa")

	c := &cobra.Command{Use: "which"}
	c.Flags().String("stack", "base", "")
	c.Flags().String("service", "backend", "")
	c.Flags().Bool("shadowed", false, "")
	c.Flags().Bool("reveal", false, "")
	if err := runEnvWhich(c, nil); err == nil {
		t.Error("env which --stack base must refuse: base is not a stack")
	}
}
