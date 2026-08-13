package panel

import (
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// The panel draws the terminal itself, and asks it nothing.
//
// A TUI library that queries the terminal for its colours costs devstack every
// command, not only this one: the query runs when the process starts, and a
// terminal that does not answer it holds the whole binary. herdr's plugin panes
// answer the cursor query and not the colour one, which is exactly the case
// that never returns. So the panel writes the escape sequences it needs, and it
// reads keys. It needs no more than that.
const (
	altScreenOn  = "\x1b[?1049h"
	altScreenOff = "\x1b[?1049l"
	cursorHide   = "\x1b[?25l"
	cursorShow   = "\x1b[?25h"
	cursorHome   = "\x1b[H"
	clearScreen  = "\x1b[2J"
	clearLine    = "\x1b[K"
	sgrReset     = "\x1b[0m"
)

type screen struct {
	out     *os.File
	in      *os.File
	restore *term.State
	width   int
	height  int
	closed  bool
}

func newScreen() (*screen, error) {
	s := &screen{out: os.Stdout, in: os.Stdin, width: 80, height: 24}

	state, err := term.MakeRaw(int(s.in.Fd()))
	if err != nil {
		return nil, err
	}
	s.restore = state
	s.readSize()

	_, _ = s.out.WriteString(altScreenOn + cursorHide + clearScreen)
	return s, nil
}

// close gives the terminal back. It runs more than once: the panel closes the
// screen early when it has a message to print on the terminal it came from, and
// the deferred close still follows.
func (s *screen) close() {
	if s.closed {
		return
	}
	s.closed = true
	_, _ = s.out.WriteString(altScreenOff + cursorShow + sgrReset)
	if s.restore != nil {
		_ = term.Restore(int(s.in.Fd()), s.restore)
	}
}

func (s *screen) readSize() {
	if w, h, err := term.GetSize(int(s.out.Fd())); err == nil && w > 0 && h > 0 {
		s.width, s.height = w, h
	}
}

func (s *screen) draw(lines []string) {
	var b strings.Builder
	for i, line := range lines {
		if i >= s.height {
			break
		}
		b.WriteString("\x1b[" + strconv.Itoa(i+1) + ";1H")
		b.WriteString(line)
		b.WriteString(sgrReset)
		b.WriteString(clearLine)
	}
	b.WriteString(cursorHome)
	_, _ = s.out.WriteString(b.String())
}

// watchResize tells the loop that the terminal changed size. It reads no size
// itself: the screen belongs to the loop, and a second writer is a data race.
func watchResize(sizes chan<- struct{}) func() {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			select {
			case sizes <- struct{}{}:
			default:
			}
		}
	}()
	return func() { signal.Stop(winch); close(winch) }
}

// watchStop reports a signal that ends the panel. Raw mode and the alternate
// screen belong to the process, so a panel killed without this leaves the
// terminal with no echo and no line editing.
func watchStop() (<-chan os.Signal, func()) {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	return stop, func() { signal.Stop(stop) }
}

func readKeys(in *os.File, keys chan<- string) {
	buf := make([]byte, 64)
	for {
		n, err := in.Read(buf)
		if err != nil {
			close(keys)
			return
		}
		for _, key := range parseKeys(buf[:n]) {
			keys <- key
		}
	}
}

func parseKeys(data []byte) []string {
	var out []string
	for i := 0; i < len(data); {
		b := data[i]
		switch {
		case b == 0x1b && i+1 < len(data) && (data[i+1] == '[' || data[i+1] == 'O'):
			key, size := parseEscape(data[i:])
			if key != "" {
				out = append(out, key)
			}
			i += size
		case b == 0x1b:
			out = append(out, "escape")
			i++
		case b == '\r' || b == '\n':
			out = append(out, "enter")
			i++
		case b == 0x7f || b == 0x08:
			out = append(out, "backspace")
			i++
		case b == 0x03:
			out = append(out, "ctrl+c")
			i++
		case b < 0x20:
			out = append(out, "ctrl+"+string(rune('a'+b-1)))
			i++
		default:
			r, size := decodeRune(data[i:])
			out = append(out, string(r))
			i += size
		}
	}
	return out
}

// parseEscape reads one escape sequence. A terminal in application-cursor mode
// sends the arrow keys as SS3 (ESC O A) instead of CSI (ESC [ A), and a reader
// whose Up key quits the panel is reading SS3 as three separate keys.
func parseEscape(data []byte) (string, int) {
	if data[1] == 'O' {
		if len(data) < 3 {
			return "", len(data)
		}
		return arrowKey(data[2]), 3
	}

	end := 2
	for end < len(data) && (data[end] < 0x40 || data[end] > 0x7e) {
		end++
	}
	if end >= len(data) {
		return "", len(data)
	}
	body := string(data[2:end])
	final := data[end]
	size := end + 1

	switch final {
	case 'A', 'B', 'C', 'D':
		return arrowKey(final), size
	case 'H':
		return "home", size
	case 'F':
		return "end", size
	case '~':
		switch body {
		case "1", "7":
			return "home", size
		case "4", "8":
			return "end", size
		case "5":
			return "pgup", size
		case "6":
			return "pgdown", size
		}
	}
	return "", size
}

func arrowKey(final byte) string {
	switch final {
	case 'A':
		return "up"
	case 'B':
		return "down"
	case 'C':
		return "right"
	case 'D':
		return "left"
	}
	return ""
}

func decodeRune(data []byte) (rune, int) {
	r := []rune(string(data))
	if len(r) == 0 {
		return 0, 1
	}
	return r[0], len(string(r[0]))
}

type style struct {
	fg   string
	bg   string
	bold bool
}

func (s style) withBG(bg string) style {
	if bg != "" {
		s.bg = bg
	}
	return s
}

func (s style) render(text string) string {
	codes := make([]string, 0, 3)
	if s.bold {
		codes = append(codes, "1")
	}
	if code := sgrColour(s.fg, false); code != "" {
		codes = append(codes, code)
	}
	if code := sgrColour(s.bg, true); code != "" {
		codes = append(codes, code)
	}
	if len(codes) == 0 {
		return text
	}
	return "\x1b[" + strings.Join(codes, ";") + "m" + text + sgrReset
}

func sgrColour(value string, background bool) string {
	if value == "" {
		return ""
	}
	layer := "38"
	if background {
		layer = "48"
	}
	if strings.HasPrefix(value, "#") {
		r, g, b, ok := parseHex(value)
		if !ok {
			return ""
		}
		return layer + ";2;" + strconv.Itoa(r) + ";" + strconv.Itoa(g) + ";" + strconv.Itoa(b)
	}
	if n, err := strconv.Atoi(value); err == nil && n >= 0 && n <= 255 {
		return layer + ";5;" + value
	}
	return ""
}

func parseHex(value string) (int, int, int, bool) {
	hex := strings.TrimPrefix(value, "#")
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	if len(hex) != 6 {
		return 0, 0, 0, false
	}
	n, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(n >> 16 & 0xff), int(n >> 8 & 0xff), int(n & 0xff), true
}

func displayWidth(s string) int {
	width := 0
	inEscape := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEscape = true
		case inEscape:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
		default:
			width++
		}
	}
	return width
}
