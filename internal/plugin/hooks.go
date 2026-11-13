package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	lua "github.com/yuin/gopher-lua"
)

// hookPoint is one plugin-registered hook:
//
//	hook{
//	  on = "before_tool",        -- after_tool | before_delegate | after_delegate
//	  name = "shell",             -- optional tool-name filter (before/after_tool)
//	  handler = function(ev) ... return { deny = true, reason = "..." } end,
//	}
//
// Handlers run in load order at their point. A before-hook's returned table
// may rewrite args (before_tool) or the request fields (before_delegate), or
// set deny = true. Errors on before-points deny the action (the model receives
// the error text); errors on after-points are warnings.
type hookPoint struct {
	on   string
	name string
	file string
	h    *Host
	fn   *lua.LFunction
}

func (p hookPoint) label() string {
	return fmt.Sprintf("%s:%s[%s]", filepath.Base(p.file), p.on, orElse(p.name, "*"))
}

// HookSet is the composition of every plugin hook. It implements
// tools.Hooks; Creator consults it from Registry.Call (tool calls: agent, MCP
// and CLI) and from the delegate manager (spawns and reports).
type HookSet struct {
	mu    sync.Mutex
	host  *Host
	hooks []hookPoint
}

func newHookSet(h *Host) *HookSet { return &HookSet{host: h} }

func (hs *HookSet) add(p hookPoint) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.hooks = append(hs.hooks, p)
}

func orElse(v, d string) string {
	if v != "" {
		return v
	}
	return d
}

// beforeTool implements tools.Hooks.BeforeTool: chain every matching
// before_tool hook in order; a deny or handler error blocks the call.
func (hs *HookSet) BeforeTool(name string, args json.RawMessage) (json.RawMessage, error) {
	hs.mu.Lock()
	points := make([]hookPoint, len(hs.hooks))
	copy(points, hs.hooks)
	hs.mu.Unlock()

	current := args
	for _, p := range points {
		if p.on != "before_tool" || (p.name != "" && p.name != name) {
			continue
		}
		ev := map[string]any{"tool": name}
		if current != nil {
			var v any
			if err := json.Unmarshal(current, &v); err == nil {
				ev["args"] = v
			}
		}
		out, err := p.call(hs.host, ev)
		if err != nil {
			return nil, fmt.Errorf("denied by plugin hook %s: %v", p.label(), err)
		}
		if out != nil {
			if b, ok := out.(map[string]any); ok {
				if deny, _ := b["deny"].(bool); deny {
					reason, _ := b["reason"].(string)
					if reason == "" {
						reason = "denied by plugin hook " + p.label()
					}
					return nil, fmt.Errorf("%s", reason)
				}
				if raw, ok := b["args"]; ok {
					encoded, err := json.Marshal(raw)
					if err != nil {
						return nil, fmt.Errorf("plugin hook %s: invalid replacement args: %w", p.label(), err)
					}
					current = encoded
				}
			}
		}
	}
	return current, nil
}

// afterTool implements tools.Hooks.AfterTool: rewrites may chain; errors warn.
func (hs *HookSet) AfterTool(name, result string, err error) string {
	hs.mu.Lock()
	points := make([]hookPoint, len(hs.hooks))
	copy(points, hs.hooks)
	hs.mu.Unlock()

	for _, p := range points {
		if p.on != "after_tool" || (p.name != "" && p.name != name) {
			continue
		}
		ev := map[string]any{"tool": name, "result": result}
		if err != nil {
			ev["error"] = err.Error()
		}
		out, herr := p.call(hs.host, ev)
		if herr != nil {
			hs.warnf("%s errored (%v); keeping original result", p.label(), herr)
			continue
		}
		if s := stringResult(out); s != "" {
			result = s
		}
	}
	return result
}

// BeforeDelegate implements tools.Hooks.BeforeDelegate: a deny or error stops
// the spawn; returned keys merge over the request.
func (hs *HookSet) BeforeDelegate(opts map[string]any) (map[string]any, error) {
	hs.mu.Lock()
	points := make([]hookPoint, len(hs.hooks))
	copy(points, hs.hooks)
	hs.mu.Unlock()

	for _, p := range points {
		if p.on != "before_delegate" {
			continue
		}
		out, err := p.call(hs.host, opts)
		if err != nil {
			return nil, fmt.Errorf("refused by plugin hook %s: %v", p.label(), err)
		}
		if m, ok := out.(map[string]any); ok {
			if deny, _ := m["deny"].(bool); deny {
				reason, _ := m["reason"].(string)
				if reason == "" {
					reason = "refused by plugin hook " + p.label()
				}
				return nil, fmt.Errorf("%s", reason)
			}
			delete(m, "deny")
			delete(m, "reason")
			if len(m) > 0 {
				// Merge over the request (as documented): a hook that returns
				// one key must not hide the rest from later hooks in the chain.
				for k, v := range m {
					opts[k] = v
				}
			}
		}
	}
	return opts, nil
}

// AfterDelegate implements tools.Hooks.AfterDelegate: the summary handed back
// to a delegator can be trimmed or enriched by plugins.
func (hs *HookSet) AfterDelegate(task, summary string) string {
	hs.mu.Lock()
	points := make([]hookPoint, len(hs.hooks))
	copy(points, hs.hooks)
	hs.mu.Unlock()

	for _, p := range points {
		if p.on != "after_delegate" {
			continue
		}
		ev := map[string]any{"task": task, "report": summary}
		out, herr := p.call(hs.host, ev)
		if herr != nil {
			hs.warnf("%s errored (%v); keeping report", p.label(), herr)
			continue
		}
		if s := stringResult(out); s != "" {
			summary = s
		}
	}
	return summary
}

// call runs one hook handler with an event table and returns its single
// Lua result converted to a Go value.
func (p hookPoint) call(h *Host, ev map[string]any) (any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	L := h.L
	tbl := L.NewTable()
	for k, v := range ev {
		tbl.RawSetString(k, jsonToLua(L, v))
	}
	L.Push(p.fn)
	L.Push(tbl)
	if err := h.withDeadline(func() error { return L.PCall(1, 1, nil) }); err != nil {
		L.SetTop(0)
		return nil, fmt.Errorf("hook %s: %w", p.label(), err)
	}
	ret := L.Get(-1)
	L.Pop(1)
	if ret == lua.LNil {
		return nil, nil
	}
	switch t := ret.(type) {
	case lua.LString:
		return map[string]any{"result": string(t)}, nil
	case *lua.LTable:
		return luaToInterface(t), nil
	default:
		return map[string]any{"result": ret.String()}, nil
	}
}

// stringResult extracts a plain replacement text from a hook return value.
func stringResult(v any) string {
	switch t := v.(type) {
	case map[string]any:
		if s, ok := t["result"].(string); ok {
			return s
		}
	case string:
		return t
	}
	return ""
}

func (hs *HookSet) warnf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "plugin hook: "+format+"\n", a...)
}
