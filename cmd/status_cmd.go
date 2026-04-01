package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"devstack/internal/tilt"
	"devstack/internal/workspace"
)


// runStatusAll shows a summary table of all registered workspaces.
func runStatusAll() error {
	workspaces, err := workspace.All()
	if err != nil {
		return fmt.Errorf("failed to load workspace registry: %w", err)
	}

	if len(workspaces) == 0 {
		fmt.Println("No workspaces registered. Run: devstack register")
		return nil
	}

	type wsResult struct {
		ws       workspace.Workspace
		status   string
		services string
	}

	results := make([]wsResult, len(workspaces))
	var wg sync.WaitGroup

	for i, ws := range workspaces {
		wg.Add(1)
		go func(idx int, w workspace.Workspace) {
			defer wg.Done()

			r := wsResult{ws: w}

			// Check PID file
			pidFile := workspace.PIDFile(w.Name)
			pidAlive := false
			if pidData, err := os.ReadFile(pidFile); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(pidData))); err == nil {
					pidAlive = isProcessAlive(pid)
				}
			}

			// Probe Tilt HTTP API
			apiURL := fmt.Sprintf("http://localhost:%d/api/view", w.TiltPort)
			client := &http.Client{Timeout: 2 * time.Second}
			resp, err := client.Get(apiURL)
			if err != nil || resp.StatusCode != http.StatusOK {
				if pidAlive {
					r.status = "starting"
				} else {
					r.status = "stopped"
				}
				r.services = "-"
				results[idx] = r
				return
			}
			defer resp.Body.Close()

			// Tilt is reachable — parse service counts
			tiltClient := tilt.NewClient("localhost", w.TiltPort)
			view, err := tiltClient.GetView()
			if err != nil {
				r.status = "running"
				r.services = "unknown"
				results[idx] = r
				return
			}

			r.status = "running"
			total := len(view.UiResources)
			active := 0
			for _, res := range view.UiResources {
				if res.Status.RuntimeStatus == "ok" {
					active++
				}
			}
			if total == 0 {
				r.services = "0 services"
			} else {
				r.services = fmt.Sprintf("%d services (%d active)", total, active)
			}
			results[idx] = r
		}(i, ws)
	}

	wg.Wait()

	fmt.Printf("%-16s %-36s %-8s %-12s %s\n", "WORKSPACE", "PATH", "PORT", "STATUS", "SERVICES")
	fmt.Println(strings.Repeat("-", 88))
	for _, r := range results {
		path := r.ws.Path
		if len(path) > 34 {
			path = "..." + path[len(path)-31:]
		}
		fmt.Printf("%-16s %-36s %-8d %-12s %s\n",
			r.ws.Name,
			path,
			r.ws.TiltPort,
			r.status,
			r.services,
		)
	}

	return nil
}

// serviceStatus derives a human-readable status from Tilt resource state.
func serviceStatus(r tilt.UIResource) string {
	if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
		return "disabled"
	}
	switch r.Status.RuntimeStatus {
	case "ok":
		return "running"
	case "pending":
		return "starting"
	case "error":
		return "error"
	}
	if r.Status.UpdateStatus == "running" {
		return "building"
	}
	if r.Status.UpdateStatus == "error" {
		return "error"
	}
	return "idle"
}

// extractPorts turns endpoint URLs into compact ":PORT" strings.
func extractPorts(links []tilt.EndpointLink) string {
	if len(links) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(links))
	for _, ep := range links {
		u, err := url.Parse(ep.URL)
		if err == nil && u.Port() != "" {
			parts = append(parts, ":"+u.Port())
		} else {
			parts = append(parts, ep.URL)
		}
	}
	return strings.Join(parts, " ")
}

// formatUptime returns a human-readable duration since an RFC3339 timestamp.
// Returns "-" if the timestamp is empty, null, or in the future.
func formatUptime(ts *string) string {
	if ts == nil || *ts == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339Nano, *ts)
	if err != nil {
		return "-"
	}
	d := time.Since(t)
	if d < 0 {
		return "-"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// shortDir shortens a path by replacing the home directory prefix with ~.
func shortDir(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

