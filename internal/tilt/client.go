package tilt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Client wraps the Tilt HTTP API and CLI.
type Client struct {
	host         string
	port         int
	portResolver func() int // if set, called on each request instead of using port
}

// NewClient creates a new Tilt client targeting the given host and port.
func NewClient(host string, port int) *Client {
	return &Client{host: host, port: port}
}

// NewDynamicClient creates a Tilt client that resolves its port on each request
// by calling portResolver. This allows the client to adapt when Tilt restarts
// and the port changes in the workspace registry.
func NewDynamicClient(host string, portResolver func() int) *Client {
	return &Client{host: host, portResolver: portResolver}
}

// currentPort returns the port to use for the current request.
// If a portResolver is set, it is called to get the latest port.
func (c *Client) currentPort() int {
	if c.portResolver != nil {
		return c.portResolver()
	}
	return c.port
}

// TiltView represents the top-level response from /api/view.
type TiltView struct {
	UiResources []UIResource `json:"uiResources"`
}

// UIResource represents a single resource managed by Tilt.
type UIResource struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Status UIResourceStatus `json:"status"`
}

// EndpointLink represents a named URL exposed by a service.
type EndpointLink struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

// DisableStatus holds the enabled/disabled state of a resource.
type DisableStatus struct {
	State string `json:"state"` // "Enabled" or "Disabled"
}

// UIResourceStatus holds the runtime and build state of a resource.
type UIResourceStatus struct {
	BuildHistory   []BuildRecord  `json:"buildHistory"`
	RuntimeStatus  string         `json:"runtimeStatus"`
	UpdateStatus   string         `json:"updateStatus"`
	EndpointLinks  []EndpointLink `json:"endpointLinks"`
	LastDeployTime *string        `json:"lastDeployTime"`
	DisableStatus  *DisableStatus `json:"disableStatus"`
}

// BuildRecord represents a single build attempt.
type BuildRecord struct {
	Error      string `json:"error"`
	StartTime  string `json:"startTime"`
	FinishTime string `json:"finishTime"`
}

// aliases maps human-friendly service names to their Tilt resource names.
// Populated via SetAliases at startup; empty by default (exact match only).
var aliases = map[string]string{}

// SetAliases replaces the alias map with the provided map.
// Keys are lower-cased human-friendly names; values are exact Tilt resource names.
func SetAliases(m map[string]string) {
	aliases = m
}

// isRealService returns true if the resource name is a real service (not a
// pseudo-resource like "(Tiltfile)" that Tilt injects during startup).
func isRealService(name string) bool {
	return len(name) == 0 || name[0] != '('
}

// GetView fetches the current dev daemon state from the HTTP API.
// Returns a descriptive error if the daemon is not running.
// Pseudo-resources (names starting with "(") are filtered from the result.
func (c *Client) GetView() (*TiltView, error) {
	view, err := c.rawView()
	if err != nil {
		return nil, err
	}

	// Filter out pseudo-resources like (Tiltfile)
	real := view.UiResources[:0]
	for _, r := range view.UiResources {
		if isRealService(r.Metadata.Name) {
			real = append(real, r)
		}
	}
	view.UiResources = real

	return view, nil
}

// rawView fetches the daemon state including pseudo-resources such as
// (Tiltfile), which carry the daemon's own reload state.
func (c *Client) rawView() (*TiltView, error) {
	url := fmt.Sprintf("http://%s:%d/api/view", c.host, c.currentPort())

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("the dev daemon is not running. To start it, run: `devstack workspace up`")
	}
	defer resp.Body.Close()

	var view TiltView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		return nil, fmt.Errorf("can not decode the Tilt API response: %w", err)
	}
	return &view, nil
}

// tiltfileResource is the pseudo-resource Tilt files its own config reloads under.
const tiltfileResource = "(Tiltfile)"

// WaitForTiltfileReload blocks until the daemon has finished loading a Tiltfile
// written at or after since, so a caller that just regenerated the file can
// trigger services knowing they will run the new spec. It returns an error if
// the reload has not landed within timeout — the daemon is still running, so
// callers generally warn rather than abort.
func (c *Client) WaitForTiltfileReload(since time.Time, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		view, err := c.rawView()
		if err != nil {
			return err
		}
		for _, r := range view.UiResources {
			if r.Metadata.Name != tiltfileResource || len(r.Status.BuildHistory) == 0 {
				continue
			}
			finish, perr := time.Parse(time.RFC3339Nano, r.Status.BuildHistory[0].FinishTime)
			if perr == nil && !finish.Before(since) {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the daemon did not reload the regenerated Tiltfile in %s", timeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// RunCLI runs a tilt CLI command with a 30-second timeout.
// Automatically appends --port so the CLI targets the same Tilt instance
// as the HTTP client. Returns combined stdout+stderr output.
func (c *Client) RunCLI(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args = append(args, "--port", strconv.Itoa(c.currentPort()))
	cmd := exec.CommandContext(ctx, "tilt", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runningCmdPrefix is the line Tilt writes before it starts a command. It is
// the only mark in a process log that separates one attempt from the one before.
const runningCmdPrefix = "Running cmd: "

// maxFailureLines caps a failure report. A reader needs the command and the
// output that follows it, not the whole log.
const maxFailureLines = 8

// FailureReason tells why a resource is in error, as the lines to show. Tilt
// keeps a build record for a build that fails, but a command that fails while it
// serves leaves no build record, so the status alone reports an error and no
// reason. The process log holds both: the command Tilt ran, and what that
// command printed before it stopped.
func (c *Client) FailureReason(r UIResource) []string {
	if len(r.Status.BuildHistory) > 0 && r.Status.BuildHistory[0].Error != "" {
		return []string{r.Status.BuildHistory[0].Error}
	}
	out, err := c.RunCLI("logs", "--tail=50", r.Metadata.Name)
	if err != nil && strings.TrimSpace(out) == "" {
		return nil
	}
	return lastAttempt(out)
}

// lastAttempt keeps the last command in a process log and every line after it.
// A log with no command line falls back to its last lines.
func lastAttempt(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, " \t\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		lines = append(lines, l)
	}
	start := 0
	for i, l := range lines {
		if strings.HasPrefix(l, runningCmdPrefix) {
			start = i
		}
	}
	lines = lines[start:]
	if len(lines) > maxFailureLines {
		kept := []string{lines[0]}
		lines = append(kept, lines[len(lines)-(maxFailureLines-1):]...)
	}
	return lines
}

// ResolveService resolves a human-friendly name to an exact Tilt resource name.
// It checks for an exact match first, then falls back to the alias map.
func ResolveService(name string, view *TiltView) (string, error) {
	// Exact match
	for _, r := range view.UiResources {
		if r.Metadata.Name == name {
			return name, nil
		}
	}

	// Alias match (case-insensitive)
	lower := strings.ToLower(name)
	if canonical, ok := aliases[lower]; ok {
		// Verify the canonical name exists in the view
		for _, r := range view.UiResources {
			if r.Metadata.Name == canonical {
				return canonical, nil
			}
		}
	}

	// Build list of available names for the error message (real services only)
	names := make([]string, 0, len(view.UiResources))
	for _, r := range view.UiResources {
		if isRealService(r.Metadata.Name) {
			names = append(names, r.Metadata.Name)
		}
	}
	return "", fmt.Errorf("devstack can not find the service %q. Available: %s", name, strings.Join(names, ", "))
}
