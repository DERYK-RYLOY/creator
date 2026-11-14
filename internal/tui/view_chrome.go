package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ---------- search bar ----------

// searchBarLines renders the ctrl+f find bar in place of the composer: the
// query with a live cursor, the match counter, and the full key map on the
// second line so the search is usable without reading /keys.
func searchBarLines(m *model) []string {
	borderColor := currentTheme.accent2
	prompt := dynStyle{name: "blue", bold: true}.Render("⌕")

	query := displayText(m.searchQuery)
	cursor := dynStyle{name: "orange", bold: true}.Render("▌")
	left := prompt + " "
	if query == "" {
		left += faint.Render("search the conversation") + cursor
	} else {
		left += cream.Render(query) + cursor
	}

	right := ""
	if m.searchTotal > 0 {
		right = mint.Render(fmt.Sprintf("%d/%d", m.matchIdx+1, m.searchTotal))
	} else if m.searchQuery != "" {
		right = bad.Render("no matches")
	}

	hint := faint.Render("enter next · alt+enter prev · pgdn/pgup jump 5 · esc clears, then closes")

	inner := spreadLine(left, right, max(m.width-4, 1))
	barWidth := m.width - 2
	if barWidth < 8 {
		barWidth = 8
	}
	box := lipgloss.NewStyle().
		Border(themeBorder()).
		BorderForeground(lipgloss.Color(borderColor)).
		Padding(0, 1).
		Width(barWidth).
		Render(inner + "\n" + hint)
	return strings.Split(box, "\n")
}

// spreadLine places left and right at opposite edges of a line of width w.
func spreadLine(left, right string, w int) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	pad := w - lw - rw
	if pad < 1 {
		// Right side does not fit: drop it, keep the query.
		if lw > w {
			return clipPlain(displayText(left), w)
		}
		return left
	}
	return left + strings.Repeat(" ", pad) + right
}

// ---------- composer ----------

// composer renders the input as a bordered box: a hairline frame, the
// accent ❯ prompt on the first line, faint placeholders when empty, and
// a paste-tail marker so huge inputs never outgrow the alt-screen.
func composerLines(m *model) []string {
	if m.searchActive {
		return searchBarLines(m)
	}
	borderColor := currentTheme.borders
	if m.running {
		borderColor = currentTheme.accent1
	}

	prompt := dynStyle{name: "orange", bold: true}.Render("❯")

	var inner []string
	text := m.input
	if text == "" {
		placeholder := "ask anything…"
		if m.running {
			placeholder = "esc to interrupt · queue another mission"
		}
		inner = []string{prompt + " " + faint.Render(placeholder) + faint.Render("  ·  / for commands")}
	} else {
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			if i == 0 {
				inner = append(inner, prompt+" "+cream.Render(line))
				continue
			}
			inner = append(inner, "  "+cream.Render(line))
		}
	}

	const maxComposerRows = 12
	if len(inner) > maxComposerRows {
		omitted := len(inner) - (maxComposerRows - 1)
		inner = append([]string{faint.Render(fmt.Sprintf("… %d more lines above", omitted))}, inner[len(inner)-(maxComposerRows-1):]...)
	}

	boxWidth := m.width - 2
	if boxWidth < 8 {
		boxWidth = 8
	}
	box := lipgloss.NewStyle().
		Border(themeBorder()).
		BorderForeground(lipgloss.Color(borderColor)).
		Padding(0, 1).
		Width(boxWidth).
		Render(strings.Join(inner, "\n"))
	return strings.Split(box, "\n")
}

// ---------- status line ----------

func (m *model) statusLine(w int) string {
	showWidget := len(m.transcript) > 0 // hero page already owns the animation
	widget := ""
	if showWidget {
		widget = brailleCornerWidget(m.animT, m.morph, w)
	}
	leftChip := approvalChip(m.approval)
	if m.lensIdx > 0 {
		if spec, _ := lensForIndex(m.lensIdx - 1); spec != nil {
			leftChip += " " + chip(" "+strings.ToUpper(spec.Name)+" ", lipgloss.Color(spec.Color))
			meta := itoa(m.lensHits) + " hits"
			if m.lensHidden > 0 {
				meta += " · " + itoa(m.lensHidden) + " hidden"
			}
			leftChip += " " + faint.Render(meta)
		}
	}
	chipW := lipgloss.Width(leftChip)

	// build the detail parts richest-to-sparsest so we can trim from the tail
	detailParts := []string{}
	switch {
	case m.running:
		elapsed := time.Since(m.runStarted).Round(time.Second)
		detailParts = append(detailParts,
			dynStyle{name: "cream", bold: true}.Render("working"),
			cream.Render(elapsed.String()),
		)
		if m.lastTool != "" {
			detailParts = append(detailParts, dynStyle{name: "mint"}.Render(displayText(m.lastTool)))
		}
		if m.runTokensIn+m.runTokensOut > 0 {
			detailParts = append(detailParts, faint.Render(readableSize(m.runTokensIn+m.runTokensOut)+" tok"))
		}
		if m.toolCount > 0 {
			detailParts = append(detailParts, faint.Render(itoa(m.toolCount)+" tools"))
		}
	case m.notice != "":
		detailParts = []string{faint.Render(displayText(m.notice))}
	case len(m.queued) > 0:
		detailParts = []string{cream.Render(itoa(len(m.queued))+" queued") + faint.Render(" · enter to send")}
	default:
		detailParts = []string{faint.Render(pathBase(m.cwd))}
		if m.gitBranch != "" {
			detailParts = append(detailParts, dynStyle{name: "mint"}.Render("⎇ "+compactOneLine(m.gitBranch, 24)))
		}
		if m.opts.Model != "" {
			detailParts = append(detailParts, faint.Render(compactOneLine(m.opts.Model, 40)))
		}
	}
	join := func(parts []string) string { return strings.Join(parts, faint.Render(" · ")) }

	// budget left of the widget; the detail sheds its tail fields until it fits
	budget := w
	if widget != "" {
		budget = w - lipgloss.Width(widget) - 3
	}
	parts := detailParts
	for len(parts) > 0 && chipW+2+lipgloss.Width(join(parts)) > budget {
		parts = parts[:len(parts)-1]
	}
	left := leftChip
	if len(parts) > 0 {
		left = leftChip + "  " + join(parts)
	}
	// chip alone still must fit: drop the widget entirely on tiny terminals
	if lipgloss.Width(left) > w-1 {
		widget = ""
	}
	if widget == "" {
		return left
	}
	return spreadWidget(left, widget, w)
}

// spreadWidget places `left` on the widget's first line with the widget
// right-aligned; the remaining widget lines ride along beneath it.
func spreadWidget(left, widget string, width int) string {
	lines := strings.Split(widget, "\n")
	if len(lines) == 0 {
		return left
	}
	lw := lipgloss.Width(left)
	wr := lipgloss.Width(lines[0])
	pad := width - lw - wr
	if pad < 1 {
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

// ---------- palette ----------

// renderPalette draws the command map: a header with the match count,
// section headers when browsing (RECENT / ALL COMMANDS) or a flat ranked
// list when searching (MATCHES n), one row per command with its shortcut,
// and a one-line key hint along the bottom.
func renderPalette(m *model, w, h int) string {
	pw := w - 8
	if pw > 90 {
		pw = 90
	}
	if pw < 24 {
		pw = 24
	}
	if pw > w-2 {
		pw = w - 2
	}
	q := strings.TrimSpace(m.paletteQ)
	total := len(m.paletteView)

	lines := []string{}
	title := dynStyle{name: "cream", bold: true}.Render("/ commands")
	count := faint.Render(itoa(total))
	if q != "" {
		count = faint.Render(itoa(total) + " matches")
	}
	headerPad := pw - 4 - lipgloss.Width(title) - lipgloss.Width(count)
	if headerPad < 1 {
		headerPad = 1
	}
	lines = append(lines, title+strings.Repeat(" ", headerPad)+count)

	maxItems := h - 4 // header + hint + breathing room
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

	// column widths inside the box (2-cell border+padding on each side)
	nameW := 15
	shortW := 8
	descW := pw - 4 - 2 - nameW - shortW - 4 // cursor + name + shortcut + gaps
	if descW < 8 {
		descW = 8
	}
	// section headers only make sense on the unfiltered browse view
	browsing := q == ""
	sawRecent := false
	printedAll := false
	for i := start; i < end; i++ {
		item := m.paletteView[i]
		if browsing {
			if item.recent && !sawRecent {
				lines = append(lines, sectionTitle("recent"))
				sawRecent = true
			}
			if !item.recent && sawRecent && !printedAll {
				lines = append(lines, sectionTitle("all commands"))
				printedAll = true
			}
		}
		cursor := " "
		if i == m.paletteIdx {
			cursor = dynStyle{name: "mint"}.Render("▸")
		}
		name := padPlain(item.Name, nameW)
		if i == m.paletteIdx {
			name = dynStyle{name: "cream", bold: true}.Render(padPlain(item.Name, nameW))
		} else {
			name = cream.Render(name)
		}
		short := padPlain(faint.Render(item.Shortcut)+strings.Repeat(" ", max(0, shortW-lipgloss.Width(item.Shortcut))), shortW)
		mark := ""
		if item.recent {
			mark = mint.Render("•") + " "
		} else {
			mark = "  "
		}
		desc := dim.Render(clipPlain(item.Desc, descW))
		lines = append(lines, cursor+" "+mark+name+short+" "+desc)
	}
	if total == 0 {
		lines = append(lines, faint.Render("  no matches — backspace to edit, esc to close"))
	}
	lines = append(lines, faint.Render("↑/↓ move · enter run · esc close"))
	return lipgloss.NewStyle().Border(themeBorder()).BorderForeground(lipgloss.Color(currentTheme.accent2)).Padding(0, 1).Width(pw).Render(strings.Join(lines, "\n"))
}

// ---------- tool compact rows ----------

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

func renderToolCompact(b block, ctx *highlightCtx) []string {
	dot := statusGlyph(b.running, b.ok)
	dotStyle := dynStyle{name: "mint"}
	status := strings.TrimSpace(b.meta)
	if b.running {
		dotStyle = dynStyle{name: "warn"}
		status = "running"
	} else if !b.ok || status == "error" {
		dotStyle = dynStyle{name: "bad"}
		status = "error"
	} else if status == "" {
		status = "done"
	}
	subj := toolSubjectLine(strings.ToLower(b.title), b.body)
	if subj == "" {
		subj = firstLineZen(b.tail, 120)
	}
	line := dotStyle.Render(dot) + " " + cream.Render(b.title) + faint.Render(" · "+status)
	if subj != "" {
		line += faint.Render(" · ") + segmentedLine(subj, dim, ctx)
	}
	return []string{line}
}

func statusGlyph(running, ok bool) string {
	if running {
		return "●"
	}
	if !ok {
		return "✗"
	}
	return "✓"
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

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
