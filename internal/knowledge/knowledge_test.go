package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func write(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSymlinkInstructionOutsideRootRefused(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	write(t, outside, "secret.md", "TOP SECRET KEY\n")
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "AGENTS.md")); err != nil {
		t.Skipf("cannot create symlink on this system: %v", err)
	}
	sec := Discover(root)
	if !sec.Empty() {
		t.Fatalf("symlinked instruction file must be refused, got: %q", sec.Text)
	}
}

func TestSymlinkedCreatorDirOutsideRootRefused(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	write(t, outside, "knowledge.md", "OUTSIDE CONTENT\n")
	if err := os.Symlink(outside, filepath.Join(root, ".creator")); err != nil {
		t.Skipf("cannot create symlink on this system: %v", err)
	}
	sec := Discover(root)
	if !sec.Empty() {
		t.Fatalf("symlinked .creator directory must be refused, got: %q", sec.Text)
	}
}

func TestSymlinkInstructionInsideRootAllowed(t *testing.T) {
	root := t.TempDir()
	write(t, root, "real.md", "REAL RULES\n")
	if err := os.Symlink(filepath.Join(root, "real.md"), filepath.Join(root, "AGENTS.md")); err != nil {
		t.Skipf("cannot create symlink on this system: %v", err)
	}
	sec := Discover(root)
	found := false
	for _, f := range sec.Files {
		if strings.Contains(f.Body, "REAL RULES") {
			found = true
		}
	}
	if !found {
		t.Fatalf("in-workspace symlinked instruction should be included, got: %+v", sec.Files)
	}
}

func TestOversizedInstructionFileReadsOnlyPrefix(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("A", MaxFileBytes+10000) + "\nTAILMARKER"
	write(t, root, "AGENTS.md", big)
	sec := Discover(root)
	if len(sec.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(sec.Files))
	}
	if !sec.Files[0].Truncated {
		t.Fatal("oversized instruction file should be marked truncated")
	}
	if strings.Contains(sec.Files[0].Body, "TAILMARKER") {
		t.Fatal("bytes beyond the read cap must not appear in the prompt")
	}
}

func TestDiscoverOrderAndRender(t *testing.T) {
	root := t.TempDir()
	write(t, root, "AGENTS.md", "# House rules\nrun gofmt\n")
	write(t, root, "CLAUDE.md", "use the internal/api package")
	write(t, root, "CREATOR.md", "creator-specific note")

	sec := Discover(root)
	if len(sec.Files) != 3 {
		t.Fatalf("files = %d, want 3: %+v", len(sec.Files), sec.Files)
	}
	want := []string{"AGENTS.md", "CLAUDE.md", "CREATOR.md"}
	for i, name := range want {
		if sec.Files[i].Path != name {
			t.Fatalf("files[%d] = %q, want %q", i, sec.Files[i].Path, name)
		}
	}
	if !strings.Contains(sec.Text, "## Repository instructions") {
		t.Fatalf("section framing missing: %q", sec.Text)
	}
	if !strings.Contains(sec.Text, "run gofmt") || !strings.Contains(sec.Text, "### AGENTS.md") {
		t.Fatalf("section body missing: %q", sec.Text)
	}
	if strings.Index(sec.Text, "run gofmt") > strings.Index(sec.Text, "internal/api") {
		t.Fatal("section must preserve precedence order")
	}
}

func TestDiscoverMissingAndEmpty(t *testing.T) {
	root := t.TempDir()
	write(t, root, "AGENTS.md", "   \n\t\n")
	sec := Discover(root)
	if !sec.Empty() || sec.Text != "" {
		t.Fatalf("empty instruction file should be ignored: %+v", sec)
	}
	if Discover(filepath.Join(root, "nope")).Text != "" {
		t.Fatal("missing workspace should produce an empty section")
	}
}

func TestDiscoverNestedKnowledgeFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, filepath.Join(".creator", "knowledge.md"), "nested instructions")
	sec := Discover(root)
	if len(sec.Files) != 1 || sec.Files[0].Path != ".creator/knowledge.md" {
		t.Fatalf("nested knowledge file not discovered: %+v", sec.Files)
	}
	if !strings.Contains(sec.Text, "nested instructions") {
		t.Fatal("nested knowledge body missing")
	}
}

func TestDiscoverTruncates(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("line of project context that must be bounded\n", 500) // ~23KB
	write(t, root, "AGENTS.md", body)

	sec := Discover(root)
	if len(sec.Files) != 1 {
		t.Fatalf("files = %d", len(sec.Files))
	}
	f := sec.Files[0]
	if !f.Truncated {
		t.Fatal("oversized instruction file should be marked truncated")
	}
	if len(f.Body) > MaxFileBytes+200 {
		t.Fatalf("body = %d bytes, budget is %d", len(f.Body), MaxFileBytes)
	}
	if !utf8.ValidString(f.Body) {
		t.Fatal("truncation split a UTF-8 rune")
	}
	if !strings.Contains(f.Body, "truncated;") {
		t.Fatalf("truncation note missing: %q", tail(f.Body, 80))
	}
}

func TestDiscoverTotalBudget(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("0123456789abcdef\n", 700) // ~12KB each
	for _, name := range Names {
		write(t, root, name, big)
	}
	sec := Discover(root)
	if len(sec.Files) == 0 {
		t.Fatal("no files discovered")
	}
	total := 0
	for _, f := range sec.Files {
		total += len(f.Body)
	}
	if total > MaxTotalBytes+500 {
		t.Fatalf("total injected = %d bytes, budget is %d", total, MaxTotalBytes)
	}
	if total < MaxTotalBytes-1000 {
		t.Fatalf("discovery under-filled the budget: %d of %d bytes", total, MaxTotalBytes)
	}
	last := sec.Files[len(sec.Files)-1]
	if !last.Truncated {
		t.Fatalf("the file that exhausted the budget should be truncated: %+v", last)
	}

}

func TestClipBytesKeepsRunes(t *testing.T) {
	s := strings.Repeat("é", 100) // 200 bytes
	got := clipBytes(s, 51)
	if len(got) > 51 {
		t.Fatalf("clip returned %d bytes, limit 51", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("clip produced invalid UTF-8")
	}
	if got == "" {
		t.Fatal("clip dropped everything")
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
