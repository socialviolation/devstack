package cmd

import (
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
	for _, want := range []string{"is down", "would run with", "devstack stack up feat"} {
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

// --stack base is how every other command names base, and this one used to
// error because base has no stack record. Base runs from the replica, so the
// replica is what it reads: the checkout is only the template, and it can sit
// on any branch with any half-finished work in it.
func TestStackConfigReadsBaseFromTheReplica(t *testing.T) {
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
		if err := runStackConfig(stackConfigCmdFor(t, "base"), []string{"backend"}); err != nil {
			t.Fatalf("runStackConfig --stack base: %v", err)
		}
	})

	for _, want := range []string{"in base", replica.Root(ws), "PORT"} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "in stack") {
		t.Errorf("base is not a stack, and the report must not call it one:\n%s", out)
	}
	// base serves the manifest port. A stack's copy of backend gets an allocated
	// one, so this is also the proof that the report is base's and not a stack's.
	if !strings.Contains(out, "8080") {
		t.Errorf("base's PORT must come from the manifest (8080), got:\n%s", out)
	}
	if !strings.Contains(out, "Base is down") {
		t.Errorf("a base that does not run must be reported as down, got:\n%s", out)
	}
}

// Before the replica is built there is nothing for base to run, and an agent
// told only "not found" would go looking for a stack of that name.
func TestStackConfigSaysWhenBaseHasNoReplica(t *testing.T) {
	buildStackScenario(t)
	useWorkspaceKey(t, "navexa")

	err := runStackConfig(stackConfigCmdFor(t, "base"), []string{"backend"})
	if err == nil {
		t.Fatal("--stack base without a replica must fail")
	}
	for _, want := range []string{"has not built the replica", "devstack workspace up"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must state %q; got %v", want, err)
		}
	}
}

// A service that base does not have must name base's services, not read as a
// missing stack.
func TestStackConfigRefusesAnUnknownServiceOnBase(t *testing.T) {
	buildStackScenario(t)
	useWorkspaceKey(t, "navexa")
	ws, err := workspace.FindByName("navexa")
	if err != nil {
		t.Fatalf("FindByName: %v", err)
	}
	if _, err := replica.Ensure(ws); err != nil {
		t.Fatalf("replica.Ensure: %v", err)
	}

	err = runStackConfig(stackConfigCmdFor(t, "base"), []string{"nope"})
	if err == nil || !strings.Contains(err.Error(), "backend") || !strings.Contains(err.Error(), "frontend") {
		t.Errorf("an unknown service must be refused and base's services listed; got %v", err)
	}
}

// "base" is how every other command names base, and base has no stack record.
// status and env which looked the name up in the stack store, so the one word
// that names base everywhere else failed on these two alone. Both now resolve
// the flag here, and "base" must resolve the way an absent flag resolves.
func TestStackFlagRecordReadsBaseAsNoStack(t *testing.T) {
	rec, _ := buildStackScenario(t)

	for _, name := range []string{"", "base"} {
		got, err := stackFlagRecord("navexa", name)
		if err != nil {
			t.Errorf("stackFlagRecord(%q): %v", name, err)
		}
		if got != nil {
			t.Errorf("stackFlagRecord(%q) = %q, want no stack record: base is not a stack", name, got.Name)
		}
	}

	got, err := stackFlagRecord("navexa", rec.Name)
	if err != nil || got == nil || got.Name != rec.Name {
		t.Fatalf("stackFlagRecord(%q) = %v, %v; want the stack record", rec.Name, got, err)
	}
	if _, err := stackFlagRecord("navexa", "nope"); err == nil {
		t.Error("a name that is neither base nor a stack must be refused")
	}
}

func TestEnvWhichAcceptsBaseAsTheStackName(t *testing.T) {
	buildStackScenario(t)
	useWorkspaceKey(t, "navexa")

	envWhichCmdFor := func(stackName string) *cobra.Command {
		c := &cobra.Command{Use: "which"}
		c.Flags().String("stack", "", "")
		c.Flags().String("service", "backend", "")
		c.Flags().Bool("shadowed", false, "")
		c.Flags().Bool("reveal", false, "")
		if stackName != "" {
			if err := c.Flags().Set("stack", stackName); err != nil {
				t.Fatalf("set --stack: %v", err)
			}
		}
		return c
	}

	plain := captureStdout(t, func() {
		if err := runEnvWhich(envWhichCmdFor(""), nil); err != nil {
			t.Fatalf("runEnvWhich with no --stack: %v", err)
		}
	})
	named := captureStdout(t, func() {
		if err := runEnvWhich(envWhichCmdFor("base"), nil); err != nil {
			t.Fatalf("runEnvWhich --stack base: %v", err)
		}
	})
	if plain != named {
		t.Errorf("--stack base must report what no --stack reports.\nno flag:\n%s\n--stack base:\n%s", plain, named)
	}
}
