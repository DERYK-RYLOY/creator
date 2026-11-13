package delegate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/creatorcli/creator/internal/provider"
	"github.com/creatorcli/creator/internal/tools"
)

const spawnSchema = `{
  "type": "object",
  "description": "Delegate a self-contained task to an isolated sub-agent. It runs with a fresh context, its own token budget and only the tools it needs; when it finishes you receive a short report plus a handle for its full output — never its raw transcript. Use it to parallelize or to quarantine noisy work.",
  "properties": {
    "task": {
      "type": "string",
      "description": "The complete, self-contained task. Include everything the delegate needs: paths, goals, acceptance criteria. It shares your workspace and policy but starts from zero context."
    },
    "name": {"type": "string", "description": "Short label shown in listings, e.g. 'explorer'"},
    "profile": {"type": "string", "description": "Named role with a curated toolset and mindset: explorer (read-only survey), planner (read-only plan), reviewer (read-only review), editor (implement + verify). Explicit tools/deny still narrow it."},
    "provider": {"type": "string", "description": "Model backend override (e.g. 'mistral', 'ollama'). Omit to inherit."},
    "model": {"type": "string", "description": "Model name override. Omit to inherit."},
    "read_only": {"type": "boolean", "description": "Restrict to read-only tools (read/list/glob/grep/out/jobs and friends)."},
    "tools": {"type": "array", "items": {"type": "string"}, "description": "Allowlist of tool names (empty = full toolset)."},
    "deny": {"type": "array", "items": {"type": "string"}, "description": "Tool names to remove even from the inherited set."},
    "can_spawn": {"type": "boolean", "description": "Allow this delegate to spawn further delegates (depth permitting)."},
    "max_steps": {"type": "integer", "description": "Tool-calling rounds for this delegate (default 24)."},
    "budget": {"type": "integer", "description": "Total token budget for this delegate."},
    "output_tokens": {"type": "integer", "description": "Token budget of the report returned to you (default 300)."},
    "max_tokens": {"type": "integer", "description": "Per-response completion cap."},
    "timeout": {"type": "integer", "description": "Hard wall in seconds (0 = none)."},
    "task_id": {"type": "string", "description": "Board task id this delegate closes on completion (t3). A report ticks the task (verify gate still applies); a failure or cancellation flags it."},
    "wait": {"type": "boolean", "description": "Block until finished and return the report immediately (otherwise returns a handle)."}
  },
  "required": ["task"]
}`

// toolset builds the delegate tools for a registry at a spawn depth. The
// handlers close over the depth so nesting is enforced at spawn time.
func (m *Manager) toolset(depth int, sessionID string) []tools.Tool {
	return []tools.Tool{
		{
			Name:        "delegate",
			Description: "Spawn an isolated sub-agent for one self-contained task. It starts a fresh context, pays its own tokens, and returns only a reduced report plus a handle. Use it to parallelize or to quarantine noisy work.",
			Schema:      json.RawMessage(spawnSchema),
			Handler:     m.spawnTool(depth, sessionID),
		},
		{
			Name:        "delegates",
			Description: "List delegates (newest first) or inspect one. Set wait_seconds to block for their completion. Reports are digests; page full output with out(handle).",
			Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "Inspect a single delegate"},
    "wait_seconds": {"type": "integer", "description": "Wait up to N seconds for the referenced (or all) delegates to finish"}
  }
}`),
			Handler: m.statusTool,
		},
		{
			Name:        "delegate_cancel",
			Description: "Stop a delegate that is queued or running. Finished delegates cannot be cancelled.",
			Schema: json.RawMessage(`{
  "type": "object",
  "properties": {"id": {"type": "string", "description": "Delegate id, e.g. d_abc123"}},
  "required": ["id"]
}`),
			Handler: m.cancelTool,
		},
	}
}

func (m *Manager) spawnTool(depth int, sessionID string) func(ctx context.Context, raw json.RawMessage) (string, error) {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		spec, err := parseSpec(raw)
		if err != nil {
			return "", fmt.Errorf("delegate arguments: %w", err)
		}
		spec.Depth = depth
		rec, err := m.Spawn(ctx, sessionID, spec)
		if err != nil {
			return "", err
		}
		if !spec.Wait {
			return fmt.Sprintf(
				"%s · %s · queued · depth %d · task: %s\nCollect with delegates(id=%s, wait_seconds=N); detail via out(handle=%s).",
				rec.ID, rec.Name, rec.Depth, rec.Task, rec.ID, rec.ID), nil
		}
		waited, err := m.Wait(ctx, rec.ID)
		if err != nil {
			if ctx.Err() != nil {
				return "", err
			}
			waited = m.Get(rec.ID)
		}
		return m.reportOf(waited), nil
	}
}

func (m *Manager) statusTool(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		ID          string `json:"id"`
		WaitSeconds int    `json:"wait_seconds"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("delegates arguments: %w", err)
		}
	}
	if args.WaitSeconds > 0 {
		// Cap the wait to a sane upper bound so a hallucinating model
		// can't park the agent loop for days.
		if args.WaitSeconds > 300 {
			args.WaitSeconds = 300
		}
		var ids []string
		if args.ID != "" {
			ids = []string{args.ID}
		} else {
			for _, r := range m.Active() {
				ids = append(ids, r.ID)
			}
		}
		if len(ids) > 0 {
			cctx, cancel := context.WithTimeout(ctx, time.Duration(args.WaitSeconds)*time.Second)
			m.WaitMany(cctx, ids)
			cancel()
		}
	}
	if args.ID != "" {
		r := m.Get(args.ID)
		if r == nil {
			return "", fmt.Errorf("unknown delegate %q", args.ID)
		}
		return m.withJournalHealth(m.reportOf(r)), nil
	}
	return m.withJournalHealth(m.listResponse()), nil
}

// withJournalHealth appends a visible warning when delegate state could not
// be persisted to the journal — a crash would strand the delegate with no
// trace, so say so explicitly instead of pretending everything is fine.
func (m *Manager) withJournalHealth(body string) string {
	if err := m.JournalHealth(); err != nil {
		return body + "\njournal warning: delegate state was not persisted: " + errText(err)
	}
	return body
}

func (m *Manager) cancelTool(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		ID string `json:"id"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("delegate_cancel arguments: %w", err)
		}
	}
	if args.ID == "" {
		return "", fmt.Errorf("delegate_cancel: id is required")
	}
	if err := m.Cancel(args.ID); err != nil {
		return "", err
	}
	if r := m.Get(args.ID); r != nil {
		return fmt.Sprintf("%s · %s — cancellation requested; poll delegates to confirm", r.ID, r.Status), nil
	}
	return "cancellation requested", nil
}

// ---------------------------------------------------------------------------
// rendering
// ---------------------------------------------------------------------------

// listResponse renders the delegate table, newest first.
func (m *Manager) listResponse() string {
	recs := m.List()
	if len(recs) == 0 {
		return "no delegates spawned in this process (older records: creator delegates)"
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Created.After(recs[j].Created) })
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-42s %-9s %-5s %-5s %-7s %s\n", "delegate", "status", "depth", "steps", "tokens", "name / result line"))
	for i, r := range recs {
		if i == 24 {
			b.WriteString("(24 recent shown)\n")
			break
		}
		active := r.Status == "queued" || r.Status == "running"
		detail := ""
		if !active {
			detail = firstLine(r.Result, 90)
			if detail == "" {
				detail = firstLine(r.Error, 90)
			}
		}
		if detail == "" {
			detail = r.Name
		}
		b.WriteString(fmt.Sprintf("%-42s %-9s %-5d %-5d %-7s %s\n",
			r.ID, r.Status, r.Depth, r.Steps, usageShort(r.Usage), detail))
	}
	return b.String()
}

// reportOf renders one delegate for the delegator: a status line, then — for
// finished delegates — the reduced report it produced.
func (m *Manager) reportOf(r *Record) string {
	if r == nil {
		return "(delegate disappeared)"
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s · %s · %s\n", r.ID, r.Status, usageShort(r.Usage)))
	switch r.Status {
	case "done":
		if r.Result != "" {
			b.WriteString(r.Result)
			b.WriteString("\n")
		} else {
			b.WriteString("(empty report)\n")
		}
		if r.Handle != "" {
			b.WriteString(fmt.Sprintf("[full report: out(handle=%s)]\n", r.Handle))
		}
	default:
		if r.Error != "" {
			b.WriteString(r.Error + "\n")
		} else {
			b.WriteString(r.Status + fmt.Sprintf(" · depth %d · %d steps · task: %s\n", r.Depth, r.Steps, r.Task))
		}
		if r.Result != "" {
			b.WriteString(r.Result + "\n")
		}
	}
	return b.String()
}

func usageShort(u provider.Usage) string {
	total := u.In + u.Out
	if total == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", total)
}

// parseSpec decodes the model's spawn arguments; unknown keys are ignored so a
// model that sends extra guesses does not trip the loop.
func parseSpec(raw json.RawMessage) (Spec, error) {
	if len(raw) == 0 {
		return Spec{}, fmt.Errorf("empty arguments")
	}
	var spec Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return Spec{}, err
	}
	return spec, nil
}
