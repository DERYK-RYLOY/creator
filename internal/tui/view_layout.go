package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/DERYK-RYLOY/creator/internal/buildinfo"
)

// ---------- layout scaffolding ----------

func (m *model) bodyWidth() int {
	w := m.width
	if m.sidebar && w >= 100 {
		w -= sidebarWidth // vrule + content block
		if w < 40 {
			w = 40
		}
	}
	return w
}

func (m *model) sidebarActive() bool {
	return m.sidebar && m.width >= 100
}

func (m *model) statusLineCount() int {
	s := m.statusLine(m.width)
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func (m *model) layoutBodyHeight(statusLines int) int {
	return m.height - 1 - 1 - len(composerLines(m)) - statusLines
}

// layoutBodyHeight is the single source of truth for the transcript region's
// height; View, mouse hit-testing and scroll math all call this.
func (m *model) bodyHeight() int {
	return m.layoutBodyHeight(m.statusLineCount())
}

func (m *model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	if m.width < 12 || m.height < 4 {
		// Not enough room for any chrome — render a single-line notice
		// instead of overflowing every surface at once.
		return faint.Render("terminal too small — resize ↥")
	}
	if m.overlay != overlayNone {
		return m.overlayView(m.width, m.height)
	}

	bodyW := m.bodyWidth()
	status := m.statusLine(m.width)
	statusLines := 0
	if status != "" {
		statusLines = strings.Count(status, "\n") + 1
	}
	bodyH := m.layoutBodyHeight(statusLines)
	if bodyH < 1 {
		bodyH = 1
	}

	var body string
	if m.palette {
		body = padToHeight(renderPalette(m, bodyW, bodyH), bodyH)
	} else {
		b := m.bodyLines(bodyW, bodyH)
		if m.selecting {
			b = m.highlightSelection(b)
		}
		if m.sidebarActive() {
			b = m.joinSidebar(b, bodyH)
		}
		body = padToHeight(strings.Join(b, "\n"), bodyH)
	}
	head := headerBar(m, m.width)
	rule := faint.Render(strings.Repeat("─", max(0, m.width)))
	parts := []string{head, body, rule}
	parts = append(parts, composerLines(m)...)
	if status != "" {
		parts = append(parts, status)
	}
	return strings.Join(parts, "\n")
}

// bodyLines renders the transcript, or the welcome hero when the session is
// empty. The result is exactly bodyH rows (joinSidebar may splice it); the
// caller joins. The window is a copy — selection highlighting mutates rows in
// place and must never touch the cached lines.
func (m *model) bodyLines(bodyW, bodyH int) []string {
	if len(m.transcript) == 0 && !m.palette {
		return m.welcomeLines(bodyW, bodyH)
	}
	lines, _ := m.visibleLines(bodyW)
	win, _, _ := windowSlice(lines, m.scroll, bodyH)
	out := make([]string, len(win))
	copy(out, win)
	return out
}

// joinSidebar splices the sidebar into each body row: transcript │ sidebar
func (m *model) joinSidebar(body []string, bodyH int) []string {
	side := sidebarRows(m, bodyH)
	out := make([]string, 0, bodyH)
	bodyW := m.bodyWidth()
	for i := 0; i < bodyH; i++ {
		left := ""
		if i < len(body) {
			left = body[i]
		}
		left = padPlain(left, bodyW)
		right := vrule()
		c := strings.Repeat(" ", sideContentWidth)
		if i < len(side) {
			c = side[i]
		}
		out = append(out, left+right+c)
	}
	return out
}

// welcomeLines is the empty-session hero: the animated CREATOR wordmark
// centered, a letterspaced tagline, and a compact key map. Falls back to a
// single faint line on terminals too short for the hero.
func (m *model) welcomeLines(bodyW, bodyH int) []string {
	if bodyH < 12 {
		msg := faint.Render("describe a mission, or / for commands")
		pad := (bodyH - 1) / 2
		out := make([]string, 0, bodyH)
		for i := 0; i < pad; i++ {
			out = append(out, "")
		}
		out = append(out, strings.Repeat(" ", max(0, (bodyW-cellWidth(msg))/2))+msg)
		for len(out) < bodyH {
			out = append(out, "")
		}
		return out
	}
	heroCells := bodyW - 4
	if heroCells > heroMaxCols {
		heroCells = heroMaxCols
	}
	hero := renderWordmarkHero(m.animT, m.morph, heroCells)
	if hero == "" {
		msg := faint.Render("describe a mission, or / for commands")
		pad := (bodyH - 1) / 2
		out := make([]string, 0, bodyH)
		for i := 0; i < pad; i++ {
			out = append(out, "")
		}
		out = append(out, strings.Repeat(" ", max(0, (bodyW-cellWidth(msg))/2))+msg)
		for len(out) < bodyH {
			out = append(out, "")
		}
		return out
	}
	_, dotH, _, _ := heroDims(heroCells)
	heroRows := dotH / 4
	headroom := bodyH - heroRows - 5
	if headroom < 0 {
		headroom = 0
	}
	out := make([]string, 0, bodyH)
	top := headroom / 2
	for i := 0; i < top; i++ {
		out = append(out, "")
	}
	out = append(out, centerBlock(hero, bodyW)...)
	out = append(out, "")
	tagline := cream.Bold(true).Render(letterSpaced("MISSION CONTROL"))
	out = append(out, strings.Repeat(" ", max(0, (bodyW-cellWidth(tagline))/2))+tagline)
	out = append(out, "")
	v := faint.Render("v"+buildinfo.Version+" · ask anything, or press ") + dynStyle{name: "cream"}.Render("/") + faint.Render(" for commands")
	out = append(out, strings.Repeat(" ", max(0, (bodyW-cellWidth(v))/2))+v)
	for len(out) < bodyH {
		out = append(out, "")
	}
	return out[:bodyH]
}

func (m *model) hitExpandableAtY(y int) int {
	bodyW := m.bodyWidth()
	if bodyW <= 0 {
		return -1
	}
	lines, ranges := m.visibleLines(bodyW)
	h := m.bodyHeight()
	if len(lines) == 0 || y < 0 || y >= h {
		return -1
	}
	_, start, _ := windowSlice(lines, m.scroll, h)
	g := start + y
	for _, r := range ranges {
		if g >= r.start && g < r.end {
			switch m.transcript[r.idx].kind {
			case kindTool, kindCommand, kindAssistant, kindThinking:
				return r.idx
			}
			return -1
		}
	}
	return -1
}

func (m *model) highlightSelection(lines []string) []string {
	if !m.selecting || len(lines) == 0 {
		return lines
	}
	start, end := m.selStart, m.selCur
	if start.line > end.line || (start.line == end.line && start.col > end.col) {
		start, end = end, start
	}
	h := len(lines)
	if start.line >= h {
		return lines
	}
	if end.line >= h {
		end.line = h - 1
	}
	selected := lipgloss.NewStyle().Background(lipgloss.Color(currentTheme.selectionBg)).Foreground(lipgloss.Color(currentTheme.bg))
	for y := start.line; y <= end.line; y++ {
		if y < 0 {
			continue
		}
		plain := displayText(lines[y])
		r := []rune(plain)
		from, to := 0, len(r)
		if y == start.line {
			from = runeIndexAtCell(plain, start.col)
		}
		if y == end.line {
			to = runeIndexAtCell(plain, end.col+1)
			if to < from {
				to = from
			}
		}
		var row strings.Builder
		if from > 0 {
			row.WriteString(string(r[:from]))
		}
		if to > from {
			row.WriteString(selected.Render(string(r[from:to])))
		}
		if to < len(r) {
			row.WriteString(string(r[to:]))
		}
		lines[y] = row.String()
	}
	return lines
}

// windowSlice computes the visible window for a rendered transcript.
// scroll counts lines from the bottom (0 = follow tail). Returns the window
// (a subslice of lines — callers that mutate must copy), the start line and
// the clamped scroll.
func windowSlice(lines []string, scroll, h int) ([]string, int, int) {
	total := len(lines)
	if h <= 0 || total == 0 {
		return nil, total, 0
	}
	if total > h {
		maxScroll := total - h
		if scroll > maxScroll {
			scroll = maxScroll
		}
		if scroll < 0 {
			scroll = 0
		}
	} else {
		scroll = 0
	}
	end := total - scroll
	start := end - h
	if start < 0 {
		start = 0
	}
	return lines[start:end], start, scroll
}

// lensPlainText flattens a block to plain text for lens match testing.
func lensPlainText(b block) string {
	if b.tail == "" && b.meta == "" {
		return displayText(b.title + "\n" + b.body)
	}
	return displayText(b.title + "\n" + b.body + "\n" + b.tail)
}

// visibleLines renders (or returns the cached) full, lens-filtered transcript
// lines plus the per-block ranges used by mouse hit testing. The cache key is
// (bodyW, gen, lensIdx, search query, theme); scroll position is deliberately
// not part of the key — scrolling only slices the cached lines.
func (m *model) visibleLines(w int) ([]string, []blockRange) {
	theme := CurrentThemeSlug()
	searchKey := ""
	if m.searchActive {
		searchKey = m.searchQuery
	}
	c := &m.renderCache
	if c.valid && c.bodyW == w && c.gen == m.gen && c.lens == m.lensIdx && c.search == searchKey && c.theme == theme {
		return c.lines, c.ranges
	}
	// lensForIndex(-1) reports "off" with nil spec and nil regexp.
	spec, re := lensForIndex(m.lensIdx - 1)
	ctx := newHighlightCtx(m.lensIdx-1, re, searchKey)
	var lines []string
	var ranges []blockRange
	hidden, hits := 0, 0
	for i, b := range m.transcript {
		if spec != nil {
			n := lensHitCount(lensPlainText(b), re)
			// user turns always stay, and the live head of the conversation
			// (running tools, streaming thinking, streaming assistant) stays
			// visible even when it doesn't match — a filter must never hide
			// the work in flight.
			live := b.running || b.streaming
			if b.kind != kindUser && !live && n == 0 {
				hidden++
				continue
			}
			hits += n
		}
		start := len(lines)
		lines = append(lines, renderBlockZen(b, w, ctx)...)
		ranges = append(ranges, blockRange{start: start, end: len(lines), idx: i})
	}
	m.lensHits = hits
	m.lensHidden = hidden
	m.matchLines = nil
	if ctx.searchRe != nil {
		for li, ln := range lines {
			n := len(ctx.searchRe.FindAllStringIndex(displayText(ln), -1))
			for k := 0; k < n; k++ {
				m.matchLines = append(m.matchLines, li)
			}
		}
	}
	m.searchTotal = len(m.matchLines)
	if m.matchIdx >= m.searchTotal {
		m.matchIdx = max(m.searchTotal-1, 0)
	}
	if m.matchIdx < 0 {
		m.matchIdx = 0
	}
	m.renderCache = renderCache{valid: true, bodyW: w, gen: m.gen, lens: m.lensIdx, search: searchKey, theme: theme, lines: lines, ranges: ranges}
	return lines, ranges
}

func (m *model) transcriptLines(w, h int, scrollOut *int) []string {
	lines, _ := m.visibleLines(w)
	win, _, scroll := windowSlice(lines, m.scroll, h)
	out := make([]string, len(win))
	copy(out, win)
	if scrollOut != nil {
		*scrollOut = scroll
	}
	return out
}
