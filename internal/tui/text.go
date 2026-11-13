package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// displayText is intentionally stricter than ANSI stripping: every runtime
// byte that can reposition the cursor or restyle the terminal is either tab-
// expanded or removed before it reaches a rendered line.
func displayText(s string) string {
	s = stripANSI(s)
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\r', '\b', '\a', '\v', '\f', '\x7f':
			b.WriteByte(' ')
		case '\n':
			b.WriteRune(r)
		case '\t':
			// Expand tabs to 4 spaces: lipgloss measures 0 cells for a tab,
			// the terminal renders at the next tab stop — they disagree.
			b.WriteString("    ")
		default:
			if r < 0x20 {
				b.WriteByte(' ')
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

func printableLine(line string) string {
	return displayText(line)
}

func cellWidth(s string) int { return lipgloss.Width(displayText(s)) }

func clipPlain(s string, n int) string {
	s = displayText(s)
	if n <= 0 {
		return ""
	}
	if cellWidth(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	r := []rune(s)
	width := 0
	cut := 0
	for i, r := range r {
		w := lipgloss.Width(string(r))
		if width+w > n-1 {
			break
		}
		width += w
		cut = i + 1
	}
	return string(r[:cut]) + "…"
}

func padPlain(s string, n int) string {
	s = displayText(s)
	w := cellWidth(s)
	if w >= n {
		return clipPlain(s, n)
	}
	return s + strings.Repeat(" ", n-w)
}
