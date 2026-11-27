package cmd

import (
	"context"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/observability"
)

type logQueryRecorder struct {
	got []observability.LogQuery
}

func (r *logQueryRecorder) QueryTraces(context.Context, observability.TraceQuery) ([][]observability.Span, error) {
	return nil, nil
}

func (r *logQueryRecorder) QueryLogs(_ context.Context, q observability.LogQuery) ([]observability.LogEntry, error) {
	r.got = append(r.got, q)
	return nil, nil
}

func (r *logQueryRecorder) ListVariants(context.Context, observability.ServiceQuery) ([]observability.ServiceVariant, error) {
	return nil, nil
}

func TestOtelLogsFiltersOnTheStackFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	rec := &logQueryRecorder{}
	prev := queryBackend
	queryBackend = func(*cobra.Command) (observability.Backend, error) { return rec, nil }
	t.Cleanup(func() { queryBackend = prev })

	cases := map[string]string{
		"":       "",
		"all":    "",
		"shared": observability.SharedStack,
		"base":   observability.SharedStack,
		"feat":   "feat",
	}
	for flag, want := range cases {
		rec.got = nil
		c := otelSubcommand(t, "")
		c.Flags().String("stack", "", "")
		c.Flags().String("service", "", "")
		c.Flags().String("trace", "", "")
		c.Flags().Duration("since", time.Minute, "")
		c.Flags().Int("limit", 5, "")
		if err := c.Flags().Set("stack", flag); err != nil {
			t.Fatal(err)
		}
		if err := c.Flags().Set("service", "api"); err != nil {
			t.Fatal(err)
		}
		captureStdout(t, func() {
			if err := runOtelLogs(c, nil); err != nil {
				t.Fatalf("otel logs --stack %q: %v", flag, err)
			}
		})
		if len(rec.got) != 1 || rec.got[0].Stack != want {
			t.Errorf("otel logs --stack %q sent %+v, want Stack %q", flag, rec.got, want)
		}
	}
}
