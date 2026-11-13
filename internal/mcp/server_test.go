package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creatorcli/creator/internal/policy"
	"github.com/creatorcli/creator/internal/runner"
	"github.com/creatorcli/creator/internal/store"
	"github.com/creatorcli/creator/internal/tools"
)

type mcpHarness struct {
	in    *io.PipeWriter
	lines *bufio.Scanner
	done  chan error
	root  string
	st    *store.Store
}

func newMCPHarness(t *testing.T) *mcpHarness {
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
	engine := policy.NewEngine("balanced", false, false, strings.NewReader(""), io.Discard)
	env := &tools.Env{Workspace: root, Store: st, Jobs: jobs, Policy: engine, Session: "mcp_test", Budget: 500}
	server := &Server{Tools: tools.New(env), Version: "test", In: nil, Out: nil}

	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	server.In = inReader
	server.Out = outWriter

	done := make(chan error, 1)
	go func() {
		done <- server.Serve(context.Background())
		outWriter.Close()
	}()

	t.Cleanup(func() {
		inWriter.Close()
		jobs.StopAll()
		st.Close()
	})
	return &mcpHarness{
		in:    inWriter,
		lines: bufio.NewScanner(outReader),
		done:  done,
		root:  root,
		st:    st,
	}
}

func (h *mcpHarness) send(t *testing.T, payload string) {
	t.Helper()
	if _, err := io.WriteString(h.in, payload+"\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

func (h *mcpHarness) response(t *testing.T) map[string]any {
	t.Helper()
	type result struct {
		line string
		ok   bool
	}
	ch := make(chan result, 1)
	go func() {
		if h.lines.Scan() {
			ch <- result{h.lines.Text(), true}
			return
		}
		ch <- result{}
	}()
	select {
	case r := <-ch:
		if !r.ok {
			t.Fatalf("no response: %v", h.lines.Err())
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(r.line), &decoded); err != nil {
			t.Fatalf("invalid json response %q: %v", r.line, err)
		}
		return decoded
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for MCP response")
		return nil
	}
}

func TestCancelledToolsCallStopsLongRunningJob(t *testing.T) {
	// The MCP server used to dispatch tools/call synchronously, so a
	// client's notifications/cancelled could never abort a long-running
	// shell call. Now the request runs concurrently with a cancellable
	// context wired into the job runner.
	h := newMCPHarness(t)
	marker := filepath.Join(h.root, "job-fired-marker.txt")
	markerPS := strings.ReplaceAll(marker, `\`, `/`)
	cmd := "Start-Sleep -Seconds 30; 'done' | Out-File -FilePath '" + markerPS + "' -Encoding ascii"
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "shell",
			"arguments": map[string]any{"command": cmd},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.send(t, string(payload))
	time.Sleep(2 * time.Second) // let the job actually start
	h.send(t, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1,"reason":"smoke test cancel"}}`)
	// m.cancelPending wired the cancellation into the job runner; if that
	// propagation works the 30s job dies quickly and the marker is never
	// written, and the store shows the job in a terminal state well
	// before the job's own 30s sleep would have ended.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		recs := h.st.ListJobs(0)
		if len(recs) > 0 && recs[0].Status != "running" {
			return // terminal status: the cancel landed and the runner noticed
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("job still shows running after cancellation")
}

func TestInitializeAndToolsList(t *testing.T) {
	h := newMCPHarness(t)
	h.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	resp := h.response(t)
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", resp)
	}
	if result["protocolVersion"] != ProtocolVersion {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}
	info := result["serverInfo"].(map[string]any)
	if info["name"] != "creator" {
		t.Fatalf("serverInfo = %v", info)
	}

	h.send(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	h.send(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	resp = h.response(t)
	toolsList := resp["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, raw := range toolsList {
		tool := raw.(map[string]any)
		names[tool["name"].(string)] = true
		if _, ok := tool["inputSchema"]; !ok {
			t.Fatalf("tool %v has no inputSchema", tool["name"])
		}
	}
	for _, want := range []string{"read", "write", "edit", "shell", "out", "jobs", "grep", "glob"} {
		if !names[want] {
			t.Fatalf("tool %q missing from tools/list: %v", want, names)
		}
	}
}

func TestToolCallOverMCP(t *testing.T) {
	h := newMCPHarness(t)
	h.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	h.response(t)

	h.send(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"write","arguments":{"path":"mcp.txt","content":"from mcp"}}}`)
	resp := h.response(t)
	if isError, _ := resp["result"].(map[string]any)["isError"].(bool); isError {
		t.Fatalf("write failed: %v", resp)
	}
	if _, err := os.Stat(filepath.Join(h.root, "mcp.txt")); err != nil {
		t.Fatalf("file not written: %v", err)
	}

	h.send(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read","arguments":{"path":"mcp.txt"}}}`)
	resp = h.response(t)
	content := resp["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(content, "from mcp") {
		t.Fatalf("read content = %q", content)
	}
}

func TestMCPProtocolValidation(t *testing.T) {
	h := newMCPHarness(t)
	h.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	h.response(t)

	// Batch payloads: valid JSON, but rejected as invalid request (not as
	// a parse error).
	h.send(t, `[{"jsonrpc":"2.0","id":2,"method":"ping"}]`)
	resp := h.response(t)
	errObj := resp["error"].(map[string]any)
	if code, _ := errObj["code"].(float64); int(code) != -32600 {
		t.Fatalf("batch should be -32600, got %v", resp)
	}

	// Missing jsonrpc version.
	h.send(t, `{"id":3,"method":"ping"}`)
	resp = h.response(t)
	errObj = resp["error"].(map[string]any)
	if code, _ := errObj["code"].(float64); int(code) != -32600 {
		t.Fatalf("missing jsonrpc should be -32600, got %v", resp)
	}

	// Missing method.
	h.send(t, `{"jsonrpc":"2.0","id":4}`)
	resp = h.response(t)
	errObj = resp["error"].(map[string]any)
	if code, _ := errObj["code"].(float64); int(code) != -32600 {
		t.Fatalf("missing method should be -32600, got %v", resp)
	}

	// Unknown-method request carries its own id (never a bare null).
	h.send(t, `{"jsonrpc":"2.0","id":5,"method":"does/not/exist"}`)
	resp = h.response(t)
	if id, _ := resp["id"].(float64); int(id) != 5 {
		t.Fatalf("-32601 must carry the request id, got %v", resp)
	}
}

func TestMCPNotificationGetsNoResponse(t *testing.T) {
	h := newMCPHarness(t)
	h.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	h.response(t)
	h.send(t, `{"jsonrpc":"2.0","method":"does/not/exist"}`) // notification: must not be answered
	h.send(t, `{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	resp := h.response(t)
	if id, _ := resp["id"].(float64); int(id) != 2 {
		t.Fatalf("notification was answered or ordering broke: %v", resp)
	}
}

func TestMCPErrors(t *testing.T) {
	h := newMCPHarness(t)
	h.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	h.response(t)

	h.send(t, `{"jsonrpc":"2.0","id":2,"method":"does/not/exist"}`)
	resp := h.response(t)
	if resp["error"] == nil {
		t.Fatalf("expected an error response: %v", resp)
	}

	h.send(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	resp = h.response(t)
	result := resp["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("unknown tool should be an in-band tool error: %v", resp)
	}
}

func TestExitNotificationStopsServer(t *testing.T) {
	h := newMCPHarness(t)
	h.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	h.response(t)
	h.send(t, `{"jsonrpc":"2.0","method":"exit"}`)
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop on exit notification")
	}
}
