package panel

import (
	"fmt"
	"strconv"
	"strings"
)

// stateMark is the dot that carries a service's state. The colour says the
// state, and the shape says it again for a reader who can not tell the colours
// apart.
var stateMark = map[string]string{
	"running":  "●",
	"starting": "◐",
	"building": "◐",
	"erroring": "✗",
	"stopped":  "○",
	"disabled": "○",
	"down":     "·",
}

type styles struct {
	theme  Theme
	title  style
	faint  style
	text   style
	plain  style
	group  style
	stack  style
	url    style
	branch style
	failed style
	ok     style
	state  map[string]style
}

func newStyles(t Theme) styles {
	s := styles{
		theme:  t,
		title:  style{fg: t.Accent, bold: true},
		faint:  style{fg: t.Overlay0},
		text:   style{fg: t.Text},
		group:  style{fg: t.Mauve, bold: true},
		stack:  style{fg: t.Peach, bold: true},
		url:    style{fg: t.Teal},
		branch: style{fg: t.Blue},
		failed: style{fg: t.Red, bold: true},
		ok:     style{fg: t.Green},
	}
	s.state = map[string]style{
		"running":  {fg: t.Green},
		"starting": {fg: t.Yellow},
		"building": {fg: t.Yellow},
		"erroring": {fg: t.Red, bold: true},
		"stopped":  {fg: t.Overlay0},
		"disabled": {fg: t.Overlay0},
		"down":     {fg: t.Overlay0},
	}
	return s
}

func (s styles) forState(state string) style {
	if st, ok := s.state[state]; ok {
		return st
	}
	return s.faint
}

// paint carries the background of the line under construction. Every piece of a
// line ends with a reset, so each piece has to carry the background itself, or a
// selected row draws its highlight in stripes.
type paint struct {
	styles styles
	bg     string
}

func (p paint) render(st style, text string) string {
	return st.withBG(p.bg).render(text)
}

func (p paint) fill(width int) string {
	if width <= 0 {
		return ""
	}
	return p.render(p.styles.plain, strings.Repeat(" ", width))
}

func (m model) view() []string {
	if m.jump {
		return m.viewJump()
	}
	if m.overlay != nil {
		return m.viewOverlay()
	}

	lines := make([]string, 0, m.height)
	lines = append(lines, m.header())

	body := m.bodyHeight()
	cols := m.layout()
	for i := m.top; i < len(m.rows) && i < m.top+body; i++ {
		lines = append(lines, m.line(m.rows[i], i == m.cursor, cols))
	}
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	return append(lines, m.footer())
}

func (m model) header() string {
	p := m.barPaint()
	left := p.render(m.style.title, "devstack")
	if len(m.snap.Workspaces) == 1 {
		left += p.render(m.style.faint, " · ") + p.render(m.style.text, m.snap.Workspaces[0].Name)
	}

	if m.busy != "" {
		left += p.render(m.style.forState("building"), "  "+m.busy+"…")
	} else {
		running, total := m.counts()
		left += p.render(m.style.faint, fmt.Sprintf("  %d of %d services running", running, total))
	}

	right := ""
	switch {
	case m.snap.Note != "":
		right = p.render(m.style.failed, m.snap.Note)
	case !m.snap.DaemonUp:
		right = p.render(m.style.failed, "the host daemon does not answer")
	}
	return m.bar(left, right)
}

func (m model) footer() string {
	p := m.barPaint()
	if m.confirm != nil {
		return m.bar(p.render(m.style.forState("erroring"), m.confirm.prompt+"  y / n"), "")
	}
	if m.status != "" {
		st := m.style.ok
		if m.failed {
			st = m.style.failed
		}
		return m.bar(p.render(st, m.status), p.render(m.style.faint, "? keys"))
	}
	hint := "enter open · O find · y copy · s start · r restart · x stop · l logs · a all · ? keys · q quit"
	return m.bar(p.render(m.style.faint, hint), "")
}

func (m model) barPaint() paint {
	return paint{styles: m.style, bg: m.style.theme.Surface0}
}

func (m model) bar(left, right string) string {
	p := m.barPaint()
	width := max(m.width, 20)

	gap := width - displayWidth(left) - displayWidth(right) - 2
	if gap < 1 {
		right = ""
		gap = max(1, width-displayWidth(left)-1)
	}
	return p.fill(1) + left + p.fill(gap) + right + p.fill(1)
}

func (m model) counts() (int, int) {
	running, total := 0, 0
	for _, svc := range m.snap.Services() {
		total++
		if svc.Running() {
			running++
		}
	}
	return running, total
}

type layout struct {
	name   int
	group  int
	branch int
}

func (m model) layout() layout {
	out := layout{name: 12}
	for _, r := range m.rows {
		if r.kind != rowService {
			continue
		}
		out.name = max(out.name, len(r.service.Name))
		out.group = max(out.group, len(r.service.Group))
		out.branch = max(out.branch, len(r.service.Branch))
	}
	out.name = min(out.name, max(12, m.width/3))
	out.group = min(out.group, 14)
	out.branch = min(out.branch, 20)
	return out
}

func (m model) line(r row, selected bool, cols layout) string {
	p := paint{styles: m.style}
	if selected {
		p.bg = m.style.theme.Surface1
	}

	var content string
	switch r.kind {
	case rowWorkspace:
		content = p.fill(1) + p.render(m.style.title, r.label)
	case rowGroup:
		content = m.groupLine(r, p)
	default:
		content = m.serviceLine(r, p, cols)
	}

	content = truncate(content, m.width)
	if !selected {
		return content
	}
	return content + p.fill(m.width-displayWidth(content))
}

func (m model) groupLine(r row, p paint) string {
	st := m.style.group
	switch {
	case r.stack != "":
		st = m.style.stack
	case r.infra:
		st = m.style.faint
	}
	line := p.fill(2) + p.render(st, r.label)
	line += p.render(m.style.faint, fmt.Sprintf("  %d/%d", r.running, r.total))
	if r.stack != "" && !r.up {
		line += p.render(m.style.faint, "  down")
	}
	return line
}

func (m model) serviceLine(r row, p paint, cols layout) string {
	svc := r.service
	state := m.style.forState(svc.State)

	mark := stateMark[svc.State]
	if mark == "" {
		mark = "·"
	}
	line := p.fill(4) + p.render(state, mark) + p.fill(1) + p.render(m.style.text, pad(svc.Name, cols.name))

	tail, tailWidth := m.tail(r, p)
	room := m.width - 6 - cols.name - tailWidth

	add := func(width int, render func() string) {
		if width == 0 || room < width+1 {
			return
		}
		room -= width + 1
		line += p.fill(1) + render()
	}
	add(9, func() string { return p.render(state, pad(svc.State, 9)) })
	add(cols.group, func() string { return p.render(m.style.faint, pad(svc.Group, cols.group)) })
	add(cols.branch, func() string { return p.render(m.style.branch, pad(svc.Branch, cols.branch)) })
	add(7, func() string { return p.render(m.style.faint, pad(portLabel(svc.Ports), 7)) })

	return line + tail
}

func (m model) tail(r row, p paint) (string, int) {
	if url := r.url(); url != "" {
		text := url
		if extra := len(r.service.URLs) - 1; extra > 0 {
			text += fmt.Sprintf(" +%d", extra)
		}
		return p.fill(1) + p.render(m.style.url, text), len(text) + 1
	}
	text := r.service.Detail
	if text == "" && r.service.Env != "" {
		text = "env:" + r.service.Env
	}
	if text == "" {
		return "", 0
	}
	return p.fill(1) + p.render(m.style.faint, text), len(text) + 1
}

func portLabel(ports []int) string {
	if len(ports) == 0 {
		return ""
	}
	return ":" + strconv.Itoa(ports[0])
}

func (m model) viewOverlay() []string {
	p := m.barPaint()
	lines := make([]string, 0, m.height)
	lines = append(lines, m.bar(p.render(m.style.title, m.overlayTitle), ""))

	body := m.bodyHeight()
	for i := m.overlayTop; i < len(m.overlay) && i < m.overlayTop+body; i++ {
		lines = append(lines, truncate(m.overlay[i], m.width))
	}
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}

	position := ""
	if len(m.overlay) > body {
		position = p.render(m.style.faint, fmt.Sprintf("%d–%d of %d",
			m.overlayTop+1, min(m.overlayTop+body, len(m.overlay)), len(m.overlay)))
	}
	return append(lines, m.bar(p.render(m.style.faint, "↑ ↓ scroll · esc closes"), position))
}

func (m model) viewJump() []string {
	p := paint{styles: m.style, bg: m.style.theme.Surface0}

	width := min(max(40, m.width-4), 96)
	if width > m.width {
		width = max(4, m.width)
	}
	rows := min(max(3, m.height-8), 12)

	inner := max(1, width-4)
	box := []string{
		m.boxLine("┌", "─", "┐", width, p, p.render(m.style.title, " open ")),
		m.boxRow(p.render(m.style.faint, "› ")+p.render(m.style.text, truncate(m.query, inner-2))+p.render(m.style.title, "▏"), width, p),
		m.boxLine("├", "─", "┤", width, p, ""),
	}

	first := max(0, min(m.linkIndex-rows+1, len(m.links)-rows))
	if len(m.links) == 0 {
		box = append(box, m.boxRow(p.render(m.style.faint, "no address matches"), width, p))
	}
	for i := first; i < len(m.links) && i < first+rows; i++ {
		box = append(box, m.boxRow(m.linkRow(m.links[i], i == m.linkIndex, inner), width, p))
	}
	box = append(box, m.boxLine("└", "─", "┘", width, p,
		p.render(m.style.faint, " enter opens · ctrl+y copies · esc closes ")))

	return m.centre(box)
}

func (m model) linkRow(l link, selected bool, inner int) string {
	lp := paint{styles: m.style}
	if selected {
		lp.bg = m.style.theme.Surface1
	}
	mark := stateMark[l.State]
	if mark == "" {
		mark = "·"
	}

	line := lp.render(m.style.forState(l.State), mark) + lp.fill(1) +
		lp.render(m.style.text, pad(l.Label, 18)) + lp.fill(1) +
		lp.render(m.style.stack, pad(l.Group, 10)) + lp.fill(1) +
		lp.render(m.style.url, l.URL)

	line = truncate(line, inner)
	return line + lp.fill(inner-displayWidth(line))
}

func (m model) boxRow(content string, width int, p paint) string {
	inner := max(1, width-4)
	content = truncate(content, inner)
	return p.render(m.style.faint, "│") + p.fill(1) + content +
		p.fill(inner-displayWidth(content)) + p.fill(1) + p.render(m.style.faint, "│")
}

func (m model) boxLine(left, fill, right string, width int, p paint, label string) string {
	room := width - 2 - displayWidth(label)
	if room < 0 {
		label = ""
		room = max(0, width-2)
	}
	return p.render(m.style.faint, left) + label +
		p.render(m.style.faint, strings.Repeat(fill, room)) + p.render(m.style.faint, right)
}

// centre puts the picker in the middle of the pane, and leaves the rest empty.
// A box drawn over the list would have to cut coloured text apart, and a blank
// field behind it reads as the modal it is.
func (m model) centre(box []string) []string {
	top := max(0, (m.height-len(box))/2)
	lines := make([]string, 0, m.height)
	for i := 0; i < top; i++ {
		lines = append(lines, "")
	}
	indent := strings.Repeat(" ", max(0, (m.width-displayWidth(box[0]))/2))
	for _, line := range box {
		lines = append(lines, indent+line)
	}
	for len(lines) < m.height {
		lines = append(lines, "")
	}
	return lines
}

func pad(s string, width int) string {
	if len(s) >= width {
		return truncate(s, width)
	}
	return s + strings.Repeat(" ", max(0, width-len(s)))
}

func truncate(s string, width int) string {
	if width <= 0 || displayWidth(s) <= width {
		return s
	}
	var b strings.Builder
	drawn := 0
	inEscape := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEscape = true
			b.WriteRune(r)
		case inEscape:
			b.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
		case drawn < width-1:
			b.WriteRune(r)
			drawn++
		}
	}
	if drawn >= width-1 {
		b.WriteString("…")
	}
	return b.String()
}
