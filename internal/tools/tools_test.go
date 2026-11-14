package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DERYK-RYLOY/creator/internal/policy"
	"github.com/DERYK-RYLOY/creator/internal/runner"
	"github.com/DERYK-RYLOY/creator/internal/store"
)

type harness struct {
	env  *Env
	reg  *Registry
	root string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := runner.NewManager(st)
	engine := policy.NewEngine("yolo", true, false, strings.NewReader(""), io.Discard)
	shell := pickShell()
	env := &Env{
		Workspace: root,
		Store:     st,
		Jobs:      jobs,
		Policy:    engine,
		Session:   "test-session",
		Budget:    300,
		Shell:     shell,
	}
	t.Cleanup(func() {
		jobs.StopAll()
		st.Close()
	})
	return &harness{env: env, reg: New(env), root: root}
}

// pickShell resolves the interpreter the same way production does (auto), so
// tests generate commands in the syntax that will actually execute.
func pickShell() string {
	for _, c := range []string{"bash", "pwsh", "powershell", "cmd"} {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	return "cmd"
}

func (h *harness) call(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.reg.Call(context.Background(), name, raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

func TestWriteReadEditCheckpoint(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	write := h.call(t, "write", map[string]any{"path": "pkg/main.go", "content": "package pkg\n\nfunc A() {}\n"})
	if !strings.Contains(write, "wrote pkg/main.go") {
		t.Fatalf("unexpected write result: %q", write)
	}

	read := h.call(t, "read", map[string]any{"path": "pkg/main.go"})
	if !strings.Contains(read, "1  package pkg") {
		t.Fatalf("read should be line numbered: %q", read)
	}

	edit := h.call(t, "edit", map[string]any{"path": "pkg/main.go", "old_string": "func A() {}", "new_string": "func A() int { return 1 }"})
	if !strings.Contains(edit, "edited pkg/main.go") {
		t.Fatalf("unexpected edit result: %q", edit)
	}
	data, err := os.ReadFile(filepath.Join(h.root, "pkg", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "return 1") {
		t.Fatalf("edit not applied: %q", data)
	}

	// Second edit must produce an ambiguous-match error rather than guessing.
	_, err = h.reg.Call(ctx, "edit", json.RawMessage(`{"path":"pkg/main.go","old_string":"","new_string":"x"}`))
	if err == nil {
		t.Fatal("expected an error for empty old_string")
	}

	// Overwriting the existing file must checkpoint the previous version too.
	h.call(t, "write", map[string]any{"path": "pkg/main.go", "content": "package pkg\n"})

	checkpoints := h.env.Store.ListCheckpoints(10)
	if len(checkpoints) < 2 {
		t.Fatalf("expected checkpoints for overwrite and edit, got %d", len(checkpoints))
	}
	cp := checkpoints[0]
	body, err := h.env.Store.ReadCheckpoint(cp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "func A() {}") && !strings.Contains(string(body), "return 1") {
		t.Fatalf("checkpoint content unexpected: %q", body)
	}
}

func TestEditRejectsAmbiguity(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "a.txt", "content": "x\nx\n"})
	_, err := h.reg.Call(context.Background(), "edit", json.RawMessage(`{"path":"a.txt","old_string":"x","new_string":"y"}`))
	if err == nil || !strings.Contains(err.Error(), "appears 2 times") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
	out := h.call(t, "edit", map[string]any{"path": "a.txt", "old_string": "x", "new_string": "y", "replace_all": true})
	if !strings.Contains(out, "replaced 2 occurrences") {
		t.Fatalf("unexpected replace_all result: %q", out)
	}
}

func TestWorkspaceEscapeRejected(t *testing.T) {
	h := newHarness(t)
	_, err := h.reg.Call(context.Background(), "read", json.RawMessage(`{"path":"../../etc/passwd"}`))
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("expected escape rejection, got %v", err)
	}
	outside := filepath.Join(h.root, "..", "evil.sh")
	raw, _ := json.Marshal(map[string]any{"path": outside, "content": "x"})
	if _, err = h.reg.Call(context.Background(), "write", raw); err == nil {
		t.Fatal("path outside the workspace must be rejected")
	}
}

func TestGlobAndGrep(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "internal/a/one.go", "content": "package a\n// TODO: fix\nfunc One() {}\n"})
	h.call(t, "write", map[string]any{"path": "internal/b/two.go", "content": "package b\nfunc Two() {}\n"})
	h.call(t, "write", map[string]any{"path": "internal/b/notes.md", "content": "# TODO notes\n"})

	glob := h.call(t, "glob", map[string]any{"pattern": "**/*.go"})
	if !strings.Contains(glob, "internal/a/one.go") || !strings.Contains(glob, "internal/b/two.go") {
		t.Fatalf("glob missed files: %q", glob)
	}
	if strings.Contains(glob, "notes.md") {
		t.Fatalf("glob matched the wrong extension: %q", glob)
	}

	grep := h.call(t, "grep", map[string]any{"pattern": "TODO", "glob": "**/*.{go,md}"})
	if !strings.Contains(grep, "internal/a/one.go:2") {
		t.Fatalf("grep missed the match: %q", grep)
	}
	if !strings.Contains(grep, "matches for") {
		t.Fatalf("grep header missing: %q", grep)
	}

	list := h.call(t, "list", map[string]any{"path": "internal"})
	if !strings.Contains(list, "a/") || !strings.Contains(list, "b/") {
		t.Fatalf("list missed directories: %q", list)
	}
}

// emit400 returns a command that prints line1..line400 in the syntax of the
// shell this harness will actually use.
func emit400(shell string) string {
	switch {
	case shell == "cmd":
		return "for /L %i in (1;1;400) do @echo line%i"
	case shell == "bash" || shell == "sh" || shell == "zsh":
		return "for i in $(seq 1 400); do echo line$i; done"
	default:
		return "for ($i = 1; $i -le 400; $i++) { Write-Output \"line$i\" }"
	}
}

func TestShellDigestAndPaging(t *testing.T) {
	h := newHarness(t)
	out := h.call(t, "shell", map[string]any{
		"command": emit400(h.env.Shell),
	})
	if !strings.Contains(out, "success") {
		t.Fatalf("expected success header: %q", out)
	}
	if !strings.Contains(out, "line1") {
		t.Fatalf("expected head of output: %q", out)
	}
	if !strings.Contains(out, "line400") {
		t.Fatalf("expected tail of output: %q", out)
	}
	if reduceTokens(out) > 800 {
		t.Fatalf("digest ignored the budget: %d tokens", reduceTokens(out))
	}
	handle := regexp.MustCompile(`handle=([a-z0-9_]+)`).FindStringSubmatch(out)
	if handle == nil {
		t.Fatalf("no handle in digest: %q", out)
	}
	page := h.call(t, "out", map[string]any{"handle": handle[1], "mode": "tail", "n": 3})
	if !strings.Contains(page, "line400") || !strings.Contains(page, "line398") {
		t.Fatalf("tail page wrong: %q", page)
	}
	if !strings.Contains(page, "400 lines total") {
		t.Fatalf("page should report total lines: %q", page)
	}

	grep := h.call(t, "out", map[string]any{"handle": handle[1], "mode": "grep", "pattern": "^line39.$"})
	if !strings.Contains(grep, "10 matches") {
		t.Fatalf("grep page wrong: %q", grep)
	}
}

func TestBackgroundShellAndJobs(t *testing.T) {
	h := newHarness(t)
	out := h.call(t, "shell", map[string]any{"command": "sleep 1; echo finished-later", "background": true})
	if !strings.Contains(out, "background") {
		t.Fatalf("expected background notice: %q", out)
	}
	handle := regexp.MustCompile(`job ([a-z0-9_]+)`).FindStringSubmatch(out)
	if handle == nil {
		t.Fatalf("no job id returned: %q", out)
	}
	jobs := h.call(t, "jobs", map[string]any{})
	if !strings.Contains(jobs, handle[1]) {
		t.Fatalf("job not listed: %q", jobs)
	}
}

type panicHook struct{}

func (p panicHook) BeforeTool(name string, args json.RawMessage) (json.RawMessage, error) {
	panic("boom-before")
}
func (p panicHook) AfterTool(name, result string, err error) string { panic("boom-after") }
func (p panicHook) BeforeDelegate(opts map[string]any) (map[string]any, error) {
	panic("boom-before-delegate")
}
func (p panicHook) AfterDelegate(task, summary string) string { panic("boom-after-delegate") }

func TestPanicInBeforeToolIsConvertedToError(t *testing.T) {
	h := newHarness(t)
	h.env.Hooks = panicHook{}
	_, err := h.reg.Call(context.Background(), "shell", json.RawMessage(`{"command":"echo hi"}`))
	if err == nil {
		t.Fatal("panic in BeforeTool must surface as an error")
	}
	if !strings.Contains(err.Error(), "BeforeTool hook panicked") {
		t.Fatalf("err = %v", err)
	}
}

type afterOnlyPanicHook struct{}

func (h afterOnlyPanicHook) BeforeTool(name string, args json.RawMessage) (json.RawMessage, error) {
	return args, nil
}
func (h afterOnlyPanicHook) AfterTool(name, result string, err error) string {
	panic("boom-after")
}
func (h afterOnlyPanicHook) BeforeDelegate(opts map[string]any) (map[string]any, error) {
	return opts, nil
}
func (h afterOnlyPanicHook) AfterDelegate(task, summary string) string { return "" }

func TestPanicInAfterToolDoesNotCorruptTheOutcome(t *testing.T) {
	h := newHarness(t)
	h.env.Hooks = afterOnlyPanicHook{}
	out, err := h.reg.Call(context.Background(), "shell", json.RawMessage(`{"command":"echo plain-proof"}`))
	if err != nil {
		t.Fatalf("AfterTool panic must not break a normal tool call: %v", err)
	}
	if !strings.Contains(out, "plain-proof") {
		t.Fatalf("tool result must survive the AfterTool panic, got %q", out)
	}
}

type stubAfterToolHook struct{ text string }

func (h stubAfterToolHook) BeforeTool(name string, args json.RawMessage) (json.RawMessage, error) {
	return args, nil
}
func (h stubAfterToolHook) AfterTool(name, result string, err error) string {
	return h.text
}
func (h stubAfterToolHook) BeforeDelegate(opts map[string]any) (map[string]any, error) {
	return opts, nil
}
func (h stubAfterToolHook) AfterDelegate(task, summary string) string { return "" }

// A hook may rewrite the message but must never silently clear the failure
// from the caller's view.
func TestAfterToolCannotClearAnError(t *testing.T) {
	h := newHarness(t)
	h.env.Hooks = stubAfterToolHook{text: "hook said everything is fine"}
	_, err := h.reg.Call(context.Background(), "shell", json.RawMessage(`{"command":""}`))
	if err == nil {
		t.Fatal("expected an error; blank command output must fail even when a hook rewrites the success text")
	}
	if !strings.Contains(err.Error(), "command is required") {
		t.Fatalf("the original failure must survive the hook, got %v", err)
	}
}

func TestUnknownTool(t *testing.T) {
	h := newHarness(t)
	if _, err := h.reg.Call(context.Background(), "nope", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected unknown-tool error")
	}
}

func reduceTokens(s string) int { return (len(s) + 3) / 4 }

// The whitespace-normalized fallback used to treat a blank pattern line as
// satisfied by ANY document line, so old_string "a\n\nb" matched "a\nX\nb"
// and the rewrite deleted the X line. Blank pattern lines may now only
// match blank(-or-whitespace) document lines.
func TestEditBlankPatternLineDoesNotEatContent(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "e.txt", "content": "a\nX\nb\n"})
	_, err := h.reg.Call(context.Background(), "edit", rawJSON(`{"path":"e.txt","old_string":"a\n\nb","new_string":"a\nb"}`))
	if err == nil {
		t.Fatal("blank pattern line must not match a non-blank document line")
	}
	body, _ := os.ReadFile(filepath.Join(h.root, "e.txt"))
	if string(body) != "a\nX\nb\n" {
		t.Fatalf("document was modified by a refused edit: %q", body)
	}
}

func TestEditBlankPatternLineMatchesWhitespaceBlank(t *testing.T) {
	h := newHarness(t)
	// The document's blank line carries stray spaces — the kind of stray
	// whitespace the fallback exists to tolerate.
	h.call(t, "write", map[string]any{"path": "e.txt", "content": "a\n   \nb\n"})
	out := h.call(t, "edit", map[string]any{
		"path":       "e.txt",
		"old_string": "a\n\nb",
		"new_string": "a\nb",
	})
	if !strings.Contains(out, "whitespace normalization") {
		t.Fatalf("expected fuzzy-edit note, got %q", out)
	}
	body, _ := os.ReadFile(filepath.Join(h.root, "e.txt"))
	if string(body) != "a\nb\n" {
		t.Fatalf("content wrong after normalized edit: %q", body)
	}
}

// A fuzzy-matched replacement must keep the document's own indentation on
// every line — the first one included — instead of flushing it to col 0.
func TestEditFuzzyWhitespaceFirstLineKeepsIndent(t *testing.T) {
	h := newHarness(t)
	src := "func A() {\n    old := 1\n    return old\n}\n"
	h.call(t, "write", map[string]any{"path": "f.go", "content": src})
	_ = h.call(t, "edit", map[string]any{
		"path":       "f.go",
		"old_string": "old := 1\n    return old",
		"new_string": "new := 2\n    return new",
	})
	body, _ := os.ReadFile(filepath.Join(h.root, "f.go"))
	want := "func A() {\n    new := 2\n    return new\n}\n"
	if string(body) != want {
		t.Fatalf("first line of replacement lost the doc indent:\n%s", body)
	}
}

func TestEditFuzzyWhitespaceFallback(t *testing.T) {
	h := newHarness(t)
	// Four-space indent on disk; the model sends a tab-indented snippet.
	src := "func App() {\n    for i := 0; i < 3; i++ {\n        total += i\n    }\n}\n"
	h.call(t, "write", map[string]any{"path": "app.go", "content": src})
	out := h.call(t, "edit", map[string]any{
		"path":       "app.go",
		"old_string": "func App() {\n\tfor i := 0; i < 3; i++ {\n\t\ttotal += i\n\t}\n}",
		"new_string": "func App() {\n\tfor i := 0; i < 3; i++ {\n\t\ttotal += i * 2\n\t}\n}",
	})
	if !strings.Contains(out, "whitespace normalization") {
		t.Fatalf("expected fuzzy-edit note, got %q", out)
	}
	body, err := os.ReadFile(filepath.Join(h.root, "app.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "total += i * 2") {
		t.Fatalf("replacement missing: %q", body)
	}
	if !strings.Contains(string(body), "    for i := 0") && !strings.Contains(string(body), "\tfor i := 0") {
		t.Fatalf("indent structure lost: %q", body)
	}
}

func TestEditNearMissReportsContext(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "near.go", "content": "package main\n\nfunc Alpha() {}\nfunc Bravo() {}\n"})
	_, err := h.reg.Call(context.Background(), "edit", json.RawMessage(`{"path":"near.go","old_string":"func Charlie() {}","new_string":"func CharlieV2() {}"}`))
	if err == nil {
		t.Fatal("editing a nonexistent symbol must fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "closest") {
		t.Fatalf("expected a closest-context hint, got %q", msg)
	}
	if !strings.Contains(msg, "Alpha") || !strings.Contains(msg, "Bravo") {
		t.Fatalf("hint should mention nearby lines, got %q", msg)
	}
}

func TestGlobRootOnlySemantics(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "root.go", "content": "package main\n"})
	h.call(t, "write", map[string]any{"path": "sub/root.go", "content": "package sub\n"})

	top := h.call(t, "glob", map[string]any{"pattern": "*.go"})
	if !strings.Contains(top, "root.go") {
		t.Fatalf("root-level match missing: %q", top)
	}
	if strings.Contains(top, "sub/root.go") {
		t.Fatalf("'*.go' must not match subdirectories: %q", top)
	}

	all := h.call(t, "glob", map[string]any{"pattern": "**/*.go"})
	if !strings.Contains(all, "sub/root.go") {
		t.Fatalf("'**/*.go' must reach subdirectories: %q", all)
	}
}

func TestHashTool(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "sample.bin", "content": "hello creator\n"})
	out := h.call(t, "hash", map[string]any{"path": "sample.bin", "algo": "sha256"})
	if !strings.Contains(out, "sha256") {
		t.Fatalf("expected sha256 in %q", out)
	}
	if !strings.Contains(out, "(14 bytes)") {
		t.Fatalf("hash should report size, got %q", out)
	}
	if _, err := h.reg.Call(context.Background(), "hash", json.RawMessage(`{"path":"sample.bin","algo":"nope"}`)); err == nil {
		t.Fatal("unsupported hash algo must fail")
	}
}

func TestStringsTool(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "sample.bin", "content": "creator-re-tool-42 the_quick_brown_fox\n"})
	out := h.call(t, "strings", map[string]any{"path": "sample.bin", "min_len": 5})
	if !strings.Contains(out, "creator-re-tool-42") {
		t.Fatalf("strings should extract the known string, got %q", out)
	}
	// min_len filtering: with min_len above the token it should not include noise? just assert it runs.
	out2 := h.call(t, "strings", map[string]any{"path": "sample.bin", "min_len": 40})
	if strings.Contains(out2, "creator-re-tool") {
		t.Fatalf("min_len should filter short strings, got %q", out2)
	}
}

func TestNetFetchTool(t *testing.T) {
	t.Setenv("CREATOR_ALLOW_PRIVATE_NET", "1")
	h := newHarness(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello-from-server"))
	}))
	defer srv.Close()
	out := h.call(t, "net_fetch", map[string]any{"url": srv.URL, "max_bytes": 200})
	if !strings.Contains(out, "hello-from-server") {
		t.Fatalf("net_fetch failed: %q", out)
	}
}

// search_files must cap max_results/context at the shared GrepResults
// constants: a ballooned caller argument must not turn into a whole-tree
// dump.
func TestSearchFilesClampsMaxResults(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 450; i++ {
		path := filepath.Join(h.root, fmt.Sprintf("f_%03d.txt", i))
		if err := os.WriteFile(path, []byte("NEEDLE marker\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out := h.call(t, "search_files", map[string]any{"pattern": "NEEDLE", "max_results": 1000000, "context": 999999})
	if !strings.Contains(out, "400 match(es) total") {
		t.Fatalf("expected max_results clamped to GrepResults=400; tail:\n%s", out[len(out)-200:])
	}
}

func TestSearchFilesRejectsEscapeAndStaysTextOnly(t *testing.T) {
	h := newHarness(t)
	outside := filepath.Join(filepath.Dir(h.root), "secret.txt")
	if err := os.WriteFile(outside, []byte("top-secret-should-not-leak\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := h.reg.Call(context.Background(), "search_files", json.RawMessage(`{"pattern":"secret","glob":"../secret.txt"}`))
	if err == nil {
		t.Fatal("search_files glob outside the workspace must be refused")
	}
	out := h.call(t, "search_files", map[string]any{"pattern": "secret", "glob": "**/*"})
	if strings.Contains(out, "top-secret-should-not-leak") {
		t.Fatalf("search_files leaked a file outside the workspace:\n%s", out)
	}
}

func TestGrepAndSearchFilesDoNotFollowOutsideSymlinks(t *testing.T) {
	h := newHarness(t)
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside-secret.txt")
	if err := os.WriteFile(outsideFile, []byte("outside-needle-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(h.root, "link.txt")); err != nil {
		t.Skipf("cannot create symlink on this system: %v", err)
	}
	out := h.call(t, "grep", map[string]any{"pattern": "outside-needle-123"})
	if strings.Contains(out, "link.txt:") || !strings.Contains(out, "no matches") {
		t.Fatalf("grep leaked a symlinked file that escapes the workspace:\n%s", out)
	}
	for _, glob := range []string{"**/*", "*"} {
		out = h.call(t, "search_files", map[string]any{"pattern": "outside-needle-123", "glob": glob})
		if strings.Contains(out, "link.txt:") || !strings.Contains(out, "no matches") {
			t.Fatalf("search_files (glob %q) leaked a symlinked file:\n%s", glob, out)
		}
	}
	// A genuine workspace file is still found.
	h.call(t, "write", map[string]any{"path": "ok.txt", "content": "outside-needle-123\n"})
	out = h.call(t, "grep", map[string]any{"pattern": "outside-needle-123"})
	if !strings.Contains(out, "ok.txt:") {
		t.Fatalf("grep must still match real files in the workspace, got:\n%s", out)
	}
}

func TestAppendAndPatchCheckpointExistingFiles(t *testing.T) {
	h := newHarness(t)
	h.call(t, "write", map[string]any{"path": "log.txt", "content": "one\n"})
	before := len(h.env.Store.ListCheckpoints(100))
	out := h.call(t, "append", map[string]any{"path": "log.txt", "content": "two\n"})
	if !strings.Contains(out, "appended") {
		t.Fatalf("unexpected append result: %q", out)
	}
	if got := len(h.env.Store.ListCheckpoints(100)); got != before+1 {
		t.Fatalf("append did not checkpoint: before=%d after=%d", before, got)
	}
	data, err := os.ReadFile(filepath.Join(h.root, "log.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "one\ntwo\n") {
		t.Fatalf("append did not persist: %q", data)
	}
}

// A workspace symlink pointing outside the sandbox (dangling, or with an
// existing target) must not be a write path out of the workspace.
func TestDanglingSymlinkWriteEscapeRejected(t *testing.T) {
	h := newHarness(t)
	outsideDir := t.TempDir()

	// esc -> existing outside directory
	if err := os.Symlink(outsideDir, filepath.Join(h.root, "esc")); err != nil {
		t.Skipf("cannot create symlink on this system: %v", err)
	}
	// esc2 -> dangling target outside the workspace
	dangling := filepath.Join(h.root, "esc2.txt")
	if err := os.Symlink(filepath.Join(outsideDir, "not-yet.txt"), dangling); err != nil {
		t.Skipf("cannot create symlink on this system: %v", err)
	}
	// pre-existing outside file targeted by another link
	outsideFile := filepath.Join(outsideDir, "exists.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(h.root, "esc3.txt")); err != nil {
		t.Skipf("cannot create symlink on this system: %v", err)
	}

	for _, path := range []string{"esc/evil.txt", "esc2.txt", "esc3.txt"} {
		raw, _ := json.Marshal(map[string]any{"path": path, "content": "pwned"})
		if _, err := h.reg.Call(context.Background(), "write", raw); err == nil {
			t.Fatalf("write through %q escaping the workspace must be rejected", path)
		}
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "evil.txt")); !os.IsNotExist(err) {
		t.Fatal("file was created outside the workspace via directory symlink")
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "not-yet.txt")); !os.IsNotExist(err) {
		t.Fatal("file was created outside the workspace via dangling symlink")
	}
	data, _ := os.ReadFile(outsideFile)
	if string(data) != "secret" {
		t.Fatalf("outside file modified through symlink: %q", data)
	}
	// A legitimate write still works.
	if _, err := h.reg.Call(context.Background(), "write", json.RawMessage(`{"path":"ok.txt","content":"fine"}`)); err != nil {
		t.Fatalf("legitimate write should succeed: %v", err)
	}
}
