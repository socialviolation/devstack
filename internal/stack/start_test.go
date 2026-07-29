package stack

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/socialviolation/devstack/internal/tilt"
)

// stubDaemon serves /api/view, reporting no resources until the caller has read
// it appearAfter times — the daemon that has been handed a new Tiltfile but has
// not read it yet.
func stubDaemon(t *testing.T, appearAfter int32, names ...string) *tilt.Client {
	t.Helper()
	var reads int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&reads, 1)
		body := `{"uiResources":[]}`
		if n > appearAfter {
			res := ""
			for i, name := range names {
				if i > 0 {
					res += ","
				}
				res += fmt.Sprintf(`{"metadata":{"name":%q},"status":{}}`, name)
			}
			body = `{"uiResources":[` + res + `]}`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	host, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split stub address: %v", err)
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("stub port: %v", err)
	}
	return tilt.NewClient(host, p)
}

func TestWaitForResourcesWaitsForTheDaemonToLoadThem(t *testing.T) {
	client := stubDaemon(t, 2, "navexa:api:agent")

	view, err := waitForResources(client, []string{"navexa:api:agent"})
	if err != nil {
		t.Fatalf("waitForResources: %v", err)
	}
	if len(view.UiResources) != 1 {
		t.Fatalf("returned a view with %d resources, want the one it waited for", len(view.UiResources))
	}
}

func TestWaitForResourcesGivesUpAtTheDeadline(t *testing.T) {
	prev := resourceWait
	resourceWait = 200 * time.Millisecond
	t.Cleanup(func() { resourceWait = prev })

	client := stubDaemon(t, 1<<30, "navexa:api:agent")

	start := time.Now()
	view, err := waitForResources(client, []string{"navexa:api:agent"})
	if err != nil {
		t.Fatalf("waitForResources: %v", err)
	}
	if elapsed := time.Since(start); elapsed < resourceWait {
		t.Errorf("gave up after %s, before the %s deadline — a slow daemon reload would look like a broken stack", elapsed, resourceWait)
	}
	if len(view.UiResources) != 0 {
		t.Errorf("expected the last view read, which has none of the wanted resources")
	}
}
