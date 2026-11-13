package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

func (m *model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	if m.overlay != overlayNone {
		return m.overlayView(m.width, m.height)
	}

	inputLines := composerLines(m)
	status := m.statusLine(m.width)
	statusLines := 0
	if status != "" {
		statusLines = strings.Count(status, "\n") + 1
	}
	bodyH := m.bodyHeightWithStatus(statusLines)
	if bodyH < 1 {
		bodyH = 1
	}
	scrollOut := new(int)

	var body string
	if m.palette {
		body = padToHeight(renderPalette(m, m.width, bodyH), bodyH)
	} else {
		b := m.transcriptLines(m.width, bodyH, scrollOut)
		if m.selecting {
			b = m.highlightSelection(b)
		}
		body = padToHeight(strings.Join(b, "\n"), bodyH)
	}
	rule := faint.Render(strings.Repeat("─", max(0, m.width)))
	parts := []string{body, rule}
	parts = append(parts, inputLines...)
	if status != "" {
		parts = append(parts, status)
	}
	return strings.Join(parts, "\n")
}

func (m *model) hitExpandableAtY(y int) int {
	if m.width <= 0 {
		return -1
	}
	type hitRange struct{ start, end, index int }
	var hits []hitRange
	line := 0
	for i, b := range m.transcript {
		n := len(renderBlockZen(b, m.width))
		end := line + n
		switch b.kind {
		case kindTool, kindCommand, kindAssistant, kindThinking:
			if n > 0 {
				hits = append(hits, hitRange{start: line, end: end, index: i})
			}
		}
		line = end
	}
	total := line
	if total <= 0 || y < 0 || y >= m.bodyHeight() {
		return -1
	}
	start := 0
	if total > m.bodyHeight() {
		maxScroll := total - m.bodyHeight()
		scroll := m.scroll
		if scroll > maxScroll {
			scroll = maxScroll
		}
		if scroll < 0 {
			scroll = 0
		}
		start = total - scroll - m.bodyHeight()
		if start < 0 {
			start = 0
		}
	}
	global := start + y
	for _, h := range hits {
		if global >= h.start && global < h.end {
			return h.index
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

func (m *model) bodyHeight() int {
	inputLines := composerLines(m)
	status := m.statusLine(m.width)
	statusLines := 0
	if status != "" {
		statusLines = strings.Count(status, "\n") + 1
	}
	return m.height - len(inputLines) - 1 - statusLines
}

func (m *model) bodyHeightWithStatus(statusLines int) int {
	inputLines := composerLines(m)
	return m.height - len(inputLines) - 1 - statusLines
}

func (m *model) transcriptLines(w, h int, scrollOut *int) []string {
	var lines []string
	for _, b := range m.transcript {
		lines = append(lines, renderBlockZen(b, w)...)
	}
	total := len(lines)
	scroll := m.scroll
	if total > h {
		maxScroll := total - h
		if scroll > maxScroll {
			scroll = maxScroll
		}
		if scroll < 0 {
			scroll = 0
		}
		end := total - scroll
		start := end - h
		if start < 0 {
			start = 0
		}
		lines = lines[start:end]
	} else {
		scroll = 0
	}
	if scrollOut != nil {
		*scrollOut = scroll
	}
	return lines
}

func renderBlockZen(b block, w int) []string {
	switch b.kind {
	case kindUser:
		return append([]string{""}, wrapLines("❯ "+b.body, w-2, "", true)...)
	case kindThinking:
		body := strings.TrimSpace(b.body)
		header := faint.Render("thinking" + thinkingDots(b))
		if body == "" {
			return []string{header}
		}
		return append([]string{header}, wrapLines(body, w-4, "  ", false)...)
	case kindAssistant:
		return append([]string{""}, wrapLines(b.body, w-2, "", true)...)
	case kindTool:
		if !heavyTool(b.title) {
			return renderToolCompact(b)
		}
		return append([]string{""}, renderToolBox(b, w)...)
	case kindCommand:
		return append([]string{""}, renderCommandBox(b, w)...)
	case kindNotice:
		line := firstLineZen(b.body, 180)
		if !b.ok {
			return []string{bad.Render("✗ " + b.title + " · " + line)}
		}
		return []string{good.Render("✓ " + b.title + " · " + line)}
	default:
		return wrapLines(b.body, w-2, "", true)
	}
}

type styledLine struct {
	text  string
	style dynStyle
}

func renderToolBox(b block, w int) []string {
	tool := strings.ToLower(strings.TrimSpace(b.title))
	if tool == "" {
		tool = "tool"
	}
	border := toolBorderColor(b)
	dot := statusGlyph(b.running, b.ok)
	status := strings.TrimSpace(b.meta)
	if status == "" {
		if b.running {
			status = "running"
		} else if b.ok {
			status = "done"
		} else {
			status = "failed"
		}
	}
	if !b.ok && status == "" {
		status = "failed"
	}
	metaParts := []string{status}
	header := dot + " " + tool + " · " + strings.Join(metaParts, " · ")

	lines := []styledLine{{text: header, style: dynStyle{name: border}}}

	subject := toolSubjectLine(tool, b.body)
	if subject != "" {
		lines = append(lines, styledLine{text: subject, style: cream})
	}
	head := lines
	splitRows := splitToolDiff(b, w)
	if splitRows != nil {
		return minimalBoxMixed(splitBoxWidth(w), border, head, splitRows, toolTailLines(b))
	}

	lines = append(lines, toolCodePreview(tool, b.body, b.expanded)...)

	lines = append(lines, toolTailLines(b)...)

	boxWidth := w
	if boxWidth > 72 {
		boxWidth = 72
	}
	return minimalBox(boxWidth, border, lines)
}

// toolTailLines renders the result tail (and the expand/collapse hint) that
// closes every heavy tool box.
func toolTailLines(b block) []styledLine {
	var lines []styledLine
	tail := strings.TrimRight(b.tail, "\n")
	if tail == "" {
		if b.running {
			lines = append(lines, styledLine{text: "working…", style: faint})
		}
		return lines
	}
	tlines := strings.Split(tail, "\n")
	limit := 4
	if b.expanded {
		limit = 60
	}
	if len(tlines) > limit {
		tlines = tlines[len(tlines)-limit:]
	}
	outputStyle := dim
	if !b.ok {
		outputStyle = bad
	}
	for _, line := range tlines {
		lines = append(lines, styledLine{text: line, style: outputStyle})
	}
	if strings.TrimSpace(strings.Join(strings.Split(tail, "\n"), "\n")) != "" {
		if b.expanded {
			lines = append(lines, styledLine{text: "[ collapse · ctrl+o ]", style: faint})
		} else {
			lines = append(lines, styledLine{text: "[ expand · ctrl+o ]", style: faint})
		}
	}
	return lines
}

func renderCommandBox(b block, w int) []string {
	border := currentTheme.cyan
	if !b.ok {
		border = currentTheme.error
	}
	icon := statusGlyph(false, b.ok)
	header := icon + " " + strings.TrimSpace(b.title)
	lines := []styledLine{{text: header, style: dynStyle{name: border}}}
	body := strings.TrimRight(b.body, "\n")
	if body == "" {
		body = "(no output)"
	}
	blines := strings.Split(body, "\n")
	limit := 8
	if b.expanded {
		limit = 60
	}
	if len(blines) > limit {
		blines = blines[len(blines)-limit:]
	}
	for _, line := range blines {
		lines = append(lines, styledLine{text: line, style: dim})
	}
	if strings.TrimSpace(body) != "" {
		if b.expanded {
			lines = append(lines, styledLine{text: "[ collapse · ctrl+o ]", style: faint})
		} else {
			lines = append(lines, styledLine{text: "[ expand · ctrl+o ]", style: faint})
		}
	}
	boxWidth := w
	if boxWidth > 72 {
		boxWidth = 72
	}
	return minimalBox(boxWidth, border, lines)
}

// minimalBoxMixed renders the split-diff tool box: head and tail go through
// the exact same clip/pad/style path as minimalBox, while mid rows are
// pre-styled to exactly contentW cells and land verbatim inside the frame.
// minimalBox itself is untouched for every other box.
func minimalBoxMixed(w int, border string, head []styledLine, mid []string, tail []styledLine) []string {
	contentW := w - 4 // border + padding on both sides
	renderLine := func(line styledLine) string {
		return line.style.Render(padPlain(clipPlain(line.text, contentW), contentW))
	}
	renderPlainLines := func(lines []styledLine) []string {
		out := make([]string, 0, len(lines))
		for _, l := range lines {
			out = append(out, l.style.Render(clipPlain(l.text, max(1, w-1))))
		}
		return out
	}
	if contentW < 10 {
		var out []string
		out = append(out, renderPlainLines(head)...)
		out = append(out, mid...)
		out = append(out, renderPlainLines(tail)...)
		return out
	}
	b := themeBorder()
	var sb strings.Builder
	first := true
	writeStyled := func(lines []styledLine) {
		for _, line := range lines {
			if !first {
				sb.WriteString("\n")
			}
			first = false
			sb.WriteString(renderLine(line))
		}
	}
	writeStyled(head)
	for _, row := range mid {
		if !first {
			sb.WriteString("\n")
		}
		first = false
		sb.WriteString(row)
	}
	writeStyled(tail)
	return strings.Split(lipgloss.NewStyle().Border(b).BorderForeground(lipgloss.Color(border)).Padding(0, 1).Width(contentW).Render(sb.String()), "\n")
}

func minimalBox(w int, border string, lines []styledLine) []string {
	contentW := w - 4 // border + padding on both sides
	if contentW < 10 {
		var out []string
		for _, l := range lines {
			out = append(out, l.style.Render(clipPlain(l.text, max(1, w-1))))
		}
		return out
	}
	b := themeBorder()
	body := ""
	for i, line := range lines {
		if i > 0 {
			body += "\n"
		}
		body += line.style.Render(padPlain(clipPlain(line.text, contentW), contentW))
	}
	return strings.Split(lipgloss.NewStyle().Border(b).BorderForeground(lipgloss.Color(border)).Padding(0, 1).Width(contentW).Render(body), "\n")
}

func toolBorderColor(b block) string {
	if !b.ok || b.meta == "error" {
		return currentTheme.error
	}
	if b.running {
		return currentTheme.warning
	}
	t := strings.ToLower(b.title)
	switch {
	case strings.Contains(t, "shell"), strings.Contains(t, "build"), strings.Contains(t, "test"), strings.Contains(t, "run"):
		return currentTheme.warning
	case strings.Contains(t, "review"), strings.Contains(t, "diff"), strings.Contains(t, "patch"), strings.Contains(t, "search"), strings.Contains(t, "grep"), strings.Contains(t, "read"):
		return currentTheme.cyan
	case strings.Contains(t, "write"), strings.Contains(t, "edit"), strings.Contains(t, "insert"), strings.Contains(t, "append"):
		return currentTheme.success
	case strings.Contains(t, "delegate"), strings.Contains(t, "task"):
		return currentTheme.varis
	case strings.Contains(t, "fetch"), strings.Contains(t, "web"):
		return currentTheme.cyan
	default:
		return currentTheme.success
	}
}

func toolCodePreview(tool, rawArgs string, expanded bool) []styledLine {
	rawArgs = strings.TrimSpace(rawArgs)
	if rawArgs == "" {
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return nil
	}
	lines := []string{}
	lineNo := 1
	showNumbers := true
	switch tool {
	case "write", "write_file":
		content, _ := args["content"].(string)
		content = strings.TrimRight(content, "\n")
		if content == "" {
			return nil
		}
		lines = strings.Split(content, "\n")
	case "append":
		content, _ := args["content"].(string)
		content = strings.TrimRight(content, "\n")
		if content == "" {
			return nil
		}
		lines = strings.Split(content, "\n")
	case "insert":
		content, _ := args["content"].(string)
		content = strings.TrimRight(content, "\n")
		if content == "" {
			return nil
		}
		lines = strings.Split(content, "\n")
	case "patch":
		diff, _ := args["diff"].(string)
		diff = strings.TrimRight(diff, "\n")
		if diff == "" {
			return nil
		}
		lines = strings.Split(diff, "\n")
		lineNo = 0
		showNumbers = false
	case "edit":
		oldString, _ := args["old_string"].(string)
		newString, _ := args["new_string"].(string)
		for _, line := range strings.Split(strings.TrimRight(oldString, "\n"), "\n") {
			if line != "" || oldString != "" {
				lines = append(lines, "- "+line)
			}
		}
		for _, line := range strings.Split(strings.TrimRight(newString, "\n"), "\n") {
			if line != "" || newString != "" {
				lines = append(lines, "+ "+line)
			}
		}
		showNumbers = false
	default:
		return nil
	}
	limit := 12
	if expanded {
		limit = 80
	}
	preview := lines
	if len(preview) > limit {
		preview = preview[:limit]
	}
	out := make([]styledLine, 0, len(preview)+1)
	for i, line := range preview {
		trimmed := line
		style := dim
		switch {
		case strings.HasPrefix(trimmed, "+"):
			style = mint
		case strings.HasPrefix(trimmed, "-"):
			style = bad
		case strings.HasPrefix(trimmed, "@"):
			style = blue
		}
		if showNumbers {
			out = append(out, styledLine{text: fmt.Sprintf("%3d %s", lineNo+i, line), style: style})
		} else {
			out = append(out, styledLine{text: line, style: style})
		}
	}
	if len(lines) > limit {
		out = append(out, styledLine{text: fmt.Sprintf("… %d more lines · [ expand · ctrl+o ]", len(lines)-limit), style: faint})
	}
	return out
}

func toolSubjectLine(tool, rawArgs string) string {
	rawArgs = strings.TrimSpace(rawArgs)
	if rawArgs == "" {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return compactOneLine(rawArgs, 180)
	}
	preferred := []struct{ key, label string }{
		{"command", "$"},
		{"path", "path"},
		{"file", "file"},
		{"pattern", "pattern"},
		{"query", "query"},
		{"glob", "glob"},
		{"task", "task"},
		{"question", "question"},
		{"old_string", "edit"},
		{"diff", "patch"},
	}
	for _, item := range preferred {
		if value, ok := args[item.key].(string); ok && strings.TrimSpace(value) != "" {
			value = strings.TrimSpace(value)
			if item.key == "command" {
				return "$ " + compactOneLine(value, 160)
			}
			return item.label + " → " + compactOneLine(value, 160)
		}
	}
	if options, ok := args["options"].([]any); ok {
		opts := make([]string, 0, len(options))
		for _, option := range options {
			if value, ok := option.(string); ok {
				opts = append(opts, value)
			}
		}
		if len(opts) > 0 {
			return "options → " + compactOneLine(strings.Join(opts, " · "), 160)
		}
	}
	return compactOneLine(rawArgs, 160)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func thinkingDots(b block) string {
	if b.streaming {
		return "…"
	}
	return ""
}

func wrapLines(s string, w int, indent string, creamText bool) []string {
	s = displayText(s)
	if s == "" {
		return []string{indent}
	}
	if w < 10 {
		w = 10
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		words := strings.Fields(line)
		if len(words) == 0 {
			out = append(out, indent)
			continue
		}
		cur := indent
		curWidth := lipgloss.Width(cur)
		takeWord := func(word string) {
			// A word wider than the line can never be split across iterations
			// with the simple Fields loop; split by rune so the terminal's
			// soft-wrap never diverges from what we measured.
			for lipgloss.Width(word) > w-lipgloss.Width(indent) && word != "" {
				// cut the longest prefix that fits in remaining space
				maxRunes := 0
				used := 0
				for i, r := range word {
					rw := lipgloss.Width(string(r))
					if used+rw > w-lipgloss.Width(indent) {
						break
					}
					used += rw
					maxRunes = i + 1
				}
				if maxRunes == 0 {
					// nothing fits in an empty line — emit anyway
					maxRunes = 1
				}
				chunk, rest := string([]rune(word)[:maxRunes]), string([]rune(word)[maxRunes:])
				if cur == indent {
					lineOut := indent + chunk
					out = append(out, styleText(lineOut, creamText))
				} else {
					out = append(out, styleText(cur, creamText))
					cur = indent + chunk
				}
				curWidth = lipgloss.Width(cur)
				word = rest
			}
			if word == "" {
				return
			}
			ww := lipgloss.Width(word)
			need := ww
			if cur != indent {
				need = ww + 1
			}
			if curWidth+need > w && curWidth > lipgloss.Width(indent) {
				out = append(out, styleText(cur, creamText))
				cur = indent + word
				curWidth = lipgloss.Width(cur)
				return
			}
			if cur == indent {
				cur = indent + word
				curWidth += ww
			} else {
				cur += " " + word
				curWidth += need
			}
		}
		for _, word := range words {
			takeWord(word)
		}
		if curWidth > lipgloss.Width(indent) {
			out = append(out, styleText(cur, creamText))
		}
	}
	return out
}

func styleText(s string, creamText bool) string {
	if creamText {
		return cream.Render(s)
	}
	return dim.Render(s)
}

// clipBytes truncates s to at most max bytes without splitting a rune —
// byte-slicing mid-multibyte renders broken glyphs in the terminal.
func clipBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func shortBody(s string, maxBytes int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxBytes {
		return s
	}
	return clipBytes(s, maxBytes) + "\n… truncated"
}

func compactOneLine(s string, maxBytes int) string {
	// Strip ANSI escape and control bytes first so the output is safe to
	// paint regardless of where the input came from (engine stderr, model text).
	s = displayText(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxBytes {
		return clipBytes(s, maxBytes) + "…"
	}
	return s
}

func firstLineZen(s string, maxBytes int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return compactOneLine(s, maxBytes)
}

func composerLines(m *model) []string {
	prompt := orange.Bold(true).Render("❯")
	text := m.input
	if text == "" {
		return []string{prompt + " " + faint.Render("ask anything · / commands")}
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if i == 0 {
			out = append(out, prompt+" "+cream.Render(line))
			continue
		}
		out = append(out, "  "+cream.Render(line))
	}
	// Never let a huge paste outgrow the alt-screen: show the tail with a
	// marker, exactly the max composer rows.
	const maxComposerRows = 12
	if len(out) > maxComposerRows {
		omitted := len(out) - (maxComposerRows - 1)
		out = append([]string{faint.Render(fmt.Sprintf("… %d more lines above", omitted))}, out[len(out)-(maxComposerRows-1):]...)
	}
	return out
}

func (m *model) statusLine(w int) string {
	widget := brailleCornerWidget(m.animT, m.morph, w)
	// budget: the working data must fit LEFT of the widget without ever
	// wrapping the status line — fields are dropped until it fits
	budget := w
	if widget != "" {
		budget = w - lipgloss.Width(widget) - 3
		if budget < 10 {
			budget = 10
		}
	}
	trim := func(parts []string) string {
		left := strings.Join(parts, " · ")
		for lipgloss.Width(left) > budget && len(parts) > 1 {
			parts = parts[:len(parts)-1]
			left = strings.Join(parts, " · ")
		}
		return left
	}
	if m.running {
		elapsed := time.Since(m.runStarted).Round(time.Second)
		tokens := readableSize(m.runTokensIn + m.runTokensOut)
		parts := []string{"working", elapsed.String()}
		if m.lastTool != "" {
			parts = append(parts, displayText(m.lastTool))
		}
		if m.runTokensIn+m.runTokensOut > 0 {
			parts = append(parts, tokens+" tok")
		}
		if m.toolCount > 0 {
			parts = append(parts, itoa(m.toolCount)+" tools")
		}
		left := trim(parts)
		if widget == "" {
			return faint.Render(left)
		}
		return spreadWidget(faint.Render(left), widget, w)
	}
	if len(m.queued) > 0 {
		parts := []string{"ready", itoa(len(m.queued)) + " queued", "enter to send"}
		left := trim(parts)
		if widget == "" {
			return faint.Render(left)
		}
		return spreadWidget(faint.Render(left), widget, w)
	}
	if m.notice != "" {
		return faint.Render(displayText(m.notice))
	}
	// idle: the floating wordmark alone — no extra furniture
	return rightAlignBlock(widget, w)
}

// spreadWidget places `left` on the widget's first line with the widget
// right-aligned; the remaining widget lines ride along beneath it. Falls
// back to the bare left string when the widget is unavailable (tiny
// terminals, decode failure) so the status area never grows weirdly.
func spreadWidget(left, widget string, width int) string {
	lines := strings.Split(widget, "\n")
	if len(lines) == 0 {
		return left
	}
	lw := lipgloss.Width(left)
	wr := lipgloss.Width(lines[0])
	pad := width - lw - wr
	if pad < 1 {
		// no room for the widget beside the data — data wins, no wrap
		return left
	}
	out := make([]string, len(lines))
	blank := strings.Repeat(" ", lw+pad)
	for i, ln := range lines {
		if i == 0 {
			out[i] = left + strings.Repeat(" ", pad) + ln
		} else {
			out[i] = blank + ln
		}
	}
	return strings.Join(out, "\n")
}

// rightAlignBlock pushes the whole widget block to the terminal's right edge.
func rightAlignBlock(widget string, width int) string {
	if widget == "" {
		return ""
	}
	lines := strings.Split(widget, "\n")
	wr := lipgloss.Width(lines[0])
	if wr >= width {
		return widget
	}
	pad := strings.Repeat(" ", width-wr)
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func statusGlyph(running, ok bool) string {
	if running {
		return "●"
	}
	if !ok {
		return "✗"
	}
	return "✓"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func padToHeight(s string, h int) string {
	lines := strings.Split(s, "\n")
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func heavyTool(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "shell", "write", "write_file", "edit", "patch", "insert", "append", "review", "diff", "delegate", "tasks":
		return true
	case "net_fetch", "web_fetch":
		return true
	default:
		return false
	}
}

func renderToolCompact(b block) []string {
	dot := statusGlyph(b.running, b.ok)
	style := mint
	status := strings.TrimSpace(b.meta)
	if b.running {
		style = dim
		status = "running"
	} else if !b.ok || status == "error" {
		style = bad
		status = "error"
	} else if status == "" {
		status = "done"
	}
	subj := toolSubjectLine(strings.ToLower(b.title), b.body)
	if subj == "" {
		subj = firstLineZen(b.tail, 120)
	}
	return []string{style.Render(fmt.Sprintf("%s %s · %s%s", dot, b.title, status, func() string {
		if subj == "" {
			return ""
		}
		return " · " + subj
	}()))}
}

func renderPalette(m *model, w, h int) string {
	var lines []string
	lines = append(lines, orange.Bold(true).Render("/ commands"))
	total := len(m.paletteView)
	// Clip to the rows we were given (minus the header and the border) — a
	// long command list must not push the alt-screen into scrolling.
	maxItems := h - 3
	if maxItems < 1 {
		maxItems = 1
	}
	start := 0
	if m.paletteIdx >= maxItems {
		start = m.paletteIdx - maxItems + 1
	}
	end := start + maxItems
	if end > total {
		end = total
	}
	for i := start; i < end; i++ {
		item := m.paletteView[i]
		cursor := " "
		if i == m.paletteIdx {
			cursor = blue.Render("▸")
		}
		name := blue.Render(fmt.Sprintf("%-16s", item.Name))
		desc := dim.Render(item.Desc)
		lines = append(lines, cursor+" "+name+desc)
	}
	if total == 0 {
		lines = append(lines, faint.Render("  no matches"))
	}
	pw := w - 8
	if pw > 90 {
		pw = 90
	}
	return lipgloss.NewStyle().Border(themeBorder()).BorderForeground(lipgloss.Color(currentTheme.cyan)).Padding(0, 1).Width(pw).Render(strings.Join(lines, "\n"))
}
