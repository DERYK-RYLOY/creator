package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/creatorcli/creator/internal/config"
)

func TestThemeOverlaySelectPersistsToConfig(t *testing.T) {
	t.Setenv("CREATOR_DATA_DIR", t.TempDir())
	t.Cleanup(func() { ApplyTheme("volcanic-ember-glacial-slate") })
	m := &model{width: 120, height: 40, cwd: t.TempDir(), opts: options{Provider: "anthropic", Model: "claude-sonnet-4-5"}}
	m.openTheme("")
	if m.overlay != overlayTheme {
		t.Fatalf("overlay = %d, want theme overlay", m.overlay)
	}
	slug := "sunlit-cove-copper-reef"
	for i, row := range m.themeRows {
		if row.Slug == slug {
			m.overlayIdx = i
			break
		}
	}
	m.previewThemeRow(m.overlayIdx)
	m.handleThemeListKey(tea.KeyMsg{Type: tea.KeyEnter})
	cfg, err := config.Load(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != slug {
		t.Fatalf("saved theme = %q, want %q", cfg.Theme, slug)
	}
	if CurrentThemeSlug() != slug {
		t.Fatalf("active theme = %q, want %q", CurrentThemeSlug(), slug)
	}
}

func TestThemeDirectCommandIsSilent(t *testing.T) {
	t.Setenv("CREATOR_DATA_DIR", t.TempDir())
	t.Cleanup(func() { ApplyTheme("volcanic-ember-glacial-slate") })
	m := &model{width: 120, height: 40, cwd: t.TempDir(), opts: options{Provider: "anthropic", Model: "claude-sonnet-4-5"}}
	m.execSlashCommand("/theme sunlit-cove-copper-reef")
	if CurrentThemeSlug() != "sunlit-cove-copper-reef" {
		t.Fatalf("direct /theme did not apply: %q", CurrentThemeSlug())
	}
	cfg, err := config.Load(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "sunlit-cove-copper-reef" {
		t.Fatalf("saved theme = %q", cfg.Theme)
	}
	for _, block := range m.transcript {
		if strings.Contains(block.title, "theme") || strings.Contains(block.body, "sunlit") {
			t.Fatalf("direct theme switch should not print a theme save notice: %+v", block)
		}
	}
}

func TestEveryThemeRendersEverySurface(t *testing.T) {
	t.Cleanup(func() { ApplyTheme("volcanic-ember-glacial-slate") })
	m := &model{width: 80, height: 40, opts: options{}, cwd: t.TempDir(), historyIdx: 0}
	for _, row := range ThemeNames() {
		if !ApplyTheme(row.Slug) {
			t.Fatalf("could not activate %s", row.Slug)
		}
		m.transcript = []block{
			{kind: kindUser, title: "YOU", body: "write a file"},
			{kind: kindAssistant, body: "drafting it", at: timeNow()},
			{kind: kindThinking, title: "thinking", body: "looking at the files", streaming: true},
			{kind: kindTool, title: "write", body: `{"path":"out.html","content":"package main"}`, running: true, meta: "running", at: timeNow()},
			{kind: kindCommand, title: "$ creator shell go test", body: "ok", ok: true},
		}
		_ = m.View()
		_ = m.taskView(80, 40)
		_ = m.themeView(80, 40)
		_ = m.sessionsView(80, 40)
	}
}

func timeNow() time.Time { return time.Now() }

func TestThemeListFiltersNamesAndSlugs(t *testing.T) {
	m := &model{}
	m.openTheme("sun")
	found := false
	for _, row := range m.filteredThemes() {
		if row.Slug == "sunlit-cove-copper-reef" {
			found = true
		}
	}
	if !found {
		t.Fatal("filter did not match slug/name by sun")
	}
}
