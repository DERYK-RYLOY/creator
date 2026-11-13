package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFilterCommandsMatchesAndDefaults(t *testing.T) {
	all := filterCommands("")
	if len(all) == 0 {
		t.Fatal("empty query should list commands")
	}
	filtered := filterCommands("rev")
	if len(filtered) == 0 {
		t.Fatal("expected /revert to match rev")
	}
	if !strings.Contains(filtered[0].Name, "revert") && len(filtered) > 1 {
		found := false
		for _, item := range filtered {
			if strings.Contains(item.Name, "revert") {
				found = true
			}
		}
		if !found {
			t.Fatalf("revert missing from matches: %+v", filtered)
		}
	}
}

func TestBuildFileTreeIgnoresNoise(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tree := buildFileTree(root, 3, 100)
	if !strings.Contains(tree, "main.go") {
		t.Fatalf("file tree missing src/main.go:\n%s", tree)
	}
	if strings.Contains(tree, "HEAD") {
		t.Fatalf("file tree leaked .git:\n%s", tree)
	}
}

func TestPolicyApprovalMapping(t *testing.T) {
	if got := policyToApproval("strict"); got != approvalManual {
		t.Fatalf("strict maps to %d", got)
	}
	if got := policyToApproval("yolo"); got != approvalBypass {
		t.Fatalf("yolo maps to %d", got)
	}
	if got := policyToApproval("balanced"); got != approvalAcceptEdits {
		t.Fatalf("balanced maps to %d", got)
	}
}

func TestReadableSize(t *testing.T) {
	if got := readableSize(999); got != "999" {
		t.Fatalf("readableSize(999) = %q", got)
	}
	if got := readableSize(1500); got != "1.5k" {
		t.Fatalf("readableSize(1500) = %q", got)
	}
}

func TestViewRendersInputAndPalette(t *testing.T) {
	m := &model{width: 120, height: 34, cwd: t.TempDir(), opts: options{Provider: "anthropic", Model: "claude-sonnet-4-5", Version: version}, historyIdx: 0}
	got := m.View()
	if !strings.Contains(got, "❯") {
		t.Fatalf("view missing prompt:\n%s", got)
	}
	if strings.Contains(got, "Welcome back") || strings.Contains(got, "MISSION CONTROL") {
		t.Fatalf("view still renders the old chrome/logo:\n%s", got)
	}
	m.openPalette("r")
	if got := m.View(); !strings.Contains(got, "/ commands") {
		t.Fatalf("palette view missing palette:\n%s", got)
	}
}

func TestMouseWheelScrolls(t *testing.T) {
	m := &model{scroll: 0}
	m.handleMouse(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if m.scroll != 2 {
		t.Fatalf("wheel up should scroll by 2, got %d", m.scroll)
	}
	m.handleMouse(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if m.scroll != 0 {
		t.Fatalf("wheel down should unscroll, got %d", m.scroll)
	}
}

func TestToolBoxRendersMinimalBox(t *testing.T) {
	b := block{kind: kindTool, title: "shell", body: `{"command":"go test ./..."}`, running: true, at: time.Now().Add(-time.Second), bytes: 2048, tail: "ok github.com/example/app"}
	lines := renderBlockZen(b, 80)
	out := strings.Join(lines, "\n")
	for _, want := range []string{"┌", "┘", "shell", "running", "go test ./...", "ok github.com/example/app"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tool box missing %q:\n%s", want, out)
		}
	}
}

func TestMouseLeftClickExpandsLatestToolBlock(t *testing.T) {
	m := &model{width: 80, height: 40, transcript: []block{{kind: kindTool, title: "write", body: `{"path":"x.go","content":"package x"}`}}}
	m.handleMouse(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m.handleMouse(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if !m.transcript[0].expanded {
		t.Fatal("left click should expand latest tool box")
	}
	m.handleMouse(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m.handleMouse(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if m.transcript[0].expanded {
		t.Fatal("left click should collapse again")
	}
}

func TestMouseClickOnlyTogglesOnPressAndHitsExactBlock(t *testing.T) {
	m := &model{width: 80, height: 40, transcript: []block{{kind: kindThinking, streaming: true}, {kind: kindAssistant, body: "answer"}, {kind: kindCommand, title: "cmd", body: "out"}}}
	m.handleMouse(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if m.transcript[2].expanded {
		t.Fatal("release events must not toggle a box")
	}
	m.handleMouse(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m.handleMouse(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if !m.transcript[0].expanded {
		t.Fatal("y=0 should hit the first block")
	}
	if m.transcript[2].expanded {
		t.Fatal("y=0 must not toggle a later block")
	}
	m.handleMouse(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m.handleMouse(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if m.transcript[0].expanded {
		t.Fatal("a second press should collapse the same block")
	}
}

func TestTranscriptPlainTextIncludesBlocks(t *testing.T) {
	m := &model{transcript: []block{
		{kind: kindUser, body: "make website"},
		{kind: kindAssistant, body: "here is the site"},
		{kind: kindTool, title: "write", body: `{"path":"a.html"}`, tail: "wrote a.html"},
	}}
	got := transcriptPlainText(m.transcript)
	for _, want := range []string{"YOU", "ASSISTANT", "WRITE", "a.html", "wrote a.html"} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript copy missing %q:\n%s", want, got)
		}
	}
}

func TestSelectionCopiesVisibleTranscriptText(t *testing.T) {
	lines := []string{
		"plain text line one",
		"link https://example.com here",
		"tail",
	}
	got := copySelectionText(lines, selectPoint{line: 0, col: 6}, selectPoint{line: 1, col: 26})
	if !strings.Contains(got, "text line one") || !strings.Contains(got, "link https://example.com") {
		t.Fatalf("selection text = %q", got)
	}
}

func TestWindowsUTF16EncodingPreservesNonASCII(t *testing.T) {
	got := encodeUTF16LE("쾟")
	if len(got) < 4 || got[0] != 0xff || got[1] != 0xfe {
		t.Fatalf("missing BOM: % x", got)
	}
}

func TestSelectionTextPreservesWordsAndSpaces(t *testing.T) {
	line := "link  https://example.com"
	got := copySelectionText([]string{line}, selectPoint{line: 0, col: 0}, selectPoint{line: 0, col: len(line) - 1})
	if got != line {
		t.Fatalf("selection text = %q, want %q", got, line)
	}
}
