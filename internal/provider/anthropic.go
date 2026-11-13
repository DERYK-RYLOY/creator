package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type anBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      string          `json:"content,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	Thinking     string          `json:"thinking,omitempty"`
	Signature    string          `json:"signature,omitempty"`
	Data         string          `json:"data,omitempty"`
	CacheControl *anCacheControl `json:"cache_control,omitempty"`
}

// anCacheControl opts the surrounding block into Anthropic's prompt cache.
type anCacheControl struct {
	Type string `json:"type"`
}

type anMessage struct {
	Role    string    `json:"role"`
	Content []anBlock `json:"content"`
}

// anSystem is one system-prompt block; it carries cache_control so the stable
// prefix (system + tools) hits Anthropic's prompt cache every turn.
type anSystem struct {
	Type         string          `json:"type"`
	Text         string          `json:"text"`
	CacheControl *anCacheControl `json:"cache_control,omitempty"`
}

type anThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type anRequest struct {
	Model       string      `json:"model"`
	System      []anSystem  `json:"system,omitempty"`
	Messages    []anMessage `json:"messages"`
	Tools       []anTool    `json:"tools,omitempty"`
	MaxTokens   int         `json:"max_tokens"`
	Temperature *float64    `json:"temperature,omitempty"`
	Thinking    *anThinking `json:"thinking,omitempty"`
	Stream      bool        `json:"stream,omitempty"`
}

type anTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *anCacheControl `json:"cache_control,omitempty"`
}

type anResponse struct {
	Content []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Thought   string          `json:"thinking"`
		Signature string          `json:"signature"`
		Data      string          `json:"data"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// stream chunk shapes
type anStreamChunk struct {
	Type  string `json:"type"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	Message *struct {
		Usage struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Index        int `json:"index"`
	ContentBlock *struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Thinking string          `json:"thinking"`
		Data     string          `json:"data"`
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Input    json.RawMessage `json:"input"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (c *Client) anthropicChat(ctx context.Context, req Request) (Response, error) {
	body := c.anthropicBody(req)
	if req.OnDelta != nil {
		body.Stream = true
		return c.anthropicStreamChat(ctx, req, body)
	}
	data, err := postJSON(ctx, joinURL(c.base, "/messages"), c.anthropicHeaders(), body)
	if err != nil {
		return Response{}, err
	}
	var decoded anResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return Response{}, fmt.Errorf("%s: cannot decode response: %w", c.Label(), err)
	}
	if decoded.Error != nil && decoded.Error.Message != "" {
		return Response{}, fmt.Errorf("%s: %s", c.Label(), decoded.Error.Message)
	}
	resp := decodeAnResponse(decoded)
	if len(decoded.Content) == 0 && resp.Text == "" {
		return Response{}, fmt.Errorf("%s: provider returned 200 with no content", c.Label())
	}
	return resp, nil
}

// decodeAnResponse turns a payload into the neutral response, preserving
// thinking blocks instead of silently dropping them.
func decodeAnResponse(decoded anResponse) Response {
	resp := Response{
		StopReason: decoded.StopReason,
		Usage: Usage{
			In:  decoded.Usage.InputTokens,
			Out: decoded.Usage.OutputTokens,
		},
	}
	var text []string
	for _, block := range decoded.Content {
		switch block.Type {
		case "text":
			text = append(text, block.Text)
		case "thinking":
			resp.Thinking = append(resp.Thinking, ThinkingBlock{
				Type: "thinking", Thinking: block.Thought, Signature: block.Signature,
			})
			if block.Thought != "" {
				resp.Reasoning = joinNonEmpty([]string{resp.Reasoning, block.Thought})
			}
		case "redacted_thinking":
			resp.Thinking = append(resp.Thinking, ThinkingBlock{Type: "redacted_thinking", Data: block.Data})
		case "tool_use":
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:   block.ID,
				Name: block.Name,
				Args: block.Input,
			})
		}
	}
	resp.Text = joinNonEmpty(text)
	resp.ToolCalls = normalizeCalls(resp.ToolCalls)
	return resp
}

// anthropicBody translates the neutral request into Anthropic wire format.
func (c *Client) anthropicBody(req Request) *anRequest {
	body := &anRequest{
		Model:     c.model,
		MaxTokens: req.MaxTokens,
	}
	system := req.System
	if system == "" {
		var folded []string
		for _, m := range req.Messages {
			if m.Role == RoleSystem && m.Text != "" {
				folded = append(folded, m.Text)
			}
		}
		system = joinNonEmpty(folded)
	}
	if system != "" {
		body.System = []anSystem{{Type: "text", Text: system, CacheControl: &anCacheControl{Type: "ephemeral"}}}
	}
	if req.ThinkingBudget > 0 {
		// Extended thinking does not accept a non-default temperature; the
		// budget reservations happen inside max_tokens.
		body.Thinking = &anThinking{Type: "enabled", BudgetTokens: req.ThinkingBudget}
	} else if req.Temperature >= 0 {
		t := req.Temperature
		body.Temperature = &t
	}
	// A negative temperature (unset) is omitted; the provider default applies.

	pendingToolResults := false
	for _, m := range req.Messages {
		switch m.Role {
		case RoleSystem:
			// already folded into body.System
		case RoleAssistant:
			pendingToolResults = false
			var blocks []anBlock
			// Thinking blocks must precede tool_use inside the same assistant
			// turn when thinking is enabled; stale signed blocks from earlier
			// turns cannot be replayed without a new budget, so they are
			// omitted otherwise.
			if req.ThinkingBudget > 0 {
				for _, t := range m.Thinking {
					if t.Type == "redacted_thinking" {
						if t.Data != "" {
							blocks = append(blocks, anBlock{Type: "redacted_thinking", Data: t.Data})
						}
						continue
					}
					blocks = append(blocks, anBlock{Type: "thinking", Thinking: t.Thinking, Signature: t.Signature})
				}
			}
			if m.Text != "" {
				blocks = append(blocks, anBlock{Type: "text", Text: m.Text})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: tc.Args,
				})
			}
			if len(blocks) == 0 {
				continue
			}
			body.Messages = append(body.Messages, anMessage{Role: "assistant", Content: blocks})
		case RoleTool:
			block := anBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Text}
			if pendingToolResults && len(body.Messages) > 0 {
				last := &body.Messages[len(body.Messages)-1]
				last.Content = append(last.Content, block)
			} else {
				body.Messages = append(body.Messages, anMessage{Role: "user", Content: []anBlock{block}})
				pendingToolResults = true
			}
		default:
			pendingToolResults = false
			body.Messages = append(body.Messages, anMessage{
				Role:    "user",
				Content: []anBlock{{Type: "text", Text: m.Text}},
			})
		}
	}

	for _, t := range req.Tools {
		at := anTool{Name: t.Name, Description: t.Description}
		if len(t.Schema) > 0 {
			at.InputSchema = t.Schema
		} else {
			// input_schema is required by the Messages API; emit a
			// permissive minimal schema instead of null.
			at.InputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		body.Tools = append(body.Tools, at)
	}
	for i := range body.Tools {
		if i == len(body.Tools)-1 {
			body.Tools[i].CacheControl = &anCacheControl{Type: "ephemeral"}
		}
	}
	return body
}

func (c *Client) anthropicHeaders() map[string]string {
	return map[string]string{
		"x-api-key":         c.key,
		"anthropic-version": "2023-06-01",
	}
}

// anBlockAcc accumulates one streaming content block across its deltas.
type anBlockAcc struct {
	blockType string
	thought   strings.Builder
	signature strings.Builder
	json      strings.Builder
	data      string
	id        string
	name      string
}

// anthropicStreamChat consumes an Anthropic SSE stream and assembles the
// neutral response.
func (c *Client) anthropicStreamChat(ctx context.Context, req Request, body *anRequest) (Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	url := joinURL(c.base, "/messages")
	return retryStream(ctx, func(delivered *bool) (Response, error) {
		resp, err := openStream(ctx, url, c.anthropicHeaders(), payload)
		if err != nil {
			return Response{}, err
		}
		defer resp.Body.Close()
		return c.consumeAnthropicStream(ctx, req, resp.Body, delivered)
	})
}

// consumeAnthropicStream assembles one streamed completion. It runs once per
// connection attempt, with all accumulators local to the attempt.
func (c *Client) consumeAnthropicStream(ctx context.Context, req Request, stream io.Reader, delivered *bool) (Response, error) {
	var (
		text       strings.Builder
		reasoning  strings.Builder
		blocks     = map[int]*anBlockAcc{}
		blockOrder []int
		thinking   []ThinkingBlock
		stopReason string
		usage      Usage
		onDelta    = func(d Delta) {
			*delivered = true
			req.OnDelta(d)
		}
	)
	err := iterateSSE(stream, func(data string) error {
		var chunk anStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("%s: cannot decode stream chunk: %w", c.Label(), err)
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return &inBandStreamError{status: anthropicStreamErrorStatus(chunk.Error.Type), msg: c.Label() + ": " + chunk.Error.Message}
		}
		switch chunk.Type {
		case "message_start":
			if chunk.Message != nil {
				usage.In = chunk.Message.Usage.InputTokens
			}
		case "content_block_start":
			if chunk.ContentBlock == nil {
				return nil
			}
			acc := &anBlockAcc{blockType: chunk.ContentBlock.Type, id: chunk.ContentBlock.ID, name: chunk.ContentBlock.Name, data: chunk.ContentBlock.Data}
			blocks[chunk.Index] = acc
			blockOrder = append(blockOrder, chunk.Index)
		case "content_block_delta":
			acc := blocks[chunk.Index]
			if acc == nil || chunk.Delta == nil {
				return nil
			}
			switch chunk.Delta.Type {
			case "text_delta":
				text.WriteString(chunk.Delta.Text)
				onDelta(Delta{Text: chunk.Delta.Text})
			case "thinking_delta":
				acc.thought.WriteString(chunk.Delta.Thinking)
				reasoning.WriteString(chunk.Delta.Thinking)
				onDelta(Delta{Reasoning: chunk.Delta.Thinking})
			case "signature_delta":
				acc.signature.WriteString(chunk.Delta.Signature)
			case "input_json_delta":
				acc.json.WriteString(chunk.Delta.PartialJSON)
				onDelta(Delta{ToolIndex: chunk.Index, ToolID: acc.id, ToolName: acc.name, ToolArgs: chunk.Delta.PartialJSON})
			}
		case "content_block_stop":
			acc := blocks[chunk.Index]
			if acc == nil {
				return nil
			}
			switch acc.blockType {
			case "thinking":
				thinking = append(thinking, ThinkingBlock{
					Type: "thinking", Thinking: acc.thought.String(), Signature: acc.signature.String(),
				})
			case "redacted_thinking":
				thinking = append(thinking, ThinkingBlock{Type: "redacted_thinking", Data: acc.data})
			}
		case "message_delta":
			if chunk.Delta != nil && chunk.Delta.StopReason != "" {
				stopReason = chunk.Delta.StopReason
			}
			if chunk.Usage != nil {
				usage.Out = chunk.Usage.OutputTokens
			}
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		respErr := fmt.Errorf("%s stream failed: %w", c.Label(), err)
		if *delivered {
			return Response{}, fmt.Errorf("%w (partial content already delivered)", respErr)
		}
		return Response{}, respErr
	}

	var toolCalls []ToolCall
	for _, idx := range blockOrder {
		acc := blocks[idx]
		if acc == nil || acc.blockType != "tool_use" {
			continue
		}
		args := acc.json.String()
		if args == "" || !json.Valid([]byte(args)) {
			args = "{}"
		}
		toolCalls = append(toolCalls, ToolCall{ID: acc.id, Name: acc.name, Args: json.RawMessage(args)})
	}

	return Response{
		Text:       text.String(),
		Reasoning:  reasoning.String(),
		Thinking:   thinking,
		ToolCalls:  normalizeCalls(toolCalls),
		StopReason: stopReason,
		Usage:      usage,
	}, nil
}

func joinNonEmpty(parts []string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += "\n"
		}
		out += p
	}
	return out
}

// anthropicStreamErrorStatus maps Anthropic's in-band error types to a
// synthetic HTTP-like status so the pre-delivery retry machinery can see
// them. Anthropic emits `{"type":"error","error":{"type":...}}` instead of
// an HTTP failure when the service is overloaded; without this, those die
// as fatal before any frame reaches the caller.
func anthropicStreamErrorStatus(errType string) int {
	switch errType {
	case "overloaded_error":
		return 529
	case "rate_limit_error":
		return 429
	case "api_error":
		return 503
	case "timeout_error":
		return 504
	default:
		// authentication/invalid/permission/not_found/request_too_large and
		// anything unknown: fatal, do not retry.
		return 400
	}
}
