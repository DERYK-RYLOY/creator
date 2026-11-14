// highlight.go — the span machinery shared by the highlight lenses and the
// in-transcript search. A span is a rune-offset range into a single rendered
// line. Rendering segmentes a line into plain / lens / search runs and styles
// each run independently, so styled output never interleaves with raw text.
package tui

import (
	"regexp"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type textSpan struct{ start, end int } // rune offsets, end exclusive

// runeIndex maps byte offsets to rune indices for one line.
func runeIndex(s string) []int {
	starts := make([]int, 0, len(s)+1)
	for i := range s {
		starts = append(starts, i)
	}
	starts = append(starts, len(s))
	return starts
}

// byteToRune converts a byte offset in starts' domain to a rune index.
func byteToRune(starts []int, b int) int {
	i := sort.Search(len(starts), func(i int) bool { return starts[i] >= b })
	if i > len(starts)-1 {
		return len(starts) - 1
	}
	return i
}

// spansOf returns rune spans matching re inside s. s must be plain text.
func spansOf(s string, re *regexp.Regexp) []textSpan {
	if re == nil || s == "" {
		return nil
	}
	locs := re.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return nil
	}
	starts := runeIndex(s)
	total := len(starts) - 1
	out := make([]textSpan, 0, len(locs))
	for _, loc := range locs {
		a, b := byteToRune(starts, loc[0]), byteToRune(starts, loc[1])
		if a < 0 {
			a = 0
		}
		if b > total {
			b = total
		}
		if b > a {
			out = append(out, textSpan{a, b})
		}
	}
	return out
}

// highlightCtx carries the compiled matchers and prebuilt styles for one
// render pass. Built fresh per visible-lines build; never shared across
// goroutines or frames.
type highlightCtx struct {
	lensRe    *regexp.Regexp
	lensStyle lipgloss.Style
	searchRe  *regexp.Regexp
	searchSty lipgloss.Style
}

const (
	segPlain = iota
	segLens
	segSearch
)

// newHighlightCtx builds the context for one render pass.
// lensIdx < 0 disables the lens; query "" disables search highlighting.
func newHighlightCtx(lensIdx int, lensRe *regexp.Regexp, query string) *highlightCtx {
	ctx := &highlightCtx{}
	if lensRe != nil && lensIdx >= 0 && lensIdx < len(lensCatalog) {
		ctx.lensRe = lensRe
		lens := lensCatalog[lensIdx]
		ctx.lensStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(lens.Color)).
			Bold(true).
			Underline(true)
	}
	if q := strings.TrimSpace(query); q != "" {
		ctx.searchRe = regexp.MustCompile("(?i)" + regexp.QuoteMeta(q))
		ctx.searchSty = lipgloss.NewStyle().
			Foreground(lipgloss.Color(currentTheme.bg)).
			Background(lipgloss.Color(currentTheme.accent2)).
			Bold(true)
	}
	return ctx
}

// active reports whether any matcher is armed.
func (c *highlightCtx) active() bool { return c != nil && (c.lensRe != nil || c.searchRe != nil) }

// segmented renders s in one or more styled segments. baseRender styles the
// plain runs (it applies the caller's foreground); matches are painted by the
// lens/search styles. Search spans take precedence over lens spans.
func (c *highlightCtx) segmented(s string, baseRender func(string) string) string {
	if c == nil || !c.active() {
		return baseRender(s)
	}
	r := []rune(s)
	flags := make([]uint8, len(r))
	mark := func(spans []textSpan, v uint8) {
		for _, sp := range spans {
			for i := sp.start; i < sp.end && i < len(flags); i++ {
				flags[i] = v
			}
		}
	}
	mark(spansOf(s, c.lensRe), segLens)
	mark(spansOf(s, c.searchRe), segSearch)
	var sb strings.Builder
	sb.Grow(len(s) + 32)
	i := 0
	for i < len(flags) {
		j := i
		for j < len(flags) && flags[j] == flags[i] {
			j++
		}
		part := string(r[i:j])
		switch flags[i] {
		case segLens:
			sb.WriteString(c.lensStyle.Render(part))
		case segSearch:
			sb.WriteString(c.searchSty.Render(part))
		default:
			sb.WriteString(baseRender(part))
		}
		i = j
	}
	return sb.String()
}

// segmentedLine renders s with base as the plain-run style, segmenting
// matched runs through the lens/search styles. Nil ctx renders unsegmented.
func segmentedLine(s string, base dynStyle, ctx *highlightCtx) string {
	return ctx.segmented(s, func(part string) string { return base.Render(part) })
}

// lensHitCount counts combined lens occurrences in one plain-text block.
func lensHitCount(s string, re *regexp.Regexp) int {
	if re == nil {
		return 0
	}
	return len(re.FindAllStringIndex(s, -1))
}
