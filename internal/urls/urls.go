// Package urls resolves the tailnet address that reaches a local service port.
//
// A request crosses three hops before it arrives at a devstack service:
//
//	tailnet https://<host>:8411 → caddy 127.0.0.1:8511 → service 127.0.0.1:20011
//
// Every hop publishes its own map. tailscale prints the first one, caddy's admin
// API answers with the second, and devstack already knows which service holds
// which port. This package joins the first two, so a caller that knows a port
// gets back the address that reaches it from another machine.
package urls

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const CaddyAdmin = "http://localhost:2019/config/"

const probeTimeout = 3 * time.Second

type Link struct {
	URL  string
	Port int
	Via  []int
}

type Map struct {
	byPort map[int][]Link
	// Err records why a source is missing. The map is still usable: an absent
	// source removes addresses, it does not make the other addresses wrong.
	Err error
}

func (m Map) For(port int) []Link {
	if m.byPort == nil {
		return nil
	}
	return m.byPort[port]
}

func (m Map) Ports() []int {
	out := make([]int, 0, len(m.byPort))
	for p := range m.byPort {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

func Discover(ctx context.Context) Map {
	serve, serveErr := readTailscaleServe(ctx)
	proxies, caddyErr := readCaddyProxies(ctx)

	m := Map{byPort: map[int][]Link{}}
	switch {
	case serveErr != nil:
		m.Err = serveErr
	case caddyErr != nil:
		m.Err = caddyErr
	}

	for _, entry := range serve {
		port, via := follow(entry.port, proxies)
		link := Link{URL: entry.url, Port: port, Via: via}
		m.byPort[port] = append(m.byPort[port], link)
	}
	for port := range m.byPort {
		sort.Slice(m.byPort[port], func(i, j int) bool { return m.byPort[port][i].URL < m.byPort[port][j].URL })
	}
	return m
}

// follow walks the proxy map from the first local port to the last one, and
// reports the ports it passed on the way. A port that proxies to more than one
// upstream stops the walk: the address does not reach one service, and guessing
// which upstream to name would name the wrong service.
func follow(port int, proxies map[int][]int) (int, []int) {
	var via []int
	seen := map[int]bool{port: true}
	for {
		next, ok := proxies[port]
		if !ok || len(next) != 1 || seen[next[0]] {
			return port, via
		}
		via = append(via, port)
		port = next[0]
		seen[port] = true
	}
}

type serveEntry struct {
	url  string
	port int
}

func readTailscaleServe(ctx context.Context) ([]serveEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "tailscale", "serve", "status", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("can not read the tailscale serve map: %w", err)
	}
	return parseTailscaleServe(out)
}

func parseTailscaleServe(data []byte) ([]serveEntry, error) {
	var doc struct {
		Web map[string]struct {
			Handlers map[string]struct {
				Proxy string `json:"Proxy"`
			} `json:"Handlers"`
		} `json:"Web"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("can not read the tailscale serve map: %w", err)
	}

	var out []serveEntry
	for hostPort, site := range doc.Web {
		handler, ok := site.Handlers["/"]
		if !ok || handler.Proxy == "" {
			continue
		}
		port, ok := localPort(handler.Proxy)
		if !ok {
			continue
		}
		out = append(out, serveEntry{url: serveURL(hostPort), port: port})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].url < out[j].url })
	return out, nil
}

func serveURL(hostPort string) string {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return "https://" + hostPort
	}
	if port == "443" {
		return "https://" + host
	}
	return "https://" + host + ":" + port
}

func readCaddyProxies(ctx context.Context) (map[int][]int, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CaddyAdmin, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("can not read the caddy proxy map: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("can not read the caddy proxy map: admin API answered %s", resp.Status)
	}

	var doc json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("can not read the caddy proxy map: %w", err)
	}
	return parseCaddyProxies(doc)
}

func parseCaddyProxies(data []byte) (map[int][]int, error) {
	var doc struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Listen []string        `json:"listen"`
					Routes json.RawMessage `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("can not read the caddy proxy map: %w", err)
	}

	out := map[int][]int{}
	for _, server := range doc.Apps.HTTP.Servers {
		upstreams := collectUpstreams(server.Routes)
		if len(upstreams) == 0 {
			continue
		}
		for _, listen := range server.Listen {
			port, ok := listenPort(listen)
			if !ok {
				continue
			}
			out[port] = append(out[port], upstreams...)
		}
	}
	for port := range out {
		out[port] = dedupe(out[port])
	}
	return out, nil
}

// collectUpstreams reports every local port the routes proxy to. It walks the
// whole route tree, because caddy nests a subroute's handlers as deep as the
// site configuration needs.
func collectUpstreams(routes json.RawMessage) []int {
	var found []int
	var walk func(v any)
	walk = func(v any) {
		switch node := v.(type) {
		case map[string]any:
			if node["handler"] == "reverse_proxy" {
				list, _ := node["upstreams"].([]any)
				for _, u := range list {
					up, _ := u.(map[string]any)
					dial, _ := up["dial"].(string)
					if port, ok := dialPort(dial); ok {
						found = append(found, port)
					}
				}
			}
			for _, child := range node {
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}

	var tree any
	if err := json.Unmarshal(routes, &tree); err != nil {
		return nil
	}
	walk(tree)
	return dedupe(found)
}

func localPort(target string) (int, bool) {
	target = strings.TrimPrefix(target, "http://")
	target = strings.TrimPrefix(target, "https://")
	target = strings.TrimSuffix(target, "/")
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return 0, false
	}
	if !isLocalHost(host) {
		return 0, false
	}
	return atoi(port)
}

func dialPort(dial string) (int, bool) {
	host, port, err := net.SplitHostPort(dial)
	if err != nil {
		return 0, false
	}
	if !isLocalHost(host) {
		return 0, false
	}
	return atoi(port)
}

func listenPort(listen string) (int, bool) {
	if strings.HasPrefix(listen, ":") {
		return atoi(listen[1:])
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return 0, false
	}
	return atoi(port)
}

func isLocalHost(host string) bool {
	switch host {
	case "127.0.0.1", "localhost", "::1", "0.0.0.0", "":
		return true
	}
	return false
}

func atoi(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func dedupe(in []int) []int {
	if len(in) < 2 {
		return in
	}
	seen := map[int]bool{}
	out := in[:0]
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
