package board

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/creatorcli/creator/internal/store"
	"github.com/creatorcli/creator/internal/tools"
)

const tasksSchema = `{
  "type": "object",
  "description": "Work the persistent task board. Plan multi-step work as tasks, start one at a time, and close each task with real evidence. A task carrying a verify command only closes when that command exits 0 — the board, not your word, decides whether work is finished.",
  "properties": {
    "op": {
      "type": "string",
      "enum": ["add", "start", "done", "flag", "cancel", "dep", "list", "verify"],
      "description": "The board operation. verify replaces the completion oracle on an existing task; pass an empty string to clear it."
    },
    "id": {"type": "string", "description": "Task id, e.g. t3 (or just 3). Required for start/done/flag/cancel/dep."},
    "title": {"type": "string", "description": "add: imperative task title, e.g. 'run the test suite'."},
    "doing": {"type": "string", "description": "add: present-continuous form shown while running, e.g. 'running the test suite'."},
    "detail": {"type": "string", "description": "add: crisp acceptance criteria; what exactly counts as done."},
    "parent": {"type": "string", "description": "add: parent task id for hierarchy."},
    "priority": {"type": "string", "enum": ["high", "med", "low"], "description": "add: priority (display ordering)."},
    "depends_on": {"type": "array", "items": {"type": "string"}, "description": "add: task ids this task waits for."},
    "verify": {"type": "string", "description": "command whose exit code decides this task is done — the completion oracle. Use a real runnable command such as 'go test ./...', 'rg -q pattern file', 'go build ./...', not prose. If no command check exists, omit verify and provide evidence."},
    "evidence": {"type": "string", "description": "done: the proof you observed (command and exit code, file path, diff). Required when the task has no verify command."},
    "force": {"type": "boolean", "description": "start: allow a second task in_progress on a board that already has one."},
    "note": {"type": "string", "description": "flag: what looks off, wrong or unfinished."},
    "add": {"type": "array", "items": {"type": "string"}, "description": "dep: dependency ids to add."},
    "remove": {"type": "array", "items": {"type": "string"}, "description": "dep: dependency ids to remove."}
  },
  "required": ["op"]
}`

// toolset returns the tasks tool for a registry.
func (b *Board) toolset(sessionID string) []tools.Tool {
	task := tools.Tool{
		Name:        "tasks",
		Description: "The persistent to-do board for real, multi-step project work. Plan the steps yourself, start one at a time, and close each task with real evidence. A task carrying a verify command only closes when that command exits 0 — the board, not your word, decides whether work is finished. If a verify command was written wrong, use op=verify to replace it with a runnable command, or op=verify with an empty string to clear it. Do not create fake tasks for trivial requests.",
		Schema:      json.RawMessage(tasksSchema),
		Handler:     b.taskHandler,
	}
	return []tools.Tool{task}
}

// taskHandler dispatches the op.
func (b *Board) taskHandler(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Op        string   `json:"op"`
		ID        string   `json:"id"`
		Title     string   `json:"title"`
		Doing     string   `json:"doing"`
		Detail    string   `json:"detail"`
		Parent    string   `json:"parent"`
		Priority  string   `json:"priority"`
		DependsOn []string `json:"depends_on"`
		Verify    string   `json:"verify"`
		Evidence  string   `json:"evidence"`
		Force     bool     `json:"force"`
		Note      string   `json:"note"`
		Add       []string `json:"add"`
		Remove    []string `json:"remove"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("tasks arguments: %w", err)
		}
	}
	switch args.Op {
	case "":
		return "", fmt.Errorf("tasks: op is required (available: add, start, done, flag, cancel, dep, list)")

	case "add":
		rec, render, err := b.Add(AddArgs{
			Title:     args.Title,
			Doing:     args.Doing,
			Detail:    args.Detail,
			Parent:    args.Parent,
			Priority:  args.Priority,
			DependsOn: args.DependsOn,
			Verify:    args.Verify,
		})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("added %s\n%s", rec.ID, render), nil

	case "verify":
		rec, render, err := b.SetVerify(ctx, args.ID, args.Verify)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("verify updated %s\n%s", rec.ID, render), nil

	case "dep":
		rec, render, err := b.Dep(args.ID, args.Add, args.Remove)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("deps updated %s\n%s", rec.ID, render), nil

	case "start":
		rec, render, err := b.Start(args.ID, args.Force)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s · %s · %s", rec.ID, rec.Status, render), nil

	case "done":
		rec, render, err := b.Done(ctx, DoneArgs{ID: args.ID, Evidence: args.Evidence})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s closed · %s", rec.ID, render), nil

	case "flag":
		rec, render, err := b.Flag(args.ID, args.Note)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("flagged %s\n%s", rec.ID, render), nil

	case "cancel":
		rec, render, err := b.Cancel(args.ID)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("cancelled %s\n%s", rec.ID, render), nil

	case "list":
		recs, err := b.List()
		if err != nil {
			return "", err
		}
		board := RenderBoard(recs, b.now())
		if idv := strings.TrimSpace(args.ID); idv != "" {
			var t store.TaskRecord
			if err := b.withWrite(func(st *store.Store) error {
				loaded, err := st.LoadTask(idv)
				if err != nil {
					return err
				}
				t = loaded
				return nil
			}); err != nil {
				return board, err
			}
			board += "\n\n" + renderTask(t, b.depStatuses(t.DependsOn), b.now())
		}
		return board, nil

	default:
		return "", fmt.Errorf("tasks: unknown op %q (available: add, start, done, flag, cancel, dep, list)", args.Op)
	}
}
