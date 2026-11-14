// help.go — /keys: a full-screen keyboard cheatsheet for mission control.
// Read-only overlay; any of esc / q / ctrl+c / f1 / /keys closes it.
package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *model) handleHelpKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "q", "f1", "enter":
		m.overlay = overlayNone
	}
	return m, nil
}

var helpSections = []struct {
	title string
	keys  [][2]string
}{
	{"talk", [][2]string{
		{"enter", "send the prompt (queue while the agent runs)"},
		{"ctrl+j · alt+enter", "new line in the composer"},
		{"esc", "interrupt the run · clear the composer"},
		{"shift+tab", "cycle permissions: manual → accept edits → bypass"},
		{"ctrl+c", "interrupt + quit"},
	}},
	{"commands & palette", [][2]string{
		{"f1 · ctrl+p", "open the command palette (35 commands, fuzzy)"},
		{"/", "type in the composer to filter the palette"},
		{"↑/↓ · enter", "move · run the highlighted command"},
		{"ctrl+n", "fresh session"},
		{"ctrl+r", "resume a saved session"},
	}},
	{"find & lenses", [][2]string{
		{"ctrl+f", "open / close the transcript search bar"},
		{"enter · ↓", "next match (wraps)"},
		{"alt+enter · ↑", "previous match"},
		{"pgdn · pgup", "jump five matches at a time"},
		{"ctrl+g", "cycle the 35 highlight lenses"},
		{"alt+g", "cycle the lens backwards"},
		{"/lens <name>", "pin a lens by name (e.g. /lens errors)"},
		{"/lens-off", "disable the lens"},
	}},
	{"scroll & expand", [][2]string{
		{"mouse wheel · ↑/↓", "scroll the transcript"},
		{"home · end", "jump to the top / the live tail"},
		{"pgup · pgdn", "page up / down"},
		{"ctrl+u · ctrl+d", "half a page up / down"},
		{"tab · ctrl+o", "expand / collapse the latest tool box"},
		{"click a box", "expand / collapse it in place"},
	}},
	{"copy & export", [][2]string{
		{"ctrl+y", "copy the full transcript"},
		{"drag", "select · copies on release"},
		{"/copy-last", "copy the last assistant reply"},
		{"/copy-code", "copy the last fenced code block"},
		{"/export", "save the transcript as markdown"},
	}},
	{"layout & theme", [][2]string{
		{"ctrl+b", "toggle the right-hand sidebar"},
		{"ctrl+l", "clear the transcript"},
		{"/theme", "browse the 35 themes"},
		{"/usage", "tokens and tools for the last run"},
		{"/stats", "transcript statistics"},
	}},
}

func (m *model) helpView(w, h int) string {
	lines := []string{
		sectionTitle("keyboard cheatsheet"),
		faint.Render("every key in mission control · esc to close"),
	}
	keyW := 22
	contentW := 76
	if contentW > w-4 {
		contentW = w - 4
	}
	descW := contentW - keyW - 2
	for _, section := range helpSections {
		lines = append(lines, "", sectionTitle(section.title))
		for _, kv := range section.keys {
			key := padPlain(kv[0], keyW)
			desc := clipPlain(kv[1], descW)
			lines = append(lines, "  "+dynStyle{name: "mint"}.Render(key)+"  "+cream.Render(desc))
		}
	}
	if len(lines) > h-1 {
		lines = lines[:h-1]
	}
	lines = append(lines, "")
	pad := padToHeight(strings.Join(lines, "\n"), h-1)
	return pad + "\n" + faint.Render("esc to close · everything here is one keystroke away")
}
