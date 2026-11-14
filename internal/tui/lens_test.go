package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DERYK-RYLOY/creator/internal/buildinfo"
)

func TestBuiltinThemesExactly35AndAllResolve(t *testing.T) {
	if len(builtinThemes) != 35 {
		t.Fatalf("want 35 built-in themes, got %d", len(builtinThemes))
	}
	if len(themeNamesSlugs()) != 35 {
		t.Fatalf("ThemeNames returned %d rows, want 35", len(themeNamesSlugs()))
	}
	for _, th := range builtinThemes {
		if !ApplyTheme(th.Slug) {
			t.Fatalf("theme %q failed to apply", th.Slug)
		}
		if _, ok := roleAssignments[th.Slug]; !ok {
			t.Fatalf("theme %q has no role assignment", th.Slug)
		}
	}
	ApplyTheme("volcanic-ember-glacial-slate")
}

func themeNamesSlugs() []ThemeRow { return ThemeNames() }

func TestLensCatalogExactly35AndWellFormed(t *testing.T) {
	if len(lensCatalog) != 35 {
		t.Fatalf("want 35 lenses, got %d", len(lensCatalog))
	}
	seen := map[string]bool{}
	for i, l := range lensCatalog {
		if seen[l.Slug] {
			t.Fatalf("duplicate lens slug %q", l.Slug)
		}
		seen[l.Slug] = true
		spec, re := lensForIndex(i)
		if spec == nil || re == nil {
			t.Fatalf("lens %q did not compile", l.Slug)
		}
		if !strings.HasPrefix(l.Color, "#") || len(l.Color) != 7 {
			t.Fatalf("lens %q has bad color %q", l.Slug, l.Color)
		}
		if strings.TrimSpace(l.Name) == "" || strings.TrimSpace(l.Desc) == "" {
			t.Fatalf("lens %q missing name/desc", l.Slug)
		}
	}
}

func TestLensCycleWrapsBothDirections(t *testing.T) {
	if got := lensCycle(0, 1); got != 1 {
		t.Fatalf("off → forward = %d, want 1", got)
	}
	if got := lensCycle(0, -1); got != 35 {
		t.Fatalf("off → backward = %d, want 35", got)
	}
	if got := lensCycle(35, 1); got != 1 {
		t.Fatalf("35 → forward = %d, want 1 (wrap)", got)
	}
	if got := lensCycle(1, -1); got != 35 {
		t.Fatalf("1 → backward = %d, want 35 (wrap)", got)
	}
}

func TestSetLensByQuery(t *testing.T) {
	m := &model{}
	if !m.setLensByQuery("errors") || m.lensIdx != 1 {
		t.Fatalf("expected errors lens, idx=%d", m.lensIdx)
	}
	if !m.setLensByQuery("off") || m.lensIdx != 0 {
		t.Fatalf("expected off, idx=%d", m.lensIdx)
	}
	if m.setLensByQuery("definitely-not-a-lens") {
		t.Fatal("unknown lens must be rejected")
	}
}

func sampleTranscript() []block {
	now := time.Now()
	return []block{
		{kind: kindUser, title: "YOU", body: "fix the build, it fails with ERROR", at: now},
		{kind: kindAssistant, title: "AI CREATOR", body: "The build breaks because the cache is stale and ERROR appears twice", at: now},
		{kind: kindTool, title: "shell", body: `{"command":"go build ./..."}`, ok: true, at: now},
		{kind: kindNotice, title: "notice", body: "engine said hello", ok: true, at: now},
	}
}

func TestLensErrorFiltersBlocksButKeepsUser(t *testing.T) {
	m := &model{width: 100, height: 30, cwd: t.TempDir(), transcript: sampleTranscript()}
	ApplyTheme("volcanic-ember-glacial-slate")
	m.lensIdx = 1 // errors
	lines, _ := m.visibleLines(90)
	if len(lines) == 0 {
		t.Fatal("visible lines empty with errors lens; user turn must always stay")
	}
	joined := displayText(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "fix the build") {
		t.Fatal("user turn was filtered out — user blocks must always render")
	}
	if m.lensHidden == 0 {
		t.Fatal("shell/notice blocks without matches should be hidden by the lens")
	}
	if m.lensHits < 2 {
		t.Fatalf("expected ≥2 error hits, got %d", m.lensHits)
	}
	// without the lens every block renders
	m.lensIdx = 0
	m.renderCache.valid = false
	m.gen++
	lines, _ = m.visibleLines(90)
	joined = displayText(strings.Join(lines, "\n"))
	for _, want := range []string{"fix the build", "ERROR appears twice", "go build", "engine said hello"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("unfiltered transcript missing %q:\n%s", want, joined)
		}
	}
}

func TestSearchCountsAndMatchLines(t *testing.T) {
	m := &model{width: 100, height: 30, cwd: t.TempDir(), transcript: sampleTranscript()}
	ApplyTheme("volcanic-ember-glacial-slate")
	m.searchActive = true
	m.searchQuery = "error"
	lines, _ := m.visibleLines(90)
	if m.searchTotal != len(m.matchLines) {
		t.Fatalf("searchTotal=%d but matchLines=%d", m.searchTotal, len(m.matchLines))
	}
	if m.searchTotal < 2 {
		t.Fatalf("expected ≥2 matches for 'error', got %d (lines=%d)", m.searchTotal, len(lines))
	}
	for i, ln := range m.matchLines {
		if ln < 0 || ln >= len(lines) {
			t.Fatalf("match line %d out of range (lines=%d)", ln, len(lines))
		}
		_ = i
	}
	m.searchQuery = "no-such-string-here"
	m.renderCache.valid = false
	m.gen++
	m.visibleLines(90)
	if m.searchTotal != 0 {
		t.Fatalf("searchTotal=%d for absent query", m.searchTotal)
	}
}

func TestClampScrollPinsHugeValue(t *testing.T) {
	m := &model{width: 100, height: 20, cwd: t.TempDir(), transcript: sampleTranscript()}
	ApplyTheme("volcanic-ember-glacial-slate")
	m.scroll = 1 << 30
	m.clampScroll()
	total := m.transcriptLineCount(m.bodyWidth())
	if total > m.bodyHeight() {
		max := total - m.bodyHeight()
		if m.scroll != max {
			t.Fatalf("scroll not clamped to %d, got %d", max, m.scroll)
		}
	} else if m.scroll != 0 {
		t.Fatalf("short transcript should clamp to 0, got %d", m.scroll)
	}
	m.scroll = -50
	m.clampScroll()
	if m.scroll != 0 {
		t.Fatalf("negative scroll not clamped, got %d", m.scroll)
	}
}

func TestFilterCommandsFuzzyAndComplete(t *testing.T) {
	items := filterCommands("")
	if len(items) != 35 {
		t.Fatalf("command catalog = %d, want 35", len(items))
	}
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.Name] {
			t.Fatalf("duplicate command %q", it.Name)
		}
		seen[it.Name] = true
		if it.Category == "" || it.Desc == "" {
			t.Fatalf("command %q missing category/desc", it.Name)
		}
	}
	rev := filterCommands("rev")
	if len(rev) == 0 || rev[0].Name != "/revert" {
		t.Fatalf("'rev' should rank /revert first, got %+v", rev)
	}
	cp := filterCommands("copy-last")
	if len(cp) == 0 || cp[0].Name != "/copy-last" {
		t.Fatalf("'copy-last' should find /copy-last, got %+v", cp)
	}
	lens := filterCommands("lens")
	found := false
	for _, it := range lens {
		if it.Name == "/lens" {
			found = true
		}
	}
	if !found {
		t.Fatal("the /lens command did not match 'lens'")
	}
}

func TestRecordRecentFrecency(t *testing.T) {
	m := &model{}
	for _, name := range []string{"/copy", "/tasks", "/usage", "/copy", "/nope"} {
		m.recordRecent(name)
	}
	want := []string{"/copy", "/usage", "/tasks"}
	if len(m.recentCmds) != len(want) {
		t.Fatalf("recentCmds = %v, want %v (cap/dedup/catalog-only)", m.recentCmds, want)
	}
	for i, name := range want {
		if m.recentCmds[i] != name {
			t.Fatalf("recentCmds = %v, want %v", m.recentCmds, want)
		}
	}
	merged := mergeRecent(commandCatalog(), m.recentCmds)
	if merged[0].Name != "/copy" || !merged[0].recent {
		t.Fatalf("merged recents should lead with /copy, got %+v", merged[0])
	}
	if strings.Count(mustStringJoin(merged), "/usage") != 1 {
		t.Fatal("recents must be deduplicated from the browse list")
	}
}

func mustStringJoin(items []commandItem) string {
	var sb strings.Builder
	for _, it := range items {
		sb.WriteString(it.Name)
		sb.WriteString("\n")
	}
	return sb.String()
}

func TestSpansOfMultibyte(t *testing.T) {
	re := mustLensRe("errors")
	spans := spansOf("an ERROR happened", re)
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %v", spans)
	}
	got := string([]rune("an ERROR happened")[spans[0].start:spans[0].end])
	if got != "ERROR" {
		t.Fatalf("span captured %q, want ERROR", got)
	}
	wide := spansOf("雪と ERROR と", re)
	if len(wide) != 1 {
		t.Fatalf("multibyte: want 1 span, got %v", wide)
	}
	got = string([]rune("雪と ERROR と")[wide[0].start:wide[0].end])
	if got != "ERROR" {
		t.Fatalf("multibyte span captured %q", got)
	}
}

func mustLensRe(slug string) *regexp.Regexp {
	for i, l := range lensCatalog {
		if l.Slug == slug {
			_, re := lensForIndex(i)
			return re
		}
	}
	panic("lens not found: " + slug)
}

func TestViewShowsSearchCounterAndLensChip(t *testing.T) {
	m := &model{
		width: 100, height: 30, cwd: t.TempDir(),
		opts:       options{Provider: "anthropic", Model: "claude-sonnet-4-5", Version: buildinfo.Version, DataDir: t.TempDir()},
		transcript: sampleTranscript(), searchActive: true, searchQuery: "error",
	}
	ApplyTheme("volcanic-ember-glacial-slate")
	m.lensIdx = 1
	got := m.View()
	plain := displayText(got)
	if !strings.Contains(plain, "ERRORS") {
		t.Fatalf("lens chip missing from status line:\n%s", plain)
	}
	if !strings.Contains(plain, "hits") {
		t.Fatalf("lens chip missing hit count:\n%s", plain)
	}
	if !strings.Contains(plain, "error") {
		t.Fatalf("search bar should show the query:\n%s", plain)
	}
	ApplyTheme("volcanic-ember-glacial-slate")
}

func TestExportTranscriptWritesMarkdown(t *testing.T) {
	dir := t.TempDir()
	m := &model{opts: options{DataDir: dir}, transcript: sampleTranscript()}
	note := m.exportTranscript()
	if !strings.HasPrefix(note, "exported → ") {
		t.Fatalf("unexpected export result %q", note)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "exports", "transcript-*.md"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one export under %v, got %v (err=%v)", dir, matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "fix the build") {
		t.Fatal("export missing transcript body")
	}
	if empty := (&model{}).exportTranscript(); empty != "nothing to export yet" {
		t.Fatalf("empty transcript export = %q", empty)
	}
}

func TestShowUsageAndStatsRenderBlocks(t *testing.T) {
	m := &model{width: 100, height: 30, cwd: t.TempDir(), transcript: sampleTranscript()}
	ApplyTheme("volcanic-ember-glacial-slate")
	m.lastRunTools = 3
	m.lastRunTokensIn = 100
	m.lastRunTokensOut = 200
	m.lastRunDuration = 2 * time.Second
	if got := m.showUsage(); got != "" {
		t.Fatalf("showUsage returns %q, want empty notice", got)
	}
	if got := m.showStats(); got != "" {
		t.Fatalf("showStats returns %q, want empty notice", got)
	}
	joined := ""
	for _, b := range m.transcript {
		joined += b.body + "\n"
	}
	if !strings.Contains(joined, "turns") || !strings.Contains(joined, "blocks") {
		t.Fatalf("usage/stats blocks missing from transcript:\n%s", joined)
	}
}

func TestViewTinyTerminalDoesNotPanic(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {8, 3}, {11, 3}, {12, 4}, {80, 3}, {12, 40}, {200, 4}} {
		for _, lens := range []int{0, 1, 17, 35} {
			m := &model{width: size[0], height: size[1], cwd: t.TempDir(), lensIdx: lens, transcript: sampleTranscript()}
			_ = m.View() // must never panic or corrupt the terminal
		}
	}
}

func Example_lensCatalog() {
	fmt.Println(len(lensCatalog))
	// Output: 35
}

// TestModelSmokeFlow drives the model through a full synthetic session —
// boot, resize, ticks, typing, submit (engine absent), search, all 35 lens
// positions, sidebar toggle, palette and the cheatsheet overlay — asserting
// no panic and a non-empty frame at every step. This is the closest thing to
// "the app starts and stays stable" a headless suite can offer.
func TestModelSmokeFlow(t *testing.T) {
	ApplyTheme("volcanic-ember-glacial-slate")
	m := &model{cwd: t.TempDir(), historyIdx: 0}
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 36})
	now := time.Now()
	for i := 0; i < 12; i++ {
		m.Update(tickMsg{at: now.Add(time.Duration(i) * time.Millisecond)})
	}
	if got := m.View(); got == "" {
		t.Fatal("empty frame after boot")
	}
	// type a prompt and submit it; the engine binary is absent in tests so
	// startRun fails gracefully into a notice instead of a process.
	for _, r := range "fix the build error" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(strings.Join(blockBodies(m.transcript), "\n"), "fix the build error") {
		t.Fatal("submitted prompt missing from transcript")
	}
	if got := m.View(); got == "" {
		t.Fatal("empty frame after submit")
	}
	// ctrl+f search flow: open, type, navigate, clear, close
	m.searchActive = false
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	if !m.searchActive {
		t.Fatal("ctrl+f did not arm the search bar")
	}
	for _, r := range "error" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_ = m.View() // builds match lines
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	for i := 0; i < 2; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	}
	if m.searchActive {
		t.Fatal("esc did not close the search bar")
	}
	// every lens position renders a frame without panic
	for i := 0; i < 36; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
		if got := m.View(); got == "" {
			t.Fatalf("empty frame with lensIdx=%d", m.lensIdx)
		}
	}
	// sidebar toggle + both palette edges
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlB})
	_ = m.View()
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if !m.palette {
		t.Fatal("ctrl+p did not open the palette")
	}
	_ = m.View()
	m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	// cheatsheet overlay renders and closes
	m.openHelp()
	_got := displayText(m.View())
	if !strings.Contains(_got, "K E Y B O A R D") {
		t.Fatalf("cheatsheet overlay missing its title:\n%s", _got)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if m.overlay != overlayNone {
		t.Fatal("esc did not close the cheatsheet")
	}
	if got := m.View(); got == "" {
		t.Fatal("empty frame at end of smoke flow")
	}
}

func blockBodies(blocks []block) []string {
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, b.body)
	}
	return out
}
