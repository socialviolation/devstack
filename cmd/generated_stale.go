package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/socialviolation/devstack/internal/config"
	"github.com/socialviolation/devstack/internal/workspace"
)

// generatedFile is one service's AGENTS.md and the devstack that wrote it.
// Version is empty when the file predates the stamp, or was never generated.
type generatedFile struct {
	Service string
	Path    string
	Version string
	Exists  bool
	// Differs is whether this devstack would write a different block than the
	// file holds. It is the staleness test; Version is only ever reported.
	Differs bool
}

// provenanceRe reads the version out of the line agentsProvenance writes. It
// anchors on the marker rather than the whole line so the trailing advice can be
// reworded without orphaning every file already on disk.
var provenanceRe = regexp.MustCompile(`(?m)^<!-- devstack (.+?)\s+(?:·|-->)`)

// scanGenerated reports the AGENTS.md of every service in a workspace and which
// devstack generated it.
//
// These files are committed into repos devstack does not own, and they name
// commands that have already been renamed once. Without reading the stamp back
// there is no way to tell instructions written this week from instructions
// written before a breaking change.
func scanGenerated(wsPath string) ([]generatedFile, error) {
	cfg, err := config.Load(wsPath)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(cfg.ServicePaths))
	for name := range cfg.ServicePaths {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]generatedFile, 0, len(names))
	for _, name := range names {
		path := filepath.Join(cfg.ServicePaths[name], "AGENTS.md")
		f := generatedFile{Service: name, Path: path}
		if data, err := os.ReadFile(path); err == nil {
			f.Exists = true
			if m := provenanceRe.FindSubmatch(data); m != nil {
				f.Version = string(m[1])
			}
			want := agentsSentinelBegin + "\n" + agentsProvenance() +
				buildAgentInstructions(name, cfg.ServicePaths[name], wsPath, "") + agentsSentinelEnd
			f.Differs = managedBody(string(data)) != managedBody(want)
		}
		out = append(out, f)
	}
	return out, nil
}

// staleGenerated returns the files this devstack would write differently. A file
// that was never generated is not stale — nobody asked for it.
//
// Staleness is decided by the content, never by the version. The stamp carries
// the commit, so comparing versions made every file in every workspace stale on
// every devstack commit, whether or not that commit changed a word of what is
// generated — regenerating then produced a diff in fifteen repos whose only
// change was the stamp saying it had been regenerated. A check that fires when
// nothing needs doing is one people learn to run with their eyes shut.
func staleGenerated(files []generatedFile, _ string) []generatedFile {
	var out []generatedFile
	for _, f := range files {
		if f.Exists && f.Differs {
			out = append(out, f)
		}
	}
	return out
}

// managedBody is the generated block with its provenance line removed, which is
// what two devstacks are compared on. The stamp records which devstack wrote the
// file and necessarily differs between them, so including it would make every
// comparison report a difference.
func managedBody(s string) string {
	begin := strings.Index(s, agentsSentinelBegin)
	if begin == -1 {
		return ""
	}
	rel := strings.Index(s[begin:], agentsSentinelEnd)
	if rel == -1 {
		return ""
	}
	body := s[begin+len(agentsSentinelBegin) : begin+rel]

	var kept []string
	for _, line := range strings.Split(body, "\n") {
		if provenanceRe.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// staleByWorkspace reports every registered workspace holding generated files
// this devstack would write differently, so `upgrade` and `doctor` agree.
func staleByWorkspace(version string) (map[string][]generatedFile, []workspace.Workspace) {
	all, err := workspace.All()
	if err != nil {
		return nil, nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })

	stale := map[string][]generatedFile{}
	var order []workspace.Workspace
	for _, ws := range all {
		files, err := scanGenerated(ws.Path)
		if err != nil {
			continue
		}
		if s := staleGenerated(files, version); len(s) > 0 {
			stale[ws.Name] = s
			order = append(order, ws)
		}
	}
	return stale, order
}

// describeStamp names a version for a reader, saying plainly when there is none
// rather than printing a blank column.
func describeStamp(v string) string {
	if v == "" {
		return "written before devstack stamped its version"
	}
	return "written by " + v
}
