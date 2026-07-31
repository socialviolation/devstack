package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/socialviolation/devstack/internal/config"
)

// Result is what one invocation did.
type Result struct {
	Invocation Invocation
	Duration   time.Duration
	Err        error
	// Aborted is true when Err was fatal under the hook's error policy and the
	// remaining hooks were not run.
	Aborted bool
}

// shell is the interpreter hook commands run under. Overridden in tests.
var shell = "/bin/sh"

// Run executes every hook the event fires, in resolution order, streaming each
// one's output to w prefixed with its label.
//
// A hook that fails under the "abort" policy stops the run and is returned as an
// error. Under "continue" the failure is reported and the next hook runs, which
// is the default for teardown events.
//
// The returned error says the hook chain stopped. It does not say the lifecycle
// action must stop. A setup caller fails on it, because a stack whose
// provisioning failed is worse than no stack. A teardown caller reports it and
// proceeds: a broken hook must never leave a stack that cannot be removed.
func Run(ev Event, src Source, w io.Writer) ([]Result, error) {
	invocations := Resolve(ev, src)
	if len(invocations) == 0 {
		return nil, nil
	}

	results := make([]Result, 0, len(invocations))
	var firstFatal error
	for _, inv := range invocations {
		start := time.Now()
		err := execute(ev, src, inv, w)
		res := Result{Invocation: inv, Duration: time.Since(start), Err: err}

		if err != nil && inv.Hook.ResolvedOnError(ev.Name) == config.OnErrorAbort {
			res.Aborted = true
			results = append(results, res)
			firstFatal = fmt.Errorf("hook %q (%s) failed on %s: %w", inv.Label(), inv.Origin, ev.Name, err)
			break
		}
		if err != nil {
			fmt.Fprintf(w, "warning: hook %q (%s) failed on %s, continuing: %v\n", inv.Label(), inv.Origin, ev.Name, err)
			writeUnwindHint(w, ev, inv)
		}
		results = append(results, res)
	}
	return results, firstFatal
}

// writeUnwindHint says what a failed teardown hook leaves behind and whether it
// can still be retried. stack.destroy is the one event with no retry: the record
// it resolves ${self...} against is deleted moments later, so by the time anyone
// reads the failure the context is gone. Printing the resolved values here is
// the only chance to make the cleanup mechanical rather than archaeological.
func writeUnwindHint(w io.Writer, ev Event, inv Invocation) {
	if !config.IsTeardownEvent(ev.Name) {
		return
	}
	fmt.Fprintf(w, "  whatever %q was cleaning up outside this machine is probably still there.\n", inv.Label())

	if ev.Name != config.EventStackDestroy {
		fmt.Fprintf(w, "  retry it once fixed: devstack hooks run %s%s\n", ev.Name, stackFlag(ev))
		return
	}
	fmt.Fprintf(w, "  this CANNOT be retried: removing the stack deletes the record that ${self...} resolves against.\n")
	fmt.Fprintf(w, "  clean it up by hand. Stack %q was serving:\n", ev.StackLabel())
	for _, name := range sortedCopy(ev.Services) {
		if port, ok := ev.Book[name]["http"]; ok {
			fmt.Fprintf(w, "    %-24s http://%s:%d\n", name, ev.Book.Host(name), port)
		}
	}
}

func stackFlag(ev Event) string {
	if strings.TrimSpace(ev.Stack) == "" {
		return ""
	}
	return " --stack " + ev.Stack
}

func execute(ev Event, src Source, inv Invocation, w io.Writer) error {
	command, err := inv.expand(inv.Hook.Run, ev.Book)
	if err != nil {
		return err
	}
	dir, err := inv.expand(inv.resolveDir(), ev.Book)
	if err != nil {
		return err
	}
	environ, err := inv.environ(ev, src)
	if err != nil {
		return err
	}
	payload, err := inv.payload(ev, src)
	if err != nil {
		return err
	}

	timeout := inv.Hook.ResolvedTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	prefix := newPrefixWriter(w, "hook "+inv.Label()+" │ ")
	defer prefix.Close()

	cmd := exec.CommandContext(ctx, shell, "-c", command)
	cmd.Dir = dir
	cmd.Env = environ
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stdout = prefix
	cmd.Stderr = prefix

	fmt.Fprintf(w, "→ hook %s (%s) on %s\n", inv.Label(), inv.Origin, ev.Name)
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("timed out after %s", timeout)
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return fmt.Errorf("exit status %d", exitErr.ExitCode())
		}
		return runErr
	}
	return nil
}

// environ is the hook process's environment: the caller's, then devstack's
// context variables, then the hook's own env — so a hook can override anything
// devstack sets, and devstack can override the ambient shell.
func (inv Invocation) environ(ev Event, src Source) ([]string, error) {
	vars := map[string]string{
		"DEVSTACK_HOOK_EVENT":     ev.Name,
		"DEVSTACK_HOOK_NAME":      inv.Hook.Name,
		"DEVSTACK_WORKSPACE":      ev.WorkspaceName,
		"DEVSTACK_WORKSPACE_ROOT": src.WorkspaceRoot,
		"DEVSTACK_STACK":          ev.StackLabel(),
		"DEVSTACK_STACK_ROOT":     ev.StackRoot,
		"DEVSTACK_STACK_BRANCH":   ev.Branch,
		"DEVSTACK_ENV":            ev.EnvName,
		"DEVSTACK_SERVICES":       strings.Join(sortedCopy(ev.Services), ","),
	}
	if inv.Service != "" {
		vars["DEVSTACK_SERVICE"] = inv.Service
		vars["DEVSTACK_SERVICE_PATH"] = src.Services[inv.Service].Path
		if port, ok := ev.Book[inv.Service]["http"]; ok {
			vars["DEVSTACK_SERVICE_PORT"] = strconv.Itoa(port)
			vars["DEVSTACK_SERVICE_URL"] = fmt.Sprintf("http://%s:%d", ev.Book.Host(inv.Service), port)
		}
	}

	environ := os.Environ()
	for _, k := range sortedKeys(vars) {
		environ = append(environ, k+"="+vars[k])
	}
	for _, k := range sortedKeys(inv.Hook.Env) {
		v, err := inv.expand(inv.Hook.Env[k], ev.Book)
		if err != nil {
			return nil, fmt.Errorf("hook %q env %s: %w", inv.Hook.Name, k, err)
		}
		environ = append(environ, k+"="+v)
	}
	return environ, nil
}

type payloadService struct {
	Path  string         `json:"path"`
	Ports map[string]int `json:"ports,omitempty"`
	URL   string         `json:"url,omitempty"`
}

type payload struct {
	Event     string                    `json:"event"`
	Hook      string                    `json:"hook"`
	Workspace map[string]string         `json:"workspace"`
	Stack     map[string]any            `json:"stack"`
	Env       string                    `json:"env,omitempty"`
	Service   string                    `json:"service,omitempty"`
	Services  map[string]payloadService `json:"services"`
}

// payload is the JSON devstack writes to a hook's stdin. It carries everything
// the context variables carry plus the full port map for every service in the
// event, which flat variables cannot express.
func (inv Invocation) payload(ev Event, src Source) ([]byte, error) {
	services := map[string]payloadService{}
	for _, name := range ev.Services {
		ps := payloadService{Path: src.Services[name].Path, Ports: ev.Book[name]}
		if port, ok := ev.Book[name]["http"]; ok {
			ps.URL = fmt.Sprintf("http://%s:%d", ev.Book.Host(name), port)
		}
		services[name] = ps
	}
	p := payload{
		Event:     ev.Name,
		Hook:      inv.Hook.Name,
		Workspace: map[string]string{"name": ev.WorkspaceName, "root": src.WorkspaceRoot},
		Stack: map[string]any{
			"name":   ev.StackLabel(),
			"root":   ev.StackRoot,
			"branch": ev.Branch,
			"isBase": strings.TrimSpace(ev.Stack) == "",
		},
		Env:      ev.EnvName,
		Service:  inv.Service,
		Services: services,
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("failed to encode hook payload: %w", err)
	}
	return append(data, '\n'), nil
}

// prefixWriter prefixes every line a hook writes, so interleaved output from
// several hooks stays attributable.
type prefixWriter struct {
	w      io.Writer
	prefix string
	buf    bytes.Buffer
}

func newPrefixWriter(w io.Writer, prefix string) *prefixWriter {
	return &prefixWriter{w: w, prefix: prefix}
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.buf.Write(b)
	for {
		line, err := p.buf.ReadString('\n')
		if err != nil {
			p.buf.WriteString(line)
			break
		}
		if _, werr := io.WriteString(p.w, p.prefix+line); werr != nil {
			return 0, werr
		}
	}
	return len(b), nil
}

// Close flushes a trailing line the hook left without a newline.
func (p *prefixWriter) Close() error {
	if p.buf.Len() == 0 {
		return nil
	}
	_, err := io.WriteString(p.w, p.prefix+p.buf.String()+"\n")
	p.buf.Reset()
	return err
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
