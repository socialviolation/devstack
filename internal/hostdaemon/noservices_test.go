package hostdaemon

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

func viewServer(t *testing.T, tiltfile string) *tilt.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"uiSession":{"status":{"tiltfileKey":%q}},"uiResources":[]}`, tiltfile)
	}))
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	return tilt.NewClient("127.0.0.1", port)
}

func TestNoServicesReasonNamesTheTiltfileTheDaemonRuns(t *testing.T) {
	withHostDir(t)
	got := NoServicesReason(viewServer(t, "/elsewhere/Tiltfile"), "feat")
	host := filepath.Join(workspace.HostTiltDir(), "Tiltfile")
	if !strings.Contains(got, "The daemon runs /elsewhere/Tiltfile, not "+host+". Restart the host daemon") {
		t.Errorf("NoServicesReason() = %q, want it to name both Tiltfiles", got)
	}
	if strings.Contains(got, "stack rm") {
		t.Errorf("NoServicesReason() = %q, want no destructive advice", got)
	}
}

func TestNoServicesReasonPointsAtTheLogsWhenTheTiltfileMatches(t *testing.T) {
	withHostDir(t)
	host := filepath.Join(workspace.HostTiltDir(), "Tiltfile")
	got := NoServicesReason(viewServer(t, host), "feat")
	if !strings.Contains(got, "Read why: journalctl --user -u devstack-tilt") {
		t.Errorf("NoServicesReason() = %q, want the daemon logs", got)
	}
	if strings.Contains(got, "stack rm") {
		t.Errorf("NoServicesReason() = %q, want no destructive advice", got)
	}
}
