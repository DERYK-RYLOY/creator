// Package provider implements a single interface over the model backends
// Creator supports.
//
// Two wire protocols cover every provider in the registry: the OpenAI
// chat-completions protocol (OpenAI, Groq, OpenRouter, Mistral, xAI, DeepSeek,
// Together, Fireworks, Cerebras, Perplexity, Gemini's compatibility endpoint,
// Ollama, LM Studio, and anything else OpenAI-shaped) and Anthropic's Messages
// API.
package provider

import (
	"context"
	"encoding/json"
)

// Role is a conversation role. Creator keeps provider-neutral roles and the
// protocol adapters translate.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is a model-issued request to run a tool.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// ThinkingBlock preserves a reasoning block exactly as the provider produced
// it, so it can be shown to the user and replayed to the same provider later
// (Anthropic requires the original signed blocks when extended thinking is
// active). Type is "thinking"; "redacted_thinking" blocks are stored as-is with
// an empty Thinking body since their payload is opaque server-side data.
type ThinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
}

// Message is one turn in a conversation.
type Message struct {
	Role       Role            `json:"role"`
	Text       string          `json:"text,omitempty"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Thinking   []ThinkingBlock `json:"thinking,omitempty"`
}

// ToolSpec describes a callable tool.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// Delta is one incremental piece of a streaming response.
type Delta struct {
	Text      string // assistant prose chunk
	Reasoning string // reasoning / thinking chunk
	ToolIndex int    // cumulative index of the tool call being streamed
	ToolID    string // set only in the first delta of a tool call
	ToolName  string // set only in the first delta of a tool call
	ToolArgs  string // arguments fragment for ToolIndex
}

// DeltaFunc receives streaming deltas. Returning an error is not supported:
// use context cancellation to stop a stream.
type DeltaFunc func(Delta)

// Request is a single completion request.
type Request struct {
	System         string
	Messages       []Message
	Tools          []ToolSpec
	MaxTokens      int
	Temperature    float64   // <0 means unset (default); >=0 is sent verbatim — including a deliberate 0
	ThinkingBudget int       // >0 asks for extended reasoning when the backend supports it
	OnDelta        DeltaFunc // when set, the provider streams and reports progress
}

// Usage reports token consumption.
type Usage struct {
	In  int `json:"in"`
	Out int `json:"out"`
}

// Response is a completed generation.
type Response struct {
	Text       string
	Reasoning  string
	Thinking   []ThinkingBlock
	ToolCalls  []ToolCall
	StopReason string
	Usage      Usage
}

// Provider is a chat backend.
type Provider interface {
	// Label identifies the backend for humans, e.g. "openai/gpt-5".
	Label() string
	// Model returns the model name in use.
	Model() string
	// Chat performs one completion with tool support. When Request.OnDelta is
	// set the backend streams and the response is assembled from the same
	// chunks the caller saw.
	Chat(ctx context.Context, req Request) (Response, error)
}
