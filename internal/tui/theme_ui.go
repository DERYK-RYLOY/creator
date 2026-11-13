package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/creatorcli/creator/internal/config"
)

func (m *model) openTheme(query string) {
	m.overlay = overlayTheme
	m.overlayQuery = strings.TrimSpace(query)
	m.overlayIdx = 0
	m.themeBefore = CurrentThemeSlug()
	m.scroll = 0
	m.refreshThemeRows(m.overlayQuery)
	for i, row := range m.themeRows {
		if row.Active {
			m.overlayIdx = i
			break
		}
	}
}

func (m *model) filteredThemes() []ThemeRow {
	q := strings.ToLower(strings.TrimSpace(m.overlayQuery))
	rows := ThemeNames()
	if q == "" {
		return rows
	}
	out := make([]ThemeRow, 0, len(rows))
	for _, t := range rows {
		if strings.Contains(strings.ToLower(t.Slug+" "+t.Name+" "+t.Font), q) {
			out = append(out, t)
		}
	}
	m.overlayIdx = clamp(m.overlayIdx, 0, max(0, len(out)-1))
	return out
}

func (m *model) refreshThemeRows(query string) {
	if strings.TrimSpace(query) == "" {
		m.themeRows = ThemeNames()
		return
	}
	q := strings.ToLower(query)
	rows := make([]ThemeRow, 0, len(builtinThemes))
	for _, t := range builtinThemes {
		if strings.Contains(strings.ToLower(t.Slug+" "+t.Name+" "+t.Font.Recommended), q) {
			info, _ := ThemeBySlug(t.Slug)
			rows = append(rows, ThemeRow{Slug: info.Slug, Name: info.Name, Mode: info.Mode, Font: info.Font.Recommended, Active: info.Slug == CurrentThemeSlug()})
		}
	}
	m.themeRows = rows
	m.overlayIdx = clamp(m.overlayIdx, 0, max(0, len(rows)-1))
}

func (m *model) previewThemeRow(idx int) {
	if len(m.themeRows) == 0 || idx < 0 || idx >= len(m.themeRows) {
		return
	}
	ApplyTheme(m.themeRows[idx].Slug)
}

func (m *model) moveThemeOverlay(up bool) {
	if len(m.themeRows) == 0 {
		return
	}
	if up {
		m.overlayIdx--
	} else {
		m.overlayIdx++
	}
	m.overlayIdx = clamp(m.overlayIdx, 0, len(m.themeRows)-1)
	m.previewThemeRow(m.overlayIdx)
}

func (m *model) commitTheme() tea.Cmd {
	if len(m.themeRows) == 0 {
		m.overlay = overlayNone
		return nil
	}
	slug := m.themeRows[clamp(m.overlayIdx, 0, len(m.themeRows)-1)].Slug
	ok := ApplyTheme(slug)
	m.overlay = overlayNone
	if !ok {
		m.notice = "unknown theme " + slug
		return nil
	}
	if err := saveActiveTheme(slug); err != nil {
		// The theme applies to this session either way — but the user must
		// know it will not persist.
		m.notice = "theme applied for this session; config save failed: " + err.Error()
		return nil
	}
	m.notice = ""
	return nil
}

func saveActiveTheme(slug string) error {
	cfg, err := config.Load(config.Config{})
	if err != nil {
		return err
	}
	cfg.Theme = slug
	return config.Save(cfg)
}

func (m *model) handleThemeListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.refreshThemeRows(m.overlayQuery)
	switch msg.String() {
	case "esc", "ctrl+c":
		ApplyTheme(m.themeBefore)
		m.overlay = overlayNone
		m.overlayQuery = ""
		m.themeRows = nil
		m.notice = "theme unchanged"
		return m, nil
	case "up", "ctrl+p":
		m.moveThemeOverlay(true)
		return m, nil
	case "down", "ctrl+n":
		m.moveThemeOverlay(false)
		return m, nil
	case "pgup", "ctrl+u":
		for i := 0; i < 10; i++ {
			m.moveThemeOverlay(true)
		}
		return m, nil
	case "pgdn", "ctrl+d":
		for i := 0; i < 10; i++ {
			m.moveThemeOverlay(false)
		}
		return m, nil
	case "enter":
		m.refreshThemeRows(m.overlayQuery)
		return m, m.commitTheme()
	case "backspace":
		if len(m.overlayQuery) > 0 {
			r := []rune(m.overlayQuery)
			m.overlayQuery = string(r[:len(r)-1])
			m.overlayIdx = 0
			m.refreshThemeRows(m.overlayQuery)
		}
		return m, nil
	default:
		if msg.Type == tea.KeyRunes {
			m.overlayQuery += string(msg.Runes)
			m.overlayIdx = 0
			m.refreshThemeRows(m.overlayQuery)
		}
		return m, nil
	}
}

func (m *model) themeView(w, h int) string {
	lines := []string{
		orange.Bold(true).Render("themes"),
		faint.Render("type to filter · ↑/↓ preview · enter apply & save · esc revert"),
	}
	if strings.TrimSpace(m.overlayQuery) != "" {
		lines = append(lines, faint.Render("filter: "+m.overlayQuery))
	} else {
		lines = append(lines, "")
	}
	if len(m.themeRows) == 0 {
		lines = append(lines, "", dim.Render("no themes match that filter"))
		return padToHeight(strings.Join(lines, "\n"), h)
	}
	show := h - 7
	if show < 4 {
		show = 4
	}
	start := 0
	if len(m.themeRows) > show {
		start = clamp(m.overlayIdx-show/2, 0, len(m.themeRows)-show)
	}
	end := start + show
	if end > len(m.themeRows) {
		end = len(m.themeRows)
	}
	for i := start; i < end; i++ {
		row := m.themeRows[i]
		cursor := " "
		if i == m.overlayIdx {
			cursor = blue.Render("›")
		}
		mark := faint.Render("·")
		if row.Active {
			mark = mint.Render("●")
		}
		theme, _ := ThemeBySlug(row.Slug)
		swatch := swatchFor(theme)
		name := padPlain(clipPlain(row.Name, 36), 36)
		lines = append(lines, cursor+" "+mark+" "+swatch+" "+cream.Render(name)+" "+faint.Render(row.Font))
	}
	selected := fmt.Sprintf("%d/%d · %s", m.overlayIdx+1, len(m.themeRows), m.themeRows[clamp(m.overlayIdx, 0, len(m.themeRows)-1)].Slug)
	if len(lines) > h {
		lines = lines[:h]
	}
	pad := padToHeight(strings.Join(lines, "\n"), h)
	if h > len(strings.Split(pad, "\n"))-1 {
		pad = strings.Join(append(strings.Split(pad, "\n")[:h-1], faint.Render(selected)), "\n")
	}
	return pad
}

func swatchFor(t Theme) string {
	bs := []int{0, len(t.Palette) / 3, 2 * len(t.Palette) / 3, len(t.Palette) - 1}
	var b strings.Builder
	for _, idx := range bs {
		if idx < 0 || idx >= len(t.Palette) {
			continue
		}
		fmt.Fprintf(&b, "%s", lipglossColorBlock(t.Palette[idx]))
	}
	return b.String()
}

func lipglossColorBlock(hex string) string {
	return dynStyle{name: hex}.Render("██")
}

func now() time.Time {
	return time.Now()
}
