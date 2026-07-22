package config

import "testing"

func TestActiveEnvName(t *testing.T) {
	tests := []struct {
		name     string
		wsEnv    string
		svcEnv   string
		stackEnv string
		want     string
	}{
		{"stack wins", "dev", "staging", "prod", "prod"},
		{"service beats workspace", "dev", "staging", "", "staging"},
		{"workspace fallback", "dev", "", "", "dev"},
		{"all empty", "", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ActiveEnvName(tc.wsEnv, tc.svcEnv, tc.stackEnv); got != tc.want {
				t.Fatalf("ActiveEnvName(%q, %q, %q) = %q, want %q", tc.wsEnv, tc.svcEnv, tc.stackEnv, got, tc.want)
			}
		})
	}
}
