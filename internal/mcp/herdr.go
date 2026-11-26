package mcp

import (
	"context"

	"github.com/socialviolation/devstack/internal/herdr"
	"github.com/socialviolation/devstack/internal/mcp/auth"
	"github.com/socialviolation/devstack/internal/workspace"
)

func bindPane(ctx context.Context, ws *workspace.Workspace, name string) {
	if auth.OverHTTP(ctx) {
		return
	}
	herdr.Bind(ws.Name, name)
}
