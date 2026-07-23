package svcconfig

import (
	"sort"
	"strconv"
	"strings"

	"github.com/socialviolation/devstack/internal/config"
)

// DriftKind classifies one difference between a service's declared config
// surface and the env it actually resolves to locally.
type DriftKind string

const (
	// DriftMissing is a key the deployment declares with a literal value that the
	// local env does not supply at all. The service falls back to whatever its
	// code defaults to, which is how a local run silently takes a different code
	// path than production.
	DriftMissing DriftKind = "missing"

	// DriftSecretMissing is a key the deployment supplies from a secret or config
	// map, with no local value. It needs a local credential before the path that
	// reads it can work.
	DriftSecretMissing DriftKind = "secret-missing"

	// DriftDiffers is a key present in both with different values. Local
	// overrides are normal — a local DB host is meant to differ — so this is
	// informational.
	DriftDiffers DriftKind = "differs"
)

// DriftEntry is one difference for one key. Values are redacted the same way the
// effective-config view redacts them.
type DriftEntry struct {
	Key      string
	Kind     DriftKind
	Declared string
	Local    string
	Source   string
}

// Drift compares what a service's declared config sources say it needs against
// the env it actually resolves to locally, and reports every difference. It
// answers the question a local run cannot: "is this service configured the way
// the thing it is standing in for is configured". A service that declares no
// config sources has nothing to compare and yields no entries.
//
// resolved is the merged env ladder — what the generator hands the process.
func Drift(svc config.ResolvedService, resolved map[string]string) ([]DriftEntry, error) {
	if svc.Manifest == nil || len(svc.Manifest.Config.Sources) == 0 {
		return nil, nil
	}

	values, provenance, err := declared(svc)
	if err != nil {
		return nil, err
	}

	portEnv := svc.Manifest.Config.PortEnv
	var out []DriftEntry
	for key, want := range values {
		// The listen port is allocated per instance by devstack; it is meant to
		// differ from the deployment's.
		if key == portEnv {
			continue
		}
		got, present := resolved[key]
		switch {
		case want == externalMarker && !present:
			out = append(out, DriftEntry{Key: key, Kind: DriftSecretMissing, Declared: externalMarker, Source: provenance[key]})
		case want == externalMarker:
			continue
		case !present:
			out = append(out, DriftEntry{Key: key, Kind: DriftMissing, Declared: RedactValue(key, want), Source: provenance[key]})
		case got != want:
			out = append(out, DriftEntry{
				Key:      key,
				Kind:     DriftDiffers,
				Declared: RedactValue(key, want),
				Local:    RedactValue(key, got),
				Source:   provenance[key],
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if ri, rj := driftRank(out[i].Kind), driftRank(out[j].Kind); ri != rj {
			return ri < rj
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

func driftRank(k DriftKind) int {
	switch k {
	case DriftMissing:
		return 0
	case DriftSecretMissing:
		return 1
	default:
		return 2
	}
}

// Render formats drift entries as a table under a one-line summary.
func Render(service string, entries []DriftEntry) string {
	var sb strings.Builder
	if len(entries) == 0 {
		sb.WriteString(service + ": no drift — every key its config sources declare is set locally with the declared value\n")
		return sb.String()
	}

	counts := map[DriftKind]int{}
	for _, e := range entries {
		counts[e.Kind]++
	}
	sb.WriteString(service + ": " + summary(counts) + "\n")
	sb.WriteString("  " + pad("KEY", 44) + pad("KIND", 16) + pad("SOURCE", 12) + "DECLARED -> LOCAL\n")
	for _, e := range entries {
		local := e.Local
		if local == "" {
			local = "(unset)"
		}
		sb.WriteString("  " + pad(e.Key, 44) + pad(string(e.Kind), 16) + pad(e.Source, 12) + e.Declared + " -> " + local + "\n")
	}
	return sb.String()
}

func summary(counts map[DriftKind]int) string {
	var parts []string
	if n := counts[DriftMissing]; n > 0 {
		parts = append(parts, plural(n, "declared key")+" not set locally — the service falls back to its code default")
	}
	if n := counts[DriftSecretMissing]; n > 0 {
		parts = append(parts, plural(n, "secret-backed key")+" with no local value")
	}
	if n := counts[DriftDiffers]; n > 0 {
		parts = append(parts, plural(n, "key")+" set to a different value locally")
	}
	return strings.Join(parts, "; ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func pad(s string, width int) string {
	if len(s) >= width {
		return s + " "
	}
	return s + strings.Repeat(" ", width-len(s))
}
