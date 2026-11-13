package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Client is a resolved provider endpoint.
type Client struct {
	desc  Descriptor
	model string
	base  string
	key   string
}

// Label identifies the backend and model, e.g. "groq/llama-3.3-70b-versatile".
func (c *Client) Label() string {
	return c.desc.Slug + "/" + c.model
}

// Model returns the model name in use.
func (c *Client) Model() string { return c.model }

// Descriptor returns the registry entry backing this client.
func (c *Client) Descriptor() Descriptor { return c.desc }

// Chat dispatches to the wire protocol the provider speaks.
func (c *Client) Chat(ctx context.Context, req Request) (Response, error) {
	if req.MaxTokens <= 0 {
		req.MaxTokens = 4096
	}
	switch c.desc.Protocol {
	case ProtocolAnthropic:
		return c.anthropicChat(ctx, req)
	case ProtocolOpenAI:
		return c.openaiChat(ctx, req)
	default:
		return Response{}, fmt.Errorf("provider %s uses unsupported protocol %q", c.desc.Slug, c.desc.Protocol)
	}
}

func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

// normalizeCalls sanitizes arguments so a malformed payload surfaces as a tool
// error instead of a panic in an adapter.
func normalizeCalls(calls []ToolCall) []ToolCall {
	out := make([]ToolCall, 0, len(calls))
	for i, c := range calls {
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d", i+1)
		}
		if len(strings.TrimSpace(string(c.Args))) == 0 {
			c.Args = json.RawMessage(`{}`)
		} else if !json.Valid(c.Args) {
			wrapped, _ := json.Marshal(map[string]string{"_invalid_json": string(c.Args)})
			c.Args = wrapped
		}
		out = append(out, c)
	}
	return out
}
