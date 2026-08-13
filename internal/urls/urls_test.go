package urls

import (
	"reflect"
	"testing"
)

// The two fixtures below are the live output of this machine: the tailnet
// address :8411 reaches caddy on :8511, and caddy forwards to the service on
// :20011. A reader who opens the address lands on the service, so the join has
// to survive the whole chain.
const serveFixture = `{
  "TCP": {"443": {"HTTPS": true}, "8411": {"HTTPS": true}},
  "Web": {
    "omarchy.tailde366c.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:8644"}}},
    "omarchy.tailde366c.ts.net:8411": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:8511"}}},
    "omarchy.tailde366c.ts.net:8787": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:8787"}}}
  }
}`

const caddyFixture = `{
  "admin": {"listen": "localhost:2019"},
  "apps": {"http": {"servers": {
    "srv0": {"listen": [":8511"], "routes": [{"handle": [
      {"handler": "encode", "encodings": {"gzip": {}}},
      {"handler": "reverse_proxy", "upstreams": [{"dial": "127.0.0.1:20011"}]}
    ]}]},
    "srv1": {"listen": [":9520"], "routes": [{"handle": [
      {"handler": "reverse_proxy", "upstreams": [{"dial": "127.0.0.1:4201"}]}
    ]}]}
  }}}
}`

func mapFrom(t *testing.T, serve, caddy string) Map {
	t.Helper()
	entries, err := parseTailscaleServe([]byte(serve))
	if err != nil {
		t.Fatalf("parseTailscaleServe: %v", err)
	}
	proxies, err := parseCaddyProxies([]byte(caddy))
	if err != nil {
		t.Fatalf("parseCaddyProxies: %v", err)
	}
	m := Map{byPort: map[int][]Link{}}
	for _, e := range entries {
		port, via := follow(e.port, proxies)
		m.byPort[port] = append(m.byPort[port], Link{URL: e.url, Port: port, Via: via})
	}
	return m
}

func TestTheAddressOfAServiceCrossesEveryProxyHop(t *testing.T) {
	m := mapFrom(t, serveFixture, caddyFixture)

	links := m.For(20011)
	if len(links) != 1 {
		t.Fatalf("For(20011) = %v, want one address", links)
	}
	if links[0].URL != "https://omarchy.tailde366c.ts.net:8411" {
		t.Errorf("URL = %q, want the tailnet address of the service", links[0].URL)
	}
	if want := []int{8511}; !reflect.DeepEqual(links[0].Via, want) {
		t.Errorf("Via = %v, want %v: the address passes caddy on the way", links[0].Via, want)
	}
}

// A port that no proxy forwards is the port the address already reaches. The
// otel UI is published that way, and dropping it would hide it from the panel.
func TestAnAddressWithNoProxyHopKeepsItsOwnPort(t *testing.T) {
	m := mapFrom(t, serveFixture, caddyFixture)

	links := m.For(8787)
	if len(links) != 1 {
		t.Fatalf("For(8787) = %v, want one address", links)
	}
	if links[0].Via != nil {
		t.Errorf("Via = %v, want no hops", links[0].Via)
	}
}

// The tailnet root serves on 443, and a browser does not want that port spelled
// out. Every other address keeps its port, or it opens the wrong site.
func TestTheDefaultHTTPSPortLeavesTheAddress(t *testing.T) {
	m := mapFrom(t, serveFixture, caddyFixture)

	links := m.For(8644)
	if len(links) != 1 || links[0].URL != "https://omarchy.tailde366c.ts.net" {
		t.Fatalf("For(8644) = %v, want the address without a port", links)
	}
}

// caddy without tailscale, or tailscale without caddy, still reports what it
// knows. An empty answer from one source must not lose the other's addresses.
func TestAMissingCaddyLeavesTheTailnetAddressesInPlace(t *testing.T) {
	m := mapFrom(t, serveFixture, `{}`)

	if links := m.For(8511); len(links) != 1 {
		t.Fatalf("For(8511) = %v, want the address that stops at caddy", links)
	}
	if links := m.For(20011); len(links) != 0 {
		t.Errorf("For(20011) = %v, want nothing: the hop is unknown", links)
	}
}

// A site that load-balances over two upstreams reaches neither service alone.
// Naming one of them would send a reader to a service that answers half the
// time, so the walk stops at the site's own port.
func TestASiteWithTwoUpstreamsStopsAtTheSite(t *testing.T) {
	caddy := `{"apps": {"http": {"servers": {"srv0": {"listen": [":8511"], "routes": [{"handle": [
      {"handler": "reverse_proxy", "upstreams": [{"dial": "127.0.0.1:20011"}, {"dial": "127.0.0.1:20012"}]}
    ]}]}}}}}`
	m := mapFrom(t, serveFixture, caddy)

	if links := m.For(8511); len(links) != 1 {
		t.Fatalf("For(8511) = %v, want the address to stop at the site", links)
	}
	if links := m.For(20011); len(links) != 0 {
		t.Errorf("For(20011) = %v, want nothing", links)
	}
}

// caddy nests handlers under subroutes as deep as the site needs. A walk that
// reads only the first level finds no upstream at all.
func TestANestedSubrouteStillReportsItsUpstream(t *testing.T) {
	caddy := `{"apps": {"http": {"servers": {"srv0": {"listen": [":8511"], "routes": [{"handle": [
      {"handler": "subroute", "routes": [{"handle": [
        {"handler": "reverse_proxy", "upstreams": [{"dial": "127.0.0.1:20011"}]}
      ]}]}
    ]}]}}}}}`
	m := mapFrom(t, serveFixture, caddy)

	if links := m.For(20011); len(links) != 1 {
		t.Fatalf("For(20011) = %v, want the nested upstream", links)
	}
}

// A proxy that points at another machine is not a hop this package can follow,
// and reporting its port as local names a service that does not exist here.
func TestAProxyToAnotherHostIsNotAHop(t *testing.T) {
	caddy := `{"apps": {"http": {"servers": {"srv0": {"listen": [":8511"], "routes": [{"handle": [
      {"handler": "reverse_proxy", "upstreams": [{"dial": "10.0.0.5:20011"}]}
    ]}]}}}}}`
	m := mapFrom(t, serveFixture, caddy)

	if links := m.For(20011); len(links) != 0 {
		t.Errorf("For(20011) = %v, want nothing: the upstream is another machine", links)
	}
}

// Two sites that forward to each other must not spin the walk forever.
func TestAProxyLoopStops(t *testing.T) {
	caddy := `{"apps": {"http": {"servers": {
      "a": {"listen": [":8511"], "routes": [{"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": "127.0.0.1:8512"}]}]}]},
      "b": {"listen": [":8512"], "routes": [{"handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": "127.0.0.1:8511"}]}]}]}
    }}}}`
	m := mapFrom(t, serveFixture, caddy)

	if len(m.Ports()) == 0 {
		t.Fatal("the walk lost every address")
	}
}
