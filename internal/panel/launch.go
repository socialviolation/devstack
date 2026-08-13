package panel

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// paneLabel is the name herdr gives the panel's pane. The manifest sets it, and
// it is how the launcher finds a panel that is already open.
const paneLabel = "devstack"

type herdrContext struct {
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceCwd  string `json:"workspace_cwd"`
	TabID         string `json:"tab_id"`
	FocusedPaneID string `json:"focused_pane_id"`
	FocusedCwd    string `json:"focused_pane_cwd"`
}

type herdrPane struct {
	PaneID        string `json:"pane_id"`
	TabID         string `json:"tab_id"`
	WorkspaceID   string `json:"workspace_id"`
	Label         string `json:"label"`
	Focused       bool   `json:"focused"`
	Cwd           string `json:"cwd"`
	ForegroundCwd string `json:"foreground_cwd"`
}

type paneList struct {
	Result struct {
		Panes []herdrPane `json:"panes"`
	} `json:"result"`
}

// launchCwd is the directory the panel opens in, and it decides which workspace
// the panel shows. herdr runs a plugin action from the plugin's own directory,
// which is nobody's workspace, so the cwd has to come from the pane the reader
// pressed the key in.
func launchCwd(ctx herdrContext, panes []herdrPane) string {
	if ctx.FocusedCwd != "" {
		return ctx.FocusedCwd
	}
	for _, p := range panes {
		if !p.Focused && p.PaneID != ctx.FocusedPaneID {
			continue
		}
		if p.ForegroundCwd != "" {
			return p.ForegroundCwd
		}
		if p.Cwd != "" {
			return p.Cwd
		}
	}
	return ctx.WorkspaceCwd
}

// LaunchCwd reports the directory a launcher must open the panel in.
func LaunchCwd(paneListJSON io.Reader, contextJSON string) string {
	var ctx herdrContext
	_ = json.Unmarshal([]byte(contextJSON), &ctx)

	var doc paneList
	if data, err := io.ReadAll(paneListJSON); err == nil {
		_ = json.Unmarshal(data, &doc)
	}
	return launchCwd(ctx, doc.Result.Panes)
}

// RunLaunchCwd writes that directory for the launcher script to read.
func RunLaunchCwd() error {
	cwd := LaunchCwd(os.Stdin, os.Getenv("HERDR_PLUGIN_CONTEXT_JSON"))
	_, err := fmt.Fprintln(os.Stdout, cwd)
	return err
}

func LaunchDecision(paneListJSON io.Reader, contextJSON string, inTab bool) string {
	var ctx herdrContext
	_ = json.Unmarshal([]byte(contextJSON), &ctx)

	var doc paneList
	data, err := io.ReadAll(paneListJSON)
	readOK := err == nil && json.Unmarshal(data, &doc) == nil

	open := "OPEN " + launchCwd(ctx, doc.Result.Panes)
	if !readOK {
		return open
	}

	var elsewhere string
	for _, p := range doc.Result.Panes {
		if p.Label != paneLabel {
			continue
		}
		if p.TabID == ctx.TabID {
			if p.Focused || p.PaneID == ctx.FocusedPaneID {
				return "CLOSE " + p.PaneID
			}
			return "FOCUS " + p.PaneID
		}
		if inTab && p.WorkspaceID == ctx.WorkspaceID && elsewhere == "" {
			elsewhere = "SWITCHTAB " + p.TabID
		}
	}
	if elsewhere != "" {
		return elsewhere
	}
	return open
}

func RunLaunchDecision(inTab bool) error {
	decision := LaunchDecision(os.Stdin, os.Getenv("HERDR_PLUGIN_CONTEXT_JSON"), inTab)
	_, err := fmt.Fprintln(os.Stdout, strings.TrimSpace(decision))
	return err
}
