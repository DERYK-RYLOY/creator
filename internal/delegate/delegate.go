// Package delegate turns the agent loop into an orchard: any agent can spawn
// isolated sub-agents ("delegates"), each with a fresh conversation, its own
// toolset, its own token budget, its own session record and its own event
// journal — without ever passing its raw working output back into the parent's
// context.
//
// The context economy is the point. A delegate that reads nine files and runs
// twenty commands burns its own context to do it; the delegator pays roughly
// one spawn-line of tokens when the delegate starts and one reduced report
// when it finishes. Nothing else crosses the boundary. The full report lives
// under the delegate's handle and is paged with `out` only when the delegator
// asks.
//
// Delegation is a tree, not a pool: a delegate allowed to spawn may spawn
// again (depth-limited), concurrency is capped while the total number of
// spawns is bounded only by responsibility, and every state change lands in a
// persistent ordered journal ("devents") that any live viewer can replay and
// subscribe to.
package delegate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creatorcli/creator/internal/agent"
	"github.com/creatorcli/creator/internal/id"
	"github.com/creatorcli/creator/internal/provider"
	"github.com/creatorcli/creator/internal/reduce"
	"github.com/creatorcli/creator/internal/store"
	"github.com/creatorcli/creator/internal/tools"
)

// Spec is one delegation request.
type Spec struct {
	Task         string   `json:"task"`                    // the job itself
	Name         string   `json:"name,omitempty"`          // human label, e.g. "explorer"
	Profile      string   `json:"profile,omitempty"`       // named role: explorer, planner, reviewer, editor
	Provider     string   `json:"provider,omitempty"`      // backend override
	Model        string   `json:"model,omitempty"`         // model override
	ReadOnly     bool     `json:"read_only,omitempty"`     // only read-only tools
	Tools        []string `json:"tools,omitempty"`         // allowlist (nil = inherit)
	Deny         []string `json:"deny,omitempty"`          // denylist (wins over allow)
	CanSpawn     bool     `json:"can_spawn,omitempty"`     // may delegate further
	MaxSteps     int      `json:"max_steps,omitempty"`     // tool-calling rounds
	BudgetTokens int      `json:"budget,omitempty"`        // total token budget
	OutputTokens int      `json:"output_tokens,omitempty"` // parent pays at most this
	MaxTokens    int      `json:"max_tokens,omitempty"`    // per-response cap
	TimeoutSecs  int      `json:"timeout,omitempty"`       // hard wall in seconds (0 = none)
	Wait         bool     `json:"wait,omitempty"`          // block until finished
	// TaskID binds this delegate to a board task: on success the task closes
	// (with the report as a delegate receipt; an attached verify command still
	// gates it), on failure or cancellation the task is flagged instead.
	TaskID string `json:"task_id,omitempty"`
	Depth  int    `json:"-"` // filled by the manager

	// role is the profile guidance merged into the prompt at spawn time.
	role string
}

// Options configure a Manager.
type Options struct {
	Workspace string
	Store     *store.Store
	Registry  *tools.Registry // root registry, filtered per spawn
	Providers func(providerSlug, model string) (provider.Provider, error)
	Fallback  provider.Provider // used when no provider/model override is asked
	Hooks     tools.Hooks
	ShellName string
	// Knowledge is the rendered repository-instructions section that every
	// delegate inherits, so a sub-agent follows the project's conventions too.
	Knowledge   string
	MaxTokens   int // per-response cap for every delegate (0 = 4096)
	Temperature float64
	Thinking    int

	MaxDepth        int // how deep delegation may nest (0 = no nesting)
	Concurrency     int // how many delegates run at once (0 = 8)
	MaxPerRoot      int // active delegates per root session (0 = 64)
	MaxSteps        int // default steps per delegate (0 = 24)
	OutputTokens    int // default report budget (0 = 300)
	MaxTotalPerRoot int // lifetime spawns per root tree (0 = unlimited)

	// Tasks closes board tasks bound to a delegate via task_id (see
	// board.Board, which implements this). Nil when no board is attached.
	Tasks TaskCloser

	OnEvent func(store.DelegateEvent) // extra consumer (front-end, ...)
}

// TaskCloser binds delegation outcomes back into the task board without the
// delegate package importing it.
type TaskCloser interface {
	// CloseTask marks the outcome of a task: "done" closes it (evidence is
	// the delegate report), anything else flags it with the outcome.
	CloseTask(taskID, outcome, summary string) error
	// BindTask records which delegate is doing the task work, so the board
	// view can attribute an in-flight task to a live delegate.
	BindTask(taskID, delegateID string) error
}

// Manager owns every delegate of one Creator process.
type Manager struct {
	opts Options

	mu         sync.Mutex
	reg        *tools.Registry // captured on first Attach
	records    map[string]*Record
	done       map[string]chan struct{}
	live       map[string]context.CancelFunc // cancels for running delegates
	spawnN     map[string]int                // root session -> lifetime spawns
	running    int
	queued     int
	slots      chan struct{} // concurrency semaphore
	journalMu  sync.Mutex
	journalErr error // last journal (SaveDelegate) failure
	subs       map[chan store.DelegateEvent]struct{}
}

// Record is the live view of one delegate.
type Record struct {
	store.DelegateRecord
	canceled atomic.Bool
}

// clone returns a snapshot copy safe to hand to callers. Internal callers
// keep working on m.records; what leaves the manager must not alias the
// pointer pump() mutates.
func (r *Record) clone() *Record {
	out := &Record{DelegateRecord: r.DelegateRecord}
	out.canceled.Store(r.canceled.Load())
	return out
}

// New creates a manager and marks any records from a previous process as
// orphaned: bbolt permits a single writer process, so anything still claiming
// to run belongs to a dead one.
func New(st *store.Store, opts Options) *Manager {
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 2
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 8
	}
	if opts.MaxPerRoot <= 0 {
		opts.MaxPerRoot = 64
	}
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = 24
	}
	if opts.OutputTokens <= 0 {
		opts.OutputTokens = 300
	}
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = 4096
	}
	m := &Manager{
		opts:    opts,
		records: map[string]*Record{},
		done:    map[string]chan struct{}{},
		live:    map[string]context.CancelFunc{},
		spawnN:  map[string]int{},
		slots:   make(chan struct{}, opts.Concurrency),
		subs:    map[chan store.DelegateEvent]struct{}{},
	}
	if st != nil {
		if n := st.OrphanDelegates(); n > 0 {
			m.emit(store.DelegateEvent{Kind: "orphan", Detail: fmt.Sprintf("marked %d stale delegate(s) orphaned", n)})
		}
	}
	return m
}

// Attach registers the delegate tools into a registry at the given depth.
// Depth 0 is the root conversation; deeper attaches happen inside spawned
// delegates that may delegate further.
func (m *Manager) Attach(reg *tools.Registry, depth int, sessionID string) error {
	m.mu.Lock()
	if m.reg == nil {
		m.reg = reg
	}
	m.mu.Unlock()
	for _, t := range m.toolset(depth, sessionID) {
		if err := reg.Add(t); err != nil {
			return fmt.Errorf("delegate tools: %w", err)
		}
	}
	return nil
}

// Subscribe returns a channel receiving events as they happen, plus a stop
// function that closes it. Drop policy is lossy-or-late: a slow subscriber
// loses individual events, never blocks the delegation machinery. For
// guaranteed delivery, replay the persistent journal instead.
func (m *Manager) Subscribe(buffer int) (<-chan store.DelegateEvent, func()) {
	if buffer <= 0 {
		buffer = 128
	}
	ch := make(chan store.DelegateEvent, buffer)
	m.mu.Lock()
	m.subs[ch] = struct{}{}
	stop := func() {
		m.mu.Lock()
		delete(m.subs, ch)
		m.mu.Unlock()
		close(ch)
	}
	m.mu.Unlock()
	return ch, stop
}

// emit publishes an event: persisted first (the journal is the substrate every
// viewer replays), then broadcast non-blockingly, then to an optional direct
// consumer.
func (m *Manager) emit(ev store.DelegateEvent) {
	m.mu.Lock()
	if idv := ev.ID; idv != "" && ev.Name == "" && m.records[idv] != nil {
		ev.Name = m.records[idv].Name
	}
	m.mu.Unlock()
	if m.opts.Store != nil {
		if saved, err := m.opts.Store.AppendDelegateEvent(ev); err == nil {
			ev = saved
		}
	}
	m.mu.Lock()
	for ch := range m.subs {
		select {
		case ch <- ev:
		default:
		}
	}
	m.mu.Unlock()
	if m.opts.OnEvent != nil {
		m.opts.OnEvent(ev)
	}
}

// ---------------------------------------------------------------------------
// Spawning
// ---------------------------------------------------------------------------

// Spawn types one delegation request and starts it. parentID is the delegating
// session (a root session id or a parent delegate id). The returned record
// reflects the moment of spawning; the run proceeds in the background.
func (m *Manager) Spawn(ctx context.Context, parentID string, spec Spec) (*Record, error) {
	spec.Task = strings.TrimSpace(spec.Task)
	if spec.Task == "" {
		return nil, errors.New("delegate: task is required")
	}
	if spec.Provider == "auto" {
		spec.Provider = ""
	}

	// Hooks gate and may rewrite the request before it exists.
	optsMap, _ := spec.optsMap()
	if m.opts.Hooks != nil {
		rewritten, err := m.opts.Hooks.BeforeDelegate(optsMap)
		if err != nil {
			return nil, fmt.Errorf("delegate: refused by hook: %w", err)
		}
		if rewritten != nil {
			b, err := json.Marshal(rewritten)
			if err != nil {
				return nil, err
			}
			_ = json.Unmarshal(b, &spec) // only keys present survive
		}
		spec.Task = strings.TrimSpace(spec.Task)
		if spec.Task == "" {
			return nil, errors.New("delegate: task is required")
		}
	}

	// A named profile supplies a curated toolset and a role mindset. It is
	// applied after hooks so a plugin can still veto or rewrite the request,
	// but before the limits so the profile's caps are what actually run.
	if spec.Profile != "" {
		p, ok := LookupProfile(spec.Profile)
		if !ok {
			return nil, fmt.Errorf("delegate: unknown profile %q (available: %s)", spec.Profile, strings.Join(ProfileNames(), ", "))
		}
		applyProfile(&spec, p)
	}

	if spec.Depth >= m.opts.MaxDepth && spec.CanSpawn {
		return nil, fmt.Errorf(
			"delegate: depth limit is %d and this spawn would create depth %d — do it yourself or make the task smaller",
			m.opts.MaxDepth, spec.Depth+1)
	}

	rootID := m.rootOf(parentID)
	m.mu.Lock()
	// Active-cap check first: refused spawns must not burn lifetime budget.
	if m.running+m.queued >= m.opts.MaxPerRoot {
		m.mu.Unlock()
		return nil, fmt.Errorf("delegate: active delegate cap of %d reached — wait for some to finish (delegates tool, wait_seconds) before spawning more", m.opts.MaxPerRoot)
	}
	if m.opts.MaxTotalPerRoot > 0 {
		if n := m.spawnN[rootID]; n >= m.opts.MaxTotalPerRoot {
			m.mu.Unlock()
			return nil, fmt.Errorf("delegate: spawn cap of %d for this tree reached — collect results with the delegates tool, then spawn again in a fresh turn", m.opts.MaxTotalPerRoot)
		}
		m.spawnN[rootID]++
	}
	rec := &Record{DelegateRecord: store.DelegateRecord{
		ID:       id.New("d"),
		Parent:   parentID,
		Name:     orDefaultStr(strings.TrimSpace(spec.Name), "delegate"),
		Task:     firstLine(spec.Task, 160),
		Status:   "queued",
		Depth:    spec.Depth,
		Created:  time.Now(),
		OwnerPID: os.Getpid(), // this manager owns the delegate it spawned
	}}
	m.records[rec.ID] = rec
	m.done[rec.ID] = make(chan struct{})
	m.queued++
	m.mu.Unlock()

	// Record the binding so the board can attribute the task to this
	// delegate while it is in flight. Best effort: a bind failure must not
	// prevent the delegation itself.
	if spec.TaskID != "" && m.opts.Tasks != nil {
		_ = m.opts.Tasks.BindTask(spec.TaskID, rec.ID)
	}

	m.emit(store.DelegateEvent{
		Kind: "spawn", ID: rec.ID, Parent: parentID, Name: rec.Name,
		Depth: spec.Depth, Status: "queued", Task: rec.Task,
	})

	go m.pump(ctx, rec, spec)
	out := rec.clone()
	return out, nil
}

// pump is the lifecycle goroutine: queue, acquire a slot, run, always clean.
// Parent cancellation drains the queue and stops running children.
func (m *Manager) pump(ctx context.Context, rec *Record, spec Spec) {
	select {
	case m.slots <- struct{}{}:
	case <-ctx.Done():
		m.mu.Lock()
		m.queued--
		r := m.records[rec.ID]
		r.Status = "canceled"
		r.Error = "canceled: delegator exited"
		ch := m.done[rec.ID]
		m.mu.Unlock()
		m.save(r)
		if spec.TaskID != "" && m.opts.Tasks != nil {
			// Flag the bound task rather than strand it forever in
			// in_progress — a delegate bound to it will never report again.
			summary := r.Error
			if summary == "" {
				summary = r.Result
			}
			if err := m.opts.Tasks.CloseTask(spec.TaskID, "canceled", summary); err != nil {
				// Leave a trace: the task may still be sitting in_progress.
				m.emit(store.DelegateEvent{Kind: "notice", ID: r.ID, Parent: r.Parent, Depth: r.Depth, Status: "canceled", Detail: "task " + spec.TaskID + " was not flagged: " + errText(err)})
			}
		}
		// Emit BEFORE releasing waiters so anyone collecting the delegate
		// right away still observes the full journal.
		m.emit(store.DelegateEvent{Kind: "cancel", ID: r.ID, Parent: r.Parent, Depth: r.Depth, Status: "canceled", Detail: r.Error})
		close(ch)
		return
	}

	m.mu.Lock()
	m.queued--
	r := m.records[rec.ID]
	if r.canceled.Load() {
		r.Status = "canceled"
		r.Error = "canceled by request"
		ch := m.done[rec.ID]
		m.mu.Unlock()
		m.save(r)
		if spec.TaskID != "" && m.opts.Tasks != nil {
			summary := r.Error
			if summary == "" {
				summary = r.Result
			}
			if err := m.opts.Tasks.CloseTask(spec.TaskID, "canceled", summary); err != nil {
				m.emit(store.DelegateEvent{Kind: "notice", ID: r.ID, Parent: r.Parent, Depth: r.Depth, Status: "canceled", Detail: "task " + spec.TaskID + " was not flagged: " + errText(err)})
			}
		}
		m.emit(store.DelegateEvent{Kind: "cancel", ID: r.ID, Parent: r.Parent, Depth: r.Depth, Status: "canceled"})
		close(ch)
		<-m.slots
		return
	}
	r.Status = "running"
	r.Started = time.Now()
	m.running++
	ch := m.done[rec.ID]
	m.save(r)
	// Pick one cancelable context up front so the first WithCancel is never
	// created just to be discarded (leaking its cancel func and timer).
	var runCtx context.Context
	var cancel context.CancelFunc
	if spec.TimeoutSecs > 0 {
		// Bound the seconds→duration conversion; values beyond a day are a
		// model error and would overflow or park forever.
		secs := spec.TimeoutSecs
		if secs > 86400 {
			secs = 86400
		}
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(secs)*time.Second)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}
	m.live[rec.ID] = cancel
	m.mu.Unlock()
	defer cancel()
	m.emit(store.DelegateEvent{Kind: "start", ID: r.ID, Parent: r.Parent, Name: r.Name, Depth: r.Depth, Status: "running"})

	result, usage, steps, runErr := m.run(runCtx, r, spec)

	m.mu.Lock()
	r = m.records[rec.ID]
	r.Usage = usage
	r.Steps = steps
	delete(m.live, rec.ID)
	switch {
	case runErr == nil:
		r.Status = "done"
	case rec.canceled.Load():
		r.Status = "canceled"
		r.Error = "canceled by request"
	default:
		r.Status = "failed"
		r.Error = errText(runErr)
	}
	r.Ended = time.Now()
	r.Result = strings.TrimSpace(result)
	if r.Result != "" {
		// The record stores the reduced report (bounded by the spec's output
		// budget); the artifact under the delegate's handle keeps the full
		// text so `out(handle)` pages it on demand.
		if res := reduce.Reduce(r.ID, r.Result, reduce.Options{
			Budget: orDefault(spec.OutputTokens, m.opts.OutputTokens),
			Kind:   reduce.KindGeneric,
		}); res.Text != "" {
			r.Result = res.Text
		}
	}
	if m.opts.Store != nil && r.Status == "done" {
		// The full report goes into the artifact file under the delegate's own
		// handle, so the delegator — or a viewer — can page it on demand.
		if err := os.WriteFile(m.opts.Store.ArtifactPath(r.ID), []byte(result), 0o600); err == nil {
			r.Handle = r.ID
		}
	}
	m.save(r)
	m.running--
	// Drop oldest finished batch so MCP servers don't grow unbounded.
	m.evictOldFinished(256)
	m.mu.Unlock()

	<-m.slots

	kind, detail := "done", ""
	switch r.Status {
	case "done":
		detail = firstLine(r.Result, 200)
	case "failed":
		kind = "fail"
		detail = firstLine(r.Error, 200)
	default:
		kind = "cancel"
		detail = firstLine(r.Error, 200)
	}

	// Task binding: a delegate bound to a board task closes it on success
	// (still gated by the task's verify command) or flags it otherwise, so a
	// plan can never be stranded by an outcome nobody noticed.
	if spec.TaskID != "" && m.opts.Tasks != nil {
		summary := detail
		if r.Status == "done" {
			summary = r.Result
		}
		if err := m.opts.Tasks.CloseTask(spec.TaskID, r.Status, summary); err != nil {
			// A failed gate must leave a trace: flag the delegate record so
			// the caller sees why the board task did not close, and emit a
			// notice instead of pretending the gate passed.
			note := "task " + spec.TaskID + " was not closed: " + errText(err)
			if r.Error != "" {
				r.Error = r.Error + "; " + note
			} else {
				r.Error = note
			}
			m.save(r)
			if kind == "done" {
				kind = "notice"
			}
			detail = firstLine(r.Error, 200)
		}
	}

	// The waiting caller only wakes after this journal write, so anything
	// collecting the delegate right away still observes the full journal.
	m.emit(store.DelegateEvent{
		Kind: kind, ID: r.ID, Parent: r.Parent, Name: r.Name, Depth: r.Depth,
		Status: r.Status, Steps: steps, Usage: usage, Detail: detail,
	})
	close(ch)
}

// run executes the delegate's agent loop. Everything the delegate reads or
// runs lives and dies inside here; only the pieces the caller asks for leave.
func (m *Manager) run(ctx context.Context, rec *Record, spec Spec) (result string, usage provider.Usage, steps int, err error) {
	prov := m.resolveProvider(spec)
	if prov == nil {
		return "", usage, 0, errors.New("delegate: no provider available for this delegate (check --provider/--model or the provider override)")
	}

	m.mu.Lock()
	root := m.reg
	m.mu.Unlock()
	if root == nil {
		return "", usage, 0, errors.New("delegate: manager is not attached to a tool registry")
	}

	sub := root.Reduce(registryPredicate(spec))
	if sub == nil || len(sub.Names()) == 0 {
		// Reduce never returns nil, so the old guard was dead: a delegate
		// whose profile matches nothing ran with zero tools, burning its
		// full budget. Refuse instead.
		return "", usage, 0, errors.New("delegate: the requested toolset matches no tool in the root registry")
	}
	if spec.CanSpawn && spec.Depth+1 <= m.opts.MaxDepth {
		if err = m.Attach(sub, spec.Depth+1, rec.ID); err != nil {
			return "", usage, 0, err
		}
	}

	sess := store.Session{
		ID:        rec.ID,
		Created:   time.Now(),
		Workspace: m.opts.Workspace,
		Provider:  spec.Provider,
		Model:     prov.Model(),
	}
	maxSteps := orDefault(spec.MaxSteps, m.opts.MaxSteps)
	maxTokens := orDefault(spec.MaxTokens, m.opts.MaxTokens)

	forward := func(ev agent.Event) {
		switch ev.Type {
		case "usage":
			if ev.Usage != nil {
				m.mu.Lock()
				m.records[rec.ID].Usage = *ev.Usage
				m.save(m.records[rec.ID])
				m.mu.Unlock()
				usage = *ev.Usage
			}
			m.emit(store.DelegateEvent{Kind: "usage", ID: rec.ID, Parent: rec.Parent, Depth: rec.Depth, Usage: usage, Steps: steps})
		case "assistant":
			m.mu.Lock()
			m.records[rec.ID].Steps++
			m.save(m.records[rec.ID])
			steps = m.records[rec.ID].Steps
			m.mu.Unlock()
			m.emit(store.DelegateEvent{Kind: "step", ID: rec.ID, Parent: rec.Parent, Depth: rec.Depth, Detail: firstLine(ev.Text, 160)})
		case "tool_start":
			m.mu.Lock()
			m.records[rec.ID].Steps++
			m.save(m.records[rec.ID])
			steps = m.records[rec.ID].Steps
			m.mu.Unlock()
			m.emit(store.DelegateEvent{Kind: "tool", ID: rec.ID, Parent: rec.Parent, Depth: rec.Depth, Tool: ev.Tool})
		case "tool_end":
			m.emit(store.DelegateEvent{
				Kind: "tool", ID: rec.ID, Parent: rec.Parent, Depth: rec.Depth,
				Tool: ev.Tool, Status: ev.Status, Detail: firstLine(ev.Result, 200),
			})
		case "reasoning_delta", "assistant_delta", "notice", "done":
			// quiet: deltas would double the event volume for nothing
		}
	}

	a := agent.New(agent.Options{
		Provider:     prov,
		Tools:        sub,
		Store:        m.opts.Store,
		Session:      sess,
		MaxSteps:     maxSteps,
		BudgetTokens: spec.BudgetTokens,
		MaxTokens:    maxTokens,
		Temperature:  m.opts.Temperature,
		Thinking:     m.opts.Thinking,
		ShellName:    m.opts.ShellName,
		Knowledge:    m.opts.Knowledge,
		OnEvent:      forward,
	})

	final, err := a.Run(ctx, m.prompt(rec.ID, spec))
	if err != nil {
		return "", usage, steps, err
	}
	text := final
	if m.opts.Hooks != nil {
		if out := m.opts.Hooks.AfterDelegate(spec.Task, text); out != "" {
			text = out
		}
	}
	usage = a.Session().Usage
	return text, usage, steps, nil
}

func (m *Manager) resolveProvider(spec Spec) provider.Provider {
	if (spec.Provider != "" || spec.Model != "") && m.opts.Providers != nil {
		slug := spec.Provider
		if slug == "" {
			slug = m.fallbackSlug()
		}
		if p, err := m.opts.Providers(slug, spec.Model); err == nil {
			return p
		}
		return nil
	}
	return m.opts.Fallback
}

func (m *Manager) fallbackSlug() string {
	if m.opts.Fallback == nil {
		return ""
	}
	// Model-only overrides ride on whatever the root provider is.
	if l := m.opts.Fallback.Label(); l != "" {
		if slug, _, ok := strings.Cut(l, "/"); ok {
			return slug
		}
	}
	return ""
}

// prompt builds the delegate's one-task-one-report mindset. The report rule
// is part of the task text because it is the only thing that crosses back over
// the context wall.
func (m *Manager) prompt(idv string, spec Spec) string {
	var b strings.Builder
	b.WriteString(spec.Task)
	if spec.role != "" {
		b.WriteString("\n\n")
		b.WriteString(spec.role)
	}
	b.WriteString(fmt.Sprintf(
		"\n\n(You are delegate %s, depth %d of a delegation tree. Rules: verify before claiming to be done; keep your own context lean — page detail with out(handle) instead of re-running commands; do not narrate for the user, you are not visible to them. Final requirement: your last message is your report and MUST be a compact summary of outcome, key evidence (paths, line numbers, exit codes) and anything left unfinished — at most ~%d tokens. The delegator sees only that summary.)",
		idv, spec.Depth, orDefault(spec.OutputTokens, m.opts.OutputTokens)))
	return b.String()
}

// registryPredicate filters the root registry into a delegate's toolset.
func registryPredicate(spec Spec) func(t tools.Tool) bool {
	deny := map[string]bool{}
	for _, d := range spec.Deny {
		deny[d] = true
	}
	allow := map[string]bool{}
	for _, a := range spec.Tools {
		allow[a] = true
	}
	return func(t tools.Tool) bool {
		switch t.Name {
		case "delegate", "delegates", "delegate_cancel":
			return false // re-attached fresh by Attach when spawning is allowed
		}
		if deny[t.Name] {
			return false
		}
		if len(spec.Tools) > 0 && !allow[t.Name] {
			return false
		}
		if spec.ReadOnly && !t.ReadOnly {
			return false
		}
		return true
	}
}

// ---------------------------------------------------------------------------
// Waiting, cancelling, listing
// ---------------------------------------------------------------------------

// Wait blocks until the delegate finishes or ctx ends.
// evictOldFinished drops the oldest completed delegate records (and their
// channels / cancel funcs / spawn counts) once the manager holds more than
// cap. Running or queued delegates are always kept; only terminal entries
// are candidates — the oldest by Ended. Caller is expected to hold m.mu.
func (m *Manager) evictOldFinished(cap int) {
	type slot struct {
		id    string
		ended time.Time
	}
	var done []slot
	for id, rec := range m.records {
		if rec.Status != "done" && rec.Status != "failed" && rec.Status != "canceled" && rec.Status != "orphaned" {
			continue
		}
		t := rec.Ended
		if t.IsZero() {
			t = rec.Started
		}
		if t.IsZero() {
			t = rec.Created
		}
		done = append(done, slot{id: id, ended: t})
	}
	if len(done) <= cap {
		return
	}
	sort.Slice(done, func(i, j int) bool { return done[i].ended.Before(done[j].ended) })
	for i := 0; i < len(done)-cap; i++ {
		id := done[i].id
		delete(m.records, id)
		delete(m.done, id)
		delete(m.live, id)
		delete(m.spawnN, id)
	}
}

func (m *Manager) Wait(ctx context.Context, id string) (*Record, error) {
	m.mu.Lock()
	ch, ok := m.done[id]
	rec := m.records[id]
	m.mu.Unlock()
	if !ok {
		// Not from this process: read the last known state from the journal.
		if rec2 := m.fromStore(id); rec2 != nil {
			return rec2, nil
		}
		return nil, fmt.Errorf("unknown delegate %q", id)
	}
	select {
	case <-ch:
		m.mu.Lock()
		r := m.records[id]
		m.mu.Unlock()
		return r.clone(), nil
	case <-ctx.Done():
		return rec.clone(), ctx.Err()
	}
}

// WaitMany blocks until each active delegate finishes or the timeout passes.
// Finished delegates resolve immediately; the return order matches ids.
func (m *Manager) WaitMany(ctx context.Context, ids []string) []*Record {
	out := make([]*Record, len(ids))
	var wg sync.WaitGroup
	for i, idv := range ids {
		wg.Add(1)
		go func(i int, idv string) {
			defer wg.Done()
			r, _ := m.Wait(ctx, idv)
			out[i] = r
		}(i, idv)
	}
	wg.Wait()
	return out
}

// Get returns the current record for an id, merging the on-disk journal for
// delegates not spawned by this process.
func (m *Manager) Get(idv string) *Record {
	m.mu.Lock()
	r := m.records[idv]
	m.mu.Unlock()
	if r != nil {
		return r.clone()
	}
	return m.fromStore(idv)
}

// List returns live records newest first; missing ids are filled from disk so
// orphaned delegates from past processes still show up.
func (m *Manager) List() []*Record {
	m.mu.Lock()
	out := make([]*Record, 0, len(m.records))
	for _, r := range m.records {
		out = append(out, r.clone())
	}
	m.mu.Unlock()
	if m.opts.Store != nil {
		for _, dr := range m.opts.Store.ListDelegates(0) {
			m.mu.Lock()
			_, known := m.records[dr.ID]
			m.mu.Unlock()
			if !known {
				out = append(out, &Record{DelegateRecord: dr})
			}
		}
	}
	return out
}

// Active returns delegates that are queued or running.
func (m *Manager) Active() []*Record {
	var out []*Record
	for _, r := range m.List() {
		if r.Status == "queued" || r.Status == "running" {
			out = append(out, r)
		}
	}
	return out
}

// Cancel asks one delegate to stop. Running delegates get their context
// cancelled; queued ones are flagged before ever starting.
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	r := m.records[id]
	if r == nil {
		m.mu.Unlock()
		return fmt.Errorf("unknown delegate %q", id)
	}
	if r.Status != "queued" && r.Status != "running" {
		// The tool contract promises finished delegates cannot be cancelled;
		// quietly flagging them was a success message that did nothing.
		m.mu.Unlock()
		return fmt.Errorf("delegate %s is already %s — only queued or running delegates can be cancelled", id, r.Status)
	}
	r.canceled.Store(true)
	cancel := m.live[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// CancelAll stops everything this process spawned.
func (m *Manager) CancelAll() {
	for _, r := range m.List() {
		m.mu.Lock()
		rec := m.records[r.ID]
		cancel := m.live[r.ID]
		if rec != nil {
			rec.canceled.Store(true)
		}
		m.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
}

// Stop cancels everything this process spawned and waits briefly for the
// goroutines to notice.
func (m *Manager) Stop() {
	m.CancelAll()
	deadline := time.Now().Add(3 * time.Second)
	for {
		m.mu.Lock()
		n := m.running + m.queued
		m.mu.Unlock()
		if n == 0 || time.Now().After(deadline) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// rootOf walks up the parent chain to the root session id.
func (m *Manager) rootOf(parentID string) string {
	// A cyclic parent chain (corrupt journal or migration artifact) must
	// terminate instead of spinning forever. The last unvisited link is
	// returned as its own root.
	seen := map[string]bool{}
	for p := parentID; p != ""; {
		if seen[p] {
			return p
		}
		seen[p] = true
		m.mu.Lock()
		rec := m.records[p]
		m.mu.Unlock()
		if rec == nil || rec.Parent == "" {
			return p
		}
		p = rec.Parent
	}
	return parentID
}

// fromStore loads one record from the persistent journal.
func (m *Manager) fromStore(id string) *Record {
	if m.opts.Store == nil {
		return nil
	}
	dr, err := m.opts.Store.LoadDelegate(id)
	if err != nil {
		return nil
	}
	return &Record{DelegateRecord: dr}
}

// save persists a record; the journal never blocks a run, it just warns by
// silence (the event carries the state change anyway).
func (m *Manager) save(r *Record) {
	if m.opts.Store == nil {
		return
	}
	if err := m.opts.Store.SaveDelegate(r.DelegateRecord); err != nil {
		// The journal is the recovery path after a crash; a silent write
		// failure strands the delegate. Keep it observable.
		m.journalMu.Lock()
		m.journalErr = err
		m.journalMu.Unlock()
	}
}

// JournalHealth reports the most recent journal write failure, or nil when
// the journal is healthy. Callers (status tools, shutdown paths) can surface
// this so a persistence problem never goes unnoticed.
func (m *Manager) JournalHealth() error {
	m.journalMu.Lock()
	defer m.journalMu.Unlock()
	return m.journalErr
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	// Rune-safe: byte slicing would cut multi-byte task text mid-character.
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

func orDefault(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}

func orDefaultStr(v, d string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return d
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled: delegator exited"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout reached before the task finished"
	}
	return err.Error()
}

// optsMap renders a Spec as the rewrite-friendly option map hooks receive.
func (s Spec) optsMap() (map[string]any, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	if s.TimeoutSecs > 0 {
		out["timeout"] = s.TimeoutSecs
	}
	return out, nil
}
