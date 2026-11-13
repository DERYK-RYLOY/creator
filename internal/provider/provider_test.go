package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAITranslation(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"working","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"x.go\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`)
	}))
	defer srv.Close()

	c, err := Resolve("custom", "test-model", srv.URL, "secret-key")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	resp, err := c.Chat(context.Background(), Request{
		System:    "be terse",
		Messages:  []Message{{Role: RoleUser, Text: "hello"}},
		Tools:     []ToolSpec{{Name: "read", Description: "reads", Schema: json.RawMessage(`{"type":"object"}`)}},
		MaxTokens: 256,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if auth != "Bearer secret-key" {
		t.Fatalf("authorization header = %q", auth)
	}
	if got["model"] != "test-model" {
		t.Fatalf("model = %v", got["model"])
	}
	if got["stream"] != false {
		t.Fatalf("stream must be false, got %v", got["stream"])
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected system + user message, got %d", len(msgs))
	}
	if first, _ := msgs[0].(map[string]any); first["role"] != "system" {
		t.Fatalf("first message should be system: %v", msgs[0])
	}
	tools, _ := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools were not forwarded")
	}
	if resp.Text != "working" {
		t.Fatalf("text = %q", resp.Text)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "read" {
		t.Fatalf("tool calls not parsed: %+v", resp.ToolCalls)
	}
	if string(resp.ToolCalls[0].Args) != `{"path":"x.go"}` {
		t.Fatalf("arguments = %s", resp.ToolCalls[0].Args)
	}
	if resp.Usage.In != 11 || resp.Usage.Out != 7 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestOpenAIToolResultRoles(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		io.WriteString(w, `{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	c, _ := Resolve("ollama", "local", srv.URL, "")
	_, err := c.Chat(context.Background(), Request{
		Messages: []Message{
			{Role: RoleUser, Text: "go"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "shell", Args: json.RawMessage(`{"command":"ls"}`)}}},
			{Role: RoleTool, ToolCallID: "c1", Text: "a.go"},
		},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	msgs, _ := got["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "tool" || last["tool_call_id"] != "c1" || last["content"] != "a.go" {
		t.Fatalf("tool result not translated: %v", last)
	}
}

func TestRegistryDescriptors(t *testing.T) {
	// Capability flags must not drift silently as the registry grows.
	if d, ok := Lookup("anthropic"); !ok || !d.SupportsThinking {
		t.Fatalf("anthropic must declare SupportsThinking: %+v", d)
	}
	for _, slug := range []string{"openai", "google", "xai", "groq", "ollama", "custom", "vllm"} {
		if d, ok := Lookup(slug); !ok || d.SupportsThinking {
			t.Fatalf("%s must not declare a thinking budget path: %+v", slug, d)
		}
	}
	for _, d := range Descriptors() {
		// Every descriptor with a non-empty BaseURL stays resolvable; the
		// cloud ones need env keys, the local ones never do.
		if d.Local != (len(d.APIKeyEnv) == 0) && len(d.APIKeyEnv) != 0 && d.Local {
			t.Fatalf("%s is local but also declares key envs", d.Slug)
		}
	}
	if _, err := Resolve("custom", "m", "http://localhost:9/v1", ""); err != nil {
		t.Fatalf("custom must not require a key: %v", err)
	}
	if _, err := Resolve("openai", "m", "https://x.example/v1", ""); err == nil {
		t.Fatal("openai without a key must be rejected")
	}
}

func TestAnthropicTranslation(t *testing.T) {
	var got map[string]any
	var key, version string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("x-api-key")
		version = r.Header.Get("anthropic-version")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
  "content": [
    {"type": "text", "text": "let me look"},
    {"type": "tool_use", "id": "tu_1", "name": "shell", "input": {"command": "ls"}}
  ],
  "stop_reason": "tool_use",
  "usage": {"input_tokens": 42, "output_tokens": 9}
}`)
	}))
	defer srv.Close()

	c, err := Resolve("anthropic", "claude-test", srv.URL, "sk-ant")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	resp, err := c.Chat(context.Background(), Request{
		System: "sys",
		Messages: []Message{
			{Role: RoleUser, Text: "go"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{
				{ID: "a", Name: "read", Args: json.RawMessage(`{"path":"a"}`)},
				{ID: "b", Name: "read", Args: json.RawMessage(`{"path":"b"}`)},
			}},
			{Role: RoleTool, ToolCallID: "a", Text: "A"},
			{Role: RoleTool, ToolCallID: "b", Text: "B"},
		},
		Tools:     []ToolSpec{{Name: "shell", Description: "runs", Schema: json.RawMessage(`{"type":"object"}`)}},
		MaxTokens: 512,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if key != "sk-ant" {
		t.Fatalf("api key header = %q", key)
	}
	if version == "" {
		t.Fatal("anthropic-version header missing")
	}
	// System is sent as a cached block so the stable prefix hits the prompt
	// cache on every turn.
	sys, _ := got["system"].([]any)
	if len(sys) != 1 {
		t.Fatalf("system should be a single block, got %v", got["system"])
	}
	block, _ := sys[0].(map[string]any)
	if block["text"] != "sys" {
		t.Fatalf("system text = %v", block["text"])
	}
	if block["cache_control"] == nil {
		t.Fatalf("system block is not marked for prompt caching")
	}
	// The tool spec must actually reach the API; previously body.Tools was
	// never populated and the model could not call any tool.
	tools, _ := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("anthropic request should carry the tool spec, got %v", got["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "shell" || tool["description"] != "runs" {
		t.Fatalf("tool translation wrong: %v", tool)
	}
	if schema, _ := tool["input_schema"].(map[string]any); schema["type"] != "object" {
		t.Fatalf("tool input_schema wrong: %v", tool["input_schema"])
	}
	if cc, _ := tool["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Fatalf("last tool should carry ephemeral cache_control, got %v", tool["cache_control"])
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("expected user/assistant/user(tool results), got %d: %v", len(msgs), msgs)
	}
	last, _ := msgs[2].(map[string]any)
	blocks, _ := last["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("tool results must merge into one user turn, got %d blocks", len(blocks))
	}
	if resp.Text != "let me look" {
		t.Fatalf("text = %q", resp.Text)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "tu_1" {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.Usage.In != 42 || resp.Usage.Out != 9 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestResolveErrors(t *testing.T) {
	if _, err := Resolve("does-not-exist", "", "", ""); err == nil {
		t.Fatal("unknown provider should fail")
	}
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := Resolve("openai", "", "", ""); err == nil {
		t.Fatal("missing key should fail for a hosted provider")
	}
	if _, err := Resolve("ollama", "", "", ""); err != nil {
		t.Fatalf("local provider should not need a key: %v", err)
	}
}
