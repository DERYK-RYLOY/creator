// actions.go — implementations behind the newer palette commands: clipboard
// copies, transcript export, expand/collapse-all, usage/stats blocks and the
// sidebar toggle. All of them are safe to call from the palette and the
// slash-command path alike.
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func (m *model) toggleSidebar() string {
	m.sidebarTouched = true
	m.sidebar = !m.sidebar
	if m.sidebar {
		m.notice = "sidebar shown"
		return ""
	}
	m.notice = "sidebar hidden"
	return ""
}

func (m *model) setAllExpanded(v bool) string {
	changed := 0
	for i := range m.transcript {
		switch m.transcript[i].kind {
		case kindTool, kindCommand, kindAssistant, kindThinking:
			if m.transcript[i].expanded != v {
				m.transcript[i].expanded = v
				changed++
			}
		}
	}
	if changed > 0 {
		m.gen++
		m.clampScroll()
	}
	if v {
		return fmt.Sprintf("expanded %d boxes", changed)
	}
	return fmt.Sprintf("collapsed %d boxes", changed)
}

func (m *model) copyLastAssistant() string {
	for i := len(m.transcript) - 1; i >= 0; i-- {
		if m.transcript[i].kind != kindAssistant {
			continue
		}
		body := strings.TrimSpace(m.transcript[i].body)
		if body == "" {
			break
		}
		if err := copyToClipboard(body); err != nil {
			return "clipboard unavailable: " + err.Error()
		}
		return fmt.Sprintf("copied last reply (%d characters)", len([]rune(body)))
	}
	return "no assistant reply yet"
}

var fencedCodeRe = regexp.MustCompile("(?s)```[^\\n]*\\n(.*?)```")

func (m *model) copyLastCode() string {
	candidates := []string{}
	for _, b := range m.transcript {
		switch b.kind {
		case kindAssistant, kindTool, kindCommand:
			candidates = append(candidates, b.body, b.tail)
		}
	}
	for i := len(candidates) - 1; i >= 0; i-- {
		matches := fencedCodeRe.FindAllStringSubmatch(candidates[i], -1)
		if len(matches) == 0 {
			continue
		}
		code := strings.TrimRight(matches[len(matches)-1][1], "\n")
		if strings.TrimSpace(code) == "" {
			return "fenced code block is empty"
		}
		if err := copyToClipboard(code); err != nil {
			return "clipboard unavailable: " + err.Error()
		}
		lines := strings.Count(code, "\n") + 1
		return fmt.Sprintf("copied code block (%d lines)", lines)
	}
	return "no fenced code block found"
}

func (m *model) dataDirOrDefault() string {
	if m.opts.DataDir != "" {
		return m.opts.DataDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".creator")
}

func (m *model) exportTranscript() string {
	if len(m.transcript) == 0 {
		return "nothing to export yet"
	}
	dir := filepath.Join(m.dataDirOrDefault(), "exports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "export failed: " + err.Error()
	}
	path := filepath.Join(dir, fmt.Sprintf("transcript-%s.md", time.Now().Format("20060102-150405")))
	header := fmt.Sprintf("# Creator transcript — %s\n\n", time.Now().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(header+transcriptPlainText(m.transcript)), 0o600); err != nil {
		return "export failed: " + err.Error()
	}
	return "exported → " + path
}

func (m *model) gitNotice() string {
	if m.gitBranch == "" {
		return "not a git repository (or HEAD unreadable) — branch unknown"
	}
	return "on branch " + m.gitBranch
}

// appendLocalBlock appends a command-output block without going through the
// engine runner. Used by /usage and /stats.
func (m *model) appendLocalBlock(title, body string) string {
	m.transcript = append(m.transcript, block{kind: kindCommand, title: title, body: body, ok: true, at: time.Now()})
	m.gen++
	m.scroll = 0
	return ""
}

func (m *model) showUsage() string {
	turns, tools, failed := 0, 0, 0
	for _, b := range m.transcript {
		switch b.kind {
		case kindUser:
			turns++
		case kindTool:
			tools++
			if !b.ok {
				failed++
			}
		}
	}
	lines := []string{
		fmt.Sprintf("session: %d turns · %d tool calls (%d failed)", turns, tools, failed),
	}
	if m.lastRunTools > 0 || m.lastRunTokensIn > 0 || m.lastRunTokensOut > 0 {
		lines = append(lines,
			fmt.Sprintf("last run: %d tools · %s in · %s out · %s",
				m.lastRunTools,
				readableSize(m.lastRunTokensIn), readableSize(m.lastRunTokensOut),
				m.lastRunDuration.Round(time.Second)))
	} else {
		lines = append(lines, "last run: nothing yet")
	}
	return m.appendLocalBlock("/usage", strings.Join(lines, "\n"))
}

func (m *model) showStats() string {
	if len(m.transcript) == 0 {
		return "transcript is empty"
	}
	kinds := map[blockKind]int{}
	words, chars := 0, 0
	for _, b := range m.transcript {
		kinds[b.kind]++
		words += len(strings.Fields(b.body))
		chars += len(b.body) + len(b.tail)
	}
	reading := words / 200 // minutes at ~200 wpm
	if reading < 1 {
		reading = 0
	}
	lines := []string{
		fmt.Sprintf("blocks: %d total (user %d · assistant %d · tools %d · thinking %d · notices %d)",
			len(m.transcript), kinds[kindUser], kinds[kindAssistant], kinds[kindTool], kinds[kindThinking], kinds[kindNotice]+kinds[kindCommand]),
		fmt.Sprintf("words: %d · chars: %d · ≈ %d min read", words, chars, reading),
	}
	return m.appendLocalBlock("/stats", strings.Join(lines, "\n"))
}

// openHelp shows the keyboard cheatsheet overlay.
func (m *model) openHelp() string {
	m.overlay = overlayHelp
	return ""
}
