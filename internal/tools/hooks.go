package tools

import (
	"encoding/json"
)

// Hooks is the interception spine for tool calls and delegations.
//
// It is deliberately an interface: the Lua plugin host implements it, and any
// other front-end could too. Registry.Call consults it for every tool
// invocation (agent, MCP and CLI alike); the delegate manager consults it for
// spawns and final reports.
type Hooks interface {
	// BeforeTool runs before a tool executes. Returning an error denies the
	// call (the error text becomes the tool result). Returning replacement
	// arguments with no error rewrites them in place. args may be nil.
	BeforeTool(name string, args json.RawMessage) (json.RawMessage, error)

	// AfterTool observes or rewrites a finished tool result. Returning an
	// empty string leaves the result untouched. An error carried out of the
	// tool call is preserved — a hook can rewrite the message but can never
	// silently clear the failure from the caller's view.
	AfterTool(name, result string, err error) string

	// BeforeDelegate gates a delegation spawn. opts carries the raw spawn
	// options (task, name, provider, model, output_tokens, max_steps, budget,
	// read_only, can_spawn, tools, deny, timeout). Returning an error denies
	// the spawn; returning a table rewrites only the keys present.
	BeforeDelegate(opts map[string]any) (map[string]any, error)

	// AfterDelegate observes or rewrites the summary a delegator receives
	// about a finished delegate. Returning an empty string leaves it as-is.
	AfterDelegate(task, summary string) string
}
