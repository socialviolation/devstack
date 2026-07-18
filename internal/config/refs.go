package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// PortBook maps a service name to its port keys and the ports bound to them. It
// is the sole input a reference resolves against, so a caller that supplies a
// different book (allocated ports, overlay-first lookup) changes what references
// resolve to without touching the resolver.
type PortBook map[string]map[string]int

// Host returns the host a service is reachable on. The base workspace is always
// localhost; stacks and tunnels revisit this, so it is routed through the book
// rather than hardcoded at the call site.
func (b PortBook) Host(service string) string {
	return "localhost"
}

// BuildPortBook builds the port book from every service's pinned ports: literals.
func BuildPortBook(rw *ResolvedWorkspace) PortBook {
	book := PortBook{}
	for name, svc := range rw.Services {
		if svc.Manifest == nil || len(svc.Manifest.Ports) == 0 {
			continue
		}
		book[name] = svc.Manifest.Ports
	}
	return book
}

// MergeStackBook merges an overlay book onto a base book, overlay-first: a
// service present in overlay replaces the base entry for that service wholesale
// (the stack runs its own instance on allocated ports); a service only in base is
// carried through unchanged (reused). Entries merge per service, never per port
// key — a service's ports come entirely from overlay or entirely from base. The
// result and every inner map are fresh copies, so mutating it cannot corrupt
// either input.
func MergeStackBook(base, overlay PortBook) PortBook {
	merged := make(PortBook, len(base)+len(overlay))
	for name, ports := range base {
		merged[name] = copyPorts(ports)
	}
	for name, ports := range overlay {
		merged[name] = copyPorts(ports)
	}
	return merged
}

func copyPorts(ports map[string]int) map[string]int {
	c := make(map[string]int, len(ports))
	for k, v := range ports {
		c[k] = v
	}
	return c
}

var refPattern = regexp.MustCompile(`\$\{([^}]*)\}`)

// ResolveRefs replaces every ${service.field} reference in s with its value from
// book, leaving literal text untouched. self names the current service, so a
// reference to "self" resolves against it. field is one of host, port.<key>, or
// url. It is a pure function of (s, self, book).
//
// Any reference that cannot be resolved — unknown service, unknown port key, a
// url for a service with no http port, or a malformed ${...} — is a hard error
// naming the reference. An unresolved reference is never returned in the string.
func ResolveRefs(s, self string, book PortBook) (string, error) {
	var resolveErr error
	out := refPattern.ReplaceAllStringFunc(s, func(match string) string {
		if resolveErr != nil {
			return match
		}
		v, err := resolveRef(match[2:len(match)-1], self, book)
		if err != nil {
			resolveErr = err
			return match
		}
		return v
	})
	if resolveErr != nil {
		return "", resolveErr
	}
	if strings.Contains(out, "${") {
		return "", fmt.Errorf("malformed reference in %q", s)
	}
	return out, nil
}

func resolveRef(inner, self string, book PortBook) (string, error) {
	parts := strings.Split(inner, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("malformed reference ${%s}", inner)
	}
	service := parts[0]
	if service == "self" {
		service = self
	}

	switch {
	case len(parts) == 2 && parts[1] == "host":
		return book.Host(service), nil

	case len(parts) == 2 && parts[1] == "url":
		port, err := lookupPort(inner, service, "http", book)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("http://%s:%d", book.Host(service), port), nil

	case len(parts) == 3 && parts[1] == "port":
		port, err := lookupPort(inner, service, parts[2], book)
		if err != nil {
			return "", err
		}
		return strconv.Itoa(port), nil

	default:
		return "", fmt.Errorf("malformed reference ${%s}", inner)
	}
}

func lookupPort(inner, service, key string, book PortBook) (int, error) {
	ports, ok := book[service]
	if !ok {
		return 0, fmt.Errorf("reference ${%s}: unknown service %q", inner, service)
	}
	port, ok := ports[key]
	if !ok {
		return 0, fmt.Errorf("reference ${%s}: service %q has no port %q", inner, service, key)
	}
	return port, nil
}
