package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// ---------- block renderers ----------

func renderBlockZen(b block, w int, ctx *highlightCtx) []string {
	switch b.kind {
	case kindUser:
		return userBlock(b, w, ctx)
	case kindThinking:
		return thinkingBlock(b, w, ctx)
	case kindAssistant:
		return append([]string{""}, wrapLines(b.body, w-2, "", true, ctx)...)
	case kindTool:
		if !heavyTool(b.title) {
			return renderToolCompact(b, ctx)
		}
		return append([]string{""}, renderToolBox(b, w, ctx)...)
	case kindCommand:
		return append([]string{""}, renderCommandBox(b, w, ctx)...)
	case kindNotice:
		return noticeBlock(b, ctx)
	default:
		return wrapLines(b.body, w-2, "", true, ctx)
	}
}

// userBlock renders the user turn with an accent spine — the one filled
// element in an otherwise foreground-only transcript.
func userBlock(b block, w int, ctx *highlightCtx) []string {
	bar := dynStyle{name: "orange", bold: true}.Render("▍")
	wrapped := wrapLines(b.body, w-2, "", true, ctx)
	out := make([]string, 0, len(wrapped)+1)
	out = append(out, "")
	for i, line := range wrapped {
		if i == 0 {
			out = append(out, bar+" "+line)
			continue
		}
		out = append(out, faint.Render("▍")+" "+line)
	}
	return out
}

// thinkingBlock renders the reasoning stream, quietly, with a live ellipsis
// while streaming.
func thinkingBlock(b block, w int, ctx *highlightCtx) []string {
	dots := ""
	if b.streaming {
		dots = " …"
	}
	header := dynStyle{name: "faint"}.Italic(true).Render("∴ thinking") + faint.Render(dots)
	body := strings.TrimSpace(b.body)
	if body == "" {
		return []string{header}
	}
	return append([]string{header}, wrapLines(body, w-4, "   ", false, ctx)...)
}

func noticeBlock(b block, ctx *highlightCtx) []string {
	line := firstLineZen(b.body, 180)
	if !b.ok {
		return []string{bad.Render("● "+b.title+" · ") + segmentedLine(line, dim, ctx)}
	}
	return []string{dynStyle{name: "mint"}.Render("● "+b.title+" · ") + segmentedLine(line, dim, ctx)}
}

type styledLine struct {
	text  string
	style dynStyle
}

func renderToolBox(b block, w int, ctx *highlightCtx) []string {
	tool := strings.ToLower(strings.TrimSpace(b.title))
	if tool == "" {
		tool = "tool"
	}
	border := toolBorderColor(b)
	dot := statusGlyph(b.running, b.ok)
	dotStyle := dynStyle{name: "mint"}
	if b.running {
		dotStyle = dynStyle{name: "warn"}
	} else if !b.ok {
		dotStyle = dynStyle{name: "bad"}
	}
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
	header := dotStyle.Render(dot) + " " + dynStyle{name: "cream", bold: true}.Render(tool) + faint.Render(" · "+status)

	lines := []styledLine{{text: header, style: dynStyle{name: "raw"}}}

	subject := toolSubjectLine(tool, b.body)
	if subject != "" {
		lines = append(lines, styledLine{text: subject, style: cream})
	}
	head := lines
	splitRows := splitToolDiff(b, w)
	if splitRows != nil {
		return minimalBoxMixed(splitBoxWidth(w), border, head, splitRows, toolTailLines(b), ctx)
	}

	lines = append(lines, toolCodePreview(tool, b.body, b.expanded)...)
	lines = append(lines, toolTailLines(b)...)

	boxWidth := w
	if boxWidth > 72 {
		boxWidth = 72
	}
	return minimalBox(boxWidth, border, lines, ctx)
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

func renderCommandBox(b block, w int, ctx *highlightCtx) []string {
	border := currentTheme.accent2
	if !b.ok {
		border = currentTheme.error
	}
	dotStyle := dynStyle{name: "mint"}
	if !b.ok {
		dotStyle = dynStyle{name: "bad"}
	}
	header := dotStyle.Render(statusGlyph(false, b.ok)) + " " + dynStyle{name: "cream", bold: true}.Render(strings.TrimSpace(b.title))
	lines := []styledLine{{text: header, style: dynStyle{name: "raw"}}}
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
	return minimalBox(boxWidth, border, lines, ctx)
}

// minimalBoxMixed renders the split-diff tool box: head and tail go through
// the exact same clip/pad/style path as minimalBox, while mid rows are
// pre-styled to exactly contentW cells and land verbatim inside the frame.
// minimalBox itself is untouched for every other box.
func minimalBoxMixed(w int, border string, head []styledLine, mid []string, tail []styledLine, ctx *highlightCtx) []string {
	contentW := w - 4 // border + padding on both sides
	renderLine := func(line styledLine) string {
		if line.style.name == "raw" {
			// already fully styled content — pad the visible width only
			return padPlain(line.text, contentW)
		}
		clipped := padPlain(clipPlain(line.text, contentW), contentW)
		return segmentedLine(clipped, line.style, ctx)
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

func minimalBox(w int, border string, lines []styledLine, ctx *highlightCtx) []string {
	contentW := w - 4 // border + padding on both sides
	if contentW < 10 {
		var out []string
		for _, l := range lines {
			out = append(out, segmentedLine(clipPlain(l.text, max(1, w-1)), l.style, ctx))
		}
		return out
	}
	b := themeBorder()
	body := ""
	for i, line := range lines {
		if i > 0 {
			body += "\n"
		}
		// "raw" style = pre-rendered colored content, no extra padding wrap
		if line.style.name == "raw" {
			body += padPlain(line.text, contentW)
			continue
		}
		body += segmentedLine(padPlain(clipPlain(line.text, contentW), contentW), line.style, ctx)
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
	// finished tools settle into a quiet hairline — the frame should never
	// shout louder than the content
	return currentTheme.borders
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

func thinkingDots(b block) string {
	if b.streaming {
		return "…"
	}
	return ""
}

func wrapLines(s string, w int, indent string, creamText bool, ctx *highlightCtx) []string {
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
					out = append(out, styleText(lineOut, creamText, ctx))
				} else {
					out = append(out, styleText(cur, creamText, ctx))
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
				out = append(out, styleText(cur, creamText, ctx))
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
			out = append(out, styleText(cur, creamText, ctx))
		}
	}
	return out
}

func styleText(s string, creamText bool, ctx *highlightCtx) string {
	base := dim
	if creamText {
		base = cream
	}
	return segmentedLine(s, base, ctx)
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
