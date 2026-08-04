package config

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// RebaseWorkDirs points the absolute runtime.workDir of a copy at the copy.
//
// A copy of a workspace is a set of git worktrees below copyRoot, one for each
// repository. A service manifest can carry an absolute runtime.workDir, and the
// generator runs the service in that directory. The manifest is a committed
// file, so the absolute path in it names a directory of the checkout the copy
// was built from. A copy that keeps that path runs the checkout, and not the
// code the copy holds.
//
// A workDir in a repository of the template is rewritten to the same directory
// in the copy. A workDir somewhere else is left as it is, and reported: the
// copy can not hold code that this workspace does not own.
func RebaseWorkDirs(template, copyWS *ResolvedWorkspace, copyRoot string) []string {
	if template == nil || copyWS == nil {
		return nil
	}
	repos := repoPairs(template, copyWS, filepath.Clean(copyRoot))

	names := make([]string, 0, len(copyWS.Services))
	for name := range copyWS.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	var warnings []string
	for _, name := range names {
		svc := copyWS.Services[name]
		if svc.Manifest == nil {
			continue
		}
		wd := svc.Manifest.Runtime.WorkDir
		if wd == "" || !filepath.IsAbs(wd) {
			continue
		}
		wd = filepath.Clean(wd)
		into, ok := rebasePath(wd, repos)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("the service %q has the absolute runtime.workDir %s. No repository of this workspace holds that directory, so the copy below %s runs it as it is. The copy does not control that code.", name, wd, copyRoot))
			continue
		}
		svc.Manifest.Runtime.WorkDir = into
	}
	return warnings
}

// repoPairs maps each repository root of the template to the same repository in
// the copy. A copy holds one repository for each first-level directory below
// copyRoot, and a service sits at the same path below its repository in both,
// so the two roots follow from the two service paths.
func repoPairs(template, copyWS *ResolvedWorkspace, copyRoot string) map[string]string {
	pairs := map[string]string{}
	for name, cs := range copyWS.Services {
		ts, ok := template.Services[name]
		if !ok {
			continue
		}
		rel, err := filepath.Rel(copyRoot, filepath.Clean(cs.RepoPath))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		parts := strings.Split(rel, string(filepath.Separator))
		below := filepath.Join(parts[1:]...)
		from := filepath.Clean(ts.RepoPath)
		if below != "" {
			trimmed := strings.TrimSuffix(from, string(filepath.Separator)+below)
			if trimmed == from {
				continue
			}
			from = trimmed
		}
		pairs[from] = filepath.Join(copyRoot, parts[0])
	}
	return pairs
}

// rebasePath maps a directory of a template repository to the same directory of
// the copy. The longest repository root wins, so a repository below another one
// keeps its own worktree.
func rebasePath(dir string, repos map[string]string) (string, bool) {
	from, into := "", ""
	for root, worktree := range repos {
		if dir != root && !strings.HasPrefix(dir, root+string(filepath.Separator)) {
			continue
		}
		if len(root) > len(from) {
			from, into = root, worktree
		}
	}
	if from == "" {
		return "", false
	}
	rel, err := filepath.Rel(from, dir)
	if err != nil {
		return "", false
	}
	return filepath.Join(into, rel), true
}
