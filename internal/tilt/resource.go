package tilt

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
)

func SplitResourceName(name, prefix string) (svc, stackNS string, ok bool) {
	if !strings.HasPrefix(name, prefix) {
		return "", "", false
	}
	rest := name[len(prefix):]
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		return rest[:i], rest[i+1:], true
	}
	return rest, "", true
}

func ResourceMap(resources []UIResource, wsName, stackName string) map[string]UIResource {
	prefix := wsName + ":"
	out := make(map[string]UIResource, len(resources))
	for _, r := range resources {
		svc, ns, ok := SplitResourceName(r.Metadata.Name, prefix)
		if !ok || ns != stackName {
			continue
		}
		out[svc] = r
	}
	return out
}

func ServiceStatus(r UIResource) string {
	if r.Status.DisableStatus != nil && r.Status.DisableStatus.State == "Disabled" {
		return "disabled"
	}
	switch r.Status.RuntimeStatus {
	case "ok":
		return "running"
	case "pending":
		return "starting"
	case "error":
		return "erroring"
	}
	if r.Status.UpdateStatus == "running" {
		return "building"
	}
	if r.Status.UpdateStatus == "error" {
		return "erroring"
	}
	return "stopped"
}

func EndpointPorts(links []EndpointLink) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(links))
	for _, ep := range links {
		u, err := url.Parse(ep.URL)
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil || port <= 0 || seen[port] {
			continue
		}
		seen[port] = true
		out = append(out, port)
	}
	sort.Ints(out)
	return out
}
