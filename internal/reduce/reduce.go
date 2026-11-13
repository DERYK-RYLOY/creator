// Package reduce turns arbitrarily large command output into a token-budgeted
// digest addressed by a stable handle.
//
// The contract that makes Creator different from a shell wrapper: a tool never
// hands raw output to a model. It stores the full text under a handle and
// returns a structural summary plus a pointer. The model decides what to page
// back in with `out`.
package reduce

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DefaultBudget is the default observation budget in tokens.
const DefaultBudget = 1200

// AdaptiveBudget picks a page size from the model's context window, following
// the Aider convention: small windows get a smaller page so a single tool
// observation cannot eat the whole conversation. The result is clamped to
// [200, 800].
func AdaptiveBudget(contextWindow int) int {
	if contextWindow <= 0 {
		contextWindow = 128000
	}
	b := contextWindow / 5
	if b < 200 {
		b = 200
	}
	if b > 800 {
		b = 800
	}
	return b
}

// Kind selects a summarization strategy.
type Kind string

const (
	KindAuto    Kind = ""
	KindGeneric Kind = "generic"
	KindListing Kind = "listing"
	KindSearch  Kind = "search"
	KindDiff    Kind = "diff"
	KindBuild   Kind = "build"
)

// Options configures Reduce.
type Options struct {
	Budget int
	Kind   Kind
}

// Result is a digest plus the metadata needed to describe it.
type Result struct {
	Handle         string
	Kind           Kind
	Text           string
	Summary        string
	TotalLines     int
	TotalBytes     int
	ShownLines     int
	OmittedLines   int
	DuplicateLines int
	Tokens         int
	Truncated      bool
}

// EstimateTokens is a cheap approximation (4 bytes per token) used for budgets.
func EstimateTokens(s string) int { return (len(s) + 3) / 4 }

// Reduce compresses raw output for a model, returning a digest whose handle can
// later be paged with Page.
func Reduce(handle, raw string, opt Options) Result {
	if opt.Budget <= 0 {
		opt.Budget = DefaultBudget
	}
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	res := Result{
		Handle:     handle,
		TotalBytes: len(raw),
	}
	lines := splitLines(raw)
	res.TotalLines = len(lines)
	if len(lines) == 0 {
		res.Kind = KindGeneric
		res.Text = "(no output)"
		res.Summary = "empty"
		return res
	}

	kind := opt.Kind
	if kind == KindAuto {
		kind = Detect(raw)
	}
	res.Kind = kind

	var body []string
	switch kind {
	case KindListing:
		body = summarizeListing(lines)
	case KindSearch:
		body = summarizeSearch(lines, &res)
	case KindDiff:
		body = summarizeDiff(lines)
	case KindBuild:
		body = summarizeBuild(lines)
	default:
		body = summarizeGeneric(lines, &res)
	}

	if kind != KindGeneric {
		// Collapse repeated lines in structured summaries too; output like a
		// build log repeats the same warning a thousand times.
		body, res.DuplicateLines = collapseRuns(body)
	}

	fitted, omitted := fit(body, opt.Budget-60) // reserve room for the trailer
	res.OmittedLines = omitted
	// The fit() marker line is not an output line: don't count it as shown.
	res.ShownLines = len(fitted)
	if omitted > 0 {
		res.ShownLines--
	}
	res.Truncated = omitted > 0 || res.ShownLines < res.TotalLines
	res.Tokens = EstimateTokens(join(fitted))

	out := join(fitted)
	if res.Truncated {
		out += "\n" + trailer(res, opt.Budget)
	}
	res.Text = out
	res.Summary = fmt.Sprintf("%d lines / %s → %s", res.TotalLines, humanBytes(res.TotalBytes), kind)
	return res
}

func trailer(res Result, budget int) string {
	return fmt.Sprintf(
		"[handle=%s · %d of %d lines shown · full output kept server-side. "+
			"Page it with out(handle=%q, mode=\"range\", start=N, end=M) or search it with mode=\"grep\", pattern=\"...\". Budget %d tokens.]",
		res.Handle, res.ShownLines, res.TotalLines, res.Handle, budget)
}

// Detect guesses the output shape.
func Detect(raw string) Kind {
	lines := splitLines(raw)
	if len(lines) == 0 {
		return KindGeneric
	}
	var searchLike, pathLike, diffLike int
	reSearch := regexp.MustCompile(`^(.+?):(\d+):`)
	reDiff := regexp.MustCompile(`^(@@ |diff --git |--- |\+\+\+ )`)
	for _, l := range lines {
		if reSearch.MatchString(l) {
			searchLike++
		}
		if looksLikePath(l) {
			pathLike++
		}
		if reDiff.MatchString(l) {
			diffLike++
		}
	}
	n := float64(len(lines))
	diagnostics := 0
	for _, l := range lines {
		if diagRE.MatchString(l) {
			diagnostics++
		}
	}
	switch {
	case float64(diffLike)/n > 0.15:
		return KindDiff
	// A compiler output is mostly "path:line: error ..." lines, which also
	// look like search hits. Diagnostics win, because clustering them is far
	// more useful than grouping them by file.
	case len(lines) >= 5 && float64(diagnostics)/n > 0.35:
		return KindBuild
	case float64(searchLike)/n > 0.6:
		return KindSearch
	case float64(pathLike)/n > 0.8:
		return KindListing
	}
	return KindGeneric
}

func looksLikePath(l string) bool {
	if l == "" || strings.ContainsAny(l, " \t") {
		return false
	}
	return strings.ContainsAny(l, `/\`)
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func join(lines []string) string { return strings.Join(lines, "\n") }

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// summarizeGeneric keeps the head and tail and records duplicate runs.
func summarizeGeneric(lines []string, res *Result) []string {
	collapsed, dupes := collapseRuns(lines)
	res.DuplicateLines = dupes
	return collapsed
}

// collapseRuns merges consecutive identical lines into one line plus a count.
func collapseRuns(lines []string) ([]string, int) {
	out := make([]string, 0, len(lines))
	removed := 0
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		run := 1
		for i+run < len(lines) && lines[i+run] == line && strings.TrimSpace(line) != "" {
			run++
		}
		out = append(out, line)
		if run > 1 {
			out = append(out, fmt.Sprintf("  … repeated %d more times", run-1))
			removed += run - 1
			i += run - 1
		}
	}
	return out, removed
}

// fit keeps as much of lines as fits in budget tokens, keeping both the head
// (where context lives) and the tail (where failures live).
func fit(lines []string, budget int) ([]string, int) {
	if budget < 80 {
		budget = 80
	}
	total := 0
	for _, l := range lines {
		total += EstimateTokens(l) + 1
	}
	if total <= budget {
		return lines, 0
	}
	headBudget := budget * 55 / 100
	tailBudget := budget * 35 / 100

	var head []string
	used := 0
	for _, l := range lines {
		cost := EstimateTokens(l) + 1
		if used+cost > headBudget {
			break
		}
		head = append(head, l)
		used += cost
	}
	var tail []string
	used = 0
	for i := len(lines) - 1; i >= 0; i-- {
		cost := EstimateTokens(lines[i]) + 1
		if used+cost > tailBudget {
			break
		}
		tail = append(tail, lines[i])
		used += cost
	}
	for i, j := 0, len(tail)-1; i < j; i, j = i+1, j-1 {
		tail[i], tail[j] = tail[j], tail[i]
	}
	if len(head)+len(tail) >= len(lines) {
		return lines, 0
	}
	omitted := len(lines) - len(head) - len(tail)
	marker := fmt.Sprintf("⋮ %d lines omitted from the middle of this output", omitted)
	out := make([]string, 0, len(head)+len(tail)+1)
	out = append(out, head...)
	out = append(out, marker)
	out = append(out, tail...)
	return out, omitted
}

// ---------------------------------------------------------------------------
// Structured summarizers
// ---------------------------------------------------------------------------

var reMatch = regexp.MustCompile(`^(.+):(\d+):(.*)$`)

// summarizeSearch groups grep-shaped results by file. res is threaded
// through for the non-grep fallback so duplicate-line accounting is kept.
func summarizeSearch(lines []string, res *Result) []string {
	type match struct {
		line int
		text string
	}
	order := []string{}
	byFile := map[string][]match{}
	for _, l := range lines {
		m := reMatch.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		ln, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		file := m[1]
		if _, ok := byFile[file]; !ok {
			order = append(order, file)
		}
		byFile[file] = append(byFile[file], match{line: ln, text: strings.TrimSpace(m[3])})
	}
	if len(order) == 0 {
		return summarizeGeneric(lines, res)
	}
	total := 0
	for _, f := range order {
		total += len(byFile[f])
	}
	out := []string{fmt.Sprintf("%d matches in %d files", total, len(order))}
	const maxFiles = 40
	const perFile = 3
	for i, f := range order {
		if i >= maxFiles {
			out = append(out, fmt.Sprintf("… %d more files matched", len(order)-maxFiles))
			break
		}
		ms := byFile[f]
		out = append(out, fmt.Sprintf("%s (%d)", f, len(ms)))
		for j, m := range ms {
			if j >= perFile {
				out = append(out, fmt.Sprintf("  … %d more matches in this file", len(ms)-perFile))
				break
			}
			text := m.text
			text = truncate(text, 160)
			out = append(out, fmt.Sprintf("  %d: %s", m.line, text))
		}
	}
	return out
}

// summarizeListing turns a huge file list into a directory and extension
// histogram plus a sample.
func summarizeListing(lines []string) []string {
	dirs := map[string]int{}
	exts := map[string]int{}
	for _, l := range lines {
		p := strings.TrimSpace(l)
		if p == "" {
			continue
		}
		slashed := filepath.ToSlash(p)
		dir := filepath.ToSlash(filepath.Dir(slashed))
		if dir == "." {
			dir = "(root)"
		}
		dirs[dir]++
		ext := filepath.Ext(slashed)
		if ext == "" {
			ext = "(none)"
		}
		exts[ext]++
	}
	out := []string{fmt.Sprintf("%d paths", len(lines))}

	type kv struct {
		k string
		n int
	}
	sortedDirs := make([]kv, 0, len(dirs))
	for k, n := range dirs {
		sortedDirs = append(sortedDirs, kv{k, n})
	}
	sort.Slice(sortedDirs, func(i, j int) bool {
		if sortedDirs[i].n != sortedDirs[j].n {
			return sortedDirs[i].n > sortedDirs[j].n
		}
		return sortedDirs[i].k < sortedDirs[j].k
	})
	out = append(out, "top directories:")
	for i, d := range sortedDirs {
		if i >= 10 {
			out = append(out, fmt.Sprintf("  … %d more directories", len(sortedDirs)-10))
			break
		}
		out = append(out, fmt.Sprintf("  %4d  %s", d.n, d.k))
	}
	sortedExts := make([]kv, 0, len(exts))
	for k, n := range exts {
		sortedExts = append(sortedExts, kv{k, n})
	}
	sort.Slice(sortedExts, func(i, j int) bool {
		if sortedExts[i].n != sortedExts[j].n {
			return sortedExts[i].n > sortedExts[j].n
		}
		return sortedExts[i].k < sortedExts[j].k
	})
	var parts []string
	for i, e := range sortedExts {
		if i >= 8 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s:%d", e.k, e.n))
	}
	out = append(out, "by extension: "+strings.Join(parts, " "), "sample:")
	limit := 25
	if len(lines) < limit {
		limit = len(lines)
	}
	out = append(out, lines[:limit]...)
	if len(lines) > limit {
		out = append(out, fmt.Sprintf("… %d more paths", len(lines)-limit))
	}
	return out
}

// summarizeDiff keeps the per-file stat instead of every hunk.
func summarizeDiff(lines []string) []string {
	files := 0
	adds, dels := 0, 0
	type stat struct{ add, del int }
	stats := map[string]*stat{}
	order := []string{}
	current := ""
	for _, l := range lines {
		if strings.HasPrefix(l, "diff --git ") {
			fields := strings.Fields(l)
			current = ""
			if len(fields) >= 4 {
				current = strings.TrimPrefix(fields[3], "b/")
			}
			if current != "" {
				stats[current] = &stat{}
				order = append(order, current)
				files++
			}
			continue
		}
		if strings.HasPrefix(l, "@@") {
			if _, ok := stats[current]; !ok && current != "" {
				stats[current] = &stat{}
				order = append(order, current)
				files++
			}
			continue
		}
		if current == "" {
			continue
		}
		switch {
		case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
			adds++
			stats[current].add++
		case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
			dels++
			stats[current].del++
		}
	}
	out := []string{fmt.Sprintf("%d files, +%d −%d", files, adds, dels)}
	for i, f := range order {
		if i >= 25 {
			out = append(out, fmt.Sprintf("… %d more files", len(order)-25))
			break
		}
		s := stats[f]
		out = append(out, fmt.Sprintf("  %-52s +%d −%d", truncate(f, 52), s.add, s.del))
	}
	return out
}

var diagRE = regexp.MustCompile(`(?i)(error|failed|failure|fatal|panic|exception|warning|undefined|cannot|unable to|no such file|not found|timed out|refused)`)
var normalizeRE = regexp.MustCompile(`[0-9]+|0x[0-9a-f]+|"[^"]*"`)

// summarizeBuild clusters diagnostics so 5000 identical warnings become one
// line with a count.
func summarizeBuild(lines []string) []string {
	type cluster struct {
		count   int
		example string
		first   string
	}
	clusters := map[string]*cluster{}
	order := []string{}
	diags := 0
	for i, l := range lines {
		if !diagRE.MatchString(l) {
			continue
		}
		diags++
		key := normalizeRE.ReplaceAllString(strings.ToLower(l), "#")
		key = strings.TrimSpace(key)
		if r := []rune(key); len(r) > 120 {
			key = string(r[:120])
		}
		if c, ok := clusters[key]; ok {
			c.count++
			continue
		}
		loc := ""
		if m := reMatch.FindStringSubmatch(l); m != nil {
			loc = m[1] + ":" + m[2]
		} else if i < 5 {
			loc = ""
		}
		clusters[key] = &cluster{count: 1, example: strings.TrimSpace(l), first: loc}
		order = append(order, key)
	}
	out := []string{fmt.Sprintf("%d diagnostic lines, %d distinct", diags, len(order))}
	if diags == 0 {
		out = append(out, "no diagnostics detected; tail follows")
	}
	type ranked struct {
		key string
		c   *cluster
	}
	rankedClusters := make([]ranked, 0, len(order))
	for _, k := range order {
		rankedClusters = append(rankedClusters, ranked{k, clusters[k]})
	}
	sort.SliceStable(rankedClusters, func(i, j int) bool {
		return rankedClusters[i].c.count > rankedClusters[j].c.count
	})
	const maxClusters = 12
	for i, r := range rankedClusters {
		if i >= maxClusters {
			out = append(out, fmt.Sprintf("… %d more distinct diagnostics", len(rankedClusters)-maxClusters))
			break
		}
		line := fmt.Sprintf("×%-4d %s", r.c.count, truncate(r.c.example, 240))
		if r.c.first != "" {
			line += "   @" + r.c.first
		}
		out = append(out, line)
	}
	tailStart := len(lines) - 8
	if tailStart < 0 {
		tailStart = 0
	}
	out = append(out, "… last lines:", "")
	out = append(out, lines[tailStart:]...)
	return out
}

func truncate(s string, n int) string {
	// Rune-safe: byte slicing corrupts multi-byte UTF-8 output.
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// Paging
// ---------------------------------------------------------------------------

// Page extracts a slice of a stored output. Mode is one of head, tail, range,
// grep, full.
func Page(raw, mode, pattern string, start, end, n int, ignoreCase bool, budget int) string {
	if budget <= 0 {
		budget = DefaultBudget
	}
	lines := splitLines(strings.ReplaceAll(raw, "\r\n", "\n"))
	if len(lines) == 0 {
		return "(no output)"
	}
	switch mode {
	case "", "head":
		if n <= 0 {
			n = 80
		}
		return renderNumbered(lines, 1, min(n, len(lines)), budget, "")
	case "tail":
		if n <= 0 {
			n = 80
		}
		from := len(lines) - n
		if from < 0 {
			from = 0
		}
		return renderNumbered(lines, from+1, len(lines), budget, "")
	case "range":
		if start <= 0 {
			start = 1
		}
		if end <= 0 || end > len(lines) {
			end = len(lines)
		}
		if start > len(lines) {
			return fmt.Sprintf("start line %d is past the end (%d lines)", start, len(lines))
		}
		if start > end {
			return fmt.Sprintf("range start %d is after range end %d; swap them or narrow the slice", start, end)
		}
		return renderNumbered(lines, start, end, budget, "")
	case "grep":
		return grepPage(lines, pattern, ignoreCase, budget)
	case "full":
		return renderNumbered(lines, 1, len(lines), budget, "")
	default:
		return fmt.Sprintf("unknown mode %q: use head, tail, range, grep or full", mode)
	}
}

// renderNumbered emits lines with running line numbers until the token budget
// is exhausted. Token use is tracked incrementally: recounting the whole
// builder for every line makes paging O(n²) on large slices.
func renderNumbered(lines []string, from, to, budget int, prefix string) string {
	var b strings.Builder
	used := 0
	if prefix != "" {
		b.WriteString(prefix)
		b.WriteString("\n")
		used = EstimateTokens(prefix) + 1
	}
	more := 0
	for i := from; i <= to && i <= len(lines); i++ {
		row := fmt.Sprintf("%d\t%s", i, lines[i-1])
		cost := EstimateTokens(row) + 1
		if used+cost > budget {
			more = to - i + 1
			break
		}
		b.WriteString(row)
		b.WriteString("\n")
		used += cost
	}
	if more > 0 {
		fmt.Fprintf(&b, "… %d more lines in this slice; narrow the range or raise output_tokens", more)
	}
	return strings.TrimRight(b.String(), "\n")
}

func grepPage(lines []string, pattern string, ignoreCase bool, budget int) string {
	if pattern == "" {
		return "mode=grep requires a pattern"
	}
	expr := pattern
	if ignoreCase {
		expr = "(?i)" + pattern
	}
	re, err := regexp.Compile(expr)
	plain := strings.ToLower(pattern)
	var hits []string
	for i, l := range lines {
		match := false
		if err == nil {
			match = re.MatchString(l)
		} else {
			hay := l
			if ignoreCase {
				hay = strings.ToLower(l)
			}
			match = strings.Contains(hay, plain)
		}
		if match {
			hits = append(hits, fmt.Sprintf("%d\t%s", i+1, l))
		}
	}
	if len(hits) == 0 {
		return fmt.Sprintf("no lines matched %q", pattern)
	}
	header := fmt.Sprintf("%d matches for %q", len(hits), pattern)
	if err != nil {
		header += " (pattern is not valid regexp; matched literally)"
	}

	// Budget accounting is incremental: summing the whole builder for every
	// hit made paging quadratic.
	var b strings.Builder
	used := EstimateTokens(header) + 1
	b.WriteString(header)
	b.WriteString("\n")
	for i, h := range hits {
		cost := EstimateTokens(h) + 1
		if used+cost > budget {
			fmt.Fprintf(&b, "… %d more matches", len(hits)-i)
			return strings.TrimRight(b.String(), "\n")
		}
		b.WriteString(h)
		b.WriteString("\n")
		used += cost
	}
	return strings.TrimRight(b.String(), "\n")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
