package tools

import (
	"regexp"
	"strings"
)

// ansiPattern matches the ANSI/ECMA-48 escape sequences a terminal would
// interpret: CSI sequences (colors, cursor motion, erase), OSC sequences
// (hyperlinks, title, clipboard) and single-character ESC sequences.
var ansiPattern = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[@-Z\\-_]`)

// stripControlSeq removes terminal control sequences and remaining control
// characters (C0, C1, DEL) so untrusted text (fetched pages, job logs,
// command lines echoed into listings) cannot move the cursor, repaint the
// screen, or inject OSC 8 hyperlinks when rendered.
//
// Newlines and tabs are preserved; everything else below 0x20, between
// 0x7f and 0x9f is dropped.
func stripControlSeq(s string) string {
	s = ansiPattern.ReplaceAllString(s, "")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			// dropped: control bytes a terminal would act on
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
