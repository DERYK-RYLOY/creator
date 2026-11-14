package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedWorkspace(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"main.go":              "package main\n\nfunc main() {\n\t// TODO: wire the TUI to the engine\n}\n",
		"internal/app.go":      "package internal\n\nfunc Run() string {\n\t// FIXME: return the digest instead of the raw output\n\treturn \"ok\"\n}\n",
		"internal/app_test.go": "package internal\n\nimport \"testing\"\n\nfunc TestRun(t *testing.T) { // NOTE: smoke\n\t_ = Run()\n}\n",
		"docs/notes.md":        "# notes\n\nNothing flagged here.\n",
	}
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTreeListsWorkspace(t *testing.T) {
	h := newHarness(t)
	seedWorkspace(t, h.root)
	out := h.call(t, "tree", map[string]any{"depth": 3})
	for _, want := range []string{"main.go", "internal/", "app.go", "app_test.go", "docs/", "notes.md"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tree missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "dirs ·") {
		t.Fatalf("tree missing summary line:\n%s", out)
	}
}

func TestTodosFindsMarkersWithCounts(t *testing.T) {
	h := newHarness(t)
	seedWorkspace(t, h.root)
	out := h.call(t, "todos", map[string]any{})
	for _, want := range []string{"TODO 1", "FIXME 1", "NOTE 1", "main.go:4", "app.go:4"} {
		if !strings.Contains(out, want) {
			t.Fatalf("todos missing %q:\n%s", want, out)
		}
	}
	out = h.call(t, "todos", map[string]any{"marker": "FIXME"})
	if strings.Contains(out, "TODO") {
		t.Fatalf("marker filter leaked other kinds:\n%s", out)
	}
	if !strings.Contains(out, "FIXME markers: 1") {
		t.Fatalf("marker summary missing:\n%s", out)
	}
}

func TestFindDefLocatesDeclaration(t *testing.T) {
	h := newHarness(t)
	seedWorkspace(t, h.root)
	out := h.call(t, "find_def", map[string]any{"symbol": "Run"})
	if !strings.Contains(out, "internal/app.go:3") {
		t.Fatalf("find_def missing declaration site:\n%s", out)
	}
	out = h.call(t, "find_def", map[string]any{"symbol": "NoSuchSymbolZzz"})
	if !strings.Contains(out, "no occurrences") {
		t.Fatalf("find_def should report absence:\n%s", out)
	}
}

func TestRepoStatsSummarizes(t *testing.T) {
	h := newHarness(t)
	seedWorkspace(t, h.root)
	out := h.call(t, "repo_stats", map[string]any{})
	for _, want := range []string{".go", ".md", "lines:", "largest files:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("repo_stats missing %q:\n%s", want, out)
		}
	}
}

func TestExploreToolsRefuseEscapes(t *testing.T) {
	h := newHarness(t)
	outside := filepath.Join(h.root, "..", "..")
	cases := []struct {
		name string
		args map[string]any
	}{
		{"tree", map[string]any{"path": outside}},
		{"todos", map[string]any{"path": outside}},
		{"find_def", map[string]any{"symbol": "main", "path": outside}},
		{"repo_stats", map[string]any{"path": outside}},
	}
	for _, tc := range cases {
		raw, err := json.Marshal(tc.args)
		if err != nil {
			t.Fatal(err)
		}
		out, err := h.reg.Call(context.Background(), tc.name, raw)
		if err == nil && !strings.Contains(out, "outside") {
			t.Fatalf("%s should refuse escaping the workspace, got: %s", tc.name, out)
		}
	}
}
