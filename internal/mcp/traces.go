// Trace presentation for MCP tool output. Everything here works on the
// backend-agnostic observability types, so the shapes rendered are the same
// whichever telemetry backend the workspace is configured with; each backend's
// wire protocol lives in its own client package.
package mcp

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type traceSpan struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Service      string
	Operation    string
	StartNs      int64 // Unix nanoseconds
	DurationNs   int64
	StatusCode   string // "ok", "error", "unset"
	StatusMsg    string
	Attrs        map[string]string
}

type traceRecord struct {
	TraceID string
	Spans   []traceSpan
}

// --- HTTP helpers ---

type logEntry struct {
	Timestamp int64 // Unix nanoseconds
	Body      string
	Service   string
	Severity  string
	TraceID   string
	SpanID    string
}

func rootSpan(r *traceRecord) *traceSpan {
	spanIDs := make(map[string]bool)
	for _, s := range r.Spans {
		spanIDs[s.SpanID] = true
	}
	for i := range r.Spans {
		s := &r.Spans[i]
		if s.ParentSpanID == "" || !spanIDs[s.ParentSpanID] {
			return s
		}
	}
	if len(r.Spans) > 0 {
		return &r.Spans[0]
	}
	return nil
}

// spanHasError returns true if a span has an error status code.
func spanHasError(s *traceSpan) bool {
	return strings.ToLower(s.StatusCode) == "error" || s.StatusCode == "2"
}

// formatDuration formats a nanosecond duration as "1234.5ms" or "12.3s".
func formatDuration(ns int64) string {
	ms := float64(ns) / 1e6
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", ms/1000)
	}
	return fmt.Sprintf("%.1fms", ms)
}

// truncate truncates s to at most n runes.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// --- formatOptions controls verbosity of formatters ---

type formatOptions struct {
	Verbose bool
}

// --- Formatters ---

func renderSpanLine(sp *traceSpan) string {
	svc := truncate(sp.Service, 12)
	op := truncate(sp.Operation, 35)
	dur := formatDuration(sp.DurationNs)
	status := "ok"
	if spanHasError(sp) {
		status = "ERROR"
	}
	return fmt.Sprintf("%-12s %-35s %-10s %s", svc, op, dur, status)
}

// formatSpanTree formats the full span tree for a single traceRecord using proper ASCII connectors.
func formatSpanTree(r *traceRecord, opts formatOptions) string {
	if len(r.Spans) == 0 {
		return "No spans found.\n"
	}

	businessKeys := map[string]bool{
		"portfolio.id": true,
		"user.id":      true,
		"process.id":   true,
		"file.type":    true,
		"provider.id":  true,
		"trade.count":  true,
		"batch.number": true,
	}
	httpKeys := map[string]bool{
		"http.method":      true,
		"http.route":       true,
		"http.status_code": true,
	}
	errorAttrKeys := map[string]bool{
		"error.message":     true,
		"exception.message": true,
		"exception.type":    true,
	}

	// Build span ID set and children map.
	spanIDSet := make(map[string]bool, len(r.Spans))
	for _, sp := range r.Spans {
		spanIDSet[sp.SpanID] = true
	}

	children := make(map[string][]traceSpan)
	var roots []traceSpan
	for _, sp := range r.Spans {
		if sp.ParentSpanID == "" || !spanIDSet[sp.ParentSpanID] {
			roots = append(roots, sp)
		} else {
			children[sp.ParentSpanID] = append(children[sp.ParentSpanID], sp)
		}
	}

	// Sort each children list and roots by StartNs.
	sort.Slice(roots, func(i, j int) bool { return roots[i].StartNs < roots[j].StartNs })
	for k := range children {
		sort.Slice(children[k], func(i, j int) bool { return children[k][i].StartNs < children[k][j].StartNs })
	}

	var sb strings.Builder

	// renderNode recursively renders a span and its children.
	// prefix is the string prepended to children continuation lines.
	// connector is "├─ " or "└─ " (or "" for root).
	var renderNode func(sp traceSpan, linePrefix string, connector string)
	renderNode = func(sp traceSpan, linePrefix string, connector string) {
		isErr := spanHasError(&sp)
		isRoot := sp.ParentSpanID == "" || !spanIDSet[sp.ParentSpanID]

		// Span line
		fmt.Fprintf(&sb, "%s%s%s\n", linePrefix, connector, renderSpanLine(&sp))

		// Determine continuation prefix for attrs/children under this node.
		var childLinePrefix string
		if connector == "" {
			// root node — no extra indentation from connector
			childLinePrefix = linePrefix
		} else if connector == "└─ " {
			childLinePrefix = linePrefix + "   "
		} else {
			childLinePrefix = linePrefix + "│  "
		}

		// Print attrs under span (indented by childLinePrefix + "  ").
		attrPrefix := childLinePrefix + "  "
		if opts.Verbose {
			// Verbose: print all business + HTTP attrs on every span.
			printedKeys := make(map[string]bool)
			for k, v := range sp.Attrs {
				if businessKeys[k] || httpKeys[k] {
					fmt.Fprintf(&sb, "%s%s: %s\n", attrPrefix, k, v)
					printedKeys[k] = true
				}
			}
			if isErr {
				for k, v := range sp.Attrs {
					if errorAttrKeys[k] && !printedKeys[k] {
						fmt.Fprintf(&sb, "%s%s: %s\n", attrPrefix, k, v)
					}
				}
				if sp.StatusMsg != "" {
					fmt.Fprintf(&sb, "%sstatus.message: %s\n", attrPrefix, sp.StatusMsg)
				}
			}
		} else {
			// Compact: only print error attrs when span has error; skip root (shown in header).
			if isErr && !isRoot {
				for k, v := range sp.Attrs {
					if errorAttrKeys[k] {
						fmt.Fprintf(&sb, "%s%s: %s\n", attrPrefix, k, v)
					}
				}
				if sp.StatusMsg != "" {
					fmt.Fprintf(&sb, "%sstatus.message: %s\n", attrPrefix, sp.StatusMsg)
				}
			}
		}

		// Render children.
		kids := children[sp.SpanID]
		for i, kid := range kids {
			isLast := i == len(kids)-1
			conn := "├─ "
			if isLast {
				conn = "└─ "
			}
			renderNode(kid, childLinePrefix, conn)
		}
	}

	for _, root := range roots {
		renderNode(root, "", "")
	}

	return sb.String()
}

// formatExecutionView formats a unified execution view: compact header + span tree + logs.
func formatExecutionView(record *traceRecord, logs []logEntry, opts formatOptions) string {
	var sb strings.Builder

	root := rootSpan(record)

	businessKeys := map[string]bool{
		"portfolio.id": true,
		"user.id":      true,
		"process.id":   true,
		"file.type":    true,
		"provider.id":  true,
		"trade.count":  true,
		"batch.number": true,
	}
	httpKeys := map[string]bool{
		"http.method":      true,
		"http.route":       true,
		"http.status_code": true,
	}

	// Compact header line: Trace: <id>  <timestamp>  <duration>  <status>
	traceIDShort := record.TraceID
	if len(traceIDShort) > 8 {
		traceIDShort = traceIDShort[:8]
	}

	if root != nil {
		ts := time.Unix(0, root.StartNs).Local().Format("2006-01-02 15:04:05")
		dur := formatDuration(root.DurationNs)
		status := "ok"
		if spanHasError(root) {
			status = "ERROR"
		}
		fmt.Fprintf(&sb, "Trace: %s  %s  %s  %s\n", traceIDShort, ts, dur, status)

		// Services + span count
		seen := make(map[string]bool)
		var services []string
		for _, sp := range record.Spans {
			if sp.Service != "" && !seen[sp.Service] {
				seen[sp.Service] = true
				services = append(services, sp.Service)
			}
		}
		sort.Strings(services)
		fmt.Fprintf(&sb, "Services: %s  Spans: %d\n", strings.Join(services, ", "), len(record.Spans))

		// Business attrs from root span
		var businessParts []string
		for k, v := range root.Attrs {
			if businessKeys[k] {
				businessParts = append(businessParts, fmt.Sprintf("%s: %s", k, v))
			}
		}
		sort.Strings(businessParts)

		var httpParts []string
		for k, v := range root.Attrs {
			if httpKeys[k] {
				httpParts = append(httpParts, fmt.Sprintf("%s: %s", k, v))
			}
		}
		sort.Strings(httpParts)

		allHeaderAttrs := append(businessParts, httpParts...)
		if len(allHeaderAttrs) > 0 {
			fmt.Fprintf(&sb, "%s\n", strings.Join(allHeaderAttrs, "  "))
		}
	} else {
		fmt.Fprintf(&sb, "Trace: %s\n", traceIDShort)
	}

	sb.WriteString("\n")

	// Span tree (no section header)
	sb.WriteString(formatSpanTree(record, opts))

	// Correlated logs section — only ERROR/WARN unless verbose.
	logLimit := 30
	if opts.Verbose {
		logLimit = 200
	}

	var filteredLogs []logEntry
	if opts.Verbose {
		filteredLogs = logs
		if len(filteredLogs) > logLimit {
			filteredLogs = filteredLogs[:logLimit]
		}
	} else {
		for _, log := range logs {
			sev := strings.ToUpper(log.Severity)
			if sev == "ERROR" || sev == "WARN" || sev == "WARNING" {
				filteredLogs = append(filteredLogs, log)
			}
		}
		if len(filteredLogs) > logLimit {
			filteredLogs = filteredLogs[:logLimit]
		}
	}

	if len(filteredLogs) > 0 {
		sb.WriteString("\nLOGS:\n")
		currentSvc := ""
		for _, log := range filteredLogs {
			if log.Service != currentSvc {
				fmt.Fprintf(&sb, "--- %s ---\n", log.Service)
				currentSvc = log.Service
			}
			ts := ""
			if log.Timestamp > 0 {
				ts = time.Unix(0, log.Timestamp).Local().Format("15:04:05.000") + " "
			}
			sev := log.Severity
			if sev == "" {
				sev = "INFO"
			}
			body := log.Body
			if len(body) > 300 {
				body = body[:297] + "..."
			}
			fmt.Fprintf(&sb, "  %s%s %s\n", ts, sev, body)
		}
		omitted := len(logs) - len(filteredLogs)
		if omitted > 0 {
			fmt.Fprintf(&sb, "(%d more lines omitted)\n", omitted)
		}
	}

	return sb.String()
}
