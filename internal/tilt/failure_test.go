package tilt

import (
	"strings"
	"testing"
)

// The log of a run command that fails at once. Tilt files no build record for
// it, so this text is the only report of what went wrong.
const miseTrustLog = `[pva:dev] $ echo PETE-V-IS-UP && sleep 600
PETE-V-IS-UP

1 File Changed: [Tiltfile]

CLI Trigger
Running cmd: mise run pva:dev
mise ERROR error parsing config file: /dev/.devstack-base/southfoundry/ptvmcp/mise.toml
mise ERROR Config files in /dev/.devstack-base/southfoundry/ptvmcp/mise.toml are not trusted.
Trust them with ` + "`mise trust`" + `.
`

func TestLastAttemptKeepsTheCommandAndWhatFollowsIt(t *testing.T) {
	got := lastAttempt(miseTrustLog)
	if len(got) == 0 {
		t.Fatal("lastAttempt() = none, want the last command and its output")
	}
	if got[0] != "Running cmd: mise run pva:dev" {
		t.Errorf("first line = %q, want the command that ran", got[0])
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "are not trusted") {
		t.Errorf("lastAttempt() = %q, want the reason the command stopped", joined)
	}
	if strings.Contains(joined, "PETE-V-IS-UP") {
		t.Errorf("lastAttempt() = %q, want nothing from the attempt before", joined)
	}
}

func TestLastAttemptDropsBlankLines(t *testing.T) {
	for _, l := range lastAttempt(miseTrustLog) {
		if strings.TrimSpace(l) == "" {
			t.Fatalf("lastAttempt() = %q, want no blank line", lastAttempt(miseTrustLog))
		}
	}
}

// A long attempt keeps its command line. Without it the report says why a copy
// stopped and never says what stopped.
func TestLastAttemptKeepsTheCommandOfALongAttempt(t *testing.T) {
	var b strings.Builder
	b.WriteString("Running cmd: go run .\n")
	for i := 0; i < maxFailureLines*3; i++ {
		b.WriteString("line\n")
	}
	b.WriteString("panic: boom\n")

	got := lastAttempt(b.String())
	if len(got) > maxFailureLines {
		t.Errorf("lastAttempt() gave %d lines, want at most %d", len(got), maxFailureLines)
	}
	if got[0] != "Running cmd: go run ." {
		t.Errorf("first line = %q, want the command that ran", got[0])
	}
	if got[len(got)-1] != "panic: boom" {
		t.Errorf("last line = %q, want the last line of the log", got[len(got)-1])
	}
}

func TestLastAttemptFallsBackToTheEndOfALogWithNoCommand(t *testing.T) {
	got := lastAttempt("first\nsecond\nthird\n")
	if strings.Join(got, ",") != "first,second,third" {
		t.Errorf("lastAttempt() = %v, want every line of a log with no command line", got)
	}
}

// A build that fails already carries its error, so FailureReason must report
// that and never shell out for a log.
func TestFailureReasonPrefersTheBuildError(t *testing.T) {
	r := UIResource{}
	r.Metadata.Name = "navexa:backend"
	r.Status.BuildHistory = []BuildRecord{{Error: "exit status 2"}}

	got := NewClient("localhost", 1).FailureReason(r)
	if len(got) != 1 || got[0] != "exit status 2" {
		t.Errorf("FailureReason() = %v, want the build error", got)
	}
}
