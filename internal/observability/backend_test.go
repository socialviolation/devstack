package observability

import "testing"

func TestResolveStackFilterTakesBaseAsTheSharedSet(t *testing.T) {
	cases := map[string]string{"": "", "all": "", "shared": "shared", "Shared": "shared", "base": "shared", "perf": "perf"}
	for in, want := range cases {
		if got := ResolveStackFilter(in); got != want {
			t.Errorf("ResolveStackFilter(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsSharedStack(t *testing.T) {
	for v, want := range map[string]bool{"": true, "shared": true, "base": true, "perf": false} {
		if got := IsSharedStack(v); got != want {
			t.Errorf("IsSharedStack(%q) = %v, want %v", v, got, want)
		}
	}
}
