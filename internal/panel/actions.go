package panel

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"

	"github.com/socialviolation/devstack/internal/tilt"
	"github.com/socialviolation/devstack/internal/workspace"
)

// The browser has to outlive the panel. The link picker closes as soon as it
// opens an address, and herdr closes the pane with it, so a child that stays in
// this process group dies before the browser window appears. A new session, and
// no terminal of its own, is what keeps it alive.
func openURL(url string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}

	cmd := exec.Command(opener, url)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err == nil {
		defer devNull.Close()
		cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("can not open %s: %w", url, err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

var clipboardTools = [][]string{
	{"wl-copy"},
	{"pbcopy"},
	{"xclip", "-selection", "clipboard"},
	{"xsel", "--clipboard", "--input"},
}

// osc52 asks the terminal that draws this pane to set the clipboard. Over ssh
// it is the only mechanism that reaches the reader: wl-copy and xclip run on
// the machine the panel runs on, and they set the clipboard of that machine,
// which nobody looks at. The sequence travels back through the ssh session to
// the terminal, so the address lands on the reader's own machine.
//
// A terminal that does not support the sequence ignores it in silence, and the
// write still succeeds. So this reports success where it can not prove it.
func osc52(out io.Writer, text string) error {
	if out == nil {
		return fmt.Errorf("the panel has no terminal to copy through")
	}
	seq := fmt.Sprintf("\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(text)))
	// tmux keeps the sequence for itself unless it is wrapped. The wrapper says
	// "pass this to the terminal that holds me", and every ESC inside it has to
	// be doubled. tmux also needs set-clipboard on to forward it.
	if os.Getenv("TMUX") != "" {
		seq = "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
	}
	_, err := fmt.Fprint(out, seq)
	return err
}

// overSSH reports whether this panel draws on a terminal somewhere else.
func overSSH() bool {
	return os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CONNECTION") != ""
}

// BrowserReaches reports whether a browser started here opens on a screen the
// reader sits at. It has to be sure. Where it can not be sure it answers no,
// because a copied address is useful to a reader anywhere, and a browser window
// on the wrong machine is useful to nobody.
//
// Over ssh the answer is no: xdg-open runs here, and the window opens here.
// A machine with no display server has nowhere to put a window at all.
//
// Inside herdr the answer is also no, and the pane can not tell why. herdr runs
// a server that holds the panes, and a client attaches to it — from this
// machine, or from another one with 'herdr --remote'. The pane inherits the
// server's environment either way, so SSH_TTY and WAYLAND_DISPLAY describe the
// machine the panel runs on, and never the machine the reader sits at. herdr
// 0.8.0 does not say where its client attached from.
func BrowserReaches() bool {
	if overSSH() || os.Getenv("HERDR_ENV") != "" {
		return false
	}
	if runtime.GOOS == "darwin" {
		return true
	}
	return os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != ""
}

func copyWithTool(text string) error {
	for _, tool := range clipboardTools {
		if _, err := exec.LookPath(tool[0]); err != nil {
			continue
		}
		cmd := exec.Command(tool[0], tool[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s failed: %w", tool[0], err)
		}
		return nil
	}
	return fmt.Errorf("this machine has no clipboard command (wl-copy, pbcopy, xclip or xsel)")
}

// copyToClipboard puts text on the clipboard the reader pastes from. The
// terminal is that clipboard whether the panel runs on this machine or on one
// at the end of an ssh session, so the escape sequence goes out every time. A
// terminal that ignores it leaves nothing behind, and on this machine wl-copy
// or its like covers that. Running one of those over ssh is pointless: it would
// set the clipboard of the machine nobody looks at.
func copyToClipboard(out io.Writer, text string) error {
	oscErr := osc52(out, text)
	if !overSSH() {
		if err := copyWithTool(text); err == nil {
			return nil
		}
	}
	return oscErr
}

func runDevstack(args ...string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		self = "devstack"
	}
	out, err := exec.Command(self, args...).CombinedOutput()
	return string(out), err
}

// stackFlag names the copy a service command acts on. base is a stack name that
// every devstack command accepts, and leaving the flag out would act on the
// copy of the working directory instead.
func stackFlag(stack string) string {
	if stack == "" {
		return "base"
	}
	return stack
}

func serviceCommand(action string, r row) []string {
	return []string{"service", action, r.service.Name,
		"--stack", stackFlag(r.stack), "--workspace", r.workspace}
}

func groupCommand(action string, r row) ([]string, error) {
	if r.stack == "" {
		return nil, fmt.Errorf("base does not go up or down from the panel. Run: devstack workspace up")
	}
	verb := "up"
	if action == "stop" {
		verb = "down"
	}
	return []string{"stack", verb, r.stack, "--workspace", r.workspace}, nil
}

func serviceLogs(resource string, lines int) (string, error) {
	client := tilt.NewClient("localhost", workspace.HostTiltPort)
	out, err := client.RunCLI("logs", fmt.Sprintf("--tail=%d", lines), resource)
	if err != nil && strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("can not read the log of %s: %w", resource, err)
	}
	return out, nil
}
