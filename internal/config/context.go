package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkspaceContext points at the prose a team wants an agent to read before
// working here, scoped the way everything else in devstack is scoped: workspace
// first, then the service. It holds paths rather than prose so the content lives
// in reviewable markdown and diffs like documentation, instead of bloating a
// manifest nobody wants to read.
//
// Workspace paths resolve against the workspace root; a service's resolve
// against that service's repo, so the doc sits with the code it describes.
type WorkspaceContext struct {
	Workspace ContextFiles            `yaml:"workspace,omitempty"`
	Service   map[string]ContextFiles `yaml:"service,omitempty"`
}

// ContextFiles accepts either one path or a list, so the common case stays a
// single line.
type ContextFiles []string

func (c *ContextFiles) UnmarshalYAML(value *yaml.Node) error {
	var one string
	if value.Decode(&one) == nil {
		if one = strings.TrimSpace(one); one != "" {
			*c = ContextFiles{one}
		}
		return nil
	}
	var many []string
	if err := value.Decode(&many); err != nil {
		return fmt.Errorf("context entries must be a file path or a list of them: %w", err)
	}
	out := make(ContextFiles, 0, len(many))
	for _, p := range many {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	*c = out
	return nil
}

func (c ContextFiles) MarshalYAML() (any, error) {
	switch len(c) {
	case 0:
		return nil, nil
	case 1:
		return c[0], nil
	default:
		return []string(c), nil
	}
}

// ContextDoc is one resolved piece of team-authored context.
type ContextDoc struct {
	Scope   string // "workspace" or a service name
	Path    string
	Body    string
	Missing bool
}

// Title names the doc in output, by where it came from rather than by a
// heading inside it that may not exist.
func (d ContextDoc) Title() string {
	if d.Scope == "workspace" {
		return "workspace"
	}
	return "service " + d.Scope
}

// ResolveContextDocs reads the context docs that apply to one service, workspace
// scope first. A service name of "" returns the workspace scope alone.
//
// A path that does not exist is reported rather than skipped: a doc referenced
// by the manifest and missing from disk is a broken promise the team should see,
// and silently omitting it looks identical to having nothing to say.
func ResolveContextDocs(rw *ResolvedWorkspace, service string) []ContextDoc {
	if rw == nil || rw.Manifest == nil {
		return nil
	}
	ctx := rw.Manifest.Context

	var docs []ContextDoc
	for _, rel := range ctx.Workspace {
		docs = append(docs, readContextDoc("workspace", rw.RootPath, rel))
	}

	names := make([]string, 0, len(ctx.Service))
	for name := range ctx.Service {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if service != "" && name != service {
			continue
		}
		base := rw.RootPath
		if svc, ok := rw.Services[name]; ok {
			base = svc.RepoPath
		}
		for _, rel := range ctx.Service[name] {
			docs = append(docs, readContextDoc(name, base, rel))
		}
	}
	return docs
}

func readContextDoc(scope, base, rel string) ContextDoc {
	path := rel
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, rel)
	}
	doc := ContextDoc{Scope: scope, Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		doc.Missing = true
		return doc
	}
	doc.Body = strings.TrimRight(string(data), "\n")
	return doc
}

// ContextServiceNames lists the services this workspace declares context for.
func (c WorkspaceContext) ContextServiceNames() []string {
	names := make([]string, 0, len(c.Service))
	for name := range c.Service {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Title is the doc's own heading, used to say what it is worth reading for. It
// is read from the file rather than declared in the manifest, so a team writes
// the summary once, at the top of the document, where a human also benefits
// from it.
func (d ContextDoc) DocTitle() string {
	for _, line := range strings.Split(d.Body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return strings.TrimSpace(strings.TrimLeft(line, "#"))
	}
	return ""
}

// Clip bounds a doc so team prose cannot push the briefing past the limit its
// host imposes. A clipped doc names its own path, so the rest is one read away
// rather than lost.
func (d ContextDoc) Clip(n int) string {
	if len(d.Body) <= n {
		return d.Body
	}
	cut := d.Body[:n]
	if i := strings.LastIndex(cut, "\n"); i > 0 {
		cut = cut[:i]
	}
	return cut + "\n… clipped. Read the rest: " + d.Path
}
