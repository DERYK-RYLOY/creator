// Package board implements Creator's task board: a persistent, verification-
// gated task system a model plans with and ticks off — designed so that
// "done" means something.
//
// Four mechanisms make completion mean something:
//
//   - The completion oracle. A task can carry a verify command; it cannot
//     close until that command has run and exited 0, and the job id of that
//     run (its artifact) is attached as a machine receipt. The board, not the
//     model's word, decides whether work is finished.
//   - The dependency graph. Tasks declare depends_on; a dependent task cannot
//     start or finish before its dependencies do, and every render shows what
//     is blocked and by what. Dependency edits that would create a cycle are
//     refused.
//   - Single focus. At most one task is in_progress per board (overridable per
//     start with force); the rules trailer reminds the agent on every tool
//     result.
//   - Imperfection signals. Flags ("OFF: ...") attach to tasks without
//     reopening them, completing without starting drops a note, long-running
//     tasks are marked stale, and if a board closes three or more tasks with
//     no verification step anywhere in sight, the board nudges before the
//     agent gets to summarise.
package board

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DERYK-RYLOY/creator/internal/policy"
	"github.com/DERYK-RYLOY/creator/internal/runner"
	"github.com/DERYK-RYLOY/creator/internal/store"
	"github.com/DERYK-RYLOY/creator/internal/tools"
)

// Task statuses. "blocked" is derived at render time, never stored.
const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
	StatusCancelled  = "cancelled"
	blockedSignal    = "blocked"
)

// staleAfter marks an in_progress task as stale once it has run this long
// without being closed: either the work is genuinely long, or the agent
// wandered off the plan. Either way it gets surfaced.
const staleAfter = 15 * time.Minute

// verifPattern recognises check-shaped titles for the closing nudge.
var verifPattern = regexp.MustCompile(`(?i)verify|check|test|lint|sanity`)

const rulesTrailer = "Rules: exactly one task in_progress at a time; a task closes only through done (evidence attached; verify command exited 0 if set); flag(…) the moment something looks off instead of ignoring it."

var nudgeLines = []string{
	"you closed 3+ tasks and not one of them carries a verify command or a check-shaped title.",
	"Before writing the final summary: add a task like \"run the test suite\" (verify: the actual command), start it, and close it. A report without a last check is worth doubting.",
}

// Options configure a board.
type Options struct {
	Root   string // workspace root verify commands run in
	Jobs   *runner.Manager
	Policy *policy.Engine
	Shell  string
	// VerifyTimeout bounds the oracle run (0 = 10 minutes).
	VerifyTimeout time.Duration
	// OnEvent receives task events in the shared journal shape so a live
	// viewer can subscribe in-process alongside the persistent feed.
	OnEvent func(store.DelegateEvent)
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Board owns the task board of one Creator process.
type Board struct {
	st      *store.Store
	opts    Options
	mu      sync.Mutex // serialises read-modify-write cycles
	session string
}

// New builds a board over the store.
func New(st *store.Store, opts Options) *Board {
	if opts.VerifyTimeout <= 0 {
		opts.VerifyTimeout = 10 * time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Board{st: st, opts: opts}
}

// Attach registers the tasks tool into a registry (root conversation or MCP
// server; delegates inherit it like any other tool). sessionID scopes every new
// task to that session so a resumed conversation sees its own checklist.
func (b *Board) Attach(reg *tools.Registry, sessionID string) error {
	if reg == nil {
		return errors.New("board: no registry")
	}
	b.mu.Lock()
	b.session = sessionID
	b.mu.Unlock()
	for _, t := range b.toolset(sessionID) {
		if err := reg.Add(t); err != nil {
			return fmt.Errorf("board tools: %w", err)
		}
	}
	return nil
}

// List returns every task in board order (creation order), scoped to the
// attached session when one is set.
func (b *Board) List() ([]store.TaskRecord, error) {
	if b.st == nil {
		return nil, errors.New("board: no store")
	}
	if b.session == "" {
		return b.st.ListTasks(0), nil
	}
	filtered := make([]store.TaskRecord, 0)
	for _, rec := range b.st.ListTasks(0) {
		if rec.Session == "" || rec.Session == b.session {
			filtered = append(filtered, rec)
		}
	}
	return filtered, nil
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

// AddArgs is one new task.
type AddArgs struct {
	Title     string
	Doing     string
	Detail    string
	Parent    string
	Priority  string
	DependsOn []string
	Verify    string
}

// Add creates a task. References must exist, dependency additions are checked
// for cycles, and an oracle command is validated up front.
func (b *Board) Add(args AddArgs) (store.TaskRecord, string, error) {
	var out store.TaskRecord
	args.Title = strings.TrimSpace(args.Title)
	if args.Title == "" {
		return out, "", errors.New("tasks add: title is required")
	}
	switch p := strings.ToLower(strings.TrimSpace(args.Priority)); p {
	case "", "high", "med", "low":
		args.Priority = p
	default:
		return out, "", fmt.Errorf("tasks add: priority must be high, med or low (got %q)", args.Priority)
	}
	if v := strings.TrimSpace(args.Verify); v != "" {
		if _, err := b.authorizeOracle(context.Background(), v); err != nil {
			return out, "", fmt.Errorf("tasks add: verify command rejected: %v", err)
		}
	}
	t := store.TaskRecord{
		Title:    args.Title,
		Doing:    strings.TrimSpace(args.Doing),
		Detail:   strings.TrimSpace(args.Detail),
		Status:   StatusPending,
		Priority: args.Priority,
		Session:  b.session,
	}
	if v := strings.TrimSpace(args.Verify); v != "" {
		t.VerifyMode = "command"
		t.Verify = v
	}

	var saved store.TaskRecord
	err := b.withWrite(func(st *store.Store) error {
		if p := strings.TrimSpace(args.Parent); p != "" {
			parent, err := st.LoadTask(p)
			if err != nil {
				return fmt.Errorf("tasks add: parent: %v", err)
			}
			t.Parent = parent.ID
		}
		if err := applyDeps(st, &t, args.DependsOn, nil, b.now); err != nil {
			return err
		}
		sav, err := st.SaveTask(t)
		if err != nil {
			return err
		}
		t = sav
		return nil
	})
	if err != nil {
		return store.TaskRecord{}, "", err
	}
	saved = t
	b.emit(saved, "add", saved.Title)
	return saved, renderTask(saved, b.depStatuses(saved.DependsOn), b.now()) + "\n" + rulesTrailer, nil
}

// Start moves a task to in_progress with pool hygiene:
//   - unfinished dependencies refuse the start (they are listed),
//   - at most one task may be in_progress unless force is set; a forced
//     parallel start records a note so the drift stays visible.
func (b *Board) Start(idv string, force bool) (store.TaskRecord, string, error) {
	var saved store.TaskRecord
	var extra string
	err := b.withWrite(func(st *store.Store) error {
		t, err := st.LoadTask(idv)
		if err != nil {
			return err
		}
		switch t.Status {
		case StatusInProgress:
			return fmt.Errorf("task %s is already in progress — work on it or stop it first", t.ID)
		case StatusDone, StatusCancelled:
			return fmt.Errorf("task %s is %s and cannot restart", t.ID, t.Status)
		}
		if blockers := blockersOf(st, t); len(blockers) > 0 {
			return fmt.Errorf(
				"task %s is blocked by unfinished dependencies: %s — finish those first (or cancel them)",
				t.ID, strings.Join(blockers, ", "))
		}
		if other := activeOf(st, t.ID); other != nil && !force {
			return fmt.Errorf(
				"task %s (%s) is already in progress — one task at a time keeps the plan honest; pass force:true to override (both stay visible)",
				other.ID, other.Title)
		}
		if other := activeOf(st, t.ID); other != nil {
			t.Notes = append(t.Notes, store.TaskNote{
				Text: fmt.Sprintf("started in parallel with %s (%s) via force", other.ID, other.Title),
				At:   b.now(),
			})
			extra = fmt.Sprintf("OFF: started in parallel with %s — recorded as a note on %s", other.ID, t.ID)
		}
		t.Status = StatusInProgress
		t.Started = b.now()
		sav, err := st.SaveTask(t)
		if err != nil {
			return err
		}
		saved = sav
		return nil
	})
	if err != nil {
		return store.TaskRecord{}, "", err
	}
	b.emit(saved, "start", saved.Title)
	render := renderTask(saved, b.depStatuses(saved.DependsOn), b.now())
	if extra != "" {
		render = extra + "\n" + render
	}
	return saved, render + "\n" + rulesTrailer, nil
}

// DoneArgs closes a task.
type DoneArgs struct {
	ID       string
	Evidence string
	// EvidenceKind overrides the default evidence kind ("claim") written to
	// the task receipt; the delegate close path passes "delegate".
	EvidenceKind string
}

// Done closes a task with a receipt. When the task carries a verify command,
// the oracle runs it synchronously and requires exit 0 before accepting the
// close; the run's job id is stored as the receipt. Without an oracle a
// non-empty evidence claim is mandatory. Closing a task that was never
// started is allowed but drops a note (visible drift). Dependencies must
// already be done. Nudge fires when the board reaches a suspicious shape.
func (b *Board) Done(ctx context.Context, args DoneArgs) (store.TaskRecord, string, error) {
	var saved store.TaskRecord
	var nudged bool
	var receipt string

	// Phase 1 — pre-checks under the lock; the task never leaves the store in
	// an oracle's hands while the mutex is held or manually wobbled.
	var t0 store.TaskRecord
	now := b.now()
	preErr := b.withWrite(func(st *store.Store) error {
		t, err := st.LoadTask(args.ID)
		if err != nil {
			return err
		}
		switch t.Status {
		case StatusDone:
			return fmt.Errorf("task %s is already done", t.ID)
		case StatusCancelled:
			return fmt.Errorf("task %s is cancelled and cannot close", t.ID)
		}
		if blockers := blockersOf(st, t); len(blockers) > 0 {
			return fmt.Errorf(
				"task %s cannot close before its dependencies: %s", t.ID, strings.Join(blockers, ", "))
		}
		t0 = t
		return nil
	})
	if preErr != nil {
		return store.TaskRecord{}, "", preErr
	}

	// Between — no lock held. The oracle is an arbitrary-command exec; a
	// panic here cannot corrupt the board mutex (it was never taken).
	var ev store.TaskEvidence
	if t0.VerifyMode == "command" {
		var line string
		var runErr error
		ev, line, runErr = b.runOracle(ctx, t0)
		receipt = line
		if runErr != nil {
			// Best effort: note the failure on the task so it doesn't look
			// silently stuck.
			_ = b.withWrite(func(st *store.Store) error {
				t, err := st.LoadTask(args.ID)
				if err != nil {
					return err
				}
				switch t.Status {
				case StatusDone, StatusCancelled:
					return nil
				}
				_, _ = st.SaveTask(noteOn(t, "verify refused/failed — task stays open: "+errText(runErr), now))
				return nil
			})
			return store.TaskRecord{}, "", fmt.Errorf("task %s stays open: %v", args.ID, runErr)
		}
	}

	// Phase 2 — re-acquire, reload, re-check, apply.
	err := b.withWrite(func(st *store.Store) error {
		t, err := st.LoadTask(args.ID)
		if err != nil {
			return err
		}
		switch t.Status {
		case StatusDone:
			return fmt.Errorf("task %s is already done", t.ID)
		case StatusCancelled:
			return fmt.Errorf("task %s is cancelled and cannot close", t.ID)
		}
		started := !t.Started.IsZero()
		if t0.VerifyMode == "command" {
			t.Evidence = append(t.Evidence, ev)
		}
		claim := strings.TrimSpace(args.Evidence)
		if claim != "" {
			kind := args.EvidenceKind
			if kind == "" {
				kind = "claim"
			}
			t.Evidence = append(t.Evidence, store.TaskEvidence{Kind: kind, Text: claim, At: now})
		} else if t.VerifyMode != "command" {
			return errors.New("tasks done: evidence is required when the task has no verify command — cite the proof you observed (command, exit code, file)")
		}
		if !started {
			t.Notes = append(t.Notes, store.TaskNote{Text: "completed without ever being started", At: now})
		}
		t.Status = StatusDone
		if t.Started.IsZero() {
			t.Started = now
		}
		t.Ended = now
		sav, err := st.SaveTask(t)
		if err != nil {
			return err
		}
		saved = sav
		nudged = closingNudge(st, sav)
		return nil
	})
	if err != nil {
		return store.TaskRecord{}, "", err
	}
	b.emit(saved, "done", firstLine(receipt, 200))
	render := renderTask(saved, b.depStatuses(saved.DependsOn), b.now()) + "\n" + rulesTrailer
	if receipt != "" {
		render += "\nreceipt: " + receipt
	}
	if nudged {
		render += "\n\nNOTE: " + strings.Join(nudgeLines, "\n")
	}
	return saved, render, nil
}

// Flag attaches an imperfection signal ("OFF: ...") to any task, regardless
// of status, without reopening it. This is how "something looks off" stays
// stamped on the plan instead of living only in prose.
func (b *Board) Flag(idv, note string) (store.TaskRecord, string, error) {
	var out store.TaskRecord
	note = strings.TrimSpace(note)
	if note == "" {
		return out, "", errors.New("tasks flag: note is required")
	}
	var saved store.TaskRecord
	err := b.withWrite(func(st *store.Store) error {
		t, err := st.LoadTask(idv)
		if err != nil {
			return err
		}
		fresh := noteOn(t, note, b.now())
		sav, err := st.SaveTask(fresh)
		if err != nil {
			return err
		}
		saved = sav
		return nil
	})
	if err != nil {
		return out, "", err
	}
	b.emit(saved, "flag", firstLine(note, 160))
	return saved, renderTask(saved, b.depStatuses(saved.DependsOn), b.now()) + "\n" + rulesTrailer, nil
}

// SetVerify replaces a task's completion oracle, or clears it when verify is
// empty. This is the escape hatch for a task whose verify command was written
// as prose; the new command is validated at task-update time.
func (b *Board) SetVerify(ctx context.Context, idv, cmd string) (store.TaskRecord, string, error) {
	var out store.TaskRecord
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		// Empty means convert it to a non-oracle task; the completion gate is
		// explicitly loosened, so a note records who did it.
		var saved store.TaskRecord
		err := b.withWrite(func(st *store.Store) error {
			t, err := st.LoadTask(idv)
			if err != nil {
				return err
			}
			if t.Status == StatusDone || t.Status == StatusCancelled {
				return fmt.Errorf("task %s is %s - closed tasks do not take verify edits", t.ID, t.Status)
			}
			t.Verify = ""
			t.VerifyMode = ""
			fresh := noteOn(t, "verify oracle cleared by SetVerify", b.now())
			sav, err := st.SaveTask(fresh)
			if err != nil {
				return err
			}
			saved = sav
			return nil
		})
		if err != nil {
			return out, "", err
		}
		b.emit(saved, "flag", "verify cleared")
		return saved, renderTask(saved, b.depStatuses(saved.DependsOn), b.now()) + "\n" + rulesTrailer, nil
	}
	if _, err := b.authorizeOracle(ctx, cmd); err != nil {
		return out, "", err
	}
	var saved store.TaskRecord
	err := b.withWrite(func(st *store.Store) error {
		t, err := st.LoadTask(idv)
		if err != nil {
			return err
		}
		if t.Status == StatusDone || t.Status == StatusCancelled {
			return fmt.Errorf("task %s is %s - closed tasks do not take verify edits", t.ID, t.Status)
		}
		t.Verify = cmd
		t.VerifyMode = "command"
		fresh := noteOn(t, "verify oracle replaced", b.now())
		sav, err := st.SaveTask(fresh)
		if err != nil {
			return err
		}
		saved = sav
		return nil
	})
	if err != nil {
		return out, "", err
	}
	b.emit(saved, "flag", "verify replaced")
	return saved, renderTask(saved, b.depStatuses(saved.DependsOn), b.now()) + "\n" + rulesTrailer, nil
}

// Cancel closes a task as cancelled. Only open tasks can be cancelled.
func (b *Board) Cancel(idv string) (store.TaskRecord, string, error) {
	var saved store.TaskRecord
	err := b.withWrite(func(st *store.Store) error {
		t, err := st.LoadTask(idv)
		if err != nil {
			return err
		}
		switch t.Status {
		case StatusDone:
			return fmt.Errorf("task %s is already done — flag(…) it instead if something is off", t.ID)
		case StatusCancelled:
			return fmt.Errorf("task %s is already cancelled", t.ID)
		}
		t, err = st.LoadTask(idv) // reload in case a run raced the load
		if err != nil {
			return err
		}
		// The reload happened after the first status check; re-check before
		// overwriting. Without this, a task that closed between the load and
		// here would be silently flipped back to cancelled.
		switch t.Status {
		case StatusDone:
			return fmt.Errorf("task %s is already done — flag(\"%s\", ...) it instead if something is off", t.ID, t.ID)
		case StatusCancelled:
			return fmt.Errorf("task %s is already cancelled", t.ID)
		}
		fresh := noteOn(t, "cancelled", b.now())
		fresh.Status = StatusCancelled
		fresh.Ended = b.now()
		sav, err := st.SaveTask(fresh)
		if err != nil {
			return err
		}
		saved = sav
		return nil
	})
	if err != nil {
		return store.TaskRecord{}, "", err
	}
	b.emit(saved, "cancel", saved.Title)
	return saved, renderTask(saved, b.depStatuses(saved.DependsOn), b.now()) + "\n" + rulesTrailer, nil
}

// ---------------------------------------------------------------------------
// Dependency edits
// ---------------------------------------------------------------------------

// Dep Edits rewrite a task's dependencies on an existing task: both directions
// (add and remove) validate every reference; the add direction also refuses a
// reference that transitively depends on the edited task (a cycle).
func (b *Board) Dep(idv string, add, remove []string) (store.TaskRecord, string, error) {
	var saved store.TaskRecord
	err := b.withWrite(func(st *store.Store) error {
		t, err := st.LoadTask(idv)
		if err != nil {
			return err
		}
		if t.Status == StatusDone || t.Status == StatusCancelled {
			return fmt.Errorf("task %s is %s — closed tasks do not take dependency edits", t.ID, t.Status)
		}
		if err := applyDeps(st, &t, add, remove, b.now); err != nil {
			return err
		}
		sav, err := st.SaveTask(t)
		if err != nil {
			return err
		}
		saved = sav
		return nil
	})
	if err != nil {
		return store.TaskRecord{}, "", err
	}
	b.emit(saved, "deps", strings.Join(saved.DependsOn, ", "))
	return saved, renderTask(saved, b.depStatuses(saved.DependsOn), b.now()) + "\n" + rulesTrailer, nil
}

// applyDeps mutates t.DependsOn in place: resolving add refs (existence, no
// self-dep, no dedup needed), removing remove refs, sorting by sequence.
// Cycle check: a new dep must not reach t through the existing graph.
func applyDeps(st *store.Store, t *store.TaskRecord, add, remove []string, now func() time.Time) error {
	var deps []string
	for _, d := range t.DependsOn {
		deps = append(deps, d)
	}
	removeSet := map[string]bool{}

	for _, r := range remove {
		if idv := normalizeTaskID(r); idv != "" {
			removeSet[idv] = true
		}
	}
	kept := deps[:0]
	for _, d := range deps {
		if removeSet[d] {
			continue
		}
		kept = append(kept, d)
	}
	deps = kept
	// Rebuild the dedup map from the post-removal set so a remove+re-add of
	// the same id in one edit is honoured rather than silently dropped.
	depSet := map[string]bool{}
	var uniq []string
	for _, d := range deps {
		if !depSet[d] {
			depSet[d] = true
			uniq = append(uniq, d)
		}
	}
	deps = uniq

	for _, raw := range add {
		d, err := st.LoadTask(raw)
		if err != nil {
			return fmt.Errorf("dependency %q: %v", raw, err)
		}
		if d.ID == t.ID {
			return errors.New("a task cannot depend on itself")
		}
		if depSet[d.ID] {
			continue
		}
		if reaches(st, d.ID, t.ID) {
			return fmt.Errorf("dependency %s would create a cycle: %s already depends on %s", d.ID, d.ID, t.ID)
		}
		depSet[d.ID] = true
		deps = append(deps, d.ID)
	}
	sortDepsBySeq(st, deps)
	if len(deps) == 0 && len(t.DependsOn) == 0 {
		return nil
	}
	t.DependsOn = deps
	return nil
}

// reaches reports whether task `from` transitively depends on `target`.
// reaches reports whether target is reachable from from by following
// dependency edges. A cycle anywhere in the board cannot make this loop
// forever (visited set) and there is no depth cap that silently accepts
// cycles via long detours. A corrupt store hits the maxVisits guard and
// yields false, the same as an ordinary unreachability verdict.
func reaches(st *store.Store, from, target string) bool {
	const maxVisits = 10000
	visited := map[string]bool{}
	stack := []string{from}
	for len(stack) > 0 && len(visited) < maxVisits {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == target {
			return true
		}
		if visited[cur] {
			continue
		}
		visited[cur] = true
		t, err := st.LoadTask(cur)
		if err != nil {
			continue
		}
		for _, d := range t.DependsOn {
			if !visited[d] {
				stack = append(stack, d)
			}
		}
	}
	return false
}

func sortDepsBySeq(st *store.Store, deps []string) {
	type row struct {
		id  string
		seq int64
	}
	var rows []row
	for _, d := range deps {
		seq := int64(0)
		if t, err := st.LoadTask(d); err == nil {
			seq = t.Seq
		}
		rows = append(rows, row{id: d, seq: seq})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].seq < rows[j].seq })
	for i, r := range rows {
		deps[i] = r.id
	}
}

// normalizeTaskID accepts "t7" or "7" and returns "t7", or "" when unusable.
func normalizeTaskID(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "t")
	if raw == "" {
		return ""
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return "t" + raw
}

// ---------------------------------------------------------------------------
// The completion oracle
// ---------------------------------------------------------------------------

// authorizeOracle screens the verify command through policy at task-creation
// time: a task must not be creatable whose oracle could never run.
func (b *Board) authorizeOracle(ctx context.Context, cmd string) (string, error) {
	if err := usableVerifyCommand(cmd); err != nil {
		return "", err
	}
	if b.opts.Policy == nil {
		return cmd, nil
	}
	if _, err := b.opts.Policy.Authorize(ctx, cmd); err != nil {
		return "", err
	}
	return cmd, nil
}

// usableVerifyCommand rejects prose passed as a "verify" command. A task's
// oracle must be runnable by the shell; otherwise the AI effectively deadlocks
// its own board instead of reporting completion.
// isEnvAssignmentName reports whether s is a valid variable name for a
// leading POSIX-style `NAME=value` assignment (letter or underscore first,
// then letters/digits/underscore).
func isEnvAssignmentName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

func usableVerifyCommand(cmd string) error {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return errors.New("verify: command is required")
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return errors.New("verify: command is required")
	}
	// Strip leading POSIX environment assignments (`GOFLAGS=-race go test
	// ./...`, `CGO_ENABLED=0 go build`): they are a common, safe wrapper
	// form and must not be misread as the command name.
	rest := fields
	for len(rest) > 0 {
		f := rest[0]
		eq := strings.IndexByte(f, '=')
		if eq <= 0 || !isEnvAssignmentName(f[:eq]) {
			break
		}
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return errors.New("verify: environment assignments require a command after them")
	}
	name := rest[0]
	name = strings.Trim(name, `'"`)
	if strings.ContainsRune(name, '`') || strings.ContainsRune(name, '$') {
		return fmt.Errorf("verify %q is too dynamic to trust as an oracle; use a simple runnable command or leave verify empty", cmd)
	}
	builtins := map[string]bool{
		"echo": true, "true": true, "false": true, "exit": true, "test": true,
		"cd": true, "pwd": true, "ls": true, "dir": true, "type": true, "cat": true,
		"pwdset": true, "set-location": true, "select-string": true, "get-childitem": true,
		"set-strictmode": true, "set-psdebug": true,
	}
	if builtins[strings.ToLower(name)] {
		return nil
	}
	if strings.ContainsAny(name[:1], "./") || strings.ContainsRune(name[:1], '\\') || strings.Contains(name, ":\\") {
		return nil // an explicit path is the user's responsibility
	}
	if _, err := exec.LookPath(name); err == nil {
		return nil
	}
	return fmt.Errorf("verify %q looks like prose, not a runnable command; use 'go test ./...', 'npm test', 'rg -q pattern file', or omit verify and provide evidence", cmd)
}

// runOracle executes the verify command synchronously and returns the
// receipt. A non-zero exit (or a policy refusal) is a failed close: the task
// stays open and a note lands on it.
func (b *Board) runOracle(ctx context.Context, t store.TaskRecord) (ev store.TaskEvidence, line string, runErr error) {
	// A panic anywhere in this path (bad output reduction, nil derefs in
	// the runner, etc.) must become an ordinary verification failure, never
	// process death or a poisoned board mutex.
	defer func() {
		if r := recover(); r != nil {
			ev = store.TaskEvidence{}
			line = ""
			runErr = fmt.Errorf("verify oracle panicked: %v", r)
		}
	}()
	now := b.now()
	if b.opts.Jobs == nil {
		return store.TaskEvidence{}, "", errors.New("no command runner attached to this board")
	}
	if b.opts.Policy != nil {
		if _, err := b.opts.Policy.Authorize(ctx, t.Verify); err != nil {
			return store.TaskEvidence{}, "", fmt.Errorf("verify %q: %v", t.Verify, err)
		}
	}
	timeout := b.opts.VerifyTimeout
	rctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	job, err := b.opts.Jobs.Start(rctx, runner.Spec{
		Command: t.Verify,
		Dir:     b.opts.Root,
		Shell:   b.opts.Shell,
		Timeout: timeout,
	})
	if err != nil {
		return store.TaskEvidence{}, "", fmt.Errorf("verify %q could not start: %v", t.Verify, err)
	}
	_ = job.Wait(rctx)
	snap := job.Snapshot()
	switch {
	case snap.Status == runner.StatusTimeout:
		return store.TaskEvidence{}, "", fmt.Errorf(
			"verify %q timed out after %s (job %s still recorded) — fix the command or the code and try again",
			t.Verify, timeout, snap.ID)
	case snap.ExitCode != 0:
		tail := "(no output)"
		if out, rerr := job.Output(); rerr == nil {
			if line := errLastLine(out); line != "" {
				tail = line
			}
		}
		return store.TaskEvidence{}, "", fmt.Errorf(
			"verify %q exited %d: %s (job %s) — the work is not done",
			t.Verify, snap.ExitCode, firstLine(tail, 140), snap.ID)
	}
	ev = store.TaskEvidence{Kind: "cmd", Text: fmt.Sprintf("verify %q exited 0 (job %s)", t.Verify, snap.ID), At: now}
	return ev, ev.Text, nil
}

// ---------------------------------------------------------------------------
// The delegation bridge
// ---------------------------------------------------------------------------

// CloseTask is the delegate-closure hook: a finished delegate either ticks
// its bound task (still gated by the oracle when the task has one) or flags
// it with the outcome. Implements delegate.TaskCloser.
func (b *Board) CloseTask(taskID, outcome, summary string) error {
	switch outcome {
	case "done":
		line := firstLine(summary, 160)
		if line == "" {
			line = "delegate report (empty)"
		}
		_, _, err := b.Done(context.Background(), DoneArgs{
			ID:           taskID,
			Evidence:     "delegate report: " + line,
			EvidenceKind: "delegate",
		})
		return err
	default:
		_, _, err := b.Flag(taskID, firstLine(outcome+": "+summary, 200))
		return err
	}
}

// BindTask records which delegate is doing the task's current work so the
// board view can attribute an in-flight task. Rebinding overwrites the
// previous entry; closed tasks are left untouched.
func (b *Board) BindTask(taskID, delegateID string) error {
	return b.withWrite(func(st *store.Store) error {
		t, err := st.LoadTask(taskID)
		if err != nil {
			return err
		}
		if t.Status == StatusDone || t.Status == StatusCancelled {
			return nil
		}
		t.DelegatedTo = delegateID
		_, err = st.SaveTask(t)
		return err
	})
}

// ---------------------------------------------------------------------------
// Graph queries
// ---------------------------------------------------------------------------

// blockersOf lists not-yet-done dependencies as "t2 (pending)" strings.
func blockersOf(st *store.Store, t store.TaskRecord) []string {
	var out []string
	for _, d := range t.DependsOn {
		dv, err := st.LoadTask(d)
		if err != nil {
			out = append(out, d+" (missing)")
			continue
		}
		// A cancelled dependency means the work was deliberately abandoned;
		// the dependent must not be stranded forever because of it.
		if dv.Status == StatusDone || dv.Status == StatusCancelled {
			continue
		}
		out = append(out, fmt.Sprintf("%s (%s)", dv.ID, dv.Status))
	}
	return out
}

// activeOf returns the first task currently in_progress other than skip.
func activeOf(st *store.Store, skip string) *store.TaskRecord {
	for _, t := range st.ListTasks(0) {
		if t.ID != skip && t.Status == StatusInProgress {
			c := t
			return &c
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Nudge
// ---------------------------------------------------------------------------

// closingNudge inspects the board after a close and reports whether the
// nudge should fire: 3+ tasks done, none of which anywhere includes a verify
// command or a check-shaped title.
func closingNudge(st *store.Store, closed store.TaskRecord) bool {
	if closed.VerifyMode == "command" || verifPattern.MatchString(closed.Title+" "+closed.Doing) {
		return false
	}
	done := 0
	verifyOrCheck := false
	for _, t := range st.ListTasks(0) {
		if t.Status == StatusDone {
			done++
		}
		if t.VerifyMode == "command" || verifPattern.MatchString(t.Title+" "+t.Doing) {
			verifyOrCheck = true
			break
		}
	}
	if verifyOrCheck {
		return false
	}
	return done >= 3
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// withWrite serialises a load-mutate-save cycle. The critical section is the
// whole callback; the oracle intentionally drops back in and out (see Done).
func (b *Board) withWrite(cb func(st *store.Store) error) error {
	if b.st == nil {
		return errors.New("board: no store attached")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return cb(b.st)
}

// depStatuses resolves dependency ids to their current statuses in one pass.
func (b *Board) depStatuses(ids []string) map[string]string {
	out := map[string]string{}
	if len(ids) == 0 || b.st == nil {
		return out
	}
	byID := map[string]bool{}
	for _, idv := range ids {
		byID[idv] = true
	}
	for _, t := range b.st.ListTasks(0) {
		if byID[t.ID] {
			out[t.ID] = t.Status
		}
	}
	return out
}

// noteOn returns a copy of t with one more note.
func noteOn(t store.TaskRecord, text string, at time.Time) store.TaskRecord {
	out := t
	out.Notes = append(out.Notes, store.TaskNote{Text: text, At: at})
	return out
}

func (b *Board) now() time.Time { return b.opts.Now() }

// emit journals a task state change into the shared delegation journal
// (kind "task"), and to the in-process subscriber if one is wired.
func (b *Board) emit(t store.TaskRecord, action, detail string) {
	if b.st == nil {
		return
	}
	ev, err := b.st.AppendDelegateEvent(store.DelegateEvent{
		Kind: "task", ID: t.ID, Status: t.Status,
		Task: firstLine(t.Title, 160), Detail: detail,
	})
	if err != nil {
		return
	}
	if b.opts.OnEvent != nil {
		b.opts.OnEvent(ev)
	}
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	// Rune-safe: byte slicing would cut multi-byte titles mid-character.
	if max > 0 {
		if r := []rune(s); len(r) > max {
			s = string(r[:max]) + "…"
		}
	}
	return s
}

func errLastLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// AutoCloseByVerify closes every open task whose verify command is exactly
// command (whitespace-collapsed comparison) and whose bound run would act on
// this workspace — the command must have exited 0 in the board's root
// directory — and stamps the receipt with the job id. It is called by the
// shell layer every time a command exits 0, from any thread, so the agent
// never has to remember to close its own plan. Tasks with unfinished
// dependencies are left open on purpose — a green build locally does not
// excuse skipping gates upstream of the task.
func (b *Board) AutoCloseByVerify(command, jobID, dir string) ([]string, error) {
	want := normalizeCmdLine(command)
	if want == "" {
		return nil, nil
	}
	if dir == "" {
		// Without the directory we cannot prove the command was the task's
		// oracle; refusing to close is the only safe verdict.
		return nil, nil
	}
	if !sameDir(dir, b.opts.Root) {
		// The oracle only ever runs at b.opts.Root; a command that ran
		// elsewhere is not evidence that this task's gate passed.
		return nil, nil
	}
	var closed []string
	var emits []store.TaskRecord
	err := b.withWrite(func(st *store.Store) error {
		recs, err := b.List()
		if err != nil {
			return err
		}
		for _, t := range recs {
			if t.Status == StatusDone || t.Status == StatusCancelled {
				continue
			}
			if t.VerifyMode != "command" || normalizeCmdLine(t.Verify) != want {
				continue
			}
			if blockers := blockersOf(st, t); len(blockers) > 0 {
				continue
			}
			now := b.now()
			t.Evidence = append(t.Evidence, store.TaskEvidence{
				Kind: "verify",
				Text: jobID + " · exit 0",
				At:   now,
			})
			if t.Started.IsZero() {
				// Same honesty rule as the manual close path: a task closed
				// without ever being started carries the drift note.
				t.Started = now
				t.Notes = append(t.Notes, store.TaskNote{Text: "completed without ever being started", At: now})
			}
			t.Ended = now
			t.Status = StatusDone
			saved, err := st.SaveTask(t)
			if err != nil {
				return err
			}
			closed = append(closed, saved.ID)
			emits = append(emits, saved)
		}
		return nil
	})
	// Emit outside the critical section: OnEvent is a front-end hook and may
	// re-enter the board (e.g. to flag the task); with a non-re-entrant
	// mutex that would deadlock if the hook ran inside withWrite.
	for _, saved := range emits {
		b.emit(saved, "done", "auto · "+jobID+" exit 0")
	}
	return closed, err
}

// normalizeCmdLine collapses runs of whitespace so 'go test ./...' and
// 'go   test    ./...' compare equal.
func normalizeCmdLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// sameDir reports whether two directory paths name the same directory.
// Case-insensitive on Windows.
func sameDir(a, b string) bool {
	na := filepath.ToSlash(filepath.Clean(a))
	nb := filepath.ToSlash(filepath.Clean(b))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(na, nb)
	}
	return na == nb
}
