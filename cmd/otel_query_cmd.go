package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/observability"
	"github.com/socialviolation/devstack/internal/otel"
)

var otelTracesCmd = &cobra.Command{
	Use:   "traces [trace-id]",
	Short: "Query traces from whatever backend this workspace uses",
	Long: `Query traces without naming a backend, URL or credential — devstack resolves
the workspace's configured backend and talks to it for you.

With no argument it lists recent root spans. With a trace ID it prints that
trace's full span tree.

Examples:
  devstack otel traces
  devstack otel traces --service=api --since=15m
  devstack otel traces --stack=feat-x
  devstack otel traces 5b8efff798038103d269b633813fc60c`,
	Args: cobra.MaximumNArgs(1),
	RunE: runOtelTraces,
}

var otelLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Query collected logs from whatever backend this workspace uses",
	Long: `Query OTEL logs without naming a backend, URL or credential.

Examples:
  devstack otel logs --service=api
  devstack otel logs --trace=5b8efff798038103d269b633813fc60c`,
	RunE: runOtelLogs,
}

var otelServicesCmd = &cobra.Command{
	Use:   "services",
	Short: "List the service variants reporting telemetry (base, each stack, each env)",
	Long: `List every distinguishable variant reporting telemetry, so it is obvious
which one to query. The same service runs many times over — in the base
workspace, in each feature stack, under each config env — and all of them report
to the one shared backend.

Where a service reports itself under a different name than devstack knows it by,
both are shown; the devstack name is the one --service filters on.`,
	RunE: runOtelServices,
}

func init() {
	otelCmd.AddCommand(otelTracesCmd)
	otelCmd.AddCommand(otelLogsCmd)
	otelCmd.AddCommand(otelServicesCmd)

	for _, sub := range []*cobra.Command{otelTracesCmd, otelLogsCmd, otelServicesCmd} {
		sub.Flags().String("workspace", "", "Workspace name or path (default: auto-detect from current directory)")
		sub.Flags().Duration("since", 15*time.Minute, "Lookback window")
	}

	otelTracesCmd.Flags().String("service", "", "Only traces from this service (default: the service you are standing in; \"all\" for the whole workspace)")
	otelTracesCmd.Flags().String("stack", "", "Only traces from this stack (\"base\" for the base workspace)")
	otelTracesCmd.Flags().String("attr", "", "Only traces with this attribute (format: key=value)")
	otelTracesCmd.Flags().Int("limit", 10, "Maximum traces to return")

	otelLogsCmd.Flags().String("service", "", "Only logs from this service (default: the service you are standing in; \"all\" for the whole workspace)")
	otelLogsCmd.Flags().String("trace", "", "Only logs correlated with this trace ID")
	otelLogsCmd.Flags().Int("limit", 50, "Maximum log lines to return")
}

// queryBackend resolves the workspace's backend with no input from the caller.
// The returned backend only ever answers with this workspace's telemetry.
func queryBackend(cmd *cobra.Command) (observability.Backend, error) {
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return nil, err
	}
	return otel.BackendFor(ws)
}

// resolveQueryService decides which service a query covers: the flag when given,
// otherwise the service whose repo the caller is standing in. Passing "all" asks
// for the whole workspace. Narrowing by default keeps a query run inside one
// repo from dumping every service's telemetry.
func resolveQueryService(cmd *cobra.Command) string {
	service, _ := cmd.Flags().GetString("service")
	if service == "all" {
		return ""
	}
	if service != "" {
		return service
	}

	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	identity, err := config.ResolveIdentity(cwd)
	if err != nil || identity.ServiceName == "" {
		return ""
	}
	color.New(color.Faint).Fprintf(os.Stderr, "(scoped to service %q — pass --service=all for the whole workspace)\n", identity.ServiceName)
	return identity.ServiceName
}

func runOtelTraces(cmd *cobra.Command, args []string) error {
	backend, err := queryBackend(cmd)
	if err != nil {
		return err
	}

	since, _ := cmd.Flags().GetDuration("since")
	limit, _ := cmd.Flags().GetInt("limit")
	stack, _ := cmd.Flags().GetString("stack")
	attr, _ := cmd.Flags().GetString("attr")

	// A trace lookup or attribute search deliberately spans services — only the
	// recent-traces listing narrows to the service the caller is standing in.
	service, _ := cmd.Flags().GetString("service")
	if len(args) == 0 && attr == "" {
		service = resolveQueryService(cmd)
	}

	req := observability.TraceQuery{
		Service: service,
		Stack:   stack,
		Since:   since,
		Limit:   limit,
	}
	if len(args) > 0 {
		req.TraceID = args[0]
	}
	if attr != "" {
		key, value, found := strings.Cut(attr, "=")
		if !found {
			return fmt.Errorf("--attr must be key=value, got %q", attr)
		}
		req.Attribute, req.Value = key, value
	}

	traces, err := backend.QueryTraces(context.Background(), req)
	if err != nil {
		return err
	}
	if len(traces) == 0 {
		fmt.Printf("No traces in the last %s.\n", since)
		return nil
	}

	for _, spans := range traces {
		printTrace(spans, req.TraceID != "")
	}
	return nil
}

// printTrace renders one trace: a single summary line, or the full span tree
// when a specific trace was asked for.
func printTrace(spans []observability.Span, full bool) {
	if len(spans) == 0 {
		return
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].StartTime.Before(spans[j].StartTime) })
	root := spans[0]

	fmt.Printf("%s  %s %s (%s, %d spans)\n",
		color.New(color.Faint).Sprint(root.TraceID),
		root.Service, root.Operation,
		formatDuration(root.DurationNano), len(spans))

	if !full {
		return
	}

	children := map[string][]observability.Span{}
	for _, s := range spans[1:] {
		children[s.ParentSpanID] = append(children[s.ParentSpanID], s)
	}
	printSpanTree(root, children, 1, map[string]bool{root.SpanID: true})
	fmt.Println()
}

// printSpanTree walks the span tree. seen guards the walk: parent links come
// from stored data, and one pointing back up the tree would otherwise recurse
// until the process dies.
func printSpanTree(span observability.Span, children map[string][]observability.Span, depth int, seen map[string]bool) {
	for _, child := range children[span.SpanID] {
		if seen[child.SpanID] {
			continue
		}
		seen[child.SpanID] = true
		status := ""
		if isErrorStatus(child.Status) {
			status = color.New(color.FgRed).Sprint(" " + child.Status)
		}
		fmt.Printf("%s%s %s %s%s\n",
			strings.Repeat("  ", depth), color.New(color.Faint).Sprint("└"),
			child.Service+"/"+child.Operation,
			formatDuration(child.DurationNano), status)
		printSpanTree(child, children, depth+1, seen)
	}
}

func isErrorStatus(status string) bool {
	s := strings.ToLower(status)
	return strings.Contains(s, "error")
}

func formatDuration(nanos int64) string {
	return time.Duration(nanos).Round(time.Microsecond).String()
}

func runOtelLogs(cmd *cobra.Command, args []string) error {
	backend, err := queryBackend(cmd)
	if err != nil {
		return err
	}

	since, _ := cmd.Flags().GetDuration("since")
	limit, _ := cmd.Flags().GetInt("limit")
	service := resolveQueryService(cmd)
	traceID, _ := cmd.Flags().GetString("trace")

	entries, err := backend.QueryLogs(context.Background(), observability.LogQuery{
		TraceID: traceID,
		Service: service,
		Since:   since,
		Limit:   limit,
	})
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Printf("No logs in the last %s.\n", since)
		return nil
	}

	for _, e := range entries {
		severity := e.Severity
		if isErrorStatus(severity) {
			severity = color.New(color.FgRed).Sprint(severity)
		}
		fmt.Printf("%s %-8s %-16s %s\n",
			color.New(color.Faint).Sprint(e.Timestamp.Format("15:04:05.000")),
			severity, e.Service, e.Body)
	}
	return nil
}

func runOtelServices(cmd *cobra.Command, args []string) error {
	backend, err := queryBackend(cmd)
	if err != nil {
		return err
	}

	since, _ := cmd.Flags().GetDuration("since")
	variants, err := backend.ListVariants(context.Background(), observability.ServiceQuery{Since: since})
	if err != nil {
		return err
	}
	if len(variants) == 0 {
		fmt.Printf("No services have reported telemetry in the last %s.\n", since)
		return nil
	}

	faint := color.New(color.Faint)
	for _, v := range variants {
		name := v.Service
		if name == "" {
			name = v.Devstack
		}
		fmt.Print(name)
		// The name devstack filters on is what a caller has to pass, so show it
		// whenever the service reports itself as something else.
		if v.Devstack != "" && v.Devstack != v.Service {
			faint.Printf("  (devstack: %s)", v.Devstack)
		}
		var qualifiers []string
		if v.Stack != "" {
			qualifiers = append(qualifiers, "stack="+v.Stack)
		}
		if v.Env != "" {
			qualifiers = append(qualifiers, "env="+v.Env)
		}
		if len(qualifiers) > 0 {
			faint.Printf("  %s", strings.Join(qualifiers, " "))
		}
		fmt.Println()
	}
	return nil
}
