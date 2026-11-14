package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func splitEditBlock() block {
	args := `{"path":"pkg/main.go","old_string":"func main() {\n\tprintln(\"old\")\n}\n","new_string":"func main() {\n\tprintln(\"new\")\n\tprintln(\"added\")\n}\n"}`
	return block{kind: kindTool, title: "edit", body: args, running: false, ok: true, at: time.Now()}
}

func TestSplitEditToolBoxRendersSideBySide(t *testing.T) {
	b := splitEditBlock()
	lines := renderToolBox(b, 110, nil)
	out := strings.Join(lines, "\n")
	if strings.Contains(out, `{"path"`) {
		t.Fatalf("raw args leaked into the box:\n%s", out)
	}
	if !strings.Contains(out, " │ ") {
		t.Fatalf("split panes missing:\n%s", out)
	}
	for _, want := range []string{"old", "new", "added", "edit"} {
		if !strings.Contains(out, want) {
			t.Fatalf("split box missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "- ") || !strings.Contains(out, "+ ") {
		t.Fatalf("marker column missing:\n%s", out)
	}
	// expanded must reveal more rows, not change the frame width
	full := renderToolBox(block{kind: kindTool, title: "edit", body: largeEditArgs(40), ok: true, at: time.Now(), expanded: true}, 110, nil)
	collapsed := renderToolBox(block{kind: kindTool, title: "edit", body: largeEditArgs(40), ok: true, at: time.Now()}, 110, nil)
	if len(full) <= len(collapsed) {
		t.Fatalf("expand should reveal more lines: full=%d collapsed=%d", len(full), len(collapsed))
	}
	if maxLineWidth(full) != maxLineWidth(collapsed) {
		t.Fatalf("expand must not change the box width: full=%d collapsed=%d", maxLineWidth(full), maxLineWidth(collapsed))
	}
}

func TestSplitWriteToolBoxRendersAdditions(t *testing.T) {
	b := block{kind: kindTool, title: "write", body: `{"path":"out.txt","content":"line one\nline two\nline three\n"}`, ok: true, at: time.Now()}
	lines := renderToolBox(b, 100, nil)
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, "+ ") {
		t.Fatalf("addition markers missing:\n%s", out)
	}
	for _, want := range []string{"line one", "line two", "line three"} {
		if !strings.Contains(out, want) {
			t.Fatalf("split box missing %q:\n%s", want, out)
		}
	}
}

func TestSplitPatchToolBoxShowsHunkNumbers(t *testing.T) {
	diff := "@@ -10,3 +10,4 @@ func main() {\n ctx\n-old line\n+new line\n+added line\n ctx2\n"
	args := fmt.Sprintf("{\"path\":\"pkg/main.go\",\"diff\":%q}", diff)
	b := block{kind: kindTool, title: "patch", body: args, ok: true, at: time.Now()}
	lines := renderToolBox(b, 110, nil)
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, "@@ -10,3 +10,4 @@") {
		t.Fatalf("hunk header missing:\n%s", out)
	}
	if !strings.Contains(out, "10") || !strings.Contains(out, "12") {
		t.Fatalf("hunk line numbers missing:\n%s", out)
	}
	if !strings.Contains(out, "new line") || !strings.Contains(out, "added line") {
		t.Fatalf("added lines missing from right pane:\n%s", out)
	}
}

func TestSplitDiffToolBoxRendersTailDiff(t *testing.T) {
	tail := "--- a/old.go\n+++ b/new.go\n@@ -1,2 +1,3 @@\n keep\n-drop\n+swap\n+extra\n"
	b := block{kind: kindTool, title: "diff", body: `{"path_a":"old.go","path_b":"new.go"}`, tail: tail, ok: true, at: time.Now()}
	lines := renderToolBox(b, 110, nil)
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, "keep") || !strings.Contains(out, "drop") || !strings.Contains(out, "swap") || !strings.Contains(out, "extra") {
		t.Fatalf("tail diff was not split into panes:\n%s", out)
	}
}

func TestSplitFallsBackOnNarrowTerminal(t *testing.T) {
	b := splitEditBlock()
	lines := renderToolBox(b, 70, nil)
	out := strings.Join(lines, "\n")
	if strings.Contains(out, " │ ") {
		t.Fatalf("narrow terminal should not render split panes:\n%s", out)
	}
	if !strings.Contains(out, "old") || !strings.Contains(out, "new") {
		t.Fatalf("fallback preview missing:\n%s", out)
	}
}

func TestSplitAlignsContextAndChanges(t *testing.T) {
	rows := alignLines(
		[]string{"a", "b", "c", "d"},
		[]string{"a", "x", "c", "d", "e"},
		1, 1,
	)
	var kinds []string
	for _, r := range rows {
		kinds = append(kinds, r.kind)
	}
	want := []string{"context", "removed", "added", "context", "context", "added"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("alignment = %v, want %v", kinds, want)
	}
	if rows[1].leftNum != 2 || rows[2].rightNum != 2 || rows[5].rightNum != 5 {
		t.Fatalf("line numbers wrong: %+v", rows)
	}
}

func largeEditArgs(n int) string {
	var oldB, newB strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&oldB, "old line %d\n", i)
		fmt.Fprintf(&newB, "new line %d\n", i)
	}
	args := fmt.Sprintf("{\"path\":\"big.go\",\"old_string\":%q,\"new_string\":%q}", oldB.String(), newB.String())
	return args
}

func maxLineWidth(lines []string) int {
	w := 0
	for _, l := range lines {
		if x := cellWidth(displayText(l)); x > w {
			w = x
		}
	}
	return w
}
