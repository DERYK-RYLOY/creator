package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestOpenAIStreamAssemblesAndForwardsUsage(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"delta":{"reasoning_content":"I should inspect"}}]}`,
			`data: {"choices":[{"delta":{"content":"Hel"}}]}`,
			`data: {"choices":[{"delta":{"content":"lo"}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read","arguments":"{\"pa"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.go\"}"}}]}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20}}`,
			`data: [DONE]`,
		}, "\n\n")+"\n\n")
	}))
	defer srv.Close()

	c, err := Resolve("custom", "test-model", srv.URL, "key")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	c.desc.StreamUsage = true // this test asserts the include_usage path
	var deltas []Delta
	resp, err := c.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
		OnDelta:  func(d Delta) { deltas = append(deltas, d) },
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if got["stream"] != true {
		t.Fatalf("stream flag not set on the wire: %v", got["stream"])
	}
	if so, ok := got["stream_options"].(map[string]any); !ok || so["include_usage"] != true {
		t.Fatalf("stream_options.include_usage missing: %v", got["stream_options"])
	}
	if resp.Text != "Hello" {
		t.Fatalf("text = %q", resp.Text)
	}
	if resp.Reasoning != "I should inspect" {
		t.Fatalf("reasoning = %q", resp.Reasoning)
	}
	if resp.Usage.In != 100 || resp.Usage.Out != 20 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "read" || string(resp.ToolCalls[0].Args) != `{"path":"a.go"}` {
		t.Fatalf("streamed tool call not assembled: %+v", resp.ToolCalls)
	}
	// deltas include reasoning + text + argument fragments, in stream order
	if len(deltas) < 4 {
		t.Fatalf("expected streamed deltas, got %+v", deltas)
	}
	if deltas[0].Reasoning == "" || deltas[1].Text != "Hel" {
		t.Fatalf("wrong delta order: %+v", deltas)
	}
}

func TestOpenAIStreamNoUsageEstimated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`+"\n\n"+`data: [DONE]`+"\n\n")
	}))
	defer srv.Close()
	c, _ := Resolve("custom", "m", srv.URL, "")
	c.desc.StreamUsage = false
	resp, err := c.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
		OnDelta:  func(Delta) {},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Usage.In <= 0 {
		t.Fatalf("usage should be estimated when the backend omits it: %+v", resp.Usage)
	}
}

// A persistently failing backend must not be amplified by the two stacked
// retry budgets (openStream's 6 attempts × retryStream's 3 re-opens = 18
// POSTs). Pre-delivery failures are final: the total must equal maxAttempts.
func TestOpenAIStreamPersistentFailureIsNotAmplified(t *testing.T) {
	fastRetries(t)
	atomicCalls := atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomicCalls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
	}))
	defer srv.Close()
	c, _ := Resolve("custom", "m", srv.URL, "")
	_, err := c.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
		OnDelta:  func(Delta) {},
	})
	if err == nil {
		t.Fatal("expected an error from the always-429 backend")
	}
	if got := atomicCalls.Load(); got != int64(maxAttempts) {
		t.Fatalf("persistent 429 must stop after %d attempts, got %d", maxAttempts, got)
	}
}

func TestOpenAIStreamRetriesBeforeFirstDelta(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	var firstByteSeen bool
	_ = firstByteSeen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(503)
			return
		}
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n"+`data: [DONE]`+"\n\n")
	}))
	defer srv.Close()
	c, _ := Resolve("custom", "m", srv.URL, "")
	resp, err := c.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
		OnDelta:  func(Delta) {},
	})
	if err != nil {
		t.Fatalf("chat with one 503 then success: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("text = %q", resp.Text)
	}
	if calls < 2 {
		t.Fatal("the server should have been hit twice (retry path)")
	}
}

func TestRetryStreamRetriesBeforeDelivery(t *testing.T) {
	fastRetries(t)
	attempts := 0
	out, err := retryStream(context.Background(), func(delivered *bool) (string, error) {
		attempts++
		if attempts == 1 {
			return "", errors.New("read tcp 10.0.0.1:443: read: connection reset by peer")
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("a reset before any delta should be retried: %v", err)
	}
	if out != "ok" || attempts != 2 {
		t.Fatalf("out = %q after %d attempts", out, attempts)
	}
}

func TestRetryStreamStopsAfterDelivery(t *testing.T) {
	fastRetries(t)
	attempts := 0
	_, err := retryStream(context.Background(), func(delivered *bool) (string, error) {
		attempts++
		*delivered = true
		return "", io.ErrUnexpectedEOF
	})
	if attempts != 1 {
		t.Fatalf("retrying after delivery would duplicate text; got %d attempts", attempts)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryStreamStopsOnPermanentError(t *testing.T) {
	fastRetries(t)
	attempts := 0
	_, err := retryStream(context.Background(), func(delivered *bool) (string, error) {
		attempts++
		return "", errors.New("invalid api key")
	})
	if attempts != 1 || err == nil {
		t.Fatalf("permanent errors must not be retried: attempts=%d err=%v", attempts, err)
	}
}

// abortMidStream writes an SSE prefix, flushes it, then kills the connection so
// the client sees a reset rather than a clean end of stream.
func abortMidStream(w http.ResponseWriter, prefix string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, prefix)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	panic(http.ErrAbortHandler)
}

func TestOpenAIStreamRetriesResetBeforeFirstDelta(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			// Headers arrive, then the socket dies before any event.
			abortMidStream(w, "")
			return
		}
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n"+`data: [DONE]`+"\n\n")
	}))
	defer srv.Close()

	c, _ := Resolve("custom", "m", srv.URL, "")
	var deltas []string
	resp, err := c.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
		OnDelta:  func(d Delta) { deltas = append(deltas, d.Text) },
	})
	if err != nil {
		t.Fatalf("a reset before the first delta should be retried: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("text = %q", resp.Text)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected 2 connections, got %d", n)
	}
	if strings.Join(deltas, "") != "ok" {
		t.Fatalf("deltas must not be duplicated by the retry: %q", deltas)
	}
}

func TestOpenAIStreamDoesNotRetryAfterPartialDelivery(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			io.WriteString(w, `data: {"choices":[{"delta":{"content":"Hello"},"finish_reason":"stop"}]}`+"\n\n"+`data: [DONE]`+"\n\n")
			return
		}
		abortMidStream(w, `data: {"choices":[{"delta":{"content":"Hel"}}]}`+"\n\n")
	}))
	defer srv.Close()

	c, _ := Resolve("custom", "m", srv.URL, "")
	var deltas []string
	_, err := c.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
		OnDelta:  func(d Delta) { deltas = append(deltas, d.Text) },
	})
	if err == nil {
		t.Fatal("a truncated stream after delivery must fail, not silently restart")
	}
	if !strings.Contains(err.Error(), "partial content already delivered") {
		t.Fatalf("err = %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("must not reconnect after delivering content; got %d connections", n)
	}
	if strings.Join(deltas, "") != "Hel" {
		t.Fatalf("deltas must be exactly what the stream produced: %q", deltas)
	}
}

func TestAnthropicStreamRetriesOnInBandOverload(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			io.WriteString(w, "data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Service overloaded\"}}\n\n")
			return
		}
		io.WriteString(w, strings.Join([]string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":7}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`data: {"type":"message_stop"}`,
		}, "\n\n")+"\n\n")
	}))
	defer srv.Close()
	c, _ := Resolve("anthropic", "claude", srv.URL, "k")
	resp, err := c.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
		OnDelta:  func(Delta) {},
	})
	if err != nil {
		t.Fatalf("overloaded-then-ok must succeed through retry: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("text = %q", resp.Text)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected 2 connections, got %d", n)
	}
}

func TestOpenAIStreamRetriesOnInBandServerError(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			io.WriteString(w, "data: {\"error\":{\"message\":\"server is overloaded\",\"type\":\"server_error\"}}\n\n")
			return
		}
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
		}, "\n\n")+"\n\n")
	}))
	defer srv.Close()
	c, _ := Resolve("custom", "m", srv.URL, "")
	resp, err := c.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
		OnDelta:  func(Delta) {},
	})
	if err != nil {
		t.Fatalf("server_error-then-ok must succeed through retry: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("text = %q", resp.Text)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected 2 connections, got %d", n)
	}
}

func TestAnthropicStreamFatalInBandErrorDoesNotRetry(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, "data: {\"type\":\"error\",\"error\":{\"type\":\"authentication_error\",\"message\":\"bad key\"}}\n\n")
	}))
	defer srv.Close()
	c, _ := Resolve("anthropic", "claude", srv.URL, "k")
	_, err := c.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
		OnDelta:  func(Delta) {},
	})
	if err == nil {
		t.Fatal("expected a fatal in-band error")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("fatal in-band error must not retry: %d calls", n)
	}
}

func TestAnthropicStreamRetriesResetBeforeFirstDelta(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			abortMidStream(w, "")
			return
		}
		io.WriteString(w, strings.Join([]string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":7}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`data: {"type":"message_stop"}`,
		}, "\n\n")+"\n\n")
	}))
	defer srv.Close()

	c, _ := Resolve("anthropic", "claude", srv.URL, "k")
	var deltas []string
	resp, err := c.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
		OnDelta:  func(d Delta) { deltas = append(deltas, d.Text) },
	})
	if err != nil {
		t.Fatalf("a reset before the first delta should be retried: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("text = %q", resp.Text)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("expected 2 connections, got %d", n)
	}
	if strings.Join(deltas, "") != "ok" {
		t.Fatalf("deltas must not be duplicated by the retry: %q", deltas)
	}
}

func TestAnthropicStreamWithThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Join([]string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":42}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan: inspect"}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig01"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"starting"}}`,
			`data: {"type":"content_block_stop","index":1}`,
			`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tu1","name":"read"}}`,
			`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"pa"}}`,
			`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"th\":\"a\"}"}}`,
			`data: {"type":"content_block_stop","index":2}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":13}}`,
			`data: {"type":"message_stop"}`,
		}, "\n\n")+"\n\n")
	}))
	defer srv.Close()

	c, err := Resolve("anthropic", "claude", srv.URL, "k")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	var sawReasoning []string
	resp, err := c.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Text: "go"}},
		OnDelta: func(d Delta) {
			if d.Reasoning != "" {
				sawReasoning = append(sawReasoning, d.Reasoning)
			}
		},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Text != "starting" {
		t.Fatalf("text = %q", resp.Text)
	}
	if resp.Reasoning != "plan: inspect" {
		t.Fatalf("reasoning = %q", resp.Reasoning)
	}
	if strings.Join(sawReasoning, "") != "plan: inspect" {
		t.Fatalf("reasoning deltas = %v", sawReasoning)
	}
	if resp.Usage.In != 42 || resp.Usage.Out != 13 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "read" || string(resp.ToolCalls[0].Args) != `{"path":"a"}` {
		t.Fatalf("tool call not assembled: %+v", resp.ToolCalls)
	}
	if len(resp.Thinking) != 1 || resp.Thinking[0].Signature != "sig01" {
		t.Fatalf("thinking block = %+v", resp.Thinking)
	}
}

func TestAnthropicThinkingReplayAndDecode(t *testing.T) {
	// 1) non-stream decode of thinking blocks
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		budgetOK := false
		if th, ok := req["thinking"].(map[string]any); ok && th["budget_tokens"] != nil {
			budgetOK = true
		}
		if _, hasTemp := req["temperature"]; hasTemp && budgetOK {
			t.Errorf("temperature must be omitted when thinking is enabled")
		}
		// history must replay the signed thinking block when the budget is on
		msgs, _ := req["messages"].([]any)
		assistant, _ := msgs[1].(map[string]any)
		blocks, _ := assistant["content"].([]any)
		if len(blocks) < 1 {
			t.Errorf("thinking blocks missing from replayed assistant turn")
		} else {
			first, _ := blocks[0].(map[string]any)
			if first["type"] != "thinking" {
				t.Errorf("thinking block must precede everything else: %v", blocks)
			}
		}
		io.WriteString(w, `{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":4}}`)
	}))
	defer srv.Close()

	c, err := Resolve("anthropic", "claude", srv.URL, "k")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	resp, err := c.Chat(context.Background(), Request{
		Messages: []Message{
			{Role: RoleUser, Text: "go"},
			{Role: RoleAssistant, Thinking: []ThinkingBlock{{Type: "thinking", Thinking: "prior plan", Signature: "s1"}}, ToolCalls: []ToolCall{{ID: "t", Name: "read"}}},
			{Role: RoleTool, ToolCallID: "t", Text: "data"},
		},
		ThinkingBudget: 1024,
		MaxTokens:      512,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Text != "done" {
		t.Fatalf("text = %q", resp.Text)
	}

	// 2) without a budget, thinking must not be replayed
	c2, _ := Resolve("anthropic", "claude", srv.URL, "k")
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if _, has := req["thinking"]; has {
			t.Errorf("thinking must be absent without a budget")
		}
		msgs, _ := req["messages"].([]any)
		assistant, _ := msgs[1].(map[string]any)
		blocks, _ := assistant["content"].([]any)
		for _, b := range blocks {
			bm, _ := b.(map[string]any)
			if bm["type"] == "thinking" {
				t.Errorf("stale thinking block replayed without a budget")
			}
		}
		io.WriteString(w, `{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":4}}`)
	}))
	defer srv2.Close()
	c2.base = srv2.URL
	_, err = c2.Chat(context.Background(), Request{
		Messages: []Message{
			{Role: RoleUser, Text: "go"},
			{Role: RoleAssistant, Thinking: []ThinkingBlock{{Type: "thinking", Thinking: "prior plan", Signature: "s1"}}, ToolCalls: []ToolCall{{ID: "t", Name: "read"}}},
			{Role: RoleTool, ToolCallID: "t", Text: "data"},
		},
		MaxTokens: 512,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
}

func TestAnthropicThinkingDecodeNonStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"content":[{"type":"thinking","thinking":"deep thought","signature":"sig"},{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`)
	}))
	defer srv.Close()
	c, _ := Resolve("anthropic", "claude", srv.URL, "k")
	resp, err := c.Chat(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "x"}}})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Reasoning != "deep thought" {
		t.Fatalf("reasoning = %q", resp.Reasoning)
	}
	if len(resp.Thinking) != 1 || resp.Thinking[0].Signature != "sig" {
		t.Fatalf("thinking = %+v", resp.Thinking)
	}
}
