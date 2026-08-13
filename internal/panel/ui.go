package panel

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const refreshInterval = 3 * time.Second

type Options struct {
	Workspace string
	Jump      bool
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
	status  string
	failed  bool
	busy    string

	overlay      []string
	overlayTitle string
	overlayTop   int
	confirm      *confirmation

	jump      bool
	jumpOnly  bool
	query     string
	links     []link
	linkIndex int
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
		theme:    LoadTheme(),
		focus:    opts.Workspace,
		width:    scr.width,
		height:   scr.height,
		jump:     opts.Jump,
		jumpOnly: opts.Jump,
	}
	m.style = newStyles(m.theme)

	keys := make(chan string, 16)
	sizes := make(chan struct{}, 1)
	snapshots := make(chan Snapshot, 1)
	results := make(chan commandResult, 4)

	go readKeys(scr.in, keys)
	stopResize := watchResize(scr, sizes)
	defer stopResize()

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
		case snap := <-snapshots:
			selected := m.selectedKey()
			m.snap = keepWorkspace(snap, m.focus)
			m.rebuild(selected)
			m.refilterLinks()
		case result := <-results:
			m.showResult(result)
			refresh()
		case <-sizes:
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
	m.setStatus(result.title+" finished", false)
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
	case "q", "ctrl+c", "escape":
		return true
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
			m.setStatus("showing every service", false)
		} else {
			m.setStatus("showing what is up", false)
		}
	case "?":
		m.overlayTitle = "keys"
		m.overlay = helpLines()
		m.overlayTop = 0
	case "enter", "o":
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
		m.setStatus("the panel does not start or stop infrastructure", true)
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
		m.setStatus("no link matches", true)
		return false
	}
	picked := m.links[m.linkIndex]

	var err error
	verb := "opened "
	if action == copyLink {
		verb = "copied "
		err = copyToClipboard(picked.URL)
	} else {
		err = openURL(picked.URL)
	}
	if err != nil {
		m.setStatus(err.Error(), true)
		return false
	}

	m.setStatus(verb+picked.URL, false)
	if m.jumpOnly {
		return true
	}
	m.jump = false
	return false
}

func (m *model) refilterLinks() {
	if !m.jump {
		return
	}
	m.links = filterLinks(collectLinks(m.snap), m.query)
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
	if err := copyToClipboard(url); err != nil {
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

func helpLines() []string {
	return []string{
		"  ↑ ↓ / j k    move",
		"  enter        open the address in the browser",
		"  O            find an address by name, and open it",
		"  y            copy the address",
		"  s            start the service, or bring the stack up",
		"  r            restart the service",
		"  x            stop the service, or take the stack down",
		"  l            read the process log",
		"  a            show every service, or only what is up",
		"  ?            these keys",
		"  q            quit",
		"",
		"  A row with no address is not published on the tailnet.",
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
