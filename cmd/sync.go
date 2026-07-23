package cmd

import (
	"fmt"

	"github.com/socialviolation/devstack/internal/hostdaemon"
	"github.com/socialviolation/devstack/internal/tilt"
)

// syncHostTiltfile regenerates the host Tiltfile from the manifests, waits for
// the running daemon to load it, and prints what changed — so restarting or
// starting a service applies manifest edits without a separate
// 'devstack workspace generate'.
func syncHostTiltfile(client *tilt.Client) {
	for _, note := range hostdaemon.SyncAndReload(client) {
		fmt.Println(note)
	}
}
