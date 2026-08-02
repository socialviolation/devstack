// Package selfcheck reports whether the running devstack is the current one.
//
// A binary that cannot say how old it is gets used long after it stopped
// matching the source: an agent runs a stale MCP server, reads tool
// descriptions that no longer exist, and nothing in the output says why. The
// commit is already embedded in every build, so the only missing half is what
// the published branch holds.
package selfcheck

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Status is what the comparison found.
type Status string

const (
	StatusCurrent  Status = "current"  // the build is the tip of the branch
	StatusBehind   Status = "behind"   // the branch has commits this build does not
	StatusAhead    Status = "ahead"    // this build has commits the branch does not
	StatusDiverged Status = "diverged" // both, so neither contains the other
	StatusLocal    Status = "local"    // the branch host has never seen this commit
	StatusUnknown  Status = "unknown"  // no answer; say nothing rather than guess
)

// Result is one comparison. Version is carried so a cached result can be
// discarded when the binary changes.
type Result struct {
	Status    Status    `json:"status"`
	BehindBy  int       `json:"behind_by"`
	AheadBy   int       `json:"ahead_by"`
	Revision  string    `json:"revision"`
	CheckedAt time.Time `json:"checked_at"`
}

// Describe renders the result as one line, or "" when there is nothing worth
// saying. Silence is deliberate for the current and unknown cases: a line that
// appears on every command teaches a reader to skip the place it appears.
func (r Result) Describe(installPath string) string {
	switch r.Status {
	case StatusBehind:
		return fmt.Sprintf("%s behind %s — update: go install %s@latest", plural(r.BehindBy, "commit"), branch, installPath)
	case StatusAhead:
		return fmt.Sprintf("%s ahead of %s — this build is not published", plural(r.AheadBy, "commit"), branch)
	case StatusDiverged:
		return fmt.Sprintf("diverged from %s: %d ahead, %d behind", branch, r.AheadBy, r.BehindBy)
	case StatusLocal:
		return "local build — this commit is not on " + host
	default:
		return ""
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

const (
	branch = "main"
	host   = "GitHub"
	// ttl keeps the check off the critical path. The answer changes when
	// somebody pushes, which is not often enough to pay a network round trip
	// for on every session.
	ttl = 24 * time.Hour
	// timeout bounds the delay a session start can inherit. A briefing that
	// waits on a network is worse than a briefing that omits one line.
	timeout = 1500 * time.Millisecond
)

// Cached returns the last stored result for this revision, without touching the
// network. It is what the fast paths read: the answer is only as old as ttl, and
// paying for it is the refreshing caller's job.
func Cached(revision string) (Result, bool) {
	if revision == "" {
		return Result{}, false
	}
	data, err := os.ReadFile(cachePath())
	if err != nil {
		return Result{}, false
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return Result{}, false
	}
	if r.Revision != revision {
		return Result{}, false
	}
	return r, true
}

// Refresh returns the cached result while it is fresh, and otherwise asks the
// branch host. Every failure resolves to StatusUnknown, which Describe renders
// as nothing: this is a courtesy, and it must never be the reason a command
// fails or hangs.
func Refresh(modulePath, revision string) Result {
	if cached, ok := Cached(revision); ok && time.Since(cached.CheckedAt) < ttl {
		return cached
	}
	r := compare(modulePath, revision)
	r.Revision = revision
	r.CheckedAt = time.Now()
	if r.Status != StatusUnknown {
		store(r)
	}
	return r
}

// compareAPI is the endpoint template, and a seam the tests replace.
var compareAPI = "https://api.github.com/repos/%s/compare/%s...%s"

func compare(modulePath, revision string) Result {
	repo := repoSlug(modulePath)
	if repo == "" || revision == "" {
		return Result{Status: StatusUnknown}
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(fmt.Sprintf(compareAPI, repo, branch, revision))
	if err != nil {
		return Result{Status: StatusUnknown}
	}
	defer resp.Body.Close()

	// A commit the host has never seen is a local build, which is a fact worth
	// reporting rather than an error.
	if resp.StatusCode == http.StatusNotFound {
		return Result{Status: StatusLocal}
	}
	if resp.StatusCode != http.StatusOK {
		return Result{Status: StatusUnknown}
	}

	var body struct {
		Status   string `json:"status"`
		AheadBy  int    `json:"ahead_by"`
		BehindBy int    `json:"behind_by"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Result{Status: StatusUnknown}
	}
	return Interpret(body.Status, body.AheadBy, body.BehindBy)
}

// Interpret maps the host's comparison onto a Status.
//
// The comparison is expressed from the branch's side: comparing main...<build>,
// the host calls the build "behind" when the build is the older one. That is
// also how a reader means it, so the words survive the trip unchanged.
func Interpret(status string, aheadBy, behindBy int) Result {
	switch status {
	case "identical":
		return Result{Status: StatusCurrent}
	case "behind":
		return Result{Status: StatusBehind, BehindBy: behindBy}
	case "ahead":
		return Result{Status: StatusAhead, AheadBy: aheadBy}
	case "diverged":
		return Result{Status: StatusDiverged, AheadBy: aheadBy, BehindBy: behindBy}
	default:
		return Result{Status: StatusUnknown}
	}
}

// repoSlug turns a Go module path into an "owner/repo" slug, and returns "" for
// anything not hosted where the comparison endpoint lives.
func repoSlug(modulePath string) string {
	const prefix = "github.com/"
	if len(modulePath) <= len(prefix) || modulePath[:len(prefix)] != prefix {
		return ""
	}
	rest := modulePath[len(prefix):]
	slash := -1
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			if slash >= 0 {
				rest = rest[:i]
				break
			}
			slash = i
		}
	}
	if slash < 0 {
		return ""
	}
	return rest
}

func cachePath() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, ".local", "share", "devstack", "update-check.json")
}

func store(r Result) {
	path := cachePath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	data, err := json.Marshal(r)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0644)
}
