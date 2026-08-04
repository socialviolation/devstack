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
	"github.com/socialviolation/devstack/internal/stack"
)

var otelTracesCmd = &cobra.Command{
	Use:   "traces [trace-id]",
	Short: "Query traces from whatever backend this workspace uses",
	Long: `Query traces without a backend name, a URL or a credential. devstack resolves
the configured backend of this workspace, and talks to it for you.

With no argument, this command lists the recent root spans. With a trace ID, it
prints the full span tree of that trace.

With no --stack, the query covers base alone. Pass --stack <name> for one stack.
Pass --stack all for base and every stack together. The investigate MCP tool has
the same default, so the two surfaces cover the same copies.

Examples:
  devstack otel traces
  devstack otel traces --service=api --since=15m
  devstack otel traces --stack=feat-x
  devstack otel traces --stack=all
  devstack otel traces 5b8efff798038103d269b633813fc60c`,
	Args: cobra.MaximumNArgs(1),
	RunE: runOtelTraces,
}

var otelLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Query collected logs from whatever backend this workspace uses",
	Long: `Query OTEL logs without a backend name, a URL or a credential.

This command has no --stack flag. To read the logs of one execution, pass
--trace <id>.

Examples:
  devstack otel logs --service=api
  devstack otel logs --trace=5b8efff798038103d269b633813fc60c`,
	RunE: runOtelLogs,
}

var otelServicesCmd = &cobra.Command{
	Use:   "services",
	Short: "List the copies that report telemetry (base, each stack, each environment)",
	Long: `List every copy that reports telemetry, so it is obvious which one to query. The
same service runs many times over — in the base workspace, in each feature stack,
and under each environment. All of them report to the one shared backend.

A service can report itself under a name that differs from the name devstack
knows it by. devstack then shows both names. The --service flag filters on the
name that devstack uses.`,
	RunE: runOtelServices,
}

func init() {
	otelCmd.AddCommand(otelTracesCmd)
	otelCmd.AddCommand(otelLogsCmd)
	otelCmd.AddCommand(otelServicesCmd)

	for _, sub := range []*cobra.Command{otelTracesCmd, otelLogsCmd, otelServicesCmd} {
		sub.Flags().String("workspace", "", "Workspace name or path. Default: the workspace of the current directory (env: DEVSTACK_WORKSPACE)")
		sub.Flags().Duration("since", 15*time.Minute, "How far back to look")
	}

	otelTracesCmd.Flags().String("service", "", "Only traces from this service. Default: the service you stand in. Pass \"all\" for the whole workspace")
	otelTracesCmd.Flags().String("stack", "", "Whose traces to search. "+observability.StackScopeDesc)
	otelTracesCmd.Flags().String("attr", "", "Only traces with this attribute, as key=value")
	otelTracesCmd.Flags().Int("limit", 10, "Maximum traces to return")

	otelLogsCmd.Flags().String("service", "", "Only logs from this service. Default: the service you stand in. Pass \"all\" for the whole workspace")
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

// resolveStackFlag maps --stack to the query's stack filter, and rejects a value
// naming no stack of this workspace.
//
// An absent flag searches base alone, which is what the investigate MCP tool
// does with an absent stack. The two used to default opposite ways, so a human
// and an agent comparing notes on the same unqualified query disagreed about
// what it had covered.
//
// A telemetry query used to accept any string and answer "No traces", so
// --stack with a typo, the wrong case, or the '<base>--<name>' identity that
// telemetry itself prints all returned a clean bill of health for a stack that
// was never queried. `devstack status --stack` had always errored on the same
// input; one flag behaving two ways is how a misspelling becomes "no errors".
//
// The name is canonicalised because lookup is case-insensitive while the stored
// telemetry attribute is not: --stack Perf resolved to the perf record and then
// filtered on "Perf", which matches nothing.
func resolveStackFlag(cmd *cobra.Command, name string) (string, error) {
	filter := observability.ResolveStackFilter(name)
	if filter == "" || filter == "base" {
		return filter, nil
	}
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return filter, nil
	}
	rec, err := stack.Resolve(ws.Name, filter)
	if err != nil {
		return "", err
	}
	return rec.Name, nil
}

// explainEmptyTraceResult says why nothing came back when the reason is knowable
// from local state. An empty result reads as "healthy" unless something says
// otherwise, and the commonest cause is that the copy asked about is not running.
func explainEmptyTraceResult(cmd *cobra.Command, stackName string) {
	faint := color.New(color.Faint)
	faint.Println("Empty means nothing matched — not that the service is healthy.")

	if stackName == "" {
		faint.Println("  This query covered every copy, base and stacks. To narrow it, pass --stack <name>, or --stack base.")
		return
	}
	if stackName == "base" {
		faint.Println("  This query covered base alone, which is the default. To include the stacks, pass --stack all, or --stack <name> for one.")
		return
	}
	ws, err := resolveOtelWorkspace(cmd)
	if err != nil {
		return
	}
	rec, err := stack.Resolve(ws.Name, stackName)
	if err != nil || rec == nil {
		return
	}
	if !rec.Active {
		faint.Printf("  Stack %q is down. Nothing of it runs, so nothing emits telemetry. To bring it up, run: devstack stack up %s\n", stackName, stackName)
		return
	}
	faint.Printf("  Stack %q is up. Make sure that the copy runs, and that it emits: devstack status --stack %s · devstack otel status\n", stackName, stackName)
}

func runOtelTraces(cmd *cobra.Command, args []string) error {
	backend, err := queryBackend(cmd)
	if err != nil {
		return err
	}

	since, _ := cmd.Flags().GetDuration("since")
	limit, _ := cmd.Flags().GetInt("limit")
	stackName, _ := cmd.Flags().GetString("stack")
	attr, _ := cmd.Flags().GetString("attr")

	stackName, err = resolveStackFlag(cmd, stackName)
	if err != nil {
		return err
	}

	// A trace lookup or attribute search deliberately spans services — only the
	// recent-traces listing narrows to the service the caller is standing in.
	service, _ := cmd.Flags().GetString("service")
	if len(args) == 0 && attr == "" {
		service = resolveQueryService(cmd)
	}

	req := observability.TraceQuery{
		Service: service,
		Stack:   stackName,
		Since:   since,
		Limit:   limit,
	}
	if len(args) > 0 {
		req.TraceID = args[0]
	}
	if attr != "" {
		key, value, found := strings.Cut(attr, "=")
		if !found {
			return fmt.Errorf("--attr must be key=value. devstack read %q", attr)
		}
		req.Attribute, req.Value = key, value
	}

	traces, err := backend.QueryTraces(context.Background(), req)
	if err != nil {
		return err
	}
	if len(traces) == 0 {
		fmt.Printf("No traces in the last %s.\n", since)
		explainEmptyTraceResult(cmd, stackName)
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
		fmt.Printf("No service reported telemetry in the last %s.\n", since)
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
