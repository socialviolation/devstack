package cmd

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"devstack/internal/tilt"
	"devstack/internal/workspace"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show status of registered workspaces or services within a workspace",
	Long: `Show system-wide status across all registered workspaces, or per-service
detail for a specific workspace.

Without --workspace: prints a summary table of all registered workspaces.
With --workspace: prints a per-service detail table for that workspace.`,
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
	statusCmd.Flags().String("workspace", "", "Workspace name or path — show per-service detail for this workspace")
}

func runStatus(cmd *cobra.Command, args []string) error {
	wsFlag, _ := cmd.Flags().GetString("workspace")

	if wsFlag != "" {
		return runStatusWorkspace(wsFlag)
	}

	return runStatusAll()
}

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

// runStatusWorkspace shows per-service detail for a specific workspace.
func runStatusWorkspace(wsFlag string) error {
	ws, err := resolveWorkspace(wsFlag)
	if err != nil {
		return err
	}

	tiltClient := tilt.NewClient("localhost", ws.TiltPort)
	view, err := tiltClient.GetView()
	if err != nil {
		fmt.Printf("Workspace '%s': Tilt is not running on port %d.\n", ws.Name, ws.TiltPort)
		fmt.Printf("Start it with: devstack start --workspace=%s\n", ws.Name)
		return nil
	}

	if len(view.UiResources) == 0 {
		fmt.Printf("Workspace '%s': Tilt is running but no services are loaded yet.\n", ws.Name)
		return nil
	}

	fmt.Printf("Workspace: %s (%s)\n\n", ws.Name, ws.Path)
	fmt.Printf("%-24s %-14s %-14s %s\n", "SERVICE", "BUILD", "RUNTIME", "ERROR")
	fmt.Println(strings.Repeat("-", 80))

	for _, r := range view.UiResources {
		buildStatus := r.Status.UpdateStatus
		if buildStatus == "" {
			buildStatus = "unknown"
		}
		runtimeStatus := r.Status.RuntimeStatus
		if runtimeStatus == "" {
			runtimeStatus = "unknown"
		}
		lastError := ""
		if len(r.Status.BuildHistory) > 0 {
			lastError = r.Status.BuildHistory[0].Error
		}
		if len(lastError) > 60 {
			lastError = lastError[:57] + "..."
		}
		fmt.Printf("%-24s %-14s %-14s %s\n", r.Metadata.Name, buildStatus, runtimeStatus, lastError)
	}

	return nil
}
