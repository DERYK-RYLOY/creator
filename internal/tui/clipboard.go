package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func (m *model) copyFullTranscript() {
	text := transcriptPlainText(m.transcript)
	if strings.TrimSpace(text) == "" {
		m.notice = "nothing to copy yet"
		return
	}
	if err := copyToClipboard(text); err != nil {
		m.notice = "clipboard unavailable: " + err.Error()
		return
	}
	m.notice = ""
}

func transcriptPlainText(transcript []block) string {
	var b strings.Builder
	for _, block := range transcript {
		switch block.kind {
		case kindUser:
			b.WriteString("YOU\n")
			b.WriteString(block.body)
			b.WriteString("\n\n")
		case kindAssistant:
			b.WriteString("ASSISTANT\n")
			b.WriteString(block.body)
			b.WriteString("\n\n")
		case kindThinking:
			if strings.TrimSpace(block.body) != "" {
				b.WriteString("THINKING\n")
				b.WriteString(block.body)
				b.WriteString("\n\n")
			}
		case kindTool:
			b.WriteString(strings.ToUpper(block.title))
			b.WriteString("\n")
			if strings.TrimSpace(block.body) != "" {
				b.WriteString(block.body)
				b.WriteString("\n")
			}
			if strings.TrimSpace(block.tail) != "" {
				b.WriteString(block.tail)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		case kindCommand:
			b.WriteString(strings.ToUpper(block.title))
			b.WriteString("\n")
			b.WriteString(block.body)
			b.WriteString("\n\n")
		case kindNotice:
			b.WriteString(block.title)
			b.WriteString(": ")
			b.WriteString(block.body)
			b.WriteString("\n\n")
		}
	}
	return b.String()
}

func copySelectionText(lines []string, start, end selectPoint) string {
	if start.line > end.line || (start.line == end.line && start.col > end.col) {
		start, end = end, start
	}
	var b strings.Builder
	for i := start.line; i <= end.line; i++ {
		if i < 0 || i >= len(lines) {
			continue
		}
		plain := displayText(lines[i])
		r := []rune(plain)
		from, to := 0, len(r)
		if i == start.line {
			from = runeIndexAtCell(plain, start.col)
		}
		if i == end.line {
			to = runeIndexAtCell(plain, end.col+1)
			if to < from {
				to = from
			}
		}
		if to > from {
			b.WriteString(string(r[from:to]))
		}
		if i < end.line {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func copyToClipboard(text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmdText := func(name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stdin = bytes.NewReader([]byte(text))
		return cmd.Run()
	}

	switch runtime.GOOS {
	case "windows":
		// Try every clipboard path in order; each attempt reports its own
		// reason instead of collapsing into one vague message.
		type attempt struct {
			name string
			run  func() error
		}
		attempts := []attempt{
			{"powershell.exe UTF-8", func() error {
				// UTF-8 with BOM + [Console]::In.ReadToEnd() — the path
				// that survives PowerShell 5.1's console quirks.
				ps := `$t = [Console]::In.ReadToEnd(); Set-Clipboard -Value $t`
				cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", ps)
				cmd.Stdin = bytes.NewReader(append([]byte{0xEF, 0xBB, 0xBF}, []byte(text)...))
				return cmd.Run()
			}},
			{"powershell.exe UTF-16LE", func() error {
				ps := `$ms=New-Object IO.MemoryStream; [Console]::OpenStandardInput().CopyTo($ms); ` +
					`$t=[Text.Encoding]::Unicode.GetString($ms.ToArray()) -replace '^\ufeff',''; ` +
					`Set-Clipboard -Value $t`
				cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", ps)
				cmd.Stdin = bytes.NewReader(encodeUTF16LE(text))
				return cmd.Run()
			}},
			{"clip.exe", func() error {
				cmd := exec.CommandContext(ctx, "clip.exe")
				cmd.Stdin = bytes.NewReader([]byte(text))
				return cmd.Run()
			}},
		}
		var reasons []string
		for _, a := range attempts {
			if err := a.run(); err == nil {
				return nil
			} else {
				reasons = append(reasons, fmt.Sprintf("%s: %v", a.name, err))
			}
		}
		return fmt.Errorf("no clipboard path worked (%s)", strings.Join(reasons, "; "))
	case "darwin":
		return cmdText("pbcopy")
	case "linux", "freebsd", "netbsd", "openbsd":
		candidates := []struct {
			name string
			args []string
		}{
			{"wl-copy", nil},
			{"xclip", []string{"-selection", "clipboard"}},
			{"xsel", []string{"--clipboard", "--input"}},
		}
		for _, c := range candidates {
			if err := cmdText(c.name, c.args...); err == nil {
				return nil
			}
		}
		return errors.New("no clipboard helper found (tried wl-copy, xclip, xsel)")
	default:
		return fmt.Errorf("no clipboard helper for %s", runtime.GOOS)
	}
}

func encodeUTF16LE(text string) []byte {
	// Prefix a BOM so Windows PowerShell sees the byte order immediately.
	out := []byte{0xff, 0xfe}
	buf := make([]uint16, 0, len([]rune(text)))
	for _, r := range text {
		if r <= 0xffff {
			buf = append(buf, uint16(r))
			continue
		}
		r -= 0x10000
		buf = append(buf, 0xd800|uint16(r>>10), 0xdc00|uint16(r&0x3ff))
	}
	for _, code := range buf {
		out = append(out, byte(code), byte(code>>8))
	}
	return out
}

func runeIndexAtCell(s string, x int) int {
	if x <= 0 {
		return 0
	}
	w := 0
	for i, r := range []rune(s) {
		cw := lipgloss.Width(string(r))
		if w+cw > x {
			return i
		}
		w += cw
	}
	return len([]rune(s))
}

func stripANSI(s string) string {
	// Remove ANSI control sequences. terminal SGR is not the only escape
	// family we see in real tool output: cursor movement, erase operations,
	// carriage returns and mode changes all move the byte stream without
	// moving the human eye.
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+1 >= len(s) {
			b.WriteString("␛")
			break
		}
		switch s[i+1] {
		case '[':
			j := i + 2
			for j < len(s) {
				c := s[j]
				j++
				if c >= 0x40 && c <= 0x7E {
					break
				}
			}
			i = j
			continue
		case ']':
			j := i + 2
			for j < len(s) {
				if s[j] == 0x07 {
					j++
					break
				}
				if j+1 < len(s) && s[j] == 0x1b && s[j+1] == '\\' {
					j += 2
					break
				}
				j++
			}
			i = j
			continue
		default:
			// private/hash sequences such as \x1b%G or ESC = — consume the following byte.
			i += 2
			continue
		}
	}
	return b.String()
}

func clampSelection(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
