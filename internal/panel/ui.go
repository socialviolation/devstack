package panel

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const refreshInterval = 3 * time.Second

type Options struct {
	Workspace string
	Jump      bool
	// EnterCopies makes the enter key copy an address instead of opening it in
	// a browser. A reader who works over ssh sets it: a browser started here
	// opens on the machine the panel runs on, where nobody sees it.
	EnterCopies bool
}

type model struct {
	theme  Theme
	style  styles
	focus  string
	snap   Snapshot
	rows   []row
	cursor int
	top    int

	width  int
	height int

	showAll bool
	// loaded reports that the first reading of the machine arrived. Before it,
	// the panel knows nothing, and it must not report the daemon as down.
	loaded bool
	status string
	failed bool
	busy   string

	overlay      []string
	overlayTitle string
	overlayTop   int
	confirm      *confirmation

	jump      bool
	jumpOnly  bool
	query     string
	links     []link
	linkIndex int

	enterCopies bool
	// out is the terminal the panel draws on. A copy over ssh writes an escape
	// sequence to it, so the address reaches the reader's own clipboard.
	out io.Writer
	// parting is what the panel prints after it gives the terminal back. The
	// picker closes the moment it copies an address, and a message drawn on the
	// alternate screen goes with it.
	parting string
}

type confirmation struct {
	prompt string
	title  string
	args   []string
}

type commandResult struct {
	title string
	out   string
	err   error
}

func Run(opts Options) error {
	scr, err := newScreen()
	if err != nil {
		return fmt.Errorf("the panel needs a terminal: %w", err)
	}
	defer scr.close()

	m := &model{
		theme:       LoadTheme(),
		focus:       opts.Workspace,
		width:       scr.width,
		height:      scr.height,
		jump:        opts.Jump,
		jumpOnly:    opts.Jump,
		enterCopies: opts.EnterCopies,
		out:         scr.out,
	}
	m.style = newStyles(m.theme)
	defer func() {
		if m.parting != "" {
			scr.close()
			fmt.Fprintln(os.Stdout, m.parting)
		}
	}()

	keys := make(chan string, 16)
	sizes := make(chan struct{}, 1)
	snapshots := make(chan Snapshot, 1)
	results := make(chan commandResult, 4)

	go readKeys(scr.in, keys)
	stopResize := watchResize(sizes)
	defer stopResize()
	stopping, stopWatching := watchStop()
	defer stopWatching()

	refresh := func() {
		go func() { snapshots <- Take(context.Background()) }()
	}
	refresh()

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	scr.draw(m.view())
	for {
		select {
		case key, ok := <-keys:
			if !ok {
				return nil
			}
			if quit := m.handleKey(key, results); quit {
				return nil
			}
		case <-ticker.C:
			refresh()
		case <-stopping:
			return nil
		case snap := <-snapshots:
			selected := m.selectedKey()
			m.snap = keepWorkspace(snap, m.focus)
			m.loaded = true
			m.rebuild(selected)
			m.refilterLinks()
		case result := <-results:
			m.showResult(result)
			refresh()
		case <-sizes:
			scr.readSize()
			m.width, m.height = scr.width, scr.height
			m.clampScroll()
		}
		scr.draw(m.view())
	}
}

func (m *model) showResult(result commandResult) {
	m.busy = ""
	m.overlayTitle = result.title
	m.overlay = strings.Split(strings.TrimRight(result.out, "\n"), "\n")
	m.overlayTop = 0
	if len(m.overlay) == 1 && m.overlay[0] == "" {
		m.overlay = nil
	}
	if result.err != nil {
		m.setStatus(fmt.Sprintf("%s: %v", result.title, result.err), true)
		return
	}
	m.setStatus(result.title+" is complete", false)
}

func (m *model) run(title string, args []string, results chan<- commandResult) {
	m.busy = title
	go func() {
		out, err := runDevstack(args...)
		results <- commandResult{title: title, out: out, err: err}
	}()
}

func (m *model) runLogs(r row, results chan<- commandResult) {
	title := "logs " + r.service.Resource
	m.busy = title
	go func() {
		out, err := serviceLogs(r.service.Resource, 200)
		results <- commandResult{title: title, out: out, err: err}
	}()
}

func (m *model) handleKey(key string, results chan<- commandResult) bool {
	if m.jump {
		return m.handleJumpKey(key)
	}

	if m.confirm != nil {
		switch key {
		case "y", "Y", "enter":
			c := m.confirm
			m.confirm = nil
			m.run(c.title, c.args, results)
		default:
			m.confirm = nil
			m.setStatus("cancelled", false)
		}
		return false
	}

	if m.overlay != nil {
		switch key {
		case "escape", "q", "enter":
			m.overlay = nil
		case "up", "k":
			m.overlayTop = max(0, m.overlayTop-1)
		case "down", "j":
			m.overlayTop = min(max(0, len(m.overlay)-1), m.overlayTop+1)
		case "pgup":
			m.overlayTop = max(0, m.overlayTop-m.bodyHeight())
		case "pgdown", " ":
			m.overlayTop = min(max(0, len(m.overlay)-1), m.overlayTop+m.bodyHeight())
		case "ctrl+c":
			return true
		}
		return false
	}

	switch key {
	case "q", "ctrl+c":
		return true
	case "escape":
		m.setStatus("", false)
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup":
		m.move(-m.bodyHeight())
	case "pgdown", " ":
		m.move(m.bodyHeight())
	case "g", "home":
		m.cursor = 0
		m.move(1)
		m.move(-1)
	case "G", "end":
		m.cursor = len(m.rows) - 1
		m.move(-1)
		m.move(1)
	case "a":
		m.showAll = !m.showAll
		m.rebuild(m.selectedKey())
		if m.showAll {
			m.setStatus("the panel shows every service", false)
		} else {
			m.setStatus("the panel shows what is up", false)
		}
	case "?":
		m.overlayTitle = "keys"
		m.overlay = helpLines(m.enterCopies)
		m.overlayTop = 0
	case "enter":
		if m.enterCopies {
			m.copySelected()
		} else {
			m.openSelected()
		}
	case "o":
		m.openSelected()
	case "O":
		m.openJump()
	case "y":
		m.copySelected()
	case "s", "r", "x", "l":
		m.act(key, results)
	}
	return false
}

func (m *model) act(key string, results chan<- commandResult) {
	r, ok := m.selected()
	if !ok {
		return
	}

	if r.service.Infra || (r.kind == rowGroup && r.infra) {
		m.setStatus("the panel does not start or stop the machine and containers rows", true)
		return
	}

	if r.kind == rowGroup {
		switch key {
		case "s", "r":
			args, err := groupCommand("start", r)
			if err != nil {
				m.setStatus(err.Error(), true)
				return
			}
			m.run("stack up "+r.stack, args, results)
		case "x":
			args, err := groupCommand("stop", r)
			if err != nil {
				m.setStatus(err.Error(), true)
				return
			}
			m.confirm = &confirmation{
				prompt: fmt.Sprintf("stop every service of stack %s?", r.stack),
				title:  "stack down " + r.stack,
				args:   args,
			}
		default:
			m.setStatus("that key acts on a service", true)
		}
		return
	}

	if key == "l" {
		if r.service.State == "down" {
			m.setStatus("the stack of this copy is down, so it has no log", true)
			return
		}
		m.runLogs(r, results)
		return
	}

	action := map[string]string{"s": "start", "r": "restart", "x": "stop"}[key]
	m.run(action+" "+r.service.Name, serviceCommand(action, r), results)
}

func (m *model) openJump() {
	m.jump = true
	m.query = ""
	m.linkIndex = 0
	m.refilterLinks()
	if len(m.links) == 0 {
		m.jump = false
		m.setStatus("nothing here has an address to open", true)
	}
}

func (m *model) handleJumpKey(key string) bool {
	switch key {
	case "escape", "ctrl+c":
		if m.jumpOnly {
			return true
		}
		m.jump = false
		return false
	case "enter":
		if m.enterCopies {
			return m.useLink(copyLink)
		}
		return m.useLink(openLink)
	case "ctrl+o":
		return m.useLink(openLink)
	case "ctrl+y":
		return m.useLink(copyLink)
	case "up", "ctrl+p":
		m.linkIndex = max(0, m.linkIndex-1)
	case "down", "ctrl+n":
		m.linkIndex = min(max(0, len(m.links)-1), m.linkIndex+1)
	case "backspace":
		if r := []rune(m.query); len(r) > 0 {
			m.query = string(r[:len(r)-1])
			m.refilterLinks()
		}
	default:
		if len([]rune(key)) == 1 {
			m.query += key
			m.refilterLinks()
		}
	}
	return false
}

type linkAction int

const (
	openLink linkAction = iota
	copyLink
)

func (m *model) useLink(action linkAction) bool {
	if m.linkIndex >= len(m.links) {
		m.setStatus("no address matches", true)
		return false
	}
	picked := m.links[m.linkIndex]

	var err error
	verb := "opened "
	if action == copyLink {
		verb = "copied "
		err = copyToClipboard(m.out, picked.URL)
	} else {
		err = openURL(picked.URL)
	}
	if err != nil {
		m.setStatus(err.Error(), true)
		return false
	}

	m.setStatus(verb+picked.URL, false)
	if m.jumpOnly {
		// The picker closes here, and the status bar goes with it. The reader
		// needs to see which address they took, so it goes to the terminal
		// underneath instead.
		m.parting = verb + picked.URL
		return true
	}
	m.jump = false
	return false
}

// refilterLinks rebuilds the list under the picker, and keeps the cursor on the
// address it was on. A service that finishes starting reorders the list, and a
// cursor that holds its position by number opens whatever moved under it.
func (m *model) refilterLinks() {
	if !m.jump {
		return
	}
	picked := ""
	if m.linkIndex < len(m.links) {
		picked = m.links[m.linkIndex].URL
	}

	m.links = filterLinks(collectLinks(m.snap), m.query)
	for i, l := range m.links {
		if l.URL == picked {
			m.linkIndex = i
			return
		}
	}
	if m.linkIndex >= len(m.links) {
		m.linkIndex = max(0, len(m.links)-1)
	}
}

func (m *model) openSelected() {
	r, ok := m.selected()
	if !ok {
		return
	}
	url := r.url()
	if url == "" {
		m.setStatus("this row has no address", true)
		return
	}
	if err := openURL(url); err != nil {
		m.setStatus(err.Error(), true)
		return
	}
	m.setStatus("opened "+url, false)
}

func (m *model) copySelected() {
	r, ok := m.selected()
	if !ok {
		return
	}
	url := r.url()
	if url == "" {
		m.setStatus("this row has no address", true)
		return
	}
	if err := copyToClipboard(m.out, url); err != nil {
		m.setStatus(err.Error(), true)
		return
	}
	m.setStatus("copied "+url, false)
}

func (m *model) setStatus(text string, failed bool) {
	m.status = text
	m.failed = failed
}

func (m *model) rebuild(keepKey string) {
	m.rows = buildRows(m.snap, m.showAll)
	if keepKey != "" {
		for i, r := range m.rows {
			if r.key() == keepKey {
				m.cursor = i
				m.clampScroll()
				return
			}
		}
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if !m.rowSelectable(m.cursor) {
		m.move(1)
	}
	m.clampScroll()
}

func (m model) rowSelectable(i int) bool {
	return i >= 0 && i < len(m.rows) && m.rows[i].selectable()
}

func (m *model) move(delta int) {
	if len(m.rows) == 0 {
		return
	}
	step := 1
	if delta < 0 {
		step = -1
	}
	remaining := delta
	if remaining < 0 {
		remaining = -remaining
	}
	i := m.cursor
	for remaining > 0 {
		next := i + step
		for next >= 0 && next < len(m.rows) && !m.rows[next].selectable() {
			next += step
		}
		if next < 0 || next >= len(m.rows) {
			break
		}
		i = next
		remaining--
	}
	m.cursor = i
	m.clampScroll()
}

func (m *model) clampScroll() {
	height := m.bodyHeight()
	if height <= 0 {
		return
	}
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+height {
		m.top = m.cursor - height + 1
	}
	if m.top < 0 {
		m.top = 0
	}
}

func (m model) selected() (row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return row{}, false
	}
	return m.rows[m.cursor], true
}

func (m model) selectedKey() string {
	if r, ok := m.selected(); ok {
		return r.key()
	}
	return ""
}

func (m model) bodyHeight() int {
	return max(1, m.height-2)
}

func helpLines(enterCopies bool) []string {
	enter := "  enter        open the address in the browser"
	if enterCopies {
		enter = "  enter        copy the address"
	}
	return []string{
		"  ↑ ↓ / j k    move",
		enter,
		"  o            open the address in the browser",
		"  O            find an address by name",
		"  y            copy the address",
		"  s            start the service, or bring the stack up",
		"  r            restart the service",
		"  x            stop the service, or take the stack down",
		"  l            read the process log",
		"  a            show every service, or only what is up",
		"  ?            these keys",
		"  q            quit",
		"",
		"",
		"  In the address picker: enter takes the address, ctrl+o opens it,",
		"  ctrl+y copies it.",
		"  enter decides for itself: it opens here, and it copies over ssh,",
		"  because a browser started over ssh opens where you do not sit.",
		"  To decide yourself: devstack panel --enter open|copy.",
		"",
		"  A row with no address is not published on the tailnet.",
		"  The panel shows the workspace of the directory it opens in.",
		"  The panel does not start or stop the machine and containers rows.",
		"  The panel reads the machine again every few seconds.",
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
