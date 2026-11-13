package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAskUserToolReachesHuman(t *testing.T) {
	h := newHarness(t)
	var seen AskRequest
	h.env.Ask = func(ctx context.Context, req AskRequest) (string, error) {
		seen = req
		return "second", nil
	}
	h.reg = New(h.env)
	out := h.call(t, "ask_user", map[string]any{
		"question": "which database?",
		"options":  []string{"first", "second"},
	})
	if !strings.Contains(out, "user answer: second") {
		t.Fatalf("answer not surfaced: %q", out)
	}
	if seen.Question != "which database?" || len(seen.Options) != 2 {
		t.Fatalf("human saw %+v", seen)
	}
}

func TestAskUserHiddenWhenNoHumanCanAnswer(t *testing.T) {
	h := newHarness(t)
	raw := json.RawMessage(`{"question":"pick one"}`)
	_, err := h.reg.Call(context.Background(), "ask_user", raw)
	if err == nil {
		t.Fatal("headless run must not register ask_user")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("headless error should be a normal missing-tool error, got: %v", err)
	}
}

func TestAskUserRequiresQuestion(t *testing.T) {
	h := newHarness(t)
	h.env.Ask = func(ctx context.Context, req AskRequest) (string, error) { return "x", nil }
	h.reg = New(h.env)
	_, err := h.reg.Call(context.Background(), "ask_user", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "question is required") {
		t.Fatalf("empty question should be refused: %v", err)
	}
}

func TestAskTerminalReadLinePreservesBytes(t *testing.T) {
	got, err := readLine(strings.NewReader("hello\r\nrest"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Fatalf("readLine = %q", got)
	}
}

// readLine is a tiny test-only adapter around the main package's unbuffered
// reader contract: the tool only needs the byte semantics, not main's os.Stdin.
func readLine(r interface{ Read([]byte) (int, error) }) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			switch buf[0] {
			case '\n':
				return b.String(), nil
			case '\r':
			default:
				b.WriteByte(buf[0])
			}
		}
		if err != nil {
			return b.String(), err
		}
	}
}
