package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type oaFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaToolCall struct {
	// Index may be absent in some backends' deltas; *int lets a missing key
	// fall through to the last-seen index instead of collapsing onto call 0.
	Index    *int       `json:"index,omitempty"`
	ID       string     `json:"id,omitempty"`
	Type     string     `json:"type,omitempty"`
	Function oaFunction `json:"function"`
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content,omitempty"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaRequest struct {
	Model               string           `json:"model"`
	Messages            []oaMessage      `json:"messages"`
	Tools               []oaTool         `json:"tools,omitempty"`
	ToolChoice          string           `json:"tool_choice,omitempty"`
	MaxTokens           int              `json:"max_tokens,omitempty"`
	MaxCompletionTokens int              `json:"max_completion_tokens,omitempty"`
	Temperature         *float64         `json:"temperature,omitempty"`
	Stream              bool             `json:"stream"`
	StreamOptions       *oaStreamOptions `json:"stream_options,omitempty"`
}

type oaStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oaResponse struct {
	Choices []struct {
		Message struct {
			Content          string       `json:"content"`
			ToolCalls        []oaToolCall `json:"tool_calls"`
			ReasoningContent string       `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

type oaStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string       `json:"content"`
			ToolCalls        []oaToolCall `json:"tool_calls"`
			ReasoningContent string       `json:"reasoning_content"`
			Reasoning        string       `json:"reasoning"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (c *Client) openaiChat(ctx context.Context, req Request) (Response, error) {
	body, err := c.openaiBody(req)
	if err != nil {
		return Response{}, err
	}
	if req.OnDelta != nil {
		return c.openaiStreamChat(ctx, req, body)
	}
	data, err := postJSON(ctx, joinURL(c.base, "/chat/completions"), c.openaiHeaders(), body)
	if err != nil {
		return Response{}, err
	}
	var decoded oaResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return Response{}, fmt.Errorf("%s: cannot decode response: %w", c.Label(), err)
	}
	if decoded.Error != nil && decoded.Error.Message != "" {
		return Response{}, fmt.Errorf("%s: %s", c.Label(), decoded.Error.Message)
	}
	resp := Response{
		Usage: Usage{
			In:  decoded.Usage.PromptTokens,
			Out: decoded.Usage.CompletionTokens,
		},
	}
	if len(decoded.Choices) > 0 {
		choice := decoded.Choices[0]
		resp.Text = choice.Message.Content
		resp.Reasoning = choice.Message.ReasoningContent
		resp.StopReason = choice.FinishReason
		for _, tc := range choice.Message.ToolCalls {
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Args: json.RawMessage(tc.Function.Arguments),
			})
		}
	} else {
		// An empty 200 response is never success; the caller needs to know.
		return resp, fmt.Errorf("provider returned 200 with no choices")
	}
	resp.ToolCalls = normalizeCalls(resp.ToolCalls)
	if resp.Usage.In == 0 && resp.Usage.Out == 0 {
		// Mirror the streaming path: backends that report no usage still get
		// an honest ledger instead of a zeroed token counter.
		if raw, merr := json.Marshal(body); merr == nil {
			resp.Usage = EstimateRequestTokens(raw, resp.Text+resp.Reasoning, resp.ToolCalls)
		}
	}
	return resp, nil
}

// isReasoningModel reports whether the model rejects the legacy 'temperature'
// and 'max_tokens' parameters (OpenAI's reasoning family: o-series, gpt-5,
// and DeepSeek's reasoner aliases).
func isReasoningModel(model string) bool {
	m := strings.ToLower(model)
	for _, seg := range strings.Split(m, "/") {
		seg = strings.TrimSpace(seg)
		for _, prefix := range []string{"o1", "o3", "o4", "gpt-5", "gpt-oss", "deepseek-reasoner", "deepseek-r1", "r1-", "-reasoner"} {
			if strings.HasPrefix(seg, prefix) {
				return true
			}
		}
	}
	return false
}

// openaiBody translates the neutral request into the OpenAI wire format for
// both the non-streaming and streaming paths.
func (c *Client) openaiBody(req Request) (*oaRequest, error) {
	body := &oaRequest{
		Model:     c.model,
		MaxTokens: req.MaxTokens,
	}
	if isReasoningModel(c.model) {
		// Reasoning backends reject the legacy fields outright: 'temperature'
		// must be omitted (only the default is allowed) and 'max_tokens' was
		// renamed to 'max_completion_tokens'. Sending ours turns every call
		// into a 400, so both are adapted here instead.
		body.MaxTokens = 0
		body.MaxCompletionTokens = req.MaxTokens
	} else if req.Temperature >= 0 {
		t := req.Temperature
		body.Temperature = &t
	}
	// A negative temperature (default) is "unset" — the provider's default applies.
	if req.System != "" {
		body.Messages = append(body.Messages, oaMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case RoleSystem:
			body.Messages = append(body.Messages, oaMessage{Role: "system", Content: m.Text})
		case RoleAssistant:
			om := oaMessage{Role: "assistant", Content: m.Text}
			for _, tc := range m.ToolCalls {
				om.ToolCalls = append(om.ToolCalls, oaToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: oaFunction{
						Name:      tc.Name,
						Arguments: string(tc.Args),
					},
				})
			}
			body.Messages = append(body.Messages, om)
		case RoleTool:
			body.Messages = append(body.Messages, oaMessage{
				Role:       "tool",
				ToolCallID: m.ToolCallID,
				Content:    m.Text,
			})
		default:
			body.Messages = append(body.Messages, oaMessage{Role: "user", Content: m.Text})
		}
	}
	for _, t := range req.Tools {
		ot := oaTool{Type: "function"}
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.Schema
		body.Tools = append(body.Tools, ot)
	}
	return body, nil
}

func (c *Client) openaiHeaders() map[string]string {
	key := c.key
	return map[string]string{"Authorization": "Bearer " + key}
}

// openaiStreamChat runs a streaming completion and assembles the response from
// the same chunks that OnDelta reported. Tool-call arguments arrive as string
// fragments under a cumulative index and must be stitched back together.
func (c *Client) openaiStreamChat(ctx context.Context, req Request, body *oaRequest) (Response, error) {
	body.Stream = true
	if c.desc.StreamUsage {
		body.StreamOptions = &oaStreamOptions{IncludeUsage: true}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	url := joinURL(c.base, "/chat/completions")
	return retryStream(ctx, func(delivered *bool) (Response, error) {
		resp, err := openStream(ctx, url, c.openaiHeaders(), payload)
		if err != nil {
			return Response{}, err
		}
		defer resp.Body.Close()
		return c.consumeOpenAIStream(ctx, req, body, resp.Body, delivered)
	})
}

// consumeOpenAIStream assembles one streamed completion from the same chunks
// that OnDelta reports. It runs once per connection attempt: every accumulator
// is local so a retry starts clean.
func (c *Client) consumeOpenAIStream(ctx context.Context, req Request, body *oaRequest, stream io.Reader, delivered *bool) (Response, error) {
	var (
		text        strings.Builder
		reasoning   strings.Builder
		calls       = map[int]*ToolCall{}
		callOrder   []int
		lastToolIdx int
		stopReason  string
		usage       Usage
		haveUsage   bool
		onDelta     = func(d Delta) {
			*delivered = true
			req.OnDelta(d)
		}
	)
	err := iterateSSE(stream, func(data string) error {
		var chunk oaStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("%s: cannot decode stream chunk: %w", c.Label(), err)
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return &inBandStreamError{status: openaiStreamErrorStatus(chunk.Error.Type), msg: c.Label() + ": " + chunk.Error.Message}
		}
		if chunk.Usage != nil {
			usage = Usage{In: chunk.Usage.PromptTokens, Out: chunk.Usage.CompletionTokens}
			haveUsage = true
		}
		for _, choice := range chunk.Choices {
			d := choice.Delta
			if d.ReasoningContent != "" {
				reasoning.WriteString(d.ReasoningContent)
				onDelta(Delta{Reasoning: d.ReasoningContent})
			}
			if d.Reasoning != "" {
				reasoning.WriteString(d.Reasoning)
				onDelta(Delta{Reasoning: d.Reasoning})
			}
			if d.Content != "" {
				text.WriteString(d.Content)
				onDelta(Delta{Text: d.Content})
			}
			for _, tc := range d.ToolCalls {
				// Some OpenAI-compatible backends emit tool_call deltas without
				// an explicit index — attribute them to the most recent one.
				var idx int
				if tc.Index == nil {
					idx = lastToolIdx
				} else {
					idx = *tc.Index
					lastToolIdx = idx
				}
				call, ok := calls[idx]
				if !ok {
					call = &ToolCall{}
					calls[idx] = call
					callOrder = append(callOrder, idx)
				}
				if tc.ID != "" {
					call.ID = tc.ID
				}
				if tc.Function.Name != "" {
					call.Name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					call.Args = json.RawMessage(string(call.Args) + tc.Function.Arguments)
					onDelta(Delta{ToolIndex: idx, ToolID: call.ID, ToolName: call.Name, ToolArgs: tc.Function.Arguments})
				}
				if tc.ID != "" || tc.Function.Name != "" {
					onDelta(Delta{ToolIndex: idx, ToolID: call.ID, ToolName: call.Name})
				}
			}
			if choice.FinishReason != "" {
				stopReason = choice.FinishReason
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

	out := Response{
		Text:       text.String(),
		Reasoning:  reasoning.String(),
		StopReason: stopReason,
		Usage:      usage,
	}
	for _, idx := range callOrder {
		call := calls[idx]
		if call.Name == "" {
			continue
		}
		if len(call.Args) == 0 || !json.Valid(call.Args) {
			call.Args = json.RawMessage(`{}`)
		}
		out.ToolCalls = append(out.ToolCalls, *call)
	}
	out.ToolCalls = normalizeCalls(out.ToolCalls)
	if !haveUsage {
		// Backends that do not honor include_usage still deserve an honest
		// ledger: estimate from the serialized exchange, minus the streaming-only
		// flags which the user never typed.
		clean := *body
		clean.Stream = false
		clean.StreamOptions = nil
		raw, merr := json.Marshal(clean)
		if merr == nil {
			out.Usage = EstimateRequestTokens(raw, text.String()+reasoning.String(), out.ToolCalls)
		}
	}
	return out, nil
}

// EstimateRequestTokens approximates the input-token count of a request plus
// assembled output. It is only used when a streamed response carries no usage.
func EstimateRequestTokens(requestBody []byte, output string, calls []ToolCall) Usage {
	in := estTokens(string(requestBody))
	out := estTokens(output)
	for _, c := range calls {
		out += estTokens(string(c.Args)) + estTokens(c.Name) + estTokens(c.ID)
	}
	return Usage{In: in, Out: out}
}

// estTokens is the same 4-bytes-per-token approximation the reduce engine
// uses, kept local so the provider package stays dependency-free.
func estTokens(s string) int { return (len(s) + 3) / 4 }

// openaiStreamErrorStatus maps OpenAI-compatible in-band error types to a
// synthetic HTTP-like status so the pre-delivery retry machinery can see
// them. Unknown or fatal types surface as 400 — fatal, not retried.
func openaiStreamErrorStatus(errType string) int {
	switch errType {
	case "server_error":
		return 503
	case "rate_limit_exceeded", "rate_limit_error":
		return 429
	case "engine_overloaded", "overloaded_error":
		return 529
	case "timeout":
		return 504
	case "insufficient_quota":
		return 402
	default:
		// invalid_request/authentication/permission/not_found and unknowns:
		// fatal, do not retry.
		return 400
	}
}
