package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DERYK-RYLOY/creator/internal/policy"
	"github.com/DERYK-RYLOY/creator/internal/provider"
	"github.com/DERYK-RYLOY/creator/internal/runner"
	"github.com/DERYK-RYLOY/creator/internal/store"
	"github.com/DERYK-RYLOY/creator/internal/tools"
)

type fakeProvider struct {
	steps    []provider.Response
	i        int
	lastReq  provider.Request
	requests int
}

func (f *fakeProvider) Label() string { return "fake/model" }
func (f *fakeProvider) Model() string { return "model" }
func (f *fakeProvider) Chat(ctx context.Context, req provider.Request) (provider.Response, error) {
	f.requests++
	f.lastReq = req
	if f.i >= len(f.steps) {
		return provider.Response{Text: "nothing left to do"}, nil
	}
	r := f.steps[f.i]
	f.i++
	return r, nil
}

func newTestAgent(t *testing.T, prov provider.Provider, budget int) (*Agent, *store.Store, string, *[]Event) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := runner.NewManager(st)
	engine := policy.NewEngine("yolo", true, false, strings.NewReader(""), io.Discard)
	env := &tools.Env{Workspace: root, Store: st, Jobs: jobs, Policy: engine, Session: "s_test", Budget: 400}
	reg := tools.New(env)
	events := &[]Event{}

	a := New(Options{
		Provider:     prov,
		Tools:        reg,
		Store:        st,
		Session:      store.Session{ID: "s_test", Workspace: root, Provider: "fake", Model: "model"},
		MaxSteps:     5,
		BudgetTokens: budget,
		OnEvent:      func(ev Event) { *events = append(*events, ev) },
	})
	t.Cleanup(func() {
		jobs.StopAll()
		st.Close()
	})
	return a, st, root, events
}

func TestRunWithoutToolsReturnsErrorNotPanic(t *testing.T) {
	a := New(Options{Provider: &fakeProvider{}})
	_, err := a.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("nil Tools must error, not panic")
	}
	if !strings.Contains(err.Error(), "tools") {
		t.Fatalf("error should name the missing dependency, got %v", err)
	}
}

func TestRunExecutesToolThenAnswers(t *testing.T) {
	prov := &fakeProvider{steps: []provider.Response{
		{
			Text: "creating the file",
			ToolCalls: []provider.ToolCall{{
				ID:   "call_1",
				Name: "write",
				Args: json.RawMessage(`{"path":"hello.txt","content":"hi from creator"}`),
			}},
			Usage: provider.Usage{In: 100, Out: 20},
		},
		{Text: "written and verified", Usage: provider.Usage{In: 30, Out: 5}},
	}}
	a, st, root, events := newTestAgent(t, prov, 0)

	answer, err := a.Run(context.Background(), "create hello.txt")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if answer != "written and verified" {
		t.Fatalf("answer = %q", answer)
	}
	data, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil {
		t.Fatalf("tool did not write the file: %v", err)
	}
	if string(data) != "hi from creator\n" {
		t.Fatalf("file content = %q", data)
	}
	if prov.requests != 2 {
		t.Fatalf("expected 2 model calls, got %d", prov.requests)
	}

	saved, err := st.LoadSession("s_test")
	if err != nil {
		t.Fatalf("session not saved: %v", err)
	}
	if len(saved.Messages) != 4 {
		t.Fatalf("expected user/assistant/tool/assistant, got %d messages", len(saved.Messages))
	}
	if saved.Usage.In != 130 || saved.Usage.Out != 25 {
		t.Fatalf("usage not accumulated: %+v", saved.Usage)
	}
	if len(*events) == 0 {
		t.Fatal("no events emitted")
	}
	var sawToolEnd bool
	for _, ev := range *events {
		if ev.Type == "tool_end" && ev.Status == "ok" {
			sawToolEnd = true
		}
	}
	if !sawToolEnd {
		t.Fatal("tool execution was not reported")
	}
	if len(prov.lastReq.Tools) == 0 {
		t.Fatal("tools were not sent to the provider")
	}
	if !strings.Contains(prov.lastReq.System, "Creator") {
		t.Fatal("system prompt missing")
	}
}

func TestToolErrorIsFedBackToModel(t *testing.T) {
	prov := &fakeProvider{steps: []provider.Response{
		{
			ToolCalls: []provider.ToolCall{{ID: "c1", Name: "read", Args: json.RawMessage(`{"path":"missing.txt"}`)}},
			Usage:     provider.Usage{In: 10, Out: 5},
		},
		{Text: "the file does not exist", Usage: provider.Usage{In: 20, Out: 5}},
	}}
	a, _, _, _ := newTestAgent(t, prov, 0)
	answer, err := a.Run(context.Background(), "read missing.txt")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if answer != "the file does not exist" {
		t.Fatalf("answer = %q", answer)
	}
	// The tool result handed back to the model must contain the error.
	found := false
	for _, m := range prov.lastReq.Messages {
		if m.Role == provider.RoleTool && strings.HasPrefix(m.Text, "ERROR:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("error was not returned as a tool result: %+v", prov.lastReq.Messages)
	}
}

func TestBudgetGovernorStopsRunawayLoop(t *testing.T) {
	prov := &fakeProvider{steps: []provider.Response{
		{
			ToolCalls: []provider.ToolCall{{ID: "c1", Name: "shell", Args: json.RawMessage(`{"command":"echo hi"}`)}},
			Usage:     provider.Usage{In: 5000, Out: 100},
		},
	}}
	a, _, _, _ := newTestAgent(t, prov, 100)
	_, err := a.Run(context.Background(), "loop forever")
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("expected budget error, got %v", err)
	}
}

func TestMaxSteps(t *testing.T) {
	steps := make([]provider.Response, 10)
	for i := range steps {
		steps[i] = provider.Response{
			ToolCalls: []provider.ToolCall{{ID: fmt.Sprintf("c%d", i), Name: "shell", Args: json.RawMessage(fmt.Sprintf(`{"command":"echo x%d"}`, i))}},
			Usage:     provider.Usage{In: 1, Out: 1},
		}
	}
	prov := &fakeProvider{steps: steps}
	a, _, _, _ := newTestAgent(t, prov, 0)
	_, err := a.Run(context.Background(), "spin")
	if err == nil || !strings.Contains(err.Error(), "steps") {
		t.Fatalf("expected step-limit error, got %v", err)
	}
}

func TestEmptyFinalAnswerRetriedOnce(t *testing.T) {
	prov := &emptyReplyProvider{}
	a, _, _, events := newTestAgent(t, prov, 0)
	answer, err := a.Run(context.Background(), "say something")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if answer != "second attempt" {
		t.Fatalf("answer = %q", answer)
	}
	if prov.requests != 2 {
		t.Fatalf("expected a transparent retry (2 calls), got %d", prov.requests)
	}
	// The blank first pass must not leak into the persisted history or events.
	for _, ev := range *events {
		if ev.Type == "assistant" && ev.Text == "" {
			t.Fatal("an empty assistant turn was announced")
		}
	}
}

func TestDoubleEmptyFinalAnswerReturnsErrEmptyAnswer(t *testing.T) {
	prov := &alwaysEmptyProvider{}
	a, _, _, _ := newTestAgent(t, prov, 0)
	_, err := a.Run(context.Background(), "say something")
	if !errors.Is(err, ErrEmptyAnswer) {
		t.Fatalf("expected ErrEmptyAnswer, got %v", err)
	}
	if prov.requests != 2 {
		t.Fatalf("expected the one transparent retry (2 calls), got %d", prov.requests)
	}
}

type alwaysEmptyProvider struct {
	requests int
}

func (f *alwaysEmptyProvider) Label() string { return "fake/always-empty" }
func (f *alwaysEmptyProvider) Model() string { return "model" }
func (f *alwaysEmptyProvider) Chat(ctx context.Context, req provider.Request) (provider.Response, error) {
	f.requests++
	return provider.Response{Usage: provider.Usage{In: 1, Out: 0}}, nil
}

type emptyReplyProvider struct {
	requests int
}

func (f *emptyReplyProvider) Label() string { return "fake/empty" }
func (f *emptyReplyProvider) Model() string { return "model" }
func (f *emptyReplyProvider) Chat(ctx context.Context, req provider.Request) (provider.Response, error) {
	f.requests++
	if f.requests == 1 {
		return provider.Response{}, nil
	}
	return provider.Response{Text: "second attempt", Usage: provider.Usage{In: 5, Out: 3}}, nil
}

func TestParamsAndShellReachProvider(t *testing.T) {
	prov := &fakeProvider{steps: []provider.Response{
		{Text: "ack", Usage: provider.Usage{In: 1, Out: 1}},
	}}
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := runner.NewManager(st)
	engine := policy.NewEngine("yolo", true, false, strings.NewReader(""), io.Discard)
	env := &tools.Env{Workspace: root, Store: st, Jobs: jobs, Policy: engine, Session: "s_params", Budget: 400}
	a := New(Options{
		Provider:    prov,
		Tools:       tools.New(env),
		Store:       st,
		Session:     store.Session{ID: "s_params", Workspace: root, Provider: "fake", Model: "model"},
		MaxSteps:    5,
		MaxTokens:   777,
		Temperature: 0.25,
		Thinking:    500,
		ShellName:   "pwsh",
	})
	t.Cleanup(func() {
		jobs.StopAll()
		st.Close()
	})
	if _, err := a.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if prov.lastReq.MaxTokens != 777 {
		t.Fatalf("max tokens = %d, want 777", prov.lastReq.MaxTokens)
	}
	if prov.lastReq.Temperature != 0.25 {
		t.Fatalf("temperature = %v, want 0.25", prov.lastReq.Temperature)
	}
	if prov.lastReq.ThinkingBudget != 500 {
		t.Fatalf("thinking budget = %d, want 500", prov.lastReq.ThinkingBudget)
	}
	if !strings.Contains(prov.lastReq.System, "pwsh") {
		t.Fatalf("shell name missing from the system prompt: %q", prov.lastReq.System)
	}
}

func TestSystemPromptWithKnowledge(t *testing.T) {
	section := "\n\n## Repository instructions (knowledge files)\n\n### AGENTS.md\n\nrun gofmt"
	got := SystemPromptWithKnowledge("/ws", "bash", section)
	if !strings.Contains(got, "run gofmt") || !strings.Contains(got, "knowledge files") {
		t.Fatalf("knowledge section missing from the prompt: %q", got)
	}
	base := SystemPrompt("/ws", "bash")
	if strings.Contains(base, "knowledge files") {
		t.Fatal("plain SystemPrompt must not invent a knowledge section")
	}
	if !strings.Contains(base, "/ws") || !strings.Contains(base, "bash") {
		t.Fatal("operating contract lost workspace or shell")
	}
}

// TestThinkingClampedBelowMaxTokens guards the API contract: ThinkingBudget
// must stay strictly below MaxTokens.
func TestThinkingClampedBelowMaxTokens(t *testing.T) {
	prov := &fakeProvider{steps: []provider.Response{{Text: "ok"}}}
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := runner.NewManager(st)
	engine := policy.NewEngine("yolo", true, false, strings.NewReader(""), io.Discard)
	env := &tools.Env{Workspace: root, Store: st, Jobs: jobs, Policy: engine, Session: "s_clamp", Budget: 400}
	events := &[]Event{}
	a := New(Options{
		Provider:  prov,
		Tools:     tools.New(env),
		Store:     st,
		Session:   store.Session{ID: "s_clamp", Workspace: root, Provider: "fake", Model: "model"},
		MaxTokens: 1024,
		Thinking:  1024,
		MaxSteps:  2,
		OnEvent:   func(ev Event) { *events = append(*events, ev) },
	})
	t.Cleanup(func() {
		jobs.StopAll()
		st.Close()
	})
	if _, err := a.Run(context.Background(), "clamp check"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := prov.lastReq.ThinkingBudget; got >= 1024 {
		t.Fatalf("thinking budget = %d, must be clamped below max_tokens", got)
	}
	var warned bool
	for _, ev := range *events {
		if ev.Type == "notice" && strings.Contains(ev.Text, "clamped") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("user was not told the thinking budget was clamped")
	}
}

func TestUserMessagePersistedEvenWhenProviderFails(t *testing.T) {
	prov := &failingProvider{}
	a, st, _, _ := newTestAgent(t, prov, 0)
	if _, err := a.Run(context.Background(), "will fail"); err == nil {
		t.Fatal("expected the provider failure to surface")
	}
	saved, err := st.LoadSession("s_test")
	if err != nil {
		t.Fatalf("session not readable after crash: %v", err)
	}
	if len(saved.Messages) == 0 || saved.Messages[0].Role != provider.RoleUser || saved.Messages[0].Text != "will fail" {
		t.Fatalf("user turn lost on failure: %+v", saved.Messages)
	}
}

type failingProvider struct{}

func (f *failingProvider) Label() string { return "fake/fail" }
func (f *failingProvider) Model() string { return "model" }
func (f *failingProvider) Chat(ctx context.Context, req provider.Request) (provider.Response, error) {
	return provider.Response{}, fmt.Errorf("connection reset")
}

// TestThinkingBudgetIgnoredOnNonThinkingProvider: providers that do not take a
// thinking budget make that fact loud, not silent.
type noThinkingProvider struct {
	fakeProvider
}

func (f *noThinkingProvider) Label() string         { return "fake/nothinking" }
func (f *noThinkingProvider) ThinkingCapable() bool { return false }

func TestThinkingBudgetIgnoredOnNonThinkingProvider(t *testing.T) {
	prov := &noThinkingProvider{fakeProvider: fakeProvider{steps: []provider.Response{
		{Text: "ack", Usage: provider.Usage{In: 1, Out: 1}},
	}}}
	a, _, _, events := newTestAgent(t, prov, 0)
	a.opts.Thinking = 1024
	if _, err := a.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if prov.lastReq.ThinkingBudget != 0 {
		t.Fatalf("budget must not be sent to a non-thinking backend: %d", prov.lastReq.ThinkingBudget)
	}
	found := false
	for _, ev := range *events {
		if ev.Type == "notice" && strings.Contains(ev.Text, "ignored") {
			found = true
		}
	}
	if !found {
		t.Fatal("user was not told the thinking budget was ignored")
	}
}

func TestIdenticalToolCallsStopBeforeMaxSteps(t *testing.T) {
	steps := make([]provider.Response, 10)
	for i := range steps {
		steps[i] = provider.Response{
			ToolCalls: []provider.ToolCall{{ID: fmt.Sprintf("c%d", i), Name: "shell", Args: json.RawMessage(`{"command":"echo x"}`)}},
			Usage:     provider.Usage{In: 1, Out: 1},
		}
	}
	prov := &fakeProvider{steps: steps}
	a, _, _, _ := newTestAgent(t, prov, 0)
	_, err := a.Run(context.Background(), "loop")
	if err == nil || !strings.Contains(err.Error(), "loop detected") {
		t.Fatalf("expected loop-detection error, got %v", err)
	}
}
