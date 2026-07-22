package stack

import "testing"

func TestSetEnvRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := saveStore("navexa", []Record{{Name: "import-review", Base: "navexa"}}); err != nil {
		t.Fatalf("saveStore: %v", err)
	}

	if err := SetEnv("navexa", "import-review", "staging"); err != nil {
		t.Fatalf("SetEnv: %v", err)
	}

	rec, err := FindStack("navexa", "import-review")
	if err != nil {
		t.Fatalf("FindStack: %v", err)
	}
	if rec.Env != "staging" {
		t.Errorf("Env = %q, want staging", rec.Env)
	}
}

func TestSetEnvUnknownStack(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SetEnv("navexa", "nope", "staging"); err == nil {
		t.Fatal("expected an error for an unknown stack")
	}
}
