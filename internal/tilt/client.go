package tilt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Client wraps the Tilt HTTP API and CLI.
type Client struct {
	host string
	port int
}

// NewClient creates a new Tilt client targeting the given host and port.
func NewClient(host string, port int) *Client {
	return &Client{host: host, port: port}
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

// GetView fetches the current Tilt state from the HTTP API.
// Returns a descriptive error if Tilt is not running.
// Pseudo-resources (names starting with "(") are filtered from the result.
func (c *Client) GetView() (*TiltView, error) {
	url := fmt.Sprintf("http://%s:%d/api/view", c.host, c.port)

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("Tilt is not running. Start it with: `tilt up`")
	}
	defer resp.Body.Close()

	var view TiltView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		return nil, fmt.Errorf("failed to decode Tilt API response: %w", err)
	}

	// Filter out pseudo-resources like (Tiltfile)
	real := view.UiResources[:0]
	for _, r := range view.UiResources {
		if isRealService(r.Metadata.Name) {
			real = append(real, r)
		}
	}
	view.UiResources = real

	return &view, nil
}

// RunCLI runs a tilt CLI command with a 30-second timeout.
// Returns combined stdout+stderr output.
func (c *Client) RunCLI(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tilt", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
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
	return "", fmt.Errorf("service %q not found. Available: %s", name, strings.Join(names, ", "))
}
