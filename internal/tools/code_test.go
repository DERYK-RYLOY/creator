package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchTool(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "p.go", "content": "package main\n\nfunc Foo() int {\n\treturn 1\n}\n"})
	diff := "--- p.go\n+++ p.go\n@@ -1,5 +1,5 @@\n package main\n \n func Foo() int {\n-\treturn 1\n+\treturn 42\n }\n"
	if out := h.call(t, "patch", map[string]any{"path": "p.go", "diff": diff}); !strings.Contains(out, "applied") {
		t.Fatalf("patch failed: %q", out)
	}
	body, _ := os.ReadFile(filepath.Join(h.root, "p.go"))
	if !strings.Contains(string(body), "return 42") {
		t.Fatalf("patch did not apply: %q", body)
	}
	if strings.Contains(string(body), "return 1") {
		t.Fatalf("old line survived: %q", body)
	}
}

// A removed line whose content starts with "--" serializes as "--- ..." and
// an added line starting with "++" serializes as "+++ ..." — the old
// prefix-scanner treated both as diff file headers, truncating the hunk and
// silently writing a wrong file. Count-driven parsing must not.
func TestPatchRemovedContentStartingWithDoubleDash(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "dash.txt", "content": "package main\n\n-- legacy comment\nkeep this line\n"})
	diff := "--- dash.txt\n+++ dash.txt\n@@ -1,4 +1,3 @@\n package main\n \n--- legacy comment\n keep this line\n"
	out := h.call(t, "patch", map[string]any{"path": "dash.txt", "diff": diff})
	if !strings.Contains(out, "applied") {
		t.Fatalf("patch failed: %q", out)
	}
	body, _ := os.ReadFile(filepath.Join(h.root, "dash.txt"))
	got := string(body)
	if strings.Contains(got, "legacy comment") {
		t.Fatalf("the -- line was not removed: %q", got)
	}
	if !strings.Contains(got, "keep this line") {
		t.Fatalf("surrounding line lost: %q", got)
	}
}

func TestPatchAddedContentStartingWithDoublePlus(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "plus.txt", "content": "package main\nline a\n"})
	diff := "--- plus.txt\n+++ plus.txt\n@@ -1,2 +1,3 @@\n package main\n line a\n+++ added ++ content\n"
	out := h.call(t, "patch", map[string]any{"path": "plus.txt", "diff": diff})
	if !strings.Contains(out, "applied") {
		t.Fatalf("patch failed: %q", out)
	}
	body, _ := os.ReadFile(filepath.Join(h.root, "plus.txt"))
	if !strings.Contains(string(body), "++ added ++ content") {
		t.Fatalf("the ++ line was not added: %q", body)
	}
}

// A hunk whose body is shorter than the @@ header promises is a hard error —
// nothing may be written.
func TestPatchTruncatedHunkFailsClosed(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "trunc.txt", "content": "line a\nline b\nline c\n"})
	diff := "--- trunc.txt\n+++ trunc.txt\n@@ -1,3 +1,3 @@\n line a\n-line b\n+line B\n"
	if _, err := h.reg.Call(context.Background(), "patch", mustRaw(t, map[string]any{"path": "trunc.txt", "diff": diff})); err == nil {
		t.Fatal("truncated hunk must error and the file must be refused")
	}
	body, _ := os.ReadFile(filepath.Join(h.root, "trunc.txt"))
	if string(body) != "line a\nline b\nline c\n" {
		t.Fatalf("file must be untouched on truncated hunk, got %q", body)
	}
}

func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPatchLineContainingDoubleDash(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "dashes.txt", "content": "this line has -- in it\nsecond line\n"})
	diff := "--- dashes.txt\n+++ dashes.txt\n@@ -1,2 +1,1 @@\n-this line has -- in it\n second line\n"
	out := h.call(t, "patch", map[string]any{"path": "dashes.txt", "diff": diff})
	if !strings.Contains(out, "applied") {
		t.Fatalf("patch failed: %q", out)
	}
	body, _ := os.ReadFile(filepath.Join(h.root, "dashes.txt"))
	if strings.Contains(string(body), "this line has") {
		t.Fatalf("the -- looking line was not removed: %q", body)
	}
}

func TestPatchRejectsBadContext(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "q.go", "content": "package main\n\nfunc Bar() int { return 2 }\n"})
	diff := "--- q.go\n+++ q.go\n@@ -1,3 +1,3 @@\n package main\n \n-\treturn 999\n+\treturn 7\n"
	if _, err := h.reg.Call(context.Background(), "patch", json.RawMessage(`{"path":"q.go","diff":"`+strings.ReplaceAll(diff, "\n", "\\n")+`"}`)); err == nil {
		t.Fatal("patch with mismatched context must fail")
	}
}

func TestSearchFilesDoubleStar(t *testing.T) {
	h := newHarness(t)
	os.MkdirAll(filepath.Join(h.root, "a", "b"), 0o755)
	h.call(t, "write", map[string]any{"path": "a/b/c.go", "content": "package c\n// MARKER_X\n"})
	h.call(t, "write", map[string]any{"path": "d.go", "content": "package d\n"})
	out := h.call(t, "search_files", map[string]any{"pattern": "MARKER_X", "glob": "**/*.go"})
	if !strings.Contains(out, "c.go") {
		t.Fatalf("double-star search missed nested file: %q", out)
	}
}

func TestOutlineGo(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "o.go", "content": "package main\n\ntype Foo struct{}\n\nfunc (f *Foo) Bar() int { return 1 }\n\nfunc Baz() {}\n"})
	out := h.call(t, "outline", map[string]any{"path": "o.go"})
	if !strings.Contains(out, "type  Foo") || !strings.Contains(out, "method  Foo.Bar") || !strings.Contains(out, "func  Baz") {
		t.Fatalf("outline missing symbols: %q", out)
	}
}

func TestReadSymbolGo(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "s.go", "content": "package main\n\nfunc Alpha() int { return 1 }\n\nfunc Beta() int { return 2 }\n"})
	out := h.call(t, "read_symbol", map[string]any{"path": "s.go", "symbol": "Beta"})
	if !strings.Contains(out, "return 2") {
		t.Fatalf("read_symbol missed body: %q", out)
	}
	if strings.Contains(out, "return 1") {
		t.Fatalf("read_symbol leaked sibling: %q", out)
	}
}

func TestReviewTool(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "r.go", "content": "package main\n\n// TODO: fix this\nfunc Long() int { return 1 }\n"})
	out := h.call(t, "review", map[string]any{"path": "r.go"})
	if !strings.Contains(out, "markers (TODO/FIXME/HACK): 1") {
		t.Fatalf("review missed marker: %q", out)
	}
	if !strings.Contains(out, "function/method declarations: 1") {
		t.Fatalf("review missed func count: %q", out)
	}
}

func TestAppendAndInsert(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "a.txt", "content": "line1\nline2\n"})
	h.call(t, "append", map[string]any{"path": "a.txt", "content": "line3\n"})
	body, _ := os.ReadFile(filepath.Join(h.root, "a.txt"))
	if !strings.Contains(string(body), "line3") {
		t.Fatalf("append failed: %q", body)
	}
	h.call(t, "insert", map[string]any{"path": "a.txt", "line": 2, "content": "inserted\n"})
	body, _ = os.ReadFile(filepath.Join(h.root, "a.txt"))
	if !strings.Contains(string(body), "inserted") {
		t.Fatalf("insert failed: %q", body)
	}
}

func TestDiffTool(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "x.txt", "content": "a\nb\nc\n"})
	h.call(t, "write", map[string]any{"path": "y.txt", "content": "a\nB\nc\n"})
	out := h.call(t, "diff", map[string]any{"path_a": "x.txt", "path_b": "y.txt"})
	if !strings.Contains(out, "-b") || !strings.Contains(out, "+B") {
		t.Fatalf("diff wrong: %q", out)
	}
}
