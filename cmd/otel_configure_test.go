package cmd

import (
	"testing"

	"github.com/socialviolation/devstack/internal/config"
)

// Credentials are accepted but must never reach the manifest — they are routed
// to the machine-local registry instead, so configuring an upstream that needs
// an api key stays possible.
func TestCredentialKeysAreRoutedAwayFromTheManifest(t *testing.T) {
	for _, key := range []string{"api_key", "auth_token", "password"} {
		got, err := parseOtelSetFlags([]string{"upstream=https://otel.example.com:4318", key + "=synthetic-value"})
		if err != nil {
			t.Fatalf("parseOtelSetFlags(%q) = %v, want it accepted", key, err)
		}
		if got[key] != "synthetic-value" {
			t.Errorf("%q was dropped instead of parsed: %v", key, got)
		}
		if !config.IsCredentialKey(key) {
			t.Errorf("IsCredentialKey(%q) = false, so it would be written to the committed manifest", key)
		}
	}
}

// A name that merely labels a credential is not itself secret; refusing these
// made it impossible to configure a real upstream.
func TestNamingKeysAreNotTreatedAsCredentials(t *testing.T) {
	for _, key := range []string{"api_key_header", "sharedAccessKeyName", "accountName"} {
		if config.IsCredentialKey(key) {
			t.Errorf("IsCredentialKey(%q) = true, but it carries a name, not a secret", key)
		}
	}
}

func TestParseOtelSetFlagsAcceptsPluginSettings(t *testing.T) {
	got, err := parseOtelSetFlags([]string{"upstream=https://otel.example.com:4318", "protocol=grpc"})
	if err != nil {
		t.Fatalf("parseOtelSetFlags: %v", err)
	}
	if got["upstream"] != "https://otel.example.com:4318" || got["protocol"] != "grpc" {
		t.Errorf("parsed = %#v", got)
	}
}
