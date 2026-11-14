// chrome.go — the shared chrome of the redesigned mission control:
// header bar, filled chips, letterspaced section titles, the right-hand
// sidebar and the composed status line. All functions are pure given the
// model; palette math stays in view.go.
package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const sidebarWidth = 30 // total block: 1 vrule column + 29 content columns
const sideContentWidth = sidebarWidth - 1

// sectionTitle renders a small-caps, letterspaced section label — the same
// voice used for sidebar groups and overlay headers.
func sectionTitle(s string) string {
	return dynStyle{name: "muted"}.Bold(true).Render(letterSpaced(strings.ToUpper(s)))
}

// letterSpaced inserts thin spaces between runes: "SESSION" -> "S E S S I O N".
func letterSpaced(s string) string {
	r := []rune(s)
	var b strings.Builder
	b.Grow(len(r) * 2)
	for i, r := range r {
		if i > 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// chip renders a filled rounded chip: accent background, near-black text.
// Used for the permission mode and the running state so the eye lands on
// the one thing that changes.
func chip(text string, bg lipgloss.Color) string {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.bg)).
		Background(bg).
		Bold(true).
		Padding(0, 1).
		Render(text)
}

// approvalChip maps the cycling approval mode to its filled chip.
func approvalChip(mode approvalMode) string {
	switch mode {
	case approvalManual:
		return chip(" MANUAL ", lipgloss.Color(currentTheme.warning))
	case approvalBypass:
		return chip(" BYPASS ", lipgloss.Color(currentTheme.error))
	default:
		return chip(" ACCEPT EDITS ", lipgloss.Color(currentTheme.accent2))
	}
}

// headerBar renders the single-line top bar: wordmark, session tag left,
// branch + model right. Width-aware: branch drops first, tagline second.
func headerBar(m *model, w int) string {
	brand := dynStyle{name: "orange", bold: true}.Render("◆") + " " +
		dynStyle{name: "cream", bold: true}.Render("creator")
	tag := faint.Render(" · mission control")

	modelName := compactOneLine(m.opts.Model, 44)
	provider := compactOneLine(m.opts.Provider, 20)
	rightParts := []string{}
	if m.gitBranch != "" {
		rightParts = append(rightParts, dynStyle{name: "mint"}.Render("⎇ "+compactOneLine(m.gitBranch, 28)))
	}
	if modelName != "" {
		ml := modelName
		if provider != "" {
			ml = provider + "/" + modelName
		}
		rightParts = append(rightParts, faint.Render(ml))
	}
	right := strings.Join(rightParts, faint.Render(" · "))

	left := brand + tag
	if lipgloss.Width(left)+lipgloss.Width(right)+2 > w {
		left = brand // drop the tagline
	}
	if lipgloss.Width(left)+lipgloss.Width(right)+2 > w {
		right = "" // drop the right side entirely on tiny terminals
	}
	contentW := w
	pad := contentW - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

// sidebarRows renders the sidebar content into exactly `h` rows, each
// padded to sideContentWidth. Returns nil when the sidebar is not active.
func sidebarRows(m *model, h int) []string {
	if h < 6 {
		h = 6
	}
	rows := []string{}
	add := func(s string) { rows = append(rows, padPlain(" "+s, sideContentWidth)) }
	blank := func() { rows = append(rows, strings.Repeat(" ", sideContentWidth)) }

	// identity
	add(sectionTitle("session"))
	runState := dynStyle{name: "mint"}.Render("● running")
	if !m.running {
		runState = faint.Render("○ idle")
	}
	turns := 0
	for _, b := range m.transcript {
		if b.kind == kindUser {
			turns++
		}
	}
	add("  " + runState)
	add(faint.Render("  " + itoa(turns) + " turns · " + itoa(m.toolCount) + " tools"))
	if m.runTokensIn+m.runTokensOut > 0 {
		add(faint.Render("  " + readableSize(m.runTokensIn) + " in · " + readableSize(m.runTokensOut) + " out"))
	} else {
		add(faint.Render("  no tokens yet"))
	}
	blank()

	// model
	add(sectionTitle("model"))
	prov := m.opts.Provider
	if prov == "" {
		prov = "session default"
	}
	modelName := m.opts.Model
	if modelName == "" {
		modelName = "provider default"
	}
	add(faint.Render("  " + compactOneLine(prov, sideContentWidth-2)))
	add(cream.Render("  " + compactOneLine(modelName, sideContentWidth-2)))
	blank()

	// workspace
	add(sectionTitle("workspace"))
	add(cream.Render("  " + compactOneLine(pathBase(m.cwd), sideContentWidth-2)))
	add(faint.Render("  " + clipPlain(compactOneLine(m.cwd, 2*sideContentWidth), sideContentWidth-2)))
	if m.gitBranch != "" {
		add(faint.Render("  ⎇ " + compactOneLine(m.gitBranch, sideContentWidth-4)))
	}
	blank()

	// lens
	if m.lensIdx > 0 {
		if spec, _ := lensForIndex(m.lensIdx - 1); spec != nil {
			add(sectionTitle("lens"))
			hitLine := "  " + dynStyle{name: spec.Color, bold: true}.Render(spec.Name)
			if m.lensHits > 0 {
				hitLine += faint.Render("  ·  " + itoa(m.lensHits) + " hits")
				if m.lensHidden > 0 {
					hitLine += faint.Render(" · " + itoa(m.lensHidden) + " hidden")
				}
			}
			add(hitLine)
			add(faint.Render("  " + clipPlain(compactOneLine(spec.Desc, 2*sideContentWidth-4), sideContentWidth-4)))
			add(faint.Render("  ctrl+g cycle · alt+g back"))
			blank()
		}
	}

	// keys
	add(sectionTitle("keys"))
	keys := [][2]string{
		{"ctrl+p", "palette"},
		{"ctrl+b", "sidebar"},
		{"tab", "expand box"},
		{"ctrl+f", "search"},
		{"ctrl+g", "lens"},
		{"ctrl+y", "copy"},
	}
	for _, kv := range keys {
		add(cream.Render("  "+kv[0]) + "  " + faint.Render(kv[1]))
	}

	// pad / clip to exactly h rows
	for len(rows) > h {
		rows = rows[:h-1]
	}
	for len(rows) < h {
		blank()
	}
	return rows
}

// vrule is the faint vertical divider between transcript and sidebar.
func vrule() string { return faint.Render("│") }

// pathBase returns the final element of a path, separator-agnostic.
func pathBase(p string) string {
	p = strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
