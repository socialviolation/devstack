package mcp

import (
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/stack"
)

func TestFormatResyncNamesStackAddAndMigrate(t *testing.T) {
	out, err := formatResync(&stack.ResyncResult{
		Name:       "feat",
		Branch:     "feat",
		Outside:    []stack.OutsideRepo{{Repo: "billing", Services: []string{"billing", "ledger"}}},
		Missing:    []stack.OverlayMember{{Service: "billing", Reason: stack.ReasonNeeds, Chain: []string{"api", "billing"}}},
		Migrations: []string{"api"},
		Notes:      []string{"devstack can not ask origin of web for branch feat"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Branch feat also changes billing, which this stack does not run. Run `stack add billing ledger`.",
		"api needs billing. This stack does not run billing. Run `stack add billing`.",
		"The new commits change migrations in api. A migration changes the shared database for every stack. Run `migrate` when you are ready.",
		"note: devstack can not ask origin of web for branch feat",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
