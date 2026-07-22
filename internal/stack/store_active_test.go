package stack

import "testing"

func TestAnyActiveRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	recs := []Record{
		{Name: "import-review", Base: "navexa", Active: true},
		{Name: "spike", Base: "navexa"},
	}
	if err := saveStore("navexa", recs); err != nil {
		t.Fatalf("saveStore: %v", err)
	}

	active, err := AnyActive("navexa")
	if err != nil {
		t.Fatalf("AnyActive: %v", err)
	}
	if !active {
		t.Fatal("AnyActive = false, want true with an active stack")
	}
}

func TestAnyActiveNoneActive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := saveStore("navexa", []Record{{Name: "spike", Base: "navexa"}}); err != nil {
		t.Fatalf("saveStore: %v", err)
	}

	active, err := AnyActive("navexa")
	if err != nil {
		t.Fatalf("AnyActive: %v", err)
	}
	if active {
		t.Fatal("AnyActive = true, want false with no active stacks")
	}
}

func TestDeactivateAllLeavesNoActiveRecord(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	recs := []Record{
		{Name: "import-review", Base: "navexa", Active: true},
		{Name: "spike", Base: "navexa"},
	}
	if err := saveStore("navexa", recs); err != nil {
		t.Fatalf("saveStore: %v", err)
	}

	deactivated, err := DeactivateAll("navexa")
	if err != nil {
		t.Fatalf("DeactivateAll: %v", err)
	}
	if len(deactivated) != 1 || deactivated[0] != "import-review" {
		t.Fatalf("deactivated = %v, want [import-review]", deactivated)
	}

	got, err := LoadStore("navexa")
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	for _, r := range got {
		if r.Active {
			t.Fatalf("stack %q still Active after DeactivateAll", r.Name)
		}
	}

	active, err := AnyActive("navexa")
	if err != nil {
		t.Fatalf("AnyActive: %v", err)
	}
	if active {
		t.Fatal("AnyActive = true after DeactivateAll, want false")
	}
}

func TestDeactivateAllNoActiveIsNoop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := saveStore("navexa", []Record{{Name: "spike", Base: "navexa"}}); err != nil {
		t.Fatalf("saveStore: %v", err)
	}

	deactivated, err := DeactivateAll("navexa")
	if err != nil {
		t.Fatalf("DeactivateAll: %v", err)
	}
	if len(deactivated) != 0 {
		t.Fatalf("deactivated = %v, want none", deactivated)
	}
}
