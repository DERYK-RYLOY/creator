// Package mcp serves Creator's tools over the Model Context Protocol.
//
// One binary is both a CLI and an MCP server: `creator mcp` speaks
// newline-delimited JSON-RPC 2.0 on stdin/stdout, exposing the same tools the
// local agent uses. That makes Creator usable from any MCP client instead of
// only its own loop.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/DERYK-RYLOY/creator/internal/tools"
)

// ProtocolVersion is the MCP revision this server implements.
const ProtocolVersion = "2025-06-18"

// Server is a stdio MCP endpoint.
type Server struct {
	Tools   *tools.Registry
	Version string
	In      io.Reader
	Out     io.Writer
	// Logf receives diagnostics; it must never write to Out.
	Logf func(format string, args ...any)

	writeMu sync.Mutex

	// pending tracks cancellable in-flight requests so a client's
	// `notifications/cancelled` can abort them. Each spawned request gets
	// a cancellable context whose key is its JSON-RPC id.
	pendMu  sync.Mutex
	pending map[string]context.CancelFunc
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve processes requests until stdin closes or an exit notification arrives.
// Individual requests run in their own goroutines so a client's
// `notifications/cancelled` can abort an in-flight tools/call even while the
// foreground loop is blocked inside a 1h-cap shell job.
func (s *Server) Serve(ctx context.Context) error {
	scanner := bufio.NewScanner(s.In)
	scanner.Buffer(make([]byte, 1<<20), 32<<20)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(trimSpace(line)) == 0 {
			continue
		}
		if bytes.HasPrefix(trimSpace(line), []byte("[")) {
			// A batch is valid JSON but unsupported by this server; it is
			// NOT a parse error. Reject it as invalid request.
			s.writeError(nil, -32600, "invalid request: batch requests are not supported")
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeError(nil, -32700, "parse error: "+err.Error())
			continue
		}
		if req.JSONRPC != "2.0" {
			s.writeError(req.ID, -32600, `invalid request: "jsonrpc" must be "2.0"`)
			continue
		}
		if req.Method == "" {
			s.writeError(req.ID, -32600, "invalid request: \"method\" is required")
			continue
		}
		if req.Method == "exit" {
			return nil
		}
		// Notifications and cheap request-free methods stay inline to keep
		// ordering. Everything else is dispatched concurrently so that one
		// stuck tool cannot block the cancellation reading it.
		if req.ID == nil || req.Method == "notifications/initialized" || req.Method == "notifications/cancelled" {
			s.handle(ctx, req, "")
			continue
		}
		rctx, cancel := context.WithCancel(ctx)
		key := string(bytes.TrimSpace(req.ID))
		s.pendMu.Lock()
		if s.pending == nil {
			s.pending = map[string]context.CancelFunc{}
		}
		s.pending[key] = cancel
		s.pendMu.Unlock()
		go s.handle(rctx, req, key)
	}
	return scanner.Err()
}

// unregisterPending removes key from the in-flight map. Called when req finishes.
func (s *Server) unregisterPending(key string) {
	s.pendMu.Lock()
	defer s.pendMu.Unlock()
	if s.pending != nil {
		delete(s.pending, key)
	}
}

// cancelPending signals the context of an in-flight request, if any.
func (s *Server) cancelPending(targetKey string) {
	s.pendMu.Lock()
	defer s.pendMu.Unlock()
	if s.pending == nil {
		return
	}
	if cancel, ok := s.pending[targetKey]; ok {
		cancel()
	}
}

func (s *Server) handle(ctx context.Context, req rpcRequest, key string) {
	if key != "" {
		defer s.unregisterPending(key)
	}
	// Notifications (no id) are processed for side effects only and never
	// answered — JSON-RPC 2.0 forbids it. Unknown notifications are dropped.
	if len(req.ID) == 0 {
		switch req.Method {
		case "notifications/initialized":
		case "notifications/cancelled":
			var params struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			_ = json.Unmarshal(req.Params, &params)
			s.cancelPending(string(bytes.TrimSpace(params.RequestID)))
		}
		return
	}
	switch req.Method {
	case "initialize":
		s.writeResult(req.ID, map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{
				"name":    "creator",
				"version": s.Version,
			},
			"instructions": "Creator exposes a terminal agent. shell returns a digest plus a handle; use out to page detail. " +
				"Long commands can be backgrounded and polled with jobs.",
		})
	case "notifications/initialized":
		// notification carries no id and expects no response
	case "notifications/cancelled":
		// The client aborts a specific in-flight request. Cancel it so a
		// long-running tools/call (bounded up to 1h by the shell layer)
		// really stops instead of spinning forever.
		var params struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		_ = json.Unmarshal(req.Params, &params)
		s.cancelPending(string(bytes.TrimSpace(params.RequestID)))
	case "ping":
		s.writeResult(req.ID, map[string]any{})
	case "shutdown":
		s.writeResult(req.ID, map[string]any{})
	case "tools/list":
		descriptors := s.Tools.Specs()
		list := make([]map[string]any, 0, len(descriptors))
		for _, d := range descriptors {
			var schema any = map[string]any{"type": "object"}
			if len(d.Schema) > 0 {
				var parsed any
				if err := json.Unmarshal(d.Schema, &parsed); err == nil {
					schema = parsed
				}
			}
			list = append(list, map[string]any{
				"name":        d.Name,
				"description": d.Description,
				"inputSchema": schema,
			})
		}
		s.writeResult(req.ID, map[string]any{"tools": list})
	case "tools/call":
		// Omitted params is legal: default to empty arguments rather than
		// punishing clients that omit the object entirely.
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if len(bytes.TrimSpace(req.Params)) == 0 {
			params.Arguments = json.RawMessage(`{}`)
		} else if err := json.Unmarshal(req.Params, &params); err != nil {
			s.writeError(req.ID, -32602, "invalid params: "+err.Error())
			return
		}
		if params.Name == "" {
			s.writeError(req.ID, -32602, "invalid params: \"name\" is required")
			return
		}
		if len(params.Arguments) == 0 {
			params.Arguments = json.RawMessage(`{}`)
		}
		result, err := s.Tools.Call(ctx, params.Name, params.Arguments)
		if err != nil {
			s.writeResult(req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": err.Error()}},
				"isError": true,
			})
			return
		}
		s.writeResult(req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": result}},
			"isError": false,
		})
	case "resources/list":
		s.writeResult(req.ID, map[string]any{"resources": []any{}})
	case "prompts/list":
		s.writeResult(req.ID, map[string]any{"prompts": []any{}})
	default:
		s.writeError(req.ID, -32601, fmt.Sprintf("method %q is not implemented", req.Method))
	}
}

func (s *Server) writeResult(id json.RawMessage, result any) {
	if len(id) == 0 {
		return // notification: no response on the wire
	}
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) writeError(id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

func (s *Server) write(resp rpcResponse) {
	payload, err := json.Marshal(resp)
	if err != nil {
		if s.Logf != nil {
			s.Logf("mcp: cannot marshal response: %v", err)
		}
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	payload = append(payload, '\n')
	if _, err := s.Out.Write(payload); err != nil && s.Logf != nil {
		s.Logf("mcp: write failed: %v", err)
	}
}

func trimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && (b[start] == ' ' || b[start] == '\t' || b[start] == '\r' || b[start] == '\n') {
		start++
	}
	end := len(b)
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\r' || b[end-1] == '\n') {
		end--
	}
	return b[start:end]
}
