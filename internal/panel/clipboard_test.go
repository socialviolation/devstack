package panel

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestOSC52CarriesTheAddressToTheTerminal(t *testing.T) {
	var out strings.Builder
	const url = "https://omarchy.tailde366c.ts.net:8443"

	if err := osc52(&out, url); err != nil {
		t.Fatalf("osc52(): %v", err)
	}

	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(url)) + "\a"
	if out.String() != want {
		t.Fatalf("osc52() wrote %q, want %q", out.String(), want)
	}
}

func TestCopyOverSSHWritesToTheTerminalAndNotThisMachine(t *testing.T) {
	t.Setenv("SSH_TTY", "/dev/pts/3")
	var out strings.Builder

	if err := copyToClipboard(&out, "https://example.test"); err != nil {
		t.Fatalf("copyToClipboard(): %v", err)
	}

	// wl-copy and xclip set the clipboard of the machine the panel runs on, and
	// over ssh nobody looks at that one. The escape sequence is the only path
	// back to the reader, so it has to be taken first and not as a fallback.
	if !strings.HasPrefix(out.String(), "\x1b]52;c;") {
		t.Fatalf("over ssh the panel wrote %q, want an OSC 52 sequence", out.String())
	}
}

func TestCopyWithNoTerminalAndNoToolSaysSo(t *testing.T) {
	t.Setenv("SSH_TTY", "")
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("PATH", t.TempDir())

	err := copyToClipboard(nil, "https://example.test")
	if err == nil {
		t.Fatal("expected an error when there is no clipboard tool and no terminal")
	}
}

func TestPlainSequenceWhereThereIsNoTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	var out strings.Builder

	if err := osc52(&out, "x"); err != nil {
		t.Fatalf("osc52(): %v", err)
	}
	// herdr and every bare terminal read the sequence as it is. Only tmux needs
	// the wrapper, and a wrapper sent to the others is rubbish on the screen.
	if strings.Contains(out.String(), "\x1bPtmux;") {
		t.Fatalf("osc52() wrapped the sequence for tmux outside tmux: %q", out.String())
	}
}

func TestTmuxGetsTheWrapper(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,123,0")
	var out strings.Builder

	if err := osc52(&out, "x"); err != nil {
		t.Fatalf("osc52(): %v", err)
	}
	if !strings.HasPrefix(out.String(), "\x1bPtmux;") || !strings.HasSuffix(out.String(), "\x1b\\") {
		t.Fatalf("osc52() in tmux wrote %q, want the passthrough wrapper", out.String())
	}
}

func TestBrowserDoesNotReachTheReaderInsideHerdr(t *testing.T) {
	t.Setenv("SSH_TTY", "")
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	t.Setenv("HERDR_ENV", "1")

	// The herdr client attaches from anywhere, and the pane inherits the
	// server's environment. A display here proves nothing about the reader.
	if BrowserReaches() {
		t.Fatal("BrowserReaches() said yes inside herdr, where the reader can be on another machine")
	}
}
