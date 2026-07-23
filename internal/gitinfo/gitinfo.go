// Package gitinfo reports the checkout state of a service's source directory —
// which branch it is on and whether it carries uncommitted work — so status can
// answer "is this running the code I think it is" without a second tool call.
package gitinfo

import (
	"context"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// gitTimeout bounds each git invocation. Status must stay responsive even when a
// service directory sits on a slow or wedged filesystem.
const gitTimeout = 3 * time.Second

// Info is the checkout state of one directory. A zero Info means the directory
// is not a git checkout (or git could not read it), which is not an error.
type Info struct {
	Branch   string
	Detached bool
	Dirty    bool
}

// Label renders the checkout as one table cell: the branch name, or
// "detached@<sha>" when HEAD is detached, with a trailing "*" when the working
// tree has uncommitted changes. Empty when the directory is not a checkout.
func (i Info) Label() string {
	if i.Branch == "" {
		return ""
	}
	label := i.Branch
	if i.Detached {
		label = "detached@" + label
	}
	if i.Dirty {
		label += "*"
	}
	return label
}

// Read reports the checkout state of dir. A directory that is not a git
// checkout yields a zero Info rather than an error — callers render it blank.
func Read(dir string) Info {
	if dir == "" {
		return Info{}
	}
	head, ok := git(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if !ok {
		return Info{}
	}

	info := Info{Branch: head}
	if head == "HEAD" {
		sha, ok := git(dir, "rev-parse", "--short", "HEAD")
		if !ok {
			return Info{}
		}
		info = Info{Branch: sha, Detached: true}
	}

	if out, ok := git(dir, "status", "--porcelain"); ok {
		info.Dirty = out != ""
	}
	return info
}

// ReadAll reads every distinct directory concurrently and returns the result
// keyed the same way the input was. Directories are deduplicated so several
// services sharing a repo cost one git call.
func ReadAll(dirs map[string]string) map[string]Info {
	unique := make(map[string]struct{}, len(dirs))
	for _, dir := range dirs {
		if dir != "" {
			unique[dir] = struct{}{}
		}
	}

	byDir := make(map[string]Info, len(unique))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for dir := range unique {
		wg.Add(1)
		go func(d string) {
			defer wg.Done()
			info := Read(d)
			mu.Lock()
			byDir[d] = info
			mu.Unlock()
		}(dir)
	}
	wg.Wait()

	out := make(map[string]Info, len(dirs))
	for key, dir := range dirs {
		out[key] = byDir[dir]
	}
	return out
}

// DirtyKeys returns the sorted keys whose checkout has uncommitted changes.
func DirtyKeys(infos map[string]Info) []string {
	var out []string
	for key, info := range infos {
		if info.Dirty {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func git(dir string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}
