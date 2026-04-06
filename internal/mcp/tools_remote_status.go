package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"devstack/internal/observability"
)

// registerRemoteStatusTool registers a "status" tool for remote environments.
// Instead of querying Tilt, it derives service health from recent trace activity.
func registerRemoteStatusTool(mcpServer *server.MCPServer, backend observability.Backend, envName, envURL string) {
	tool := mcp.NewTool("status",
		mcp.WithDescription(fmt.Sprintf(
			"Show health of services in the **%s** environment based on recent trace activity (last 5 minutes). "+
				"Reports request count, error rate, and approximate P99 latency per service. "+
				"READ-ONLY — this is a remote environment, service control is not available.",
			envName,
		)),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		since := 5 * time.Minute
		services, err := backend.ListServices(queryCtx, since)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to list services: %v", err)), nil
		}
		if len(services) == 0 {
			return mcp.NewToolResultText(fmt.Sprintf(
				"Environment: %s (remote)\nNo services with trace activity in the last 5 minutes.", envName,
			)), nil
		}

		// Fetch stats per service concurrently
		type serviceResult struct {
			service string
			spans   []observability.Span
			err     error
		}
		resultsCh := make(chan serviceResult, len(services))
		for _, svc := range services {
			svc := svc
			go func() {
				traces, err := backend.QueryTraces(queryCtx, observability.TraceQuery{
					Service: svc,
					Since:   since,
					Limit:   200,
				})
				var spans []observability.Span
				for _, t := range traces {
					spans = append(spans, t...)
				}
				resultsCh <- serviceResult{svc, spans, err}
			}()
		}

		type stats struct {
			requests int
			errors   int
			p99ns    int64
			lastSeen time.Time
		}
		svcStats := map[string]stats{}
		for range services {
			r := <-resultsCh
			if r.err != nil {
				continue
			}
			var durations []int64
			s := stats{}
			for _, span := range r.spans {
				s.requests++
				if strings.Contains(strings.ToLower(span.Status), "error") {
					s.errors++
				}
				durations = append(durations, span.DurationNano)
				if span.StartTime.After(s.lastSeen) {
					s.lastSeen = span.StartTime
				}
			}
			if len(durations) > 0 {
				sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
				p99idx := int(float64(len(durations)) * 0.99)
				if p99idx >= len(durations) {
					p99idx = len(durations) - 1
				}
				s.p99ns = durations[p99idx]
			}
			svcStats[r.service] = s
		}

		// Format output
		var sb strings.Builder
		fmt.Fprintf(&sb, "Environment: %s (remote)\n", envName)
		fmt.Fprintf(&sb, "Observability: signoz @ %s\n", envURL)
		fmt.Fprintf(&sb, "Window: last 5 minutes\n\n")

		w := tabwriter.NewWriter(&sb, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "SERVICE\tREQUESTS\tERRORS\tERROR%\tP99\tLAST SEEN")
		fmt.Fprintln(w, "-------\t--------\t------\t------\t---\t---------")
		sort.Strings(services)
		for _, svc := range services {
			s := svcStats[svc]
			errPct := 0.0
			if s.requests > 0 {
				errPct = float64(s.errors) / float64(s.requests) * 100
			}
			p99 := fmt.Sprintf("%dms", s.p99ns/1e6)
			if s.p99ns > 1e9 {
				p99 = fmt.Sprintf("%.1fs", float64(s.p99ns)/1e9)
			}
			ago := "-"
			if !s.lastSeen.IsZero() {
				ago = time.Since(s.lastSeen).Round(time.Second).String() + " ago"
			}
			fmt.Fprintf(w, "%s\t%d\t%d\t%.1f%%\t%s\t%s\n", svc, s.requests, s.errors, errPct, p99, ago)
		}
		w.Flush()

		return mcp.NewToolResultText(sb.String()), nil
	})
}
