package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/creatorcli/creator/internal/store"
)

type taskBook struct {
	Rec       store.TaskRecord
	Depth     int
	LastChild bool
	Blocked   bool
	Missing   []string
}

func (m *model) openTasks() {
	m.sessionRows = nil
	m.taskRows = loadTaskBooks(m)
	m.overlay = overlayTasks
	m.overlayQuery = ""
	m.overlayIdx = 0
	m.scroll = 0
}

func loadTaskBooks(m *model) []taskBook {
	dir := sessionDataDir(m)
	st, err := store.Open(dir)
	if err != nil {
		m.notice = "cannot open the task board: " + err.Error()
		return nil
	}
	defer st.Close()

	recs := st.ListTasks(0)
	if m.sessionID != "" {
		out := make([]store.TaskRecord, 0, len(recs))
		for _, rec := range recs {
			if rec.Session == "" || rec.Session == m.sessionID {
				out = append(out, rec)
			}
		}
		recs = out
	}
	if len(recs) == 0 {
		return nil
	}

	byID := make(map[string]store.TaskRecord, len(recs))
	for _, rec := range recs {
		byID[rec.ID] = rec
	}
	children := make(map[string][]int, len(recs))
	parents := make([]int, 0, len(recs))
	for i, rec := range recs {
		parent, parentKnown := byID[rec.Parent]
		_ = parent
		if rec.Parent != "" && parentKnown {
			children[rec.Parent] = append(children[rec.Parent], i)
			continue
		}
		parents = append(parents, i)
	}
	sortTaskIndexes(recs, parents)
	for parent := range children {
		sortTaskIndexes(recs, children[parent])
	}

	blocked := func(i int, seen map[string]bool) (bool, []string) {
		if len(recs[i].DependsOn) == 0 {
			return false, nil
		}
		if seen == nil {
			seen = map[string]bool{}
		}
		var missing []string
		for _, dep := range recs[i].DependsOn {
			drec, ok := byID[dep]
			if !ok {
				missing = append(missing, dep)
				continue
			}
			if drec.Status != "done" {
				return true, append(missing, dep)
			}
		}
		return len(missing) > 0, missing
	}

	var out []taskBook
	visited := make([]bool, len(recs))
	var walk func(i, depth int, last bool)
	walk = func(i, depth int, last bool) {
		if visited[i] {
			return // parent cycle back-edge: stop, never loop forever
		}
		visited[i] = true
		blockedNow, missing := blocked(i, nil)
		out = append(out, taskBook{Rec: recs[i], Depth: depth, LastChild: last, Blocked: blockedNow, Missing: missing})
		deps := children[recs[i].ID]
		for k, child := range deps {
			walk(child, depth+1, k == len(deps)-1)
		}
	}
	for k, i := range parents {
		walk(i, 0, k == len(parents)-1)
	}
	// A parent island outside every root's reach (a cycle like A→B→A)
	// would silently vanish from the board — surface it as flat rows.
	if len(out) < len(recs) {
		var islands []int
		for i := range recs {
			if !visited[i] {
				islands = append(islands, i)
			}
		}
		sortTaskIndexes(recs, islands)
		for _, i := range islands {
			blockedNow, missing := blocked(i, nil)
			out = append(out, taskBook{Rec: recs[i], Depth: 0, Blocked: blockedNow, Missing: missing})
		}
	}
	if len(out) == 0 { // corrupt board: show every task as a flat row
		for i := range recs {
			blockedNow, missing := blocked(i, nil)
			out = append(out, taskBook{Rec: recs[i], Depth: 0, Blocked: blockedNow, Missing: missing})
		}
	}
	return out
}

func sortTaskIndexes(recs []store.TaskRecord, indexes []int) {
	group := func(i int) int {
		switch recs[i].Status {
		case "in_progress":
			return 0
		case "pending":
			return 1
		case "done":
			return 2
		case "cancelled":
			return 3
		default:
			return 4
		}
	}
	weight := func(p string) int {
		switch strings.ToLower(p) {
		case "high":
			return 0
		case "med", "":
			return 1
		case "low":
			return 2
		default:
			return 1
		}
	}
	for i := 1; i < len(indexes); i++ {
		for j := i; j > 0; j-- {
			a, b := indexes[j-1], indexes[j]
			ga, gb := group(a), group(b)
			if ga > gb || (ga == gb && weight(recs[a].Priority) > weight(recs[b].Priority)) {
				indexes[j-1], indexes[j] = b, a
				continue
			}
			if ga == gb && weight(recs[a].Priority) == weight(recs[b].Priority) && recs[a].Seq > recs[b].Seq {
				indexes[j-1], indexes[j] = b, a
			}
		}
	}
}

func taskSymbol(r store.TaskRecord, blocked bool) string {
	switch {
	case r.Status == "in_progress":
		return "[>]"
	case r.Status == "done":
		return "[x]"
	case r.Status == "cancelled":
		return "[-]"
	case blocked:
		return "[B]"
	default:
		return "[ ]"
	}
}

func taskStyle(r store.TaskRecord, blocked bool) func(string) string {
	switch {
	case r.Status == "in_progress":
		return func(s string) string { return blue.Render(s) }
	case r.Status == "done":
		return func(s string) string { return good.Render(s) }
	case r.Status == "cancelled":
		return func(s string) string { return bad.Render(s) }
	case blocked:
		return func(s string) string { return orange.Render(s) }
	default:
		return func(s string) string { return cream.Render(s) }
	}
}

func (m *model) filteredTaskBooks() []taskBook {
	q := strings.ToLower(strings.TrimSpace(m.overlayQuery))
	if q == "" {
		return m.taskRows
	}
	out := make([]taskBook, 0, len(m.taskRows))
	for _, row := range m.taskRows {
		r := row.Rec
		h := strings.ToLower(r.ID + " " + r.Title + " " + r.Detail + " " + r.Verify + " " + r.Status)
		if strings.Contains(h, q) {
			out = append(out, row)
		}
	}
	m.overlayIdx = clamp(m.overlayIdx, 0, max(0, len(out)-1))
	return out
}

func (m *model) printTaskDetail() {
	rows := m.filteredTaskBooks()
	if len(rows) == 0 {
		m.notice = "no tasks yet"
		return
	}
	r := rows[clamp(m.overlayIdx, 0, len(rows)-1)].Rec
	m.transcript = append(m.transcript, block{kind: kindNotice, title: r.ID, body: strings.TrimSpace(r.Detail), ok: true, at: time.Now()})
}

func (m *model) handleTaskListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.filteredTaskBooks()
	switch msg.String() {
	case "esc", "ctrl+c":
		m.overlay = overlayNone
		m.overlayQuery = ""
		return m, nil
	case "up", "ctrl+p":
		if m.overlayIdx > 0 {
			m.overlayIdx--
		}
		return m, nil
	case "down", "ctrl+n":
		if m.overlayIdx < len(rows)-1 {
			m.overlayIdx++
		}
		return m, nil
	case "pgup", "ctrl+u":
		m.overlayIdx = clamp(m.overlayIdx-10, 0, max(0, len(rows)-1))
		return m, nil
	case "pgdn", "ctrl+d":
		m.overlayIdx = clamp(m.overlayIdx+10, 0, max(0, len(rows)-1))
		return m, nil
	case "enter":
		m.printTaskDetail()
		return m, nil
	case "backspace":
		if len(m.overlayQuery) > 0 {
			r := []rune(m.overlayQuery)
			m.overlayQuery = string(r[:len(r)-1])
			m.overlayIdx = 0
		}
		return m, nil
	default:
		if msg.Type == tea.KeyRunes {
			m.overlayQuery += string(msg.Runes)
			m.overlayIdx = 0
		}
		return m, nil
	}
}

func (m *model) taskView(w, h int) string {
	rows := m.filteredTaskBooks()
	lines := []string{
		orange.Bold(true).Render("to do"),
		faint.Render("type to filter · ↑/↓ select · enter inspect · esc back"),
	}
	if strings.TrimSpace(m.overlayQuery) != "" {
		lines = append(lines, faint.Render("filter: "+m.overlayQuery))
	} else {
		lines = append(lines, "")
	}
	if len(rows) == 0 {
		lines = append(lines, "", dim.Render("no checklist yet"), dim.Render("send a real multi-step mission and the AI will build one"), faint.Render("the system handles IDs, dependencies and verification"))
		return padToHeight(strings.Join(lines, "\n"), h)
	}

	total := len(rows)
	done := 0
	verified := 0
	flagged := 0
	blocked := 0
	for _, row := range rows {
		if row.Rec.Status == "done" {
			done++
			if row.Rec.Verify != "" {
				verified++
			}
		}
		if len(row.Rec.Notes) > 0 {
			flagged++
		}
		if row.Blocked {
			blocked++
		}
	}
	fill := 0
	if total > 0 {
		fill = int(float64(done) / float64(total) * 14)
	}
	bar := mint.Render(strings.Repeat("█", fill)) + faint.Render(strings.Repeat("░", 14-fill))
	lines = append(lines, fmt.Sprintf("%s  %d/%d done · %d verified · %d blocked · %d flagged", bar, done, total, verified, blocked, flagged))

	show := h - 8
	if show < 4 {
		show = 4
	}
	start := 0
	if len(rows) > show {
		start = clamp(m.overlayIdx-show/2, 0, len(rows)-show)
	}
	end := start + show
	if end > len(rows) {
		end = len(rows)
	}
	for i := start; i < end; i++ {
		row := rows[i]
		r := row.Rec
		cursor := " "
		if i == m.overlayIdx {
			cursor = blue.Render("›")
		}
		prefix := strings.Repeat("  ", row.Depth)
		if row.Depth > 0 {
			prefix = strings.Repeat("    ", row.Depth-1) + "└─ "
		}
		style := taskStyle(r, row.Blocked)
		symbol := taskSymbol(r, row.Blocked)
		title := r.Title
		if len(strings.TrimSpace(title)) > 46 {
			tr := []rune(strings.TrimSpace(title))
			title = string(tr[:45]) + "…"
		}
		meta := ""
		if r.Verify != "" {
			meta = " · verify"
		}
		if len(r.Notes) > 0 {
			meta += " · flag"
		}
		if row.Blocked && len(row.Missing) > 0 {
			meta += " · wait " + strings.Join(row.Missing, ",")
		} else if r.Status == "in_progress" && r.Doing != "" {
			meta += " · " + compactOneLine(r.Doing, 24)
		}
		lines = append(lines, cursor+" "+style(symbol)+" "+style(r.ID)+"  "+cream.Render(prefix+title)+faint.Render(meta))
	}
	selected := fmt.Sprintf("%d/%d · %s · enter shows acceptance criteria", m.overlayIdx+1, len(rows), rows[clamp(m.overlayIdx, 0, len(rows)-1)].Rec.ID)
	if len(lines) > h {
		lines = lines[:h]
	}
	pad := padToHeight(strings.Join(lines, "\n"), h)
	if h > len(strings.Split(pad, "\n"))-1 {
		pad = strings.Join(append(strings.Split(pad, "\n")[:h-1], faint.Render(selected)), "\n")
	}
	return pad
}
