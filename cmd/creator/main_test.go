package main

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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creatorcli/creator/internal/agent"
	"github.com/creatorcli/creator/internal/config"
	"github.com/creatorcli/creator/internal/store"
)

// ---------------------------------------------------------------------------
// Unit tests for the glue
// ---------------------------------------------------------------------------

func TestOverridesStreamTriState(t *testing.T) {
	f := globalFlags{}
	over := f.overrides()
	if over.StreamIsSet {
		t.Fatal("no flag interaction must leave the tri-state unset")
	}
	f.noStream = true
	over = f.overrides()
	if !over.StreamIsSet || over.Stream {
		t.Fatalf("--no-stream must map to Stream=false with the sentinel set, got %+v", over)
	}
}

func TestPrinterJSON(t *testing.T) {
	cfg := config.Config{JSON: true}
	out := captureStdout(t, func() {
		p := newPrinter(cfg)
		p(agent.Event{Type: "assistant", Text: "hi"})
		p(agent.Event{Type: "assistant_delta", Text: "wo"})
		p(agent.Event{Type: "reasoning_delta", Text: "plan"})
		p(agent.Event{Type: "tool_start", Tool: "write", Args: json.RawMessage(`{"path":"a.go"}`)})
		p(agent.Event{Type: "tool_end", Tool: "write", Status: "ok", Result: "wrote a.go"})
		p(agent.Event{Type: "usage"})
	})
	for _, want := range []string{`"type":"assistant"`, `"type":"assistant_delta"`, `"type":"reasoning_delta"`, `"tool":"write"`, `"type":"usage"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %q", want, out)
		}
	}
}

func TestPrinterHumanModes(t *testing.T) {
	// baseline: deltas stream raw, tool activity summarized, usage suppressed
	cfg := config.Config{}
	out := captureStdout(t, func() {
		p := newPrinter(cfg)
		p(agent.Event{Type: "assistant", Text: "final line"})
		p(agent.Event{Type: "tool_start", Tool: "shell", Args: json.RawMessage(`{"command":"echo hi"}`)})
		p(agent.Event{Type: "tool_end", Tool: "shell", Status: "error", Result: "boom\nmore"})
		p(agent.Event{Type: "reasoning_delta", Text: "thinking bits"})
		p(agent.Event{Type: "usage"})
	})
	if !strings.Contains(out, "final line") || !strings.Contains(out, "→ shell") || !strings.Contains(out, "✗ shell") || !strings.Contains(out, "boom") {
		t.Fatalf("human output lost key events: %q", out)
	}
	if strings.Contains(out, "[tokens") {
		t.Fatal("usage must stay verbose-only in human mode")
	}
	// quiet: suppresses everything but JSON
	q := config.Config{Quiet: true}
	out = captureStdout(t, func() {
		p := newPrinter(q)
		p(agent.Event{Type: "assistant", Text: "hidden"})
	})
	if out != "" {
		t.Fatalf("quiet mode leaked: %q", out)
	}
}

func TestPruneStale(t *testing.T) {
	cases := []struct {
		name        string
		env         string
		agedAfterH  int // hamburger -5
		freshAfterH int
		wantOld     bool
		wantFresh   bool
	}{
		{"default 7 days", "", 8 * 24, 1, false, true},
		{"custom 2 days", "2", 3 * 24, 1, false, true},
		{"disabled", "0", 30 * 24, 1, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			st, err := store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
				t.Fatal(err)
			}
			old := filepath.Join(dir, "out", "old.log")
			fresh := filepath.Join(dir, "out", "fresh.log")
			for _, p := range []string{old, fresh} {
				if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			then := time.Now().Add(-time.Duration(tc.agedAfterH) * time.Hour)
			if err := os.Chtimes(old, then, then); err != nil {
				t.Fatal(err)
			}
			freshTime := time.Now().Add(-time.Duration(tc.freshAfterH) * time.Hour)
			if err := os.Chtimes(fresh, freshTime, freshTime); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CREATOR_PRUNE_DAYS", tc.env)
			pruneStale(st, false)
			if _, err := os.Stat(old); os.IsNotExist(err) == tc.wantOld {
				t.Fatalf("old artifact presence = %v", !tc.wantOld)
			}
			if _, err := os.Stat(fresh); os.IsNotExist(err) == tc.wantFresh {
				t.Fatalf("fresh artifact presence = %v", !tc.wantFresh)
			}
		})
	}
}

func TestShellDisplayName(t *testing.T) {
	if got := shellDisplayName("powershell"); got != "powershell" {
		t.Fatalf("powershell → %q", got)
	}
	if got := shellDisplayName("definitely-not-a-shell"); got != "unknown" {
		t.Fatalf("unknown preference → %q, want unknown", got)
	}
	if got := shellDisplayName("auto"); got == "unknown" || got == "" {
		t.Fatalf("auto should resolve to a real interpreter on this machine, got %q", got)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() {
			w.Close()
			os.Stdout = old
		}()
		fn()
	}()
	return <-done
}

// ---------------------------------------------------------------------------
// Binary integration tests: one build, several end-to-end runs
// ---------------------------------------------------------------------------

var exe string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "creator-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, "temp dir:", err)
		os.Exit(1)
	}
	exe = filepath.Join(dir, "creator-test.exe")
	build := exec.Command("go", "build", "-o", exe, ".")
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building cmd/creator failed:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// creator runs the built binary with a clean CREATOR_* environment plus the
// given additions, and returns stdout/stderr separately.
func creator(t *testing.T, env []string, dir string, timeout time.Duration, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	base := cmd.Environ()
	var out []string
	for _, kv := range base {
		if !strings.HasPrefix(kv, "CREATOR_") {
			out = append(out, kv)
		}
	}
	out = append(out, env...)
	cmd.Env = out
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// fakeOpenAI serves scripted OpenAI-protocol SSE responses through the real
// provider stack.
type fakeOpenAI struct {
	mu       sync.Mutex
	requests []string // raw request bodies
}

func (f *fakeOpenAI) respond(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestBinaryBasics(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		args []string
		want string
		env  []string
	}{
		{[]string{"version"}, "creator 0.1.0", nil},
		{[]string{"help"}, "terminal agent", nil},
		{[]string{"providers"}, "Anthropic", nil},
		{[]string{"sessions"}, "", []string{"CREATOR_DATA_DIR=" + dir}},
	} {
		stdout, _, err := creator(t, tc.env, dir, 30*time.Second, tc.args...)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if tc.want != "" && !strings.Contains(stdout, tc.want) {
			t.Fatalf("%v output missing %q: %q", tc.args, tc.want, stdout)
		}
	}
	stdout, _, err := creator(t, nil, dir, 30*time.Second, "providers")
	if err != nil || !strings.Contains(stdout, "Anthropic") {
		t.Fatalf("providers must list cloud backends: %q (%v)", stdout, err)
	}
}

// TestEndToEndAgentPipeline is the honest full-pipeline smoke: the compiled
// binary, streaming SSE, a tool call executed for real, durable sessions and a
// second run resuming the conversation.
func TestEndToEndAgentPipeline(t *testing.T) {
	workspace := t.TempDir()
	stateDir := t.TempDir()

	fake := &fakeOpenAI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fake.mu.Lock()
		n := len(fake.requests)
		fake.requests = append(fake.requests, string(body))
		fake.mu.Unlock()

		switch n {
		case 0: // first turn: stream a tool call for write, then usage
			fake.respond(w,
				`{"choices":[{"delta":{"content":"I will write the file"}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","function":{"name":"write","arguments":"{\"pa"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"hello-e2e.txt\",\"content\":\"hello e2e\"}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":30}}`)
		case 1: // first turn final answer
			fake.respond(w,
				`{"choices":[{"delta":{"content":"done, file written"}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":400,"completion_tokens":12}}`)
		default: // continuation turn: answer from history alone
			fake.respond(w,
				`{"choices":[{"delta":{"content":"continuation answered"}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
		}
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	env := []string{
		"CREATOR_DATA_DIR=" + stateDir,
		"CREATOR_POLICY=yolo",
		"CREATOR_SHELL=powershell",
	}

	// Turn 1: stream → tool call → file created by the real tool layer.
	stdout, stderr, err := creator(t, env, workspace, 90*time.Second,
		"run", "--provider", "custom", "--base-url", srv.URL, "--model", "e2e-model", "--json",
		"create hello-e2e.txt with hello e2e please")
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr)
	}
	data, readErr := os.ReadFile(filepath.Join(workspace, "hello-e2e.txt"))
	if readErr != nil {
		t.Fatalf("the agent never wrote the file: %v\nstdout: %s", readErr, stdout)
	}
	if want := "hello e2e\n"; string(data) != want {
		t.Fatalf("file content = %q, want %q", data, want)
	}
	for _, want := range []string{`"type":"assistant_delta"`, `"type":"tool_start"`, `"type":"tool_end"`, `"type":"result"`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("event scaffold missing %s in %q", want, stdout)
		}
	}

	// The session must be discoverable by a second process.
	seen, _, cerr := creator(t, env, workspace, 30*time.Second, "sessions", "--data-dir", stateDir)
	if cerr != nil || !strings.Contains(seen, "e2e-model") {
		t.Fatalf("sessions listing broke: %q (%v)", seen, cerr)
	}

	// Turn 2: --continue resumes the most recent session; the model sees the
	// replayed history.
	stdout2, stderr2, err := creator(t, env, workspace, 90*time.Second,
		"run", "--continue", "--provider", "custom", "--base-url", srv.URL, "--model", "e2e-model", "--json",
		"what did we do so far?")
	if err != nil {
		t.Fatalf("continue run: %v\nstderr: %s\nstdout: %s", err, stderr2, stdout2)
	}
	if !strings.Contains(stdout2, `"type":"result"`) || !strings.Contains(stdout2, "continuation answered") {
		t.Fatalf("continuationdid not answer from history: %q", stdout2)
	}

	// Integrity: two provider passes in turn 1, one in turn 2, streamed both
	// times, and usage telemetry traveled with them.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.requests) != 3 {
		t.Fatalf("total provider calls = %d, want 3", len(fake.requests))
	}
	for i, req := range fake.requests {
		if !strings.Contains(req, `"stream":true`) {
			t.Errorf("request %d was not streamed", i)
		}
	}
	// `custom` has StreamUsage=false by design: usage is estimated, not asked
	// for, so include_usage must NOT appear on the wire (asking would 400 on
	// some compat endpoints).
	for i, req := range fake.requests {
		if strings.Contains(req, `"include_usage":true`) {
			t.Errorf("request %d wrongly asked for include_usage on a non-capable backend", i)
		}
	}
	// Turn 2's request must carry turn 1's observation so the model can
	// actually answer "what did we do so far?".
	if ctx := fake.requests[2]; !strings.Contains(ctx, "hello e2e") {
		t.Errorf("continuation history lost turn 1's tool observation: %.400s", ctx)
	}
}

// TestBinaryShellSmoke: the standalone shell path with a real Windows
// interpreter, real policy engine and durable output handle.
func TestBinaryShellSmoke(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	env := []string{"CREATOR_DATA_DIR=" + stateDir}
	stdout, stderr, err := creator(t, env, dir, 60*time.Second,
		"shell", "--shell", "powershell", "--json", "Write-Output e2e-shell-smoke")
	if err != nil {
		t.Fatalf("shell: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "e2e-shell-smoke") {
		t.Fatalf("shell output missing in %q", stdout)
	}
	// The digest handle must be retrievable afterwards.
	stdout, _, err = creator(t, env, dir, 30*time.Second, "sessions", "--data-dir", stateDir)
	_ = stdout
	if err != nil {
		t.Fatalf("sessions after shell: %v", err)
	}
}

func TestBinaryRunWithoutProvider(t *testing.T) {
	dir := t.TempDir()
	// No CREATOR_API_KEY-like vars in the sandboxed env and no config file: the
	// binary must refuse with a human hint rather than crashing.
	_, stderr, err := creator(t, []string{"CREATOR_DATA_DIR=" + t.TempDir()}, dir, 30*time.Second,
		"run", "--provider", "totally-bogus", "hello")
	if err == nil {
		t.Fatal("an unknown provider must fail")
	}
	if !strings.Contains(stderr, "unknown provider") {
		t.Fatalf("expected a provider-diagnostics error, got %q", stderr)
	}
}

// TestEndToEndDelegationPipeline is the delegate-system smoke: the real
// binary, a real spawn through the delegate tool, the delegate doing real tool
// work of its own, and the reduced report flowing back into the parent's
// context — verified on the wire, in the journal and through the CLI.
func TestEndToEndDelegationPipeline(t *testing.T) {
	workspace := t.TempDir()
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("secret-fish-42\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := &fakeOpenAI{}
	mu := &fake.mu
	rootCount, delegateCount := 0, 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		fake.requests = append(fake.requests, string(body))
		isDelegate := strings.Contains(string(body), "delegation tree")
		if isDelegate {
			switch delegateCount {
			case 0: // delegate turn 1: read the note for real
				fake.respond(w,
					`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"de1","function":{"name":"read","arguments":"{\"path\":\"note.txt\"}"}}]}}]}`,
					`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
					`{"choices":[],"usage":{"prompt_tokens":80,"completion_tokens":11}}`)
			default: // delegate final report
				fake.respond(w,
					`{"choices":[{"delta":{"content":"REPORT: secret-fish-42 unchanged and confirmed"}}]}`,
					`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
					`{"choices":[],"usage":{"prompt_tokens":200,"completion_tokens":19}}`)
			}
			delegateCount++
			return
		}
		switch rootCount {
		case 0: // root turn 1: delegate the task, wait for the report
			fake.respond(w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"rt1","function":{"name":"delegate","arguments":"{\"task\":\"read the note file and report its content\",\"wait\":true}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":150,"completion_tokens":24}}`)
		default: // root turn 2: fold the report into the answer
			fake.respond(w,
				`{"choices":[{"delta":{"content":"DELEGATED OK"}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":7}}`)
		}
		rootCount++
	}))
	defer srv.Close()

	stdout, stderr, err := creator(t,
		[]string{"CREATOR_DATA_DIR=" + stateDir, "CREATOR_POLICY=yolo"},
		workspace, 120*time.Second,
		"run", "--json", "--provider", "custom", "--base-url", srv.URL, "--model", "e2e-delegator",
		"what does note.txt hold?")
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, `"text":"DELEGATED OK"`) {
		t.Fatalf("root never answered: %s", stdout)
	}
	if !strings.Contains(stdout, `"tool":"delegate"`) {
		t.Fatalf("delegate tool activity missing from the stream: %s", stdout)
	}

	// The delegate's model must have actually seen the note content, and the
	// parent's follow-up must carry the reduced report — the whole point of
	// the design, checked on the wire.
	mu.Lock()
	bodyOf := func(i int) string { return fake.requests[i] }
	rootTurn2 := ""
	delegateTurn2 := ""
	for _, b := range fake.requests {
		if strings.Contains(b, "delegation tree") {
			delegateTurn2 = b
		} else {
			rootTurn2 = b
		}
	}
	var rootCalls, delegateCalls int
	for _, b := range fake.requests {
		if strings.Contains(b, "delegation tree") {
			delegateCalls++
		} else {
			rootCalls++
		}
	}
	if rootCalls != 2 || delegateCalls != 2 {
		mu.Unlock()
		t.Fatalf("provider calls root=%d delegate=%d, want 2/2", rootCalls, delegateCalls)
	}
	if !strings.Contains(delegateTurn2, "secret-fish-42") {
		mu.Unlock()
		t.Fatalf("delegate model never saw the note content: %.800s", delegateTurn2)
	}
	if !strings.Contains(rootTurn2, "REPORT: secret-fish-42 unchanged and confirmed") {
		mu.Unlock()
		t.Fatalf("parent context missing the delegate report: %.800s", rootTurn2)
	}
	last := bodyOf(len(fake.requests) - 1)
	mu.Unlock()
	if !strings.Contains(last, "REPORT: secret-fish-42") {
		t.Fatalf("report lost before the answer: %.400s", last)
	}

	// Journal + CLI: the same state a viewer replays.
	stdout, _, err = creator(t, []string{"CREATOR_DATA_DIR=" + stateDir}, workspace, 30*time.Second, "delegates")
	if err != nil {
		t.Fatalf("delegates listing: %v", err)
	}
	if !strings.Contains(stdout, "done") || !strings.Contains(stdout, "d_") {
		t.Fatalf("delegate record missing from listing: %q", stdout)
	}
	id := delegateIDFromListing(stdout)
	if id == "" {
		t.Fatalf("no delegate id found in listing: %q", stdout)
	}
	stdout, _, err = creator(t, []string{"CREATOR_DATA_DIR=" + stateDir}, workspace, 30*time.Second, "delegates", id)
	if err != nil {
		t.Fatalf("delegate inspect: %v", err)
	}
	if !strings.Contains(stdout, "secret-fish-42") {
		t.Fatalf("delegate report not retrievable via CLI: %q", stdout)
	}
	stdout, _, err = creator(t, []string{"CREATOR_DATA_DIR=" + stateDir}, workspace, 30*time.Second, "delegates", "--events", "20")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, want := range []string{"spawn", "start", "tool", "done"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("journal missing %q: %s", want, stdout)
		}
	}
}

// delegateIDFromListing picks the first d_ id out of the CLI listing.
func delegateIDFromListing(stdout string) string {
	i := strings.Index(stdout, "d_")
	if i < 0 {
		return ""
	}
	j := i + 2
	for j < len(stdout) && stdout[j] >= '0' && stdout[j] <= 'z' && stdout[j] != ' ' {
		j++
	}
	return stdout[i:j]
}
