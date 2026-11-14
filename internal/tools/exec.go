package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/DERYK-RYLOY/creator/internal/reduce"
	"github.com/DERYK-RYLOY/creator/internal/runner"
)

func shellTool() Tool {
	return Tool{
		Name: "shell",
		Description: "Run a shell command in the workspace. The full output is captured and returned as a " +
			"token-budgeted digest with a handle, so huge output never floods the conversation; page it with out(). " +
			"Set background=true for servers, watchers and anything long-running, then poll with jobs. " +
			"Commands are classified by the policy engine before they run.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "Shell command line."},
    "cwd": {"type": "string", "description": "Working directory inside the workspace (default: workspace root)."},
    "timeout_sec": {"type": "integer", "description": "Kill the command after this many seconds (default 120; background=true defaults to no timeout)."},
    "background": {"type": "boolean", "description": "Return immediately with a job id instead of waiting."}
  },
  "required": ["command"]
}`),
		Handler: handleShell,
	}
}

type shellArgs struct {
	Command    string `json:"command"`
	Cwd        string `json:"cwd"`
	TimeoutSec int    `json:"timeout_sec"`
	Background bool   `json:"background"`
	PTY        bool   `json:"pty"`
}

func handleShell(ctx context.Context, raw json.RawMessage) (string, error) {
	var a shellArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	if strings.TrimSpace(a.Command) == "" {
		return "", errors.New("command is required")
	}
	dir := env.Workspace
	if a.Cwd != "" {
		resolved, err := env.resolve(a.Cwd, "dir")
		if err != nil {
			return "", err
		}
		dir = resolved
	}
	if env.Policy != nil {
		if _, err := env.Policy.Authorize(ctx, a.Command); err != nil {
			return "", err
		}
	}
	timeout := time.Duration(a.TimeoutSec) * time.Second
	if a.TimeoutSec <= 0 {
		if a.Background {
			timeout = 0 // no kill for backgrounded servers/watchers unless the caller asks
		} else {
			timeout = 120 * time.Second
		}
	}
	if timeout > time.Hour && !a.Background {
		timeout = time.Hour
	}

	job, err := env.Jobs.Start(ctx, runner.Spec{
		Command: a.Command,
		Dir:     dir,
		Timeout: timeout,
		Shell:   env.Shell,
		PTY:     a.PTY,
	})
	if err != nil {
		return "", err
	}
	progressCtx, stopProgress := context.WithCancel(context.Background())
	defer stopProgress()
	if env.OnProgress != nil && !a.Background {
		go streamShellProgress(progressCtx, env, job)
	}

	if a.Background {
		timeoutText := timeout.String()
		if timeout == 0 {
			timeoutText = "no timeout"
		}
		note := ""
		if timeout == 0 {
			note = " (not killed automatically; stop it with the process tools if it should exit)"
		}
		return fmt.Sprintf(
			"job %s started in the background (timeout %s)%s\ncommand: %s\n"+
				"Poll with jobs(id=%q); read output with out(handle=%q, mode=\"tail\").",
			job.ID, timeoutText, note, a.Command, job.ID, job.ID), nil
	}

	_ = job.Wait(ctx)
	snap := job.Snapshot()
	output, readErr := job.Output()
	if readErr != nil {
		return "", readErr
	}
	return renderJob(env, snap, output), nil
}

// streamShellProgress emits live shell output snapshots while a foreground job
// runs. It keeps the signal intentionally small: identity, elapsed time, bytes
// captured and a short tail, never the whole artifact on every tick.
func streamShellProgress(ctx context.Context, env *Env, job *runner.Job) {
	if env.OnProgress == nil {
		return
	}
	emit := func() {
		snap := job.Snapshot()
		meta := map[string]string{
			"job":     snap.ID,
			"bytes":   fmt.Sprintf("%d", snap.Bytes),
			"elapsed": snap.Duration.Round(time.Second).String(),
			"status":  string(snap.Status),
		}
		text := ""
		if out, err := job.Tail(3000); err == nil {
			text = out
		}
		env.OnProgress("shell", "running", text, meta)
	}
	emit()
	ticker := time.NewTicker(350 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap := job.Snapshot()
			if snap.Status != "running" && snap.Status != "" {
				return
			}
			emit()
		}
	}
}

func renderJob(env *Env, snap runner.Snapshot, output string) string {
	head := fmt.Sprintf("job %s · %s · exit %d · %s · %s",
		snap.ID, snap.Status, snap.ExitCode, snap.Duration.Round(time.Millisecond), humanBytes(snap.Bytes))
	if snap.Truncated {
		head += " (output truncated at 64MB)"
	}
	head += "\n$ " + stripControlSeq(snap.Command)
	if snap.ExitCode == 0 && env.OnCommandExit != nil {
		// The verify oracle: a command that exited 0 may close board tasks
		// that were waiting on it. Idempotent — done tasks are skipped.
		env.OnCommandExit(snap.Command, snap.ExitCode, snap.ID, snap.Dir)
	}
	if strings.TrimSpace(output) == "" {
		return head + "\n(no output)"
	}
	body, _ := env.summarize(snap.ID, stripControlSeq(output), reduce.KindAuto)
	return head + "\n" + body
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func jobsTool() Tool {
	return Tool{
		Name:     "jobs",
		ReadOnly: true,
		Description: "List running and recent jobs, or inspect one job by id. Use this after starting a background command " +
			"to check whether it is still running, what it exited with, and what it printed.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "Job id to inspect. Omit to list jobs."}
  }
}`),
		Handler: handleJobs,
	}
}

type jobsArgs struct {
	ID string `json:"id"`
}

func handleJobs(ctx context.Context, raw json.RawMessage) (string, error) {
	var a jobsArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)

	if a.ID != "" {
		if job, ok := env.Jobs.Get(a.ID); ok {
			output, _ := job.Output()
			return renderJob(env, job.Snapshot(), output), nil
		}
		rec, err := env.Store.LoadJob(a.ID)
		if err != nil {
			return "", fmt.Errorf("no job %q (it may have expired)", a.ID)
		}
		snap := runner.Snapshot{
			ID: rec.ID, Command: rec.Command, Dir: rec.Dir, Shell: rec.Shell,
			Status: runner.Status(rec.Status), Started: rec.Started, Ended: rec.Ended,
			ExitCode: rec.ExitCode, Bytes: rec.Bytes, Truncated: rec.Truncated,
		}
		output, _ := os.ReadFile(env.Store.ArtifactPath(rec.ID))
		return renderJob(env, snap, string(output)), nil
	}

	lines := []string{}
	live := env.Jobs.Running()
	seen := map[string]bool{}
	for _, j := range live {
		seen[j.ID] = true
		lines = append(lines, fmt.Sprintf("%s  running  %s  $ %s", j.ID, j.Duration.Round(time.Millisecond), truncate(stripControlSeq(j.Command), 90)))
	}
	recs := env.Store.ListJobs(20)
	sort.Slice(recs, func(i, j int) bool { return recs[i].Started.After(recs[j].Started) })
	for _, r := range recs {
		if seen[r.ID] {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s  %-7s  exit %-3d  $ %s", r.ID, r.Status, r.ExitCode, truncate(stripControlSeq(r.Command), 90)))
	}
	if len(lines) == 0 {
		return "no jobs yet", nil
	}
	return clip(env, strings.Join(lines, "\n"), reduce.KindGeneric), nil
}

func outTool() Tool {
	return Tool{
		Name:     "out",
		ReadOnly: true,
		Description: "Page the full output stored behind a handle (from shell, read, grep, glob or jobs). " +
			"mode=range needs start/end, mode=grep needs pattern, mode=tail/head take n. " +
			"This is how you see detail that was collapsed from a digest.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "handle": {"type": "string", "description": "Handle id, e.g. j_01h... or h_01h..."},
    "mode": {"type": "string", "enum": ["head", "tail", "range", "grep", "full"], "description": "Default head."},
    "pattern": {"type": "string", "description": "Regexp for mode=grep."},
    "start": {"type": "integer", "description": "1-based first line for mode=range."},
    "end": {"type": "integer", "description": "1-based last line for mode=range."},
    "n": {"type": "integer", "description": "Line count for head/tail (default 80)."},
    "ignore_case": {"type": "boolean"}
  },
  "required": ["handle"]
}`),
		Handler: handleOut,
	}
}

type outArgs struct {
	Handle     string `json:"handle"`
	Mode       string `json:"mode"`
	Pattern    string `json:"pattern"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
	N          int    `json:"n"`
	IgnoreCase bool   `json:"ignore_case"`
}

func handleOut(ctx context.Context, raw json.RawMessage) (string, error) {
	var a outArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	if a.Handle == "" {
		return "", errors.New("handle is required")
	}
	data, err := env.Store.ReadArtifact(a.Handle)
	if err != nil {
		// Live job that has not been flushed to the store yet? Fall back to it.
		if job, ok := env.Jobs.Get(a.Handle); ok {
			if data, err = job.Output(); err != nil {
				return "", err
			}
		} else if errors.Is(err, os.ErrNotExist) {
			// The store's message already names the handle and how to find one.
			return "", err
		} else {
			return "", fmt.Errorf("unknown handle %q: %w", a.Handle, err)
		}
	}
	page := reduce.Page(data, a.Mode, a.Pattern, a.Start, a.End, a.N, a.IgnoreCase, env.Budget)
	return fmt.Sprintf("handle %s (%d lines total)\n%s", a.Handle, len(splitLines(data)), page), nil
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
