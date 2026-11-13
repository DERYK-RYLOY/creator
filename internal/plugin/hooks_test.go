package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func loadHooks(t *testing.T, body string) *Host {
	t.Helper()
	dir := t.TempDir()
	writePlugin(t, dir, "hooks.lua", body)
	host, warnings, err := Load([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	return host
}

func TestHookRewritesToolArguments(t *testing.T) {
	host := loadHooks(t, `
hook{
  on = "before_tool",
  name = "search",
  handler = function(ev)
    ev.args.query = string.upper(ev.args.query)
    return { args = ev.args }
  end,
}
`)
	defer host.Close()
	hs := host.Hooks()
	out, err := hs.BeforeTool("search", json.RawMessage(`{"query":"hello world"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"query":"HELLO WORLD"}` {
		t.Fatalf("rewritten args: %s", out)
	}
	// Other tools pass through untouched.
	pass, err := hs.BeforeTool("read", json.RawMessage(`{"path":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(pass) != `{"path":"x"}` {
		t.Fatalf("unfiltered tool changed: %s", pass)
	}
}

func TestHookDeniesToolCall(t *testing.T) {
	host := loadHooks(t, `
hook{
  on = "before_tool",
  name = "shell",
  handler = function(ev)
    if string.find(ev.args.command, "rm") then
      return { deny = true, reason = "no destruction today" }
    end
  end,
}
`)
	defer host.Close()
	_, err := host.Hooks().BeforeTool("shell", json.RawMessage(`{"command":"rm -rf /"}`))
	if err == nil || !strings.Contains(err.Error(), "no destruction today") {
		t.Fatalf("expected denial, got %v", err)
	}
	// A command that passes the filter is untouched.
	out, err := host.Hooks().BeforeTool("shell", json.RawMessage(`{"command":"go test"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"command":"go test"}` {
		t.Fatalf("allowed command changed: %s", out)
	}
}

func TestHookRewritesToolResult(t *testing.T) {
	host := loadHooks(t, `
hook{
  on = "after_tool",
  handler = function(ev)
    return { result = "tagged: " .. ev.result }
  end,
}
`)
	defer host.Close()
	if got := host.Hooks().AfterTool("read", "body", nil); got != "tagged: body" {
		t.Fatalf("after_tool result: %q", got)
	}
}

func TestHookGatesDelegationAndRewritesTask(t *testing.T) {
	host := loadHooks(t, `
hook{
  on = "before_delegate",
  handler = function(ev)
    if string.find(ev.task, "production") then
      return { deny = true, reason = "production is off limits" }
    end
    ev.task = "SAFETY REVIEWED: " .. ev.task
    return ev
  end,
}
`)
	defer host.Close()
	hs := host.Hooks()

	denied, derr := hs.BeforeDelegate(map[string]any{"task": "redeploy production", "name": "x"})
	if derr == nil || !strings.Contains(derr.Error(), "production is off limits") {
		t.Fatalf("expected delegate denial, got %v", derr)
	}
	_ = denied

	rewritten, err := hs.BeforeDelegate(map[string]any{"task": "read logs"})
	if err != nil {
		t.Fatal(err)
	}
	if rewritten["task"] != "SAFETY REVIEWED: read logs" {
		t.Fatalf("task not rewritten: %v", rewritten)
	}
}

func TestHookBadPointIsAWarning(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "bad.lua", `hook{ on = "mid_tool", handler = function(ev) return nil end }`)
	_, warnings, err := Load([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected a warning for the unknown hook point, got %v", warnings)
	}
}

func TestHookErrorDeniesAction(t *testing.T) {
	host := loadHooks(t, `
hook{
  on = "before_tool",
  handler = function(ev)
    error("hook exploded")
  end,
}
`)
	defer host.Close()
	_, err := host.Hooks().BeforeTool("read", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "hook exploded") {
		t.Fatalf("expected handler error to deny, got %v", err)
	}
	if len(host.HookPoints()) != 1 || !strings.Contains(host.HookPoints()[0], "before_tool") {
		t.Fatalf("hook point labels broken: %v", host.HookPoints())
	}
}
