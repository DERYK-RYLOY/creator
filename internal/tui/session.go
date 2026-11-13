package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/creatorcli/creator/internal/config"
	"github.com/creatorcli/creator/internal/provider"
	"github.com/creatorcli/creator/internal/store"
)

type sessionRowsMsg []sessionRow

func (m *model) openSessions(query string) {
	dir := sessionDataDir(m)
	st, err := store.Open(dir)
	if err != nil {
		m.notice = "cannot open sessions: " + err.Error()
		return
	}
	defer st.Close()
	m.sessionRows = loadSessionRows(st)
	m.overlay = overlaySessions
	m.overlayQuery = strings.TrimSpace(query)
	m.overlayIdx = 0
	m.scroll = 0
}

func sessionDataDir(m *model) string {
	if m.opts.DataDir != "" {
		return m.opts.DataDir
	}
	if cfg, err := config.Load(config.Config{}); err == nil && cfg.DataDir != "" {
		return cfg.DataDir
	}
	return config.Dir()
}

func loadSessionRows(st *store.Store) []sessionRow {
	metas := st.ListSessions(0)
	rows := make([]sessionRow, 0, len(metas))
	for _, meta := range metas {
		ag := meta.Updated.Local().Format("Jan 02 15:04")
		if age := time.Since(meta.Updated); age >= 0 && age < 24*time.Hour {
			e := int(age.Minutes())
			if e < 1 {
				e = 1
			}
			ag = fmt.Sprintf("%dm ago", e)
		} else if age < 7*24*time.Hour && age >= 24*time.Hour {
			ag = fmt.Sprintf("%dd ago", int(age.Hours()/24))
		}
		rows = append(rows, sessionRow{ID: meta.ID, Updated: ag, Model: meta.Model, Turns: meta.Turns})
	}
	return rows
}

func (m *model) filteredSessionRows() []sessionRow {
	q := strings.ToLower(strings.TrimSpace(m.overlayQuery))
	rows := m.sessionRows
	if q == "" {
		return rows
	}
	out := make([]sessionRow, 0, len(rows))
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.ID+" "+r.Model+" "+r.Workspace), q) {
			out = append(out, r)
		}
	}
	return out
}

func (m *model) attachSession(id string) {
	if m.running {
		m.notice = "finish or interrupt the running mission first"
		return
	}
	dir := sessionDataDir(m)
	st, err := store.Open(dir)
	if err != nil {
		m.notice = "cannot open sessions: " + err.Error()
		return
	}
	defer st.Close()
	id = strings.TrimSpace(id)
	if id == "" {
		m.openSessions("")
		return
	}
	if id == "latest" {
		latest, ok := st.LatestSession()
		if !ok {
			m.notice = "no previous session"
			return
		}
		id = latest
	}
	sess, err := st.LoadSession(id)
	if err != nil {
		m.notice = "no session " + id
		return
	}
	m.transcript = nil
	m.finalText = ""
	m.streamed = false
	m.sessionID = sess.ID
	if sess.Provider != "" {
		m.opts.Provider = sess.Provider
	}
	if sess.Model != "" {
		m.opts.Model = sess.Model
	}
	for _, msg := range sess.Messages {
		switch msg.Role {
		case provider.RoleUser:
			if strings.TrimSpace(msg.Text) == "" {
				continue
			}
			m.transcript = append(m.transcript, block{kind: kindUser, title: "YOU", body: strings.TrimSpace(msg.Text), at: sess.Updated})
		case provider.RoleAssistant:
			if strings.TrimSpace(msg.Text) == "" {
				continue
			}
			m.transcript = append(m.transcript, block{kind: kindAssistant, title: "AI CREATOR", body: strings.TrimSpace(msg.Text), at: sess.Updated})
		case provider.RoleTool:
			body := strings.TrimSpace(msg.Text)
			if body == "" {
				continue
			}
			m.transcript = append(m.transcript, block{kind: kindNotice, title: "tool", body: body, ok: true, at: sess.Updated})
		}
	}
	m.runTokensIn = int(sess.Usage.In)
	m.runTokensOut = int(sess.Usage.Out)
	m.toolCount = 0
	m.lastTool = ""
	m.scroll = 0
	m.queued = nil
	m.notice = ""
	m.overlay = overlayNone
	m.transcript = append(m.transcript, block{kind: kindNotice, title: "session", body: "resumed " + sess.ID, ok: true, at: time.Now()})
}

func (m *model) handleSessionListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.filteredSessionRows()
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
		if len(rows) == 0 {
			m.attachSession("")
			return m, nil
		}
		// Clamp against the current filtered rows: the index may be stale
		// relative to what the filter now yields.
		idx := clamp(m.overlayIdx, 0, len(rows)-1)
		m.overlayIdx = idx
		m.attachSession(rows[idx].ID)
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

func (m *model) sessionsView(w, h int) string {
	rows := m.filteredSessionRows()
	lines := []string{
		orange.Bold(true).Render("sessions"),
		faint.Render("type to filter · ↑/↓ scroll · enter resume · esc back"),
	}
	if strings.TrimSpace(m.overlayQuery) != "" {
		lines = append(lines, faint.Render("filter: "+m.overlayQuery))
	} else {
		lines = append(lines, "")
	}
	if len(rows) == 0 {
		lines = append(lines, "", dim.Render("no saved sessions yet"))
		return padToHeight(strings.Join(lines, "\n"), h)
	}
	show := h - 6
	if show < 3 {
		show = 3
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
		r := rows[i]
		cursor := " "
		if i == m.overlayIdx {
			cursor = blue.Render("›")
		}
		updated := padPlain(r.Updated, 9)
		turns := padPlain(fmt.Sprintf("%d turns", r.Turns), 8)
		model := r.Model
		if model == "" {
			model = "(unknown model)"
		}
		line := cursor + " " + faint.Render(updated) + " " + faint.Render(turns) + " " + cream.Render(clipPlain(model, 64))
		lines = append(lines, line)
	}
	selected := fmt.Sprintf("%d/%d · %s", m.overlayIdx+1, len(rows), rows[clamp(m.overlayIdx, 0, len(rows)-1)].ID)
	if len(lines) > h {
		lines = lines[:h]
	}
	pad := padToHeight(strings.Join(lines, "\n"), h)
	if h > len(strings.Split(pad, "\n"))-1 {
		pad = strings.Join(append(strings.Split(pad, "\n")[:h-1], faint.Render(selected)), "\n")
	}
	return pad
}
