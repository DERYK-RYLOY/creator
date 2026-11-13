package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuin/gopher-lua"

	"github.com/creatorcli/creator/internal/tools"
)

const slugifyPlugin = `
tool{
  name = "slugify",
  description = "Turn a title into a URL slug",
  schema = {
    type = "object",
    properties = { text = { type = "string" } },
    required = { "text" },
  },
  handler = function(args)
    local s = string.lower(args.text)
    s = string.gsub(s, "[^%w%s-]", "")
    s = string.gsub(s, "%s+", "-")
    return s
  end,
}

tool{
  name = "shout",
  description = "Uppercase text",
  handler = function(args)
    return string.upper(args.text) .. "!"
  end,
}
`

func writePlugin(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAndCallPluginTools(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "text.lua", slugifyPlugin)

	host, warnings, err := Load([]string{dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer host.Close()
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if got := host.Names(); len(got) != 2 {
		t.Fatalf("expected 2 tools, got %v", got)
	}

	env := &tools.Env{Budget: 500}
	reg := tools.New(env)
	for _, tool := range host.Tools() {
		if err := reg.Add(tool); err != nil {
			t.Fatalf("add %s: %v", tool.Name, err)
		}
	}

	out, err := reg.Call(context.Background(), "slugify", json.RawMessage(`{"text":"Hello, Creator World!"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out != "hello-creator-world" {
		t.Fatalf("slug = %q", out)
	}

	out, err = reg.Call(context.Background(), "shout", json.RawMessage(`{"text":"hey"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out != "HEY!" {
		t.Fatalf("shout = %q", out)
	}
}

func TestSchemaIsConvertedToJSON(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "text.lua", slugifyPlugin)
	host, _, err := Load([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	var found bool
	for _, tool := range host.Tools() {
		if tool.Name != "slugify" {
			continue
		}
		found = true
		var schema map[string]any
		if err := json.Unmarshal(tool.Schema, &schema); err != nil {
			t.Fatalf("schema is not json: %v", err)
		}
		props := schema["properties"].(map[string]any)
		if _, ok := props["text"]; !ok {
			t.Fatalf("properties lost: %v", schema)
		}
		required := schema["required"].([]any)
		if len(required) != 1 || required[0] != "text" {
			t.Fatalf("required array lost: %v", schema)
		}
	}
	if !found {
		t.Fatal("slugify not loaded")
	}
}

func TestLuaCyclicTableDoesNotStackOverflow(t *testing.T) {
	L := lua.NewState()
	defer L.Close()
	tbl := L.CreateTable(0, 1)
	tbl.RawSetString("self", tbl)
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("cyclic table panicked: %v", r)
			}
		}()
		out := luaToInterface(tbl)
		m, ok := out.(map[string]any)
		if !ok {
			t.Fatalf("out type %T", out)
		}
		self, ok := m["self"].(map[string]any)
		if !ok {
			t.Fatalf("unexpected self: %v", m["self"])
		}
		if self["__lua_cycle"] != "table reference cycle" {
			t.Fatalf("cycle not detected: %v", self)
		}
	}()
}

func TestLuaDeepNestingCapped(t *testing.T) {
	L := lua.NewState()
	defer L.Close()
	root := L.CreateTable(0, 1)
	cur := root
	for i := 0; i < 300; i++ {
		next := L.CreateTable(0, 1)
		cur.RawSetInt(1, next)
		cur = next
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("deep nesting panicked: %v", r)
			}
		}()
		_ = luaToInterface(root)
	}()
}

func TestPluginInfiniteLoopDoesNotWedgeHost(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "spin.lua", `
tool{name="spin", description="d", handler=function(a) while true do end end}
hook{on="before_tool", name="*", handler=function(ev) while true do end end}
`)
	host, _, err := Load([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately no host.Close(): the leaked goroutine keeps spinning,
	// but the host must stay responsive.
	var handler func(ctx context.Context, raw json.RawMessage) (string, error)
	for _, tool := range host.Tools() {
		if tool.Name == "spin" {
			handler = tool.Handler
		}
	}
	if handler == nil {
		t.Fatal("spin tool not loaded")
	}
	start := time.Now()
	_, err = handler(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("handler err = %v, want timeout", err)
	}
	el := time.Since(start)
	if el < time.Second || el > 6*time.Second {
		t.Fatalf("timeout window wrong: %v", el)
	}
	// All subsequent plugin calls fail fast with disabled.
	_, err = handler(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("second call err = %v, want disabled", err)
	}
	// The tool-path timeout disabled the whole runtime, so the hook path
	// also fails fast afterwards.
	start = time.Now()
	_, err = host.Hooks().BeforeTool("spin", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("hook err = %v, want disabled", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("hook call must fail fast, took %v", time.Since(start))
	}
}

func TestBrokenPluginIsAWarning(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "broken.lua", "this is not lua ((")
	writePlugin(t, dir, "good.lua", `tool{name="ok", description="d", handler=function(a) return "fine" end}`)

	host, warnings, err := Load([]string{dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer host.Close()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "broken.lua") {
		t.Fatalf("expected one warning for the broken file, got %v", warnings)
	}
	if len(host.Names()) != 1 || host.Names()[0] != "ok" {
		t.Fatalf("good plugin should still load: %v", host.Names())
	}
}

func TestPluginRuntimeErrorIsReturned(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "boom.lua", `tool{name="boom", description="d", handler=function(a) error("kaboom") end}`)
	host, _, err := Load([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	env := &tools.Env{Budget: 500}
	reg := tools.New(env)
	for _, tool := range host.Tools() {
		_ = reg.Add(tool)
	}
	_, err = reg.Call(context.Background(), "boom", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("expected the lua error to surface, got %v", err)
	}
}

func TestDangerousGlobalsAreNotAvailable(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "probe.lua", `
local missing = {}
for _, name in ipairs({"os","io","require","dofile","load","loadfile","debug","package"}) do
  if _G[name] ~= nil then table.insert(missing, name) end
end
tool{
  name = "probe",
  description = "report unsafe globals",
  handler = function(a) return table.concat(missing, ",") end,
}`)
	host, warnings, err := Load([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if len(warnings) > 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	env := &tools.Env{Budget: 500}
	reg := tools.New(env)
	for _, tool := range host.Tools() {
		_ = reg.Add(tool)
	}
	out, err := reg.Call(context.Background(), "probe", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("unsafe globals are reachable: %q", out)
	}
}
