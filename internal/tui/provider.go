package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/creatorcli/creator/internal/config"
	"github.com/creatorcli/creator/internal/provider"
)

const providerScreenFooterHeight = 3

func (m *model) handleOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.overlay == overlayModels && m.modelsBusy {
		switch msg.String() {
		case "esc", "ctrl+c":
			// Invalidate the still-running scan so its modelsDoneMsg is
			// dropped when it eventually arrives.
			m.modelsScanGen++
			m.modelsBusy = false
			m.overlay = overlayNone
			m.notice = "model scan cancelled"
		}
		return m, nil
	}
	switch m.overlay {
	case overlayProviderKey:
		return m.handleProviderKeyKey(msg)
	case overlayModels:
		return m.handleModelListKey(msg)
	case overlayProviders:
		return m.handleProviderListKey(msg)
	case overlaySessions:
		return m.handleSessionListKey(msg)
	case overlayTasks:
		return m.handleTaskListKey(msg)
	case overlayTheme:
		return m.handleThemeListKey(msg)
	default:
		m.overlay = overlayNone
		return m, nil
	}
}

func (m *model) handleProviderListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	items := m.filteredProviders()
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
		if m.overlayIdx < len(items)-1 {
			m.overlayIdx++
		}
		return m, nil
	case "pgup", "ctrl+u":
		m.overlayIdx -= 10
		if m.overlayIdx < 0 {
			m.overlayIdx = 0
		}
		return m, nil
	case "pgdn", "ctrl+d":
		m.overlayIdx += 10
		m.overlayIdx = clamp(m.overlayIdx, 0, max(0, len(items)-1))
		return m, nil
	case "enter":
		if len(items) == 0 {
			return m, nil
		}
		m.overlayIdx = clamp(m.overlayIdx, 0, len(items)-1)
		d := items[m.overlayIdx]
		m.beginProviderConnect(d)
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

func (m *model) handleProviderKeyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.keyBusy {
		switch msg.String() {
		case "esc", "ctrl+c":
			// Invalidate and abort the still-running verify: its
			// messages are dropped and the probe stops.
			m.verifyGen++
			if m.verifyCancel != nil {
				m.verifyCancel()
				m.verifyCancel = nil
			}
			m.keyBusy = false
			m.pendingProvider = provider.Descriptor{}
			m.keySteps = nil
			m.keyError = ""
			m.overlay = overlayProviders
			m.notice = "connect cancelled"
		}
		return m, nil
	}
	d := m.pendingProvider
	switch msg.String() {
	case "esc", "ctrl+c":
		m.overlay = overlayProviders
		m.keyInput = ""
		m.keySteps = nil
		m.keyError = ""
		return m, nil
	case "enter":
		if d.Local {
			return m, m.startVerify(d, "")
		}
		if strings.TrimSpace(m.keyInput) == "" {
			m.keyError = "paste the API key first"
			return m, nil
		}
		key := strings.TrimSpace(m.keyInput)
		if d.NeedsAccountID() {
			m.keyError = "Cloudflare needs your account id in base_url — start creator with that config first, then retry"
			return m, nil
		}
		return m, m.startVerify(d, key)
	case "backspace":
		if len(m.keyInput) > 0 {
			r := []rune(m.keyInput)
			m.keyInput = string(r[:len(r)-1])
			m.keyError = ""
		}
		return m, nil
	default:
		if msg.Type == tea.KeyRunes {
			m.keyInput += string(msg.Runes)
			m.keyError = ""
		}
		return m, nil
	}
}

func (m *model) handleModelListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.filteredModelRows()
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
			return m, nil
		}
		m.overlayIdx = clamp(m.overlayIdx, 0, len(rows)-1)
		choice := rows[m.overlayIdx]
		m.opts.Provider = choice.Provider
		m.opts.Model = choice.Model
		m.saveActiveProvider(choice.Provider, choice.Model)
		m.overlay = overlayNone
		m.overlayQuery = ""
		m.notice = "active: " + choice.Provider + " / " + choice.Model
		m.transcript = append(m.transcript, block{kind: kindNotice, title: "provider", body: "active model set to " + choice.Provider + " / " + choice.Model, ok: true, at: time.Now()})
		m.scroll = 0
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

func (m *model) beginProviderConnect(d provider.Descriptor) {
	m.pendingProvider = d
	m.overlay = overlayProviderKey
	m.keyInput = ""
	m.keyBusy = false
	m.keySteps = nil
	m.keyError = ""
	if d.Local {
		m.notice = d.Name + " runs locally — no API key required"
	}
}

func (m *model) startVerify(d provider.Descriptor, key string) tea.Cmd {
	m.keyBusy = true
	m.keySteps = nil
	m.keyError = ""
	m.verifyGen++
	gen := m.verifyGen
	// Hoisted cancel: ESC during verify can now actually abort the probe,
	// not just drop its messages.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	m.verifyCancel = cancel
	return func() tea.Msg {
		defer cancel()
		base := d.BaseURL
		if strings.Contains(base, "{account_id}") {
			base = ""
		}
		result, err := provider.Verify(ctx, d, key, base, "", func(s provider.Step) { m.program.Send(providerStepMsg{step: s, gen: gen}) })
		return providerVerifyMsg{result: result, err: err, gen: gen}
	}
}

func (m *model) finishProviderVerify(msg providerVerifyMsg) tea.Model {
	if !m.keyBusy {
		return m
	}
	m.keyBusy = false
	m.verifyCancel = nil
	d := m.pendingProvider
	if msg.err != nil {
		m.keyError = msg.err.Error()
		return m
	}
	if !d.Local {
		if err := provider.SaveCred(d.Slug, provider.Cred{Key: strings.TrimSpace(m.keyInput)}); err != nil {
			m.keyError = "verified, but saving the credential failed: " + err.Error()
			return m
		}
	}
	// The key is persisted; never keep it in the model state or render it again.
	m.keyInput = ""
	m.keyError = ""
	model := d.DefaultModel()
	if model == "" && len(msg.result.Models) > 0 {
		model = msg.result.Models[0].ID
	}
	m.opts.Provider = d.Slug
	m.opts.Model = model
	m.saveActiveProvider(d.Slug, model)
	m.transcript = append(m.transcript, block{kind: kindNotice, title: "provider", body: "connected " + d.Name + " (" + d.Slug + ")", ok: true, at: time.Now()})
	m.notice = "connected " + d.Slug + " — opening /models"
	m.openModels()
	return m
}

func (m *model) saveActiveProvider(slug, model string) {
	cfg, err := config.Load(config.Config{})
	if err != nil {
		m.notice = slug + " active for this session"
		return
	}
	cfg.Provider = slug
	cfg.Model = model
	if err := config.Save(cfg); err != nil {
		m.notice = slug + " active for this session (config save failed)"
		return
	}
}

func (m *model) openProviders(query string) {
	m.overlay = overlayProviders
	m.overlayQuery = strings.TrimSpace(query)
	m.overlayIdx = 0
	m.scroll = 0
}

func (m *model) openModels() {
	connected := provider.ConnectedProviders()
	if len(connected) == 0 {
		m.overlay = overlayNone
		m.notice = "no providers connected yet — use /providers to pick one"
		return
	}
	m.overlay = overlayModels
	m.overlayQuery = ""
	m.overlayIdx = 0
	m.modelRows = nil
	m.modelsSources = nil
	m.modelsBusy = true
	m.modelsDone.Store(0)
	m.modelsTotal = len(connected)
	m.keySteps = nil
	m.modelsScanGen++      // invalidate any still-running older scan
	gen := m.modelsScanGen // captured before the goroutines launch
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		rows := make([]modelRow, 0, 256)
		var mu sync.Mutex
		sources := make([]string, 0, len(connected))
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for _, d := range connected {
			wg.Add(1)
			go func(d provider.Descriptor) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				models, source, err := provider.ListModels(ctx, d.Slug)
				if err != nil {
					source = "registry fallback (live catalog unavailable)"
					models = nil
				}
				mu.Lock()
				sources = append(sources, d.Slug+": "+source)
				for _, mi := range models {
					rows = append(rows, modelRow{Provider: d.Slug, Model: mi.ID, Free: mi.Free, Note: mi.Note, Source: source})
				}
				m.modelsDone.Add(1)
				mu.Unlock()
			}(d)
		}
		wg.Wait()
		sortModelRows(rows)
		m.program.Send(modelsDoneMsg{rows: rows, sources: sources, gen: gen})
	}()
}

func sortModelRows(rows []modelRow) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && lessModelRow(rows[j], rows[j-1]); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

func lessModelRow(a, b modelRow) bool {
	if a.Free != b.Free {
		return a.Free
	}
	if a.Provider != b.Provider {
		return a.Provider < b.Provider
	}
	return a.Model < b.Model
}

func (m *model) filteredProviders() []provider.Descriptor {
	q := strings.ToLower(strings.TrimSpace(m.overlayQuery))
	list := provider.Descriptors()
	if q == "" {
		return list
	}
	out := make([]provider.Descriptor, 0, len(list))
	for _, d := range list {
		h := strings.Join([]string{d.Slug, d.Name, strings.Join(d.Models, " "), strings.Join(d.APIKeyEnv, " "), d.Free}, " ")
		if strings.Contains(strings.ToLower(h), q) {
			out = append(out, d)
		}
	}
	m.overlayIdx = clamp(m.overlayIdx, 0, max(0, len(out)-1))
	return out
}

func (m *model) filteredModelRows() []modelRow {
	q := strings.ToLower(strings.TrimSpace(m.overlayQuery))
	out := make([]modelRow, 0, len(m.modelRows))
	for _, r := range m.modelRows {
		if !r.Free {
			continue
		}
		if q == "" || strings.Contains(strings.ToLower(r.Provider+" "+r.Model+" "+r.Note), q) {
			out = append(out, r)
		}
	}
	m.overlayIdx = clamp(m.overlayIdx, 0, max(0, len(out)-1))
	return out
}

func formatProviderStep(s provider.Step) string {
	icon := "✓"
	if !s.OK {
		icon = "✗"
	}
	return icon + " " + s.Name + " · " + s.Detail
}

func providerStatus(d provider.Descriptor) string {
	if d.Local {
		return "local"
	}
	if provider.MissingKey(d) {
		return "connect"
	}
	return "connected"
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (m *model) overlayView(w, h int) string {
	switch m.overlay {
	case overlayProviderKey:
		return m.providerKeyView(w, h)
	case overlayModels:
		return m.modelsView(w, h)
	case overlaySessions:
		return m.sessionsView(w, h)
	case overlayTasks:
		return m.taskView(w, h)
	case overlayTheme:
		return m.themeView(w, h)
	default:
		return m.providersView(w, h)
	}
}

func (m *model) providersView(w, h int) string {
	items := m.filteredProviders()
	lines := []string{
		orange.Bold(true).Render("providers"),
		faint.Render("type to filter · ↑/↓ scroll · enter connect · esc back"),
	}
	if q := strings.TrimSpace(m.overlayQuery); q != "" {
		lines = append(lines, faint.Render("filter: "+q))
	} else {
		lines = append(lines, "")
	}
	show := h - 6
	if show < 3 {
		show = 3
	}
	start := 0
	if len(items) > show {
		start = clamp(m.overlayIdx-show/2, 0, len(items)-show)
	}
	end := start + show
	if end > len(items) {
		end = len(items)
	}
	for i := start; i < end; i++ {
		d := items[i]
		cursor := " "
		if i == m.overlayIdx {
			cursor = blue.Render("›")
		}
		mark := faint.Render("·")
		if !provider.MissingKey(d) || d.Local {
			mark = mint.Render("●")
		}
		status := providerStatus(d)
		name := padPlain(clipPlain(d.Name, 24), 24)
		free := ""
		if d.Free != "" {
			free = "  " + mint.Render("free")
		}
		line := cursor + " " + mark + " " + blue.Render(padPlain(d.Slug, 12)) + " " + cream.Render(name) + " " + faint.Render(status) + free
		lines = append(lines, line)
	}
	if len(items) == 0 {
		lines = append(lines, faint.Render("no matches"))
	}
	selected := ""
	if len(items) > 0 {
		d := items[clamp(m.overlayIdx, 0, len(items)-1)]
		selected = faint.Render(fmt.Sprintf("%d/%d · %s · %s", m.overlayIdx+1, len(items), d.Name, d.Docs))
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	pad := padToHeight(strings.Join(lines, "\n"), h)
	if selected != "" && h > len(strings.Split(pad, "\n"))-1 {
		pad = strings.Join(append(strings.Split(pad, "\n")[:h-1], selected), "\n")
	}
	return pad
}

func (m *model) providerKeyView(w, h int) string {
	d := m.pendingProvider
	lines := []string{
		orange.Bold(true).Render("connect · " + d.Name),
		faint.Render("doc: " + d.Docs),
	}
	if d.Local {
		lines = append(lines, "", dim.Render(d.Name+" runs locally — no API key is required."))
	} else {
		lines = append(lines, "", dim.Render("paste the API key — it is verified before saving"))
		mask := strings.Repeat("•", len([]rune(m.keyInput)))
		if mask == "" {
			mask = faint.Render("API key")
		} else {
			mask = cream.Render(mask)
		}
		lines = append(lines, "", orange.Render("› ")+mask)
		if len(d.APIKeyEnv) > 0 {
			lines = append(lines, faint.Render("env: "+strings.Join(d.APIKeyEnv, " or ")+" · keys are stored in ~/.creator/keys.json"))
		}
		if d.Free != "" {
			lines = append(lines, faint.Render("free tier: "+d.Free))
		}
	}
	if len(m.keySteps) > 0 {
		lines = append(lines, "")
		for _, s := range m.keySteps {
			lines = append(lines, dim.Render("  "+s))
		}
	}
	if m.keyError != "" {
		lines = append(lines, "", bad.Render("✗ "+m.keyError))
	}
	if m.keyBusy {
		lines = append(lines, "", mint.Render("verifying connection…"))
	}
	foot := faint.Render("enter to verify · esc to cancel")
	contentH := h - 1
	pad := padToHeight(strings.Join(lines, "\n"), contentH)
	return pad + "\n" + foot
}

func (m *model) modelsView(w, h int) string {
	lines := []string{
		orange.Bold(true).Render("models · free only"),
		faint.Render("/filter: type · ↑/↓ scroll · enter select · esc back"),
	}
	if strings.TrimSpace(m.overlayQuery) != "" {
		lines = append(lines, faint.Render("filter: "+m.overlayQuery))
	} else {
		lines = append(lines, "")
	}
	if m.modelsBusy {
		progress := fmt.Sprintf("scanning %d/%d providers…", m.modelsDone.Load(), m.modelsTotal)
		lines = append(lines, "", mint.Render(progress), faint.Render("free models are ranked first once the live catalog answers"))
		return padToHeight(strings.Join(lines, "\n"), h)
	}
	rows := m.filteredModelRows()
	show := h - 6
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
		r := rows[i]
		cursor := " "
		if i == m.overlayIdx {
			cursor = blue.Render("›")
		}
		tree := bad.Render("  ")
		if r.Free {
			tree = mint.Render("● ")
		}
		src := faint.Render(r.Source)
		line := cursor + " " + tree + blue.Render(padPlain(r.Provider, 12)) + " " + cream.Render(padPlain(clipPlain(r.Model, 48), 48)) + " " + src
		lines = append(lines, line)
	}
	if len(rows) == 0 {
		lines = append(lines, faint.Render("no models match that filter"))
	}
	foot := fmt.Sprintf("%d/%d models", m.overlayIdx+1, max(len(rows), 1))
	if len(m.modelsSources) > 0 {
		foot += " · " + strings.Join(firstN(m.modelsSources, 3), ", ")
		if len(m.modelsSources) > 3 {
			foot += ", …"
		}
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	pad := padToHeight(strings.Join(lines, "\n"), h)
	if h > len(strings.Split(pad, "\n"))-1 {
		pad = strings.Join(append(strings.Split(pad, "\n")[:h-1], faint.Render(foot)), "\n")
	}
	return pad
}

func firstN(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	return list[:n]
}
