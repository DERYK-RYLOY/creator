package reduce

import (
	"fmt"
	"strings"
	"testing"
)

func TestSmallOutputPassesThrough(t *testing.T) {
	res := Reduce("h1", "alpha\nbeta\ngamma", Options{Budget: 500})
	if res.Text != "alpha\nbeta\ngamma" {
		t.Fatalf("expected passthrough, got %q", res.Text)
	}
	if res.Truncated {
		t.Fatal("small output should not be truncated")
	}
	if res.TotalLines != 3 {
		t.Fatalf("expected 3 lines, got %d", res.TotalLines)
	}
}

func TestLargeOutputKeepsHeadAndTail(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&b, "line %05d some reasonably long filler text to burn tokens\n", i)
	}
	res := Reduce("h2", b.String(), Options{Budget: 500})
	if !res.Truncated {
		t.Fatal("expected truncation")
	}
	if !strings.Contains(res.Text, "line 00000") {
		t.Fatal("head was dropped")
	}
	if !strings.Contains(res.Text, "line 19999") {
		t.Fatal("tail was dropped: the last line is where failures usually are")
	}
	if !strings.Contains(res.Text, "handle=h2") {
		t.Fatalf("digest must expose the handle: %q", res.Text)
	}
	if got := EstimateTokens(res.Text); got > 900 {
		t.Fatalf("digest exceeded budget: %d tokens", got)
	}
}

func TestEmptyOutput(t *testing.T) {
	res := Reduce("h3", "", Options{})
	if res.Text != "(no output)" {
		t.Fatalf("unexpected text %q", res.Text)
	}
}

func TestDetectSearch(t *testing.T) {
	raw := "internal/a.go:12:func Foo()\ninternal/a.go:40:func Bar()\ninternal/b.go:7:func Baz()\n"
	if got := Detect(raw); got != KindSearch {
		t.Fatalf("expected search, got %s", got)
	}
	res := Reduce("h4", raw, Options{Budget: 500})
	if !strings.Contains(res.Text, "3 matches in 2 files") {
		t.Fatalf("search summary wrong: %q", res.Text)
	}
}

func TestDetectListing(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "internal/pkg%d/file%d.go\n", i%7, i)
	}
	if got := Detect(b.String()); got != KindListing {
		t.Fatalf("expected listing, got %s", got)
	}
	res := Reduce("h5", b.String(), Options{Budget: 600})
	if !strings.Contains(res.Text, "200 paths") {
		t.Fatalf("missing path count: %q", res.Text)
	}
	if !strings.Contains(res.Text, "by extension:") {
		t.Fatalf("missing extension histogram: %q", res.Text)
	}
}

func TestDetectDiff(t *testing.T) {
	raw := `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,2 @@
-old
+new
diff --git a/b.go b/b.go
--- a/b.go
+++ b/b.go
@@ -5 +5 @@
-x
+y
`
	if got := Detect(raw); got != KindDiff {
		t.Fatalf("expected diff, got %s", got)
	}
	res := Reduce("h6", raw, Options{Budget: 500})
	if !strings.Contains(res.Text, "2 files") {
		t.Fatalf("expected file count: %q", res.Text)
	}
	if !strings.Contains(res.Text, "+2") {
		t.Fatalf("expected insertion count: %q", res.Text)
	}
}

func TestBuildLogClustersRepeatedErrors(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, "pkg/a.go:%d: cannot use value (variable of type int) as string\n", i+1)
	}
	b.WriteString("pkg/b.go:9: undefined: helper\n")
	res := Reduce("h7", b.String(), Options{Budget: 400})
	if !strings.Contains(res.Text, "diagnostic lines") {
		t.Fatalf("missing diagnostics header: %q", res.Text)
	}
	if !strings.Contains(res.Text, "×") {
		t.Fatalf("repeated diagnostics were not clustered: %q", res.Text)
	}
	if !strings.Contains(res.Text, "undefined: helper") {
		t.Fatalf("distinct error missing: %q", res.Text)
	}
}

func TestCollapseRepeatedLines(t *testing.T) {
	raw := strings.Repeat("waiting for lock\n", 500)
	res := Reduce("h8", raw, Options{Budget: 300})
	if !strings.Contains(res.Text, "repeated") {
		t.Fatalf("expected repeated-run marker: %q", res.Text)
	}
	if len(res.Text) > 400 {
		t.Fatalf("collapse should shrink the text, got %d bytes", len(res.Text))
	}
}

func TestPageGrep(t *testing.T) {
	raw := "ok\nFAIL one\nok\nFAIL two\n"
	got := Page(raw, "grep", "FAIL", 0, 0, 0, false, 500)
	if !strings.Contains(got, "2 matches") || !strings.Contains(got, "FAIL two") {
		t.Fatalf("unexpected grep page: %q", got)
	}
	if !strings.Contains(got, "4\t") {
		t.Fatalf("expected line numbers: %q", got)
	}
}

func TestPageRangeAndTail(t *testing.T) {
	raw := "1\n2\n3\n4\n5\n"
	if got := Page(raw, "range", "", 2, 3, 0, false, 500); got != "2\t2\n3\t3" {
		t.Fatalf("unexpected range page: %q", got)
	}
	if got := Page(raw, "tail", "", 0, 0, 2, false, 500); got != "4\t4\n5\t5" {
		t.Fatalf("unexpected tail page: %q", got)
	}
}

func TestPageInvalidRegexpFallsBackToLiteral(t *testing.T) {
	got := Page("a(b\nc\n", "grep", "a(b", 0, 0, 0, false, 500)
	if !strings.Contains(got, "matched literally") {
		t.Fatalf("expected literal fallback notice: %q", got)
	}
}
