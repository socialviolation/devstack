package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/socialviolation/devstack/internal/stack"
	"github.com/socialviolation/devstack/internal/urls"
	"github.com/socialviolation/devstack/internal/workspace"
)

const Source = "devstack"

var tokenKeys = []string{"stack", "stack_state", "stack_url", "stack_stop"}

var (
	Run = func(bin string, args ...string) ([]byte, error) {
		return exec.Command(bin, args...).Output()
	}
	LinkFor = func(ctx context.Context) func(port int) string {
		m := urls.Discover(ctx)
		return func(port int) string {
			if l := m.For(port); len(l) > 0 {
				return l[0].URL
			}
			return ""
		}
	}
	Notify = func() {
		if err := Publish(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: devstack can not update the herdr panes: %v\n", err)
		}
	}
)

func Bin() string {
	if p := os.Getenv("HERDR_BIN_PATH"); p != "" {
		return p
	}
	p, err := exec.LookPath("herdr")
	if err != nil {
		return ""
	}
	return p
}

func socketPath() string {
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "herdr", "herdr.sock")
}

func available() string {
	bin := Bin()
	if bin == "" {
		return ""
	}
	if _, err := os.Stat(socketPath()); err != nil {
		return ""
	}
	return bin
}

func Bind(base, name string) {
	pane := os.Getenv("HERDR_PANE_ID")
	if pane == "" {
		return
	}
	if err := stack.SetPane(base, name, pane); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: devstack can not bind stack %s to herdr pane %s: %v\n", name, pane, err)
	}
}

type Pane struct {
	ID     string            `json:"pane_id"`
	Tokens map[string]string `json:"tokens"`
}

func ListPanes(bin string) ([]Pane, error) {
	out, err := Run(bin, "pane", "list")
	if err != nil {
		return nil, fmt.Errorf("herdr pane list: %w", err)
	}
	var resp struct {
		Result struct {
			Panes []Pane `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("can not read the output of herdr pane list: %w", err)
	}
	return resp.Result.Panes, nil
}

type bound struct {
	ws  string
	rec stack.Record
}

func loadBound() (map[string][]bound, []error) {
	all, err := workspace.All()
	if err != nil {
		return nil, []error{err}
	}
	var errs []error
	byPane := map[string][]bound{}
	for i := range all {
		recs, err := stack.LoadStore(all[i].Name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, rec := range recs {
			if rec.Pane != "" {
				byPane[rec.Pane] = append(byPane[rec.Pane], bound{ws: all[i].Name, rec: rec})
			}
		}
	}
	return byPane, errs
}

func Publish(ctx context.Context) error {
	bin := available()
	if bin == "" {
		return nil
	}
	panes, err := ListPanes(bin)
	if err != nil {
		return err
	}
	live := map[string]bool{}
	for _, p := range panes {
		live[p.ID] = true
	}

	byPane, errs := loadBound()
	for pane, recs := range byPane {
		if len(panes) == 0 || live[pane] {
			continue
		}
		for _, b := range recs {
			if err := stack.ClearPane(b.ws, b.rec.Name, pane); err != nil {
				errs = append(errs, err)
			}
		}
	}

	var linkFor func(int) string
	for _, p := range panes {
		recs := byPane[p.ID]
		if len(recs) == 0 && !hasOurTokens(p) {
			continue
		}
		var tokens map[string]string
		if len(recs) > 0 {
			if linkFor == nil {
				linkFor = LinkFor(ctx)
			}
			tokens = paneTokens(recs, linkFor)
		}
		if _, err := Run(bin, reportArgs(p.ID, tokens)...); err != nil {
			errs = append(errs, fmt.Errorf("herdr pane report-metadata %s: %w", p.ID, err))
		}
	}
	return errors.Join(errs...)
}

func hasOurTokens(p Pane) bool {
	for _, k := range tokenKeys {
		if _, ok := p.Tokens[k]; ok {
			return true
		}
	}
	return false
}

func reportArgs(pane string, tokens map[string]string) []string {
	args := []string{"pane", "report-metadata", pane, "--source", Source}
	for _, k := range tokenKeys {
		if v, ok := tokens[k]; ok {
			args = append(args, "--token", k+"="+v)
		} else {
			args = append(args, "--clear-token", k)
		}
	}
	return args
}

func lastUp(r stack.Record) time.Time {
	t := r.CreatedAt
	if up := r.StopAt.Add(-stack.StopAfter); !r.StopAt.IsZero() && up.After(t) {
		t = up
	}
	return t
}

func paneTokens(recs []bound, linkFor func(int) string) map[string]string {
	sort.SliceStable(recs, func(i, j int) bool {
		ti, tj := lastUp(recs[i].rec), lastUp(recs[j].rec)
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return recs[i].rec.Name < recs[j].rec.Name
	})
	rec := recs[0].rec
	name := rec.Name
	if len(recs) > 1 {
		name = fmt.Sprintf("%s +%d", rec.Name, len(recs)-1)
	}
	tokens := map[string]string{"stack": name}
	switch {
	case rec.Active:
		tokens["stack_state"] = "up"
		if !rec.StopAt.IsZero() {
			tokens["stack_stop"] = rec.StopAt.Local().Format("15:04")
		}
		if u := firstURL(rec, linkFor); u != "" {
			tokens["stack_url"] = u
		}
	case rec.StoppedBy == stack.StoppedBySweep:
		tokens["stack_state"] = "stopped-auto"
	default:
		tokens["stack_state"] = "stopped"
	}
	return tokens
}

func firstURL(rec stack.Record, linkFor func(int) string) string {
	keys := make([]string, 0, len(rec.Ports))
	for k := range rec.Ports {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if u := linkFor(rec.Ports[k]); u != "" {
			return u
		}
	}
	return ""
}

func ContextPane() string {
	var c struct {
		Pane string `json:"focused_pane_id"`
	}
	if raw := os.Getenv("HERDR_PLUGIN_CONTEXT_JSON"); raw != "" && json.Unmarshal([]byte(raw), &c) == nil && c.Pane != "" {
		return c.Pane
	}
	return os.Getenv("HERDR_PANE_ID")
}

func PaneURL(ctx context.Context, pane string) (string, error) {
	byPane, errs := loadBound()
	if err := errors.Join(errs...); err != nil {
		return "", err
	}
	recs := byPane[pane]
	if len(recs) == 0 {
		return "", fmt.Errorf("no stack is bound to herdr pane %s. In that pane, run devstack stack up <name>", pane)
	}
	if u := paneTokens(recs, LinkFor(ctx))["stack_url"]; u != "" {
		return u, nil
	}
	return "", fmt.Errorf("the stack of herdr pane %s has no tailnet address. It is down, or it publishes no service", pane)
}
