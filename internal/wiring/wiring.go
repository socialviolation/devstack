package wiring

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/socialviolation/devstack/internal/config"
)

type Copy struct {
	Label string
	Env   map[string]map[string]string
	Book  config.PortBook
	Scope config.StackScope
}

type Hop struct {
	Value  string
	Target string
	Err    string
}

type Link struct {
	From     string
	To       string
	Key      string
	Via      string
	Declared bool
	Base     *Hop
	Stack    *Hop
}

func (l Link) Flag(stackLabel string) string {
	switch {
	case l.Stack != nil && l.Stack.Target == "base":
		return fmt.Sprintf("stack %s %s → %s at its declared port", stackLabel, l.From, l.To)
	case l.Base != nil && l.Base.Target != "" && l.Base.Target != "base":
		return fmt.Sprintf("%s with no stack → stack %s %s", l.From, l.Base.Target, l.To)
	}
	return ""
}

type Issue struct {
	Severity string
	Code     string
	From     string
	To       string
	Message  string
}

type Map struct {
	Links  []Link
	Issues []Issue
}

func RawEnv(rw *config.ResolvedWorkspace, stackEnv string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for name, svc := range rw.Services {
		if svc.Manifest == nil {
			continue
		}
		layers, err := config.EnvLadder(svc.EnvDir(), rw.Manifest, svc.Manifest, stackEnv, nil, svc.ManifestPath)
		if err != nil {
			return nil, fmt.Errorf("can not resolve the environment for %s: %w", name, err)
		}
		out[name] = config.MergeEnvLadder(layers)
	}
	return out, nil
}

var refPattern = regexp.MustCompile(`\$\{([^}]*)\}`)

const suffixPrefix = "stack.suffix:"

type ref struct {
	to   string
	via  string
	port int
}

func Derive(services []string, deps map[string][]string, base Copy, stack *Copy) Map {
	known := map[string]bool{}
	for _, s := range services {
		known[s] = true
	}
	stackLabel := ""
	if stack != nil {
		stackLabel = stack.Label
	}

	type linkKey struct{ from, key, to, via string }
	links := map[linkKey]*Link{}
	var issues []Issue
	reported := map[string]bool{}

	scan := func(c *Copy, isStack bool) {
		for _, from := range sortedKeys(c.Env) {
			env := c.Env[from]
			for _, key := range sortedKeys(env) {
				raw := env[key]
				refs, unknown := findRefs(raw, from, known, base, stack)
				for _, u := range unknown {
					id := from + "\x00" + key + "\x00" + u
					if reported[id] {
						continue
					}
					reported[id] = true
					issues = append(issues, Issue{
						Severity: "error", Code: "unknown-service", From: from, To: u,
						Message: fmt.Sprintf("%s sets %s to a reference to the service %q, and the workspace has no such service", from, key, u),
					})
				}
				for _, r := range refs {
					k := linkKey{from, key, r.to, r.via}
					l, ok := links[k]
					if !ok {
						l = &Link{From: from, To: r.to, Key: key, Via: r.via, Declared: slices.Contains(deps[from], r.to)}
						links[k] = l
					}
					hop := resolveHop(c, from, raw, r, base, stackLabel)
					if isStack {
						l.Stack = hop
					} else {
						l.Base = hop
					}
				}
			}
		}
	}
	scan(&base, false)
	if stack != nil {
		scan(stack, true)
	}

	var out Map
	carried := map[[2]string]bool{}
	for _, l := range links {
		out.Links = append(out.Links, *l)
		carried[[2]string{l.From, l.To}] = true
	}
	sort.Slice(out.Links, func(i, j int) bool {
		a, b := out.Links[i], out.Links[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Via < b.Via
	})

	undeclared := map[[2]string]bool{}
	for _, l := range out.Links {
		pair := [2]string{l.From, l.To}
		if l.Declared || undeclared[pair] {
			continue
		}
		undeclared[pair] = true
		issues = append(issues, Issue{
			Severity: "note", Code: "undeclared-edge", From: l.From, To: l.To,
			Message: fmt.Sprintf("%s reaches %s through %s, and no needs or calls edge goes from %s to %s", l.From, l.To, l.Key, l.From, l.To),
		})
	}
	for _, from := range services {
		for _, to := range deps[from] {
			if !known[to] || carried[[2]string{from, to}] {
				continue
			}
			issues = append(issues, Issue{
				Severity: "note", Code: "no-key", From: from, To: to,
				Message: fmt.Sprintf("%s declares an edge to %s, and no env key of %s carries the address of %s. The address is in code or in a config file devstack does not read", from, to, from, to),
			})
		}
	}

	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Severity != issues[j].Severity {
			return issues[i].Severity < issues[j].Severity
		}
		return issues[i].Message < issues[j].Message
	})
	out.Issues = issues
	return out
}

func findRefs(raw, self string, known map[string]bool, base Copy, stack *Copy) ([]ref, []string) {
	var refs []ref
	var unknown []string
	seen := map[string]bool{}
	add := func(r ref) {
		id := r.to + "\x00" + r.via
		if !seen[id] {
			seen[id] = true
			refs = append(refs, r)
		}
	}

	for _, m := range refPattern.FindAllStringSubmatch(raw, -1) {
		inner := m[1]
		if list, ok := strings.CutPrefix(inner, suffixPrefix); ok {
			for _, name := range strings.Split(list, ",") {
				name = strings.TrimSpace(name)
				switch {
				case name == "" || name == self:
				case !known[name]:
					unknown = append(unknown, name)
				default:
					add(ref{to: name, via: m[0]})
				}
			}
			continue
		}
		name, _, _ := strings.Cut(inner, ".")
		switch {
		case name == "self" || name == self:
		case !known[name]:
			unknown = append(unknown, name)
		default:
			add(ref{to: name, via: m[0]})
		}
	}

	literal := refPattern.ReplaceAllString(raw, "")
	for _, to := range sortedKeys(known) {
		if to == self {
			continue
		}
		for _, port := range portsOf(to, base, stack) {
			if hasPort(literal, port) {
				add(ref{to: to, via: "literal port " + strconv.Itoa(port), port: port})
			}
		}
	}
	return refs, unknown
}

func resolveHop(c *Copy, from, raw string, r ref, base Copy, stackLabel string) *Hop {
	hop := &Hop{Target: "base"}
	value, err := config.ResolveStackRefs(raw, from, c.Book, c.Scope)
	if err != nil {
		hop.Err = err.Error()
	} else {
		hop.Value = value
	}

	switch {
	case r.port != 0:
		if !slices.Contains(portValues(base.Book[r.to]), r.port) {
			hop.Target = stackLabel
		}
	case strings.HasPrefix(r.via, "${"+suffixPrefix):
		if s, _ := config.ResolveStackRefs(r.via, from, c.Book, c.Scope); s != "" {
			hop.Target = c.Label
		}
	case c.Scope.Name != "" && slices.Contains(c.Scope.Services, r.to):
		hop.Target = c.Label
	}
	return hop
}

func portsOf(service string, base Copy, stack *Copy) []int {
	ports := portValues(base.Book[service])
	if stack != nil && slices.Contains(stack.Scope.Services, service) {
		ports = append(ports, portValues(stack.Book[service])...)
	}
	return ports
}

func portValues(m map[string]int) []int {
	out := make([]int, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

func hasPort(s string, port int) bool {
	p := strconv.Itoa(port)
	if s == p {
		return true
	}
	for i := 0; ; {
		j := strings.Index(s[i:], ":"+p)
		if j < 0 {
			return false
		}
		end := i + j + 1 + len(p)
		if end == len(s) || s[end] < '0' || s[end] > '9' {
			return true
		}
		i = end
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
