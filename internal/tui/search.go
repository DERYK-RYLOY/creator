// search.go — the in-transcript find bar (ctrl+f) and the lens cycling keys.
// Both are render-cheap: search highlights through the shared highlightCtx,
// lenses additionally filter the rendered transcript (see visibleLines).
package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// openSearch arms the search bar, keeping any previous query so repeat
// ctrl+f → type → esc → ctrl+f is frictionless.
func (m *model) openSearch() {
	m.searchActive = true
	m.palette = false
	m.matchIdx = 0
}

func (m *model) closeSearch() { m.searchActive = false }

func (m *model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.running {
			m.interrupt("ctrl+c")
		}
		return m, tea.Quit
	case "ctrl+f":
		m.closeSearch()
		return m, nil
	case "esc":
		if m.searchQuery != "" {
			m.searchQuery = ""
			m.matchIdx = 0
			return m, nil
		}
		m.closeSearch()
		return m, nil
	case "enter", "down":
		m.jumpMatch(1)
		return m, nil
	case "up", "alt+enter":
		m.jumpMatch(-1)
		return m, nil
	case "pgdn":
		m.jumpMatch(5)
		return m, nil
	case "pgup":
		m.jumpMatch(-5)
		return m, nil
	case "backspace":
		r := []rune(m.searchQuery)
		if len(r) > 0 {
			m.searchQuery = string(r[:len(r)-1])
			m.matchIdx = 0
		}
		return m, nil
	default:
		if msg.Type == tea.KeyRunes {
			m.searchQuery += string(msg.Runes)
			m.matchIdx = 0
		}
		return m, nil
	}
}

// jumpMatch moves the focused match by delta and scrolls it into view.
func (m *model) jumpMatch(delta int) {
	// Refresh the render cache first so the totals and match lines reflect
	// the current query, not the previous keystroke's.
	_ = m.transcriptLineCount(m.bodyWidth())
	if m.searchTotal <= 0 {
		return
	}
	m.matchIdx = ((m.matchIdx+delta)%m.searchTotal + m.searchTotal) % m.searchTotal
	if len(m.matchLines) == 0 || m.matchIdx >= len(m.matchLines) {
		return
	}
	g := m.matchLines[m.matchIdx]
	total := m.transcriptLineCount(m.bodyWidth())
	h := m.bodyHeight()
	start := g - h/2
	if start < 0 {
		start = 0
	}
	if maxStart := max(0, total-h); start > maxStart {
		start = maxStart
	}
	m.scroll = total - start - h
	if m.scroll < 0 {
		m.scroll = 0
	}
}

// cycleLens advances (or steps back) the highlight lens and reports the new
// state in the notice line.
func (m *model) cycleLens(delta int) {
	m.lensIdx = lensCycle(m.lensIdx, delta)
	m.matchIdx = 0
	if spec, _ := lensForIndex(m.lensIdx - 1); spec != nil {
		m.notice = "lens: " + strings.ToLower(spec.Name) + " — ctrl+g next · alt+g previous · /lens-off to disable"
	}
}

// setLensByQuery resolves "/lens errors" style input against slug, name and
// substring. Returns the index set (or lensNone for "off"), or false when no
// lens matches the query.
func (m *model) setLensByQuery(q string) bool {
	idx := lensByQuery(q)
	switch idx {
	case lensNone:
		m.lensIdx = 0
		m.notice = "lens off"
		return true
	case -2:
		return false
	default:
		m.lensIdx = idx + 1
		if spec, _ := lensForIndex(idx); spec != nil {
			m.notice = "lens: " + strings.ToLower(spec.Name)
		}
		return true
	}
}
