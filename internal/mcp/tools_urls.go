package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/socialviolation/devstack/internal/panel"
	"github.com/socialviolation/devstack/internal/workspace"
)

func registerURLsTool(mcpServer *server.MCPServer, ws *workspace.Workspace) {
	tool := mcp.NewTool("urls",
		mcp.WithDescription("Report the address that reaches each service of this workspace from another machine on the tailnet.\n"+
			"A request crosses three hops: the tailnet address, then the local proxy, then the service. devstack reads the map of every hop and joins them. Give the address at the front of the chain to a person. That person opens it in a browser, on any machine of the tailnet.\n"+
			"A service with no address is not published. It still runs, and it still answers on its own port on this machine. Pass all=true to list these services too. To reach a service with no address from one named machine, use the tunnel tool instead.\n"+
			"With all=true, the tool also lists a service that is stopped or disabled. Read the state of each row before you report it.\n"+
			"Use this tool after 'stack up', when somebody asks where to see the work.\n"+
			"The CLI equivalent is `devstack urls`. `devstack panel` shows the same addresses in a terminal, and opens one in a browser."),
		mcp.WithString("stack",
			mcp.Description("Report the addresses of one feature stack. With no stack, the tool reports base and every stack of this workspace.")),
		mcp.WithString("workspace",
			mcp.Description("Report another workspace of this machine, by name. With no workspace, the tool reports the workspace of this repository. Pass 'all' for every workspace of this machine.")),
		mcp.WithBoolean("all",
			mcp.Description("If true, list every service, published or not. The default is false.")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	mcpServer.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		stackName := req.GetString("stack", "")
		all := req.GetBool("all", false)

		snap := panel.Take(ctx)
		wsName := ""
		if ws != nil {
			wsName = ws.Name
		}
		switch asked := req.GetString("workspace", ""); asked {
		case "":
		case "all":
			wsName = ""
		default:
			wsName = asked
		}

		rows := []map[string]any{}
		for _, w := range snap.Workspaces {
			if wsName != "" && w.Name != wsName {
				continue
			}
			if stackName == "" {
				rows = append(rows, urlRowsOf(w.Name, "", w.Base, all)...)
			}
			for _, st := range w.Stacks {
				if stackName != "" && st.Name != stackName {
					continue
				}
				rows = append(rows, urlRowsOf(w.Name, st.Name, st.Services, all)...)
			}
		}

		if len(rows) == 0 && !all {
			return mcp.NewToolResultText("No service here has a tailnet address. The machine publishes none, or the services are down. Call this tool again with all=true to list the services and their local ports."), nil
		}

		out, err := json.MarshalIndent(map[string]any{"services": rows, "note": snap.Note}, "", "  ")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("can not write the answer: %v", err)), nil
		}
		return mcp.NewToolResultText(string(out)), nil
	})
}

func urlRowsOf(wsName, stackName string, services []panel.Service, all bool) []map[string]any {
	rows := []map[string]any{}
	for _, svc := range services {
		if !all && len(svc.URLs) == 0 {
			continue
		}
		row := map[string]any{
			"workspace": wsName,
			"service":   svc.Name,
			"state":     svc.State,
		}
		if stackName != "" {
			row["stack"] = stackName
		}
		if len(svc.Ports) > 0 {
			row["ports"] = svc.Ports
		}
		if len(svc.URLs) > 0 {
			row["urls"] = svc.URLs
		}
		rows = append(rows, row)
	}
	return rows
}
