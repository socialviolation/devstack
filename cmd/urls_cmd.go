package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/socialviolation/devstack/internal/panel"
)

var urlsCmd = &cobra.Command{
	Use:   "urls",
	Short: "Show the tailnet address that reaches each service",
	Long: `Show the address that reaches each service from another machine.

A request crosses three hops: the tailnet address, then the local proxy, then
the service. devstack reads the map of every hop, and prints the address at the
front of the chain. Open that address on your phone, or give it to another
person on the tailnet.

A service with no address is not published. It still runs, and it still answers
on its own port on this machine.

  devstack urls                      the addresses of this workspace
  devstack urls --stack orbit        the addresses of one feature stack
  devstack urls --all                every service, published or not
  devstack urls --json               the same list, for a script or an agent`,
	RunE: runURLs,
}

func init() {
	rootCmd.AddCommand(urlsCmd)
	urlsCmd.Flags().String("stack", "", "Show the addresses of one feature stack")
	urlsCmd.Flags().Bool("all", false, "List every service, published or not")
	urlsCmd.Flags().Bool("json", false, "Write the list as JSON")
}

// urlRow is one line of the JSON output. It is a contract with the scripts that
// read it, so the keys stay as they are.
type urlRow struct {
	Workspace string   `json:"workspace"`
	Stack     string   `json:"stack,omitempty"`
	Service   string   `json:"service"`
	State     string   `json:"state"`
	Ports     []int    `json:"ports,omitempty"`
	URLs      []string `json:"urls,omitempty"`
}

func runURLs(cmd *cobra.Command, args []string) error {
	stackName, _ := cmd.Flags().GetString("stack")
	all, _ := cmd.Flags().GetBool("all")
	asJSON, _ := cmd.Flags().GetBool("json")

	snap := panel.Take(context.Background())

	wsName := ""
	if ws, err := resolveWorkspace(viper.GetString("workspace")); err == nil {
		wsName = ws.Name
	}

	rows := urlRows(snap, wsName, stackName, all)
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	printURLRows(rows, snap.Note, all)
	return nil
}

func urlRows(snap panel.Snapshot, wsName, stackName string, all bool) []urlRow {
	rows := []urlRow{}
	for _, ws := range snap.Workspaces {
		if wsName != "" && ws.Name != wsName {
			continue
		}
		if stackName == "" {
			for _, svc := range ws.Base {
				if all || len(svc.URLs) > 0 {
					rows = append(rows, urlRow{ws.Name, "", svc.Name, svc.State, svc.Ports, svc.URLs})
				}
			}
		}
		for _, st := range ws.Stacks {
			if stackName != "" && st.Name != stackName {
				continue
			}
			for _, svc := range st.Services {
				if all || len(svc.URLs) > 0 {
					rows = append(rows, urlRow{ws.Name, st.Name, svc.Name, svc.State, svc.Ports, svc.URLs})
				}
			}
		}
	}
	return rows
}

func printURLRows(rows []urlRow, note string, all bool) {
	faint := color.New(color.Faint)
	if note != "" {
		faint.Printf("  %s\n\n", note)
	}
	if len(rows) == 0 {
		if all {
			fmt.Println("No services here.")
			return
		}
		fmt.Println("No service of this workspace has a tailnet address.")
		faint.Println("  devstack urls --all lists every service.")
		return
	}

	width := 0
	for _, r := range rows {
		if n := len(r.Service); n > width {
			width = n
		}
	}

	group := ""
	for _, r := range rows {
		label := r.Workspace
		if r.Stack != "" {
			label += "  stack " + r.Stack
		}
		if label != group {
			group = label
			color.New(color.Bold).Printf("%s\n", label)
		}
		fmt.Printf("  %-*s  ", width, r.Service)
		switch {
		case len(r.URLs) > 0:
			fmt.Print(r.URLs[0])
			for _, extra := range r.URLs[1:] {
				fmt.Printf("  %s", extra)
			}
		case len(r.Ports) > 0:
			faint.Printf("no address  ·  localhost:%d", r.Ports[0])
		default:
			faint.Print("no address")
		}
		if r.State != "running" {
			faint.Printf("  (%s)", r.State)
		}
		fmt.Println()
	}
}
