// Package plugin loads Lua-defined tools into Creator's registry.
//
// A plugin file declares one or more tools:
//
//	tool{
//	  name = "slugify",
//	  description = "Turn a title into a URL slug",
//	  schema = {
//	    type = "object",
//	    properties = { text = { type = "string" } },
//	    required = { "text" },
//	  },
//	  handler = function(args)
//	    return (string.gsub(string.lower(args.text), "%s+", "-"))
//	  end,
//	}
//
// Plugins run in a restricted interpreter: base, string, table and math are
// available; os, io, package and debug are not. Tool arguments and string
// results are the interface, which keeps plugins deterministic and keeps all
// filesystem and shell effects inside the policy engine.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"

	"github.com/DERYK-RYLOY/creator/internal/tools"
)

// Host owns a Lua state and the tools defined in it.
type Host struct {
	mu    sync.Mutex
	L     *lua.LState
	defs  []*definition
	hooks *HookSet
	files []string

	// disabled turns true after a plugin handler hangs past the deadline or
	// panics. Every subsequent lua call then fails fast instead of either
	// wedging the host or crashing the process. The leaked goroutine from
	// the defective call is allowed to spin out harmlessly.
	disabled bool
}

type definition struct {
	name        string
	description string
	schema      json.RawMessage
	fn          *lua.LFunction
	file        string
}

// pluginCallTimeout bounds every plugin-handler call. A hang from this
// point on is fatal to the plugin runtime for this process — never wedged.
const pluginCallTimeout = 2 * time.Second

var (
	// errPluginCallTimeout marks a plugin handler that exceeded the call
	// deadline. The runtime is then disabled for the session.
	errPluginCallTimeout = errors.New("plugin handler timed out")
	// errPluginCallPanicked marks a plugin-side panic converted to an error.
	errPluginCallPanicked = errors.New("plugin handler panicked")
	// errPluginRuntimeDisabled marks every call after a timeout or panic.
	errPluginRuntimeDisabled = errors.New("plugin runtime is disabled after a timeout or panic")
)

// withDeadline runs fn under a hard deadline. A hang or panic disables the
// runtime permanently — the leaked goroutine is allowed to spin, it can no
// longer touch the host or wedge other subsystems.
func (h *Host) withDeadline(fn func() error) error {
	if h.disabled {
		return errPluginRuntimeDisabled
	}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				h.disabled = true
				select {
				case done <- fmt.Errorf("%w: %v", errPluginCallPanicked, r):
				default:
				}
			}
		}()
		done <- fn()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(pluginCallTimeout):
		h.disabled = true
		return errPluginCallTimeout
	}
}

// Load reads every *.lua file in dirs (in order) and returns a host. Problems
// with individual files are returned as warnings rather than errors so one bad
// plugin cannot make the CLI unusable.
func Load(dirs []string) (*Host, []string, error) {
	h := &Host{L: lua.NewState(lua.Options{SkipOpenLibs: true})}
	h.hooks = newHookSet(h)
	var warnings []string

	openSafeLibs(h.L)
	h.L.SetGlobal("tool", h.L.NewFunction(h.luaTool))
	h.L.SetGlobal("hook", h.L.NewFunction(h.luaHook))
	h.L.SetGlobal("log", h.L.NewFunction(func(L *lua.LState) int {
		fmt.Fprintln(os.Stderr, "plugin:", L.CheckString(1))
		return 0
	}))

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".lua") {
				continue
			}
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			path := filepath.Join(dir, name)
			h.L.SetGlobal("__FILE__", lua.LString(path))
			if err := h.L.DoFile(path); err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			h.files = append(h.files, path)
		}
	}
	return h, warnings, nil
}

func openSafeLibs(L *lua.LState) {
	for _, pair := range []struct {
		name string
		fn   lua.LGFunction
	}{
		{lua.LoadLibName, lua.OpenPackage},
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
		{lua.MathLibName, lua.OpenMath},
	} {
		L.Push(L.NewFunction(pair.fn))
		L.Push(lua.LString(pair.name))
		L.Call(1, 0)
	}
	// Remove package/require and the file-loading primitives from base.
	for _, name := range []string{"require", "dofile", "loadfile", "load", "loadstring", "collectgarbage", "package", "module", "_printregs", "newproxy", "setfenv", "getfenv"} {
		L.SetGlobal(name, lua.LNil)
	}
	// gopher-lua's print() writes to the process stdout; on the MCP path
	// that corrupts the JSON-RPC frame stream. Route it to stderr with a
	// size cap, mirroring the host's log sink.
	L.SetGlobal("print", L.NewFunction(func(L *lua.LState) int {
		top := L.GetTop()
		parts := make([]string, 0, top)
		for i := 1; i <= top; i++ {
			s := L.ToStringMeta(L.Get(i)).String()
			if len(s) > 4096 {
				s = s[:4096] + "...(truncated)"
			}
			parts = append(parts, s)
		}
		fmt.Fprintln(os.Stderr, "plugin:", strings.Join(parts, "\t"))
		return 0
	}))
}

// Close releases the interpreter.
func (h *Host) Close() {
	if h != nil && h.L != nil {
		h.L.Close()
	}
}

// Files lists the plugin files that loaded successfully.
func (h *Host) Files() []string { return append([]string{}, h.files...) }

// Names lists the tools the plugins define.
func (h *Host) Names() []string {
	out := make([]string, 0, len(h.defs))
	for _, d := range h.defs {
		out = append(out, d.name)
	}
	return out
}

// Tools converts plugin definitions into registry tools.
func (h *Host) Tools() []tools.Tool {
	out := make([]tools.Tool, 0, len(h.defs))
	for _, d := range h.defs {
		def := d
		schema := def.schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, tools.Tool{
			Name:        def.name,
			Description: def.description + " [lua: " + filepath.Base(def.file) + "]",
			Schema:      schema,
			Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
				return h.call(def, raw)
			},
		})
	}
	return out
}

// fieldString reads a string-ish field; a missing value is an empty string,
// never the literal "nil" (which LNil.String() happily produces).
func fieldString(L *lua.LState, tbl *lua.LTable, key string) string {
	v := L.GetField(tbl, key)
	if v == lua.LNil {
		return ""
	}
	return v.String()
}

// luaTool is the `tool{...}` global.
func (h *Host) luaTool(L *lua.LState) int {
	tbl := L.CheckTable(1)
	name := strings.TrimSpace(fieldString(L, tbl, "name"))
	if name == "" || name == "nil" {
		L.RaiseError("tool requires a name")
		return 0
	}
	fn, ok := L.GetField(tbl, "handler").(*lua.LFunction)
	if !ok {
		L.RaiseError("tool %q requires a handler function", name)
		return 0
	}
	desc := fieldString(L, tbl, "description")
	var schema json.RawMessage
	if schemaVal := L.GetField(tbl, "schema"); schemaVal != lua.LNil {
		encoded, err := json.Marshal(luaToInterface(schemaVal))
		if err != nil {
			L.RaiseError("tool %q has an invalid schema: %v", name, err)
			return 0
		}
		schema = encoded
	}
	file := L.GetGlobal("__FILE__").String()
	h.defs = append(h.defs, &definition{
		name:        name,
		description: desc,
		schema:      schema,
		fn:          fn,
		file:        file,
	})
	return 0
}

// knownHookPoints are the interception points a Lua `hook{}` may register.
var knownHookPoints = map[string]bool{
	"before_tool":     true,
	"after_tool":      true,
	"before_delegate": true,
	"after_delegate":  true,
}

// luaHook is the `hook{...}` global.
func (h *Host) luaHook(L *lua.LState) int {
	tbl := L.CheckTable(1)
	on := strings.TrimSpace(fieldString(L, tbl, "on"))
	if !knownHookPoints[on] {
		L.RaiseError("hook requires on = one of before_tool, after_tool, before_delegate, after_delegate (got %q)", on)
		return 0
	}
	fn, ok := L.GetField(tbl, "handler").(*lua.LFunction)
	if !ok {
		L.RaiseError("hook %q requires a handler function", on)
		return 0
	}
	name := strings.TrimSpace(fieldString(L, tbl, "name"))
	if name == "*" || name == "nil" {
		name = ""
	}
	file := L.GetGlobal("__FILE__").String()
	h.hooks.add(hookPoint{on: on, name: name, file: file, h: h, fn: fn})
	return 0
}

// Hooks returns the hook set the host has; nil-safe.
func (h *Host) Hooks() *HookSet {
	if h == nil {
		return nil
	}
	return h.hooks
}

// HookPoints lists registered hooks in load order, as "file:on[name]" labels.
func (h *Host) HookPoints() []string {
	if h == nil {
		return nil
	}
	h.hooks.mu.Lock()
	defer h.hooks.mu.Unlock()
	out := make([]string, 0, len(h.hooks.hooks))
	for _, p := range h.hooks.hooks {
		out = append(out, p.label())
	}
	return out
}

func (h *Host) call(def *definition, raw json.RawMessage) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	var args any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("plugin %s: invalid arguments: %w", def.name, err)
		}
	}
	L := h.L
	L.Push(def.fn)
	L.Push(jsonToLua(L, args))
	if err := h.withDeadline(func() error { return L.PCall(1, 1, nil) }); err != nil {
		L.SetTop(0)
		if errors.Is(err, errPluginCallTimeout) || errors.Is(err, errPluginCallPanicked) || errors.Is(err, errPluginRuntimeDisabled) {
			L.SetTop(0)
			return "", fmt.Errorf("plugin %s: %w", def.name, err)
		}
		return "", fmt.Errorf("plugin %s: %w", def.name, err)
	}
	ret := L.Get(-1)
	L.Pop(1)
	return luaResult(ret), nil
}

func luaResult(v lua.LValue) string {
	switch t := v.(type) {
	case lua.LString:
		return string(t)
	case lua.LNumber:
		return t.String()
	case lua.LBool:
		if bool(t) {
			return "true"
		}
		return "false"
	case *lua.LTable:
		if arr, ok := tableArray(t); ok {
			parts := make([]string, 0, len(arr))
			for _, item := range arr {
				parts = append(parts, luaResult(item))
			}
			return strings.Join(parts, "\n")
		}
		encoded, err := json.Marshal(luaToInterface(t))
		if err == nil {
			return string(encoded)
		}
		return t.String()
	default:
		return ""
	}
}

func tableArray(t *lua.LTable) ([]lua.LValue, bool) {
	n := t.Len()
	if n == 0 {
		return nil, false
	}
	count := 0
	array := true
	t.ForEach(func(k, _ lua.LValue) {
		count++
		if num, ok := k.(lua.LNumber); !ok || int(num) < 1 || int(num) > n {
			array = false
		}
	})
	if !array || count != n {
		return nil, false
	}
	out := make([]lua.LValue, n)
	for i := 1; i <= n; i++ {
		out[i-1] = t.RawGetInt(i)
	}
	return out, true
}

func luaToInterface(v lua.LValue) any {
	return luaToInterfaceDepth(v, 0, map[*lua.LTable]bool{})
}

func luaToInterfaceDepth(v lua.LValue, depth int, seen map[*lua.LTable]bool) any {
	if depth > 256 {
		return map[string]any{"__lua_error": "table nesting too deep"}
	}
	switch t := v.(type) {
	case *lua.LTable:
		if seen[t] {
			// A self-referential table would recurse forever on Go stack —
			// the Lua VM has no opcode budget, so stop it here.
			return map[string]any{"__lua_cycle": "table reference cycle"}
		}
		seen[t] = true
		defer delete(seen, t)
		if arr, ok := tableArray(t); ok {
			out := make([]any, len(arr))
			for i, item := range arr {
				out[i] = luaToInterfaceDepth(item, depth+1, seen)
			}
			return out
		}
		out := map[string]any{}
		t.ForEach(func(k, val lua.LValue) {
			out[k.String()] = luaToInterfaceDepth(val, depth+1, seen)
		})
		return out
	case lua.LString:
		return string(t)
	case lua.LNumber:
		return float64(t)
	case lua.LBool:
		return bool(t)
	default:
		return nil
	}
}

func jsonToLua(L *lua.LState, v any) lua.LValue {
	switch t := v.(type) {
	case nil:
		return lua.LNil
	case map[string]any:
		tbl := L.NewTable()
		for k, val := range t {
			tbl.RawSetString(k, jsonToLua(L, val))
		}
		return tbl
	case []any:
		tbl := L.NewTable()
		for _, item := range t {
			tbl.Append(jsonToLua(L, item))
		}
		return tbl
	case string:
		return lua.LString(t)
	case float64:
		return lua.LNumber(t)
	case bool:
		return lua.LBool(t)
	case json.Number:
		return lua.LString(t.String())
	default:
		return lua.LNil
	}
}
