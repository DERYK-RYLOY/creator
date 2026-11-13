package tui

// Side-by-side before/after panes for mutation tools, styled the way OpenCode
// renders edits: two boxes (left = old, right = new), line-aligned with line
// numbers and +/- markers, tinted with the theme's add/remove roles.
//
// This is a superset of the regular tool box: everything it needs comes from
// the block data (args and tail). Tool boxes on narrow terminals and blocks
// without a readable before/after both fall back to the single-column preview,
// so the fixed-width box system is never disturbed.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	splitMinWidth = 84  // below this the panes are unreadable; keep the old layout
	splitMaxWidth = 112 // keep the panes readable on very wide terminals
	splitSep      = " │ "
)

// splitBoxWidth is the outer box width used for the split layout; the view
// uses it too so the pre-styled rows always land exactly inside the border.
func splitBoxWidth(w int) int {
	bw := w
	if bw > splitMaxWidth {
		bw = splitMaxWidth
	}
	return bw
}

// splitRowWidth is the visible width of one fully styled pane row: the outer
// box width minus border(2), padding(2) and lipgloss's 2-cell trim slack.
func splitRowWidth(w int) int { return splitBoxWidth(w) - 6 }

type splitRow struct {
	kind        string // "context" | "removed" | "added" | "hunk"
	left, right string
	leftNum     int // 1-based, 0 = no line
	rightNum    int
}

// splitToolDiff reports the fully styled pane rows for this block, each
// exactly contentW cells wide, or nil when the block has no usable before/
// after data. Row count respects expanded; truncation is reported in-band.
func splitToolDiff(b block, w int) []string {
	if w < splitMinWidth {
		return nil
	}
	// lipgloss border trims body lines to Width-2 (padding consumes 2 cells);
	// pre-styled rows must land inside that effective width, never beyond.
	contentW := splitBoxWidth(w) - 6 // border(2) + padding(2) + trim slack(2)

	var rows []splitRow
	switch strings.ToLower(strings.TrimSpace(b.title)) {
	case "edit":
		rows = splitFromEditArgs(b.body)
	case "patch":
		rows = splitFromUnifiedDiff(unifiedDiffFromArgs(b.body))
	case "diff":
		// code review between two files: the result tail IS a unified diff
		rows = splitFromUnifiedDiff(b.tail)
	case "write", "write_file", "append", "insert":
		rows = splitFromWriteArgs(b.body)
	}
	if len(rows) == 0 {
		return nil
	}

	rowLimit := 12
	if b.expanded {
		rowLimit = 80
	}
	truncated := 0
	if len(rows) > rowLimit {
		truncated = len(rows) - rowLimit
		rows = rows[:rowLimit]
	}
	out := renderSplitRows(rows, contentW)
	if truncated > 0 {
		hint := fmt.Sprintf("… %d more lines · [ expand · ctrl+o ]", truncated)
		out = append(out, padPlain(faint.Render(clipPlain(hint, contentW)), contentW))
	}
	return out
}

// ---- parsing ---------------------------------------------------------

func splitFromEditArgs(rawArgs string) []splitRow {
	var args map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(rawArgs)), &args); err != nil {
		return nil
	}
	oldText, _ := args["old_string"].(string)
	newText, ok := args["new_string"].(string)
	if !ok {
		return nil
	}
	return alignLines(splitContentLines(oldText), splitContentLines(newText), 1, 1)
}

func splitFromWriteArgs(rawArgs string) []splitRow {
	var args map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(rawArgs)), &args); err != nil {
		return nil
	}
	content, ok := args["content"].(string)
	if !ok {
		return nil
	}
	lines := splitContentLines(content)
	if len(lines) == 0 {
		return nil
	}
	rows := make([]splitRow, 0, len(lines))
	for i, line := range lines {
		rows = append(rows, splitRow{kind: "added", right: line, rightNum: i + 1})
	}
	return rows
}

func unifiedDiffFromArgs(rawArgs string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(rawArgs)), &args); err != nil {
		return ""
	}
	diff, _ := args["diff"].(string)
	return diff
}

// splitFromUnifiedDiff converts a unified diff into aligned panes, honoring
// hunk headers (@@ -a,b +c,d @@) for real line numbers.
func splitFromUnifiedDiff(diff string) []splitRow {
	diff = strings.TrimRight(diff, "\n")
	if strings.TrimSpace(diff) == "" {
		return nil
	}
	var rows []splitRow
	oldNum, newNum := 0, 0
	started := false
	flushHunkHeader := func(line string) {
		rows = append(rows, splitRow{kind: "hunk", left: line, right: line})
	}
	for _, raw := range strings.Split(diff, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case strings.HasPrefix(line, "@@"):
			flushHunkHeader(line)
			oldNum = hunkStart(line, 1)
			newNum = hunkStart(line, 2)
			started = true
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "), strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "):
			continue
		case strings.HasPrefix(line, "\\"): // \ No newline at end of file
			continue
		case strings.HasPrefix(line, "-"):
			if !started {
				continue
			}
			rows = append(rows, splitRow{kind: "removed", left: line[1:], leftNum: oldNum})
			oldNum++
		case strings.HasPrefix(line, "+"):
			if !started {
				continue
			}
			rows = append(rows, splitRow{kind: "added", right: line[1:], rightNum: newNum})
			newNum++
		case strings.HasPrefix(line, " "):
			if !started {
				continue
			}
			rows = append(rows, splitRow{kind: "context", left: line[1:], right: line[1:], leftNum: oldNum, rightNum: newNum})
			oldNum++
			newNum++
		}
	}
	return rows
}

func hunkStart(header string, idx int) int {
	// @@ -a,b +c,d @@ — idx 1 is the '-' spec, idx 2 is the '+' spec
	fields := strings.Fields(header)
	if len(fields) < 3 {
		return 1
	}
	spec := fields[idx]
	spec = strings.TrimPrefix(spec, "-")
	spec = strings.TrimPrefix(spec, "+")
	if comma := strings.Index(spec, ","); comma >= 0 {
		spec = spec[:comma]
	}
	var n int
	if _, err := fmt.Sscanf(spec, "%d", &n); err != nil || n <= 0 {
		return 1
	}
	return n
}

// splitContentLines splits content like a file would be split, dropping the
// phantom trailing line a final newline produces.
func splitContentLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return lines
}

// alignLines computes a line-level LCS between the two sides and emits rows
// aligned for side-by-side display. oldStart/newStart are the 1-based line
// numbers for the two panes.
func alignLines(oldL, newL []string, oldStart, newStart int) []splitRow {
	n, m := len(oldL), len(newL)
	if n == 0 && m == 0 {
		return nil
	}
	if int64(n)*int64(m) > 400000 {
		return alignLinesNaive(oldL, newL, oldStart, newStart)
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if oldL[i-1] == newL[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
				continue
			}
			if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	rows := make([]splitRow, 0, n+m)
	i, j := n, m
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && oldL[i-1] == newL[j-1]:
			rows = append(rows, splitRow{kind: "context", left: oldL[i-1], right: newL[j-1]})
			i--
			j--
		case j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]):
			rows = append(rows, splitRow{kind: "added", right: newL[j-1]})
			j--
		default:
			rows = append(rows, splitRow{kind: "removed", left: oldL[i-1]})
			i--
		}
	}
	for l, r := 0, len(rows)-1; l < r; l, r = l+1, r-1 {
		rows[l], rows[r] = rows[r], rows[l]
	}
	oldN, newN := oldStart, newStart
	for k := range rows {
		switch rows[k].kind {
		case "context":
			rows[k].leftNum = oldN
			rows[k].rightNum = newN
			oldN++
			newN++
		case "removed":
			rows[k].leftNum = oldN
			oldN++
		case "added":
			rows[k].rightNum = newN
			newN++
		}
	}
	return rows
}

func alignLinesNaive(oldL, newL []string, oldStart, newStart int) []splitRow {
	rows := make([]splitRow, 0, max(len(oldL), len(newL)))
	oldN, newN := oldStart, newStart
	for i := 0; i < max(len(oldL), len(newL)); i++ {
		var row splitRow
		switch {
		case i < len(oldL) && i < len(newL) && oldL[i] == newL[i]:
			row = splitRow{kind: "context", left: oldL[i], right: newL[i], leftNum: oldN, rightNum: newN}
			oldN++
			newN++
		case i < len(oldL):
			row = splitRow{kind: "removed", left: oldL[i], leftNum: oldN}
			oldN++
		default:
			row = splitRow{kind: "added", right: newL[i], rightNum: newN}
			newN++
		}
		rows = append(rows, row)
	}
	return rows
}

// ---- rendering --------------------------------------------------------

func renderSplitRows(rows []splitRow, contentW int) []string {
	paneW := (contentW - lipgloss.Width(splitSep)) / 2
	rightPaneW := contentW - lipgloss.Width(splitSep) - paneW
	textW := paneW - 7 // "NNNN X " prefix
	if paneW < 12 || textW < 4 {
		return nil
	}
	sep := faint.Render(splitSep)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.kind == "hunk" {
			out = append(out, faint.Render(padPlain(clipPlain(r.left, contentW), contentW)))
			continue
		}
		leftCell := splitCell(r.left, r.leftNum, "-", r.kind == "removed", currentTheme.removeBg, currentTheme.removeFg, paneW, textW)
		rightCell := splitCell(r.right, r.rightNum, "+", r.kind == "added", currentTheme.addBg, currentTheme.addFg, rightPaneW, textW)
		out = append(out, leftCell+sep+rightCell)
	}
	return out
}

func splitCell(text string, num int, marker string, changed bool, bg, fg string, paneW, textW int) string {
	numStr := "    "
	if num > 0 {
		numStr = fmt.Sprintf("%4d", num)
	}
	mk := " "
	if changed {
		mk = marker
	}
	body := padPlain(fmt.Sprintf("%s %s %s", numStr, mk, clipPlain(displayText(text), textW)), paneW)
	switch {
	case changed:
		return lipgloss.NewStyle().Background(lipgloss.Color(bg)).Foreground(lipgloss.Color(fg)).Render(body)
	case num > 0:
		return dim.Render(body)
	default:
		return body
	}
}
