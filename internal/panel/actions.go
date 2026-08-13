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
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	_, err := fmt.Fprintf(out, "\x1b]52;c;%s\a", encoded)
	return err
}

// overSSH reports whether this panel draws on a terminal somewhere else.
func overSSH() bool {
	return os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CONNECTION") != ""
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

// copyToClipboard puts text on the clipboard the reader can paste from. Over
// ssh that is the terminal's clipboard, and on this machine it is the local
// one. Each mechanism is the fallback of the other.
func copyToClipboard(out io.Writer, text string) error {
	if overSSH() {
		if err := osc52(out, text); err == nil {
			return nil
		}
	}
	if err := copyWithTool(text); err == nil {
		return nil
	}
	return osc52(out, text)
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
