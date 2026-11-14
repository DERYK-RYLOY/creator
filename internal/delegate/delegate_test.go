package delegate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DERYK-RYLOY/creator/internal/board"
	"github.com/DERYK-RYLOY/creator/internal/policy"
	"github.com/DERYK-RYLOY/creator/internal/provider"
	"github.com/DERYK-RYLOY/creator/internal/runner"
	"github.com/DERYK-RYLOY/creator/internal/store"
	"github.com/DERYK-RYLOY/creator/internal/tools"
)

// stubHooks implements tools.Hooks for manager-gate tests.
type stubHooks struct {
	denyDelegateTask string
	rewriteToolArgs  map[string]string // tool -> replacement args JSON
	denied           []string          // observed BeforeDelegate tasks
	mu               sync.Mutex
}

func (h *stubHooks) BeforeTool(name string, args json.RawMessage) (json.RawMessage, error) {
	if repl, ok := h.rewriteToolArgs[name]; ok {
		return json.RawMessage(repl), nil
	}
	return args, nil
}

func (h *stubHooks) AfterTool(name, result string, err error) string { return "" }

func (h *stubHooks) BeforeDelegate(opts map[string]any) (map[string]any, error) {
	task, _ := opts["task"].(string)
	h.mu.Lock()
	h.denied = append(h.denied, task)
	h.mu.Unlock()
	if h.denyDelegateTask != "" && task == h.denyDelegateTask {
		return nil, stHookErr
	}
	return nil, nil
}

func (h *stubHooks) AfterDelegate(task, summary string) string { return "" }

type stHookType struct{ msg string }

func (e stHookType) Error() string { return e.msg }

var stHookErr = stHookType{msg: "not on my watch"}

// scriptEcho plays recorded responses first, then answers every further call
// with an echo of the last tool observation ("REPORT: ..."), with fixed usage
// so token accounting is testable. An optional gate blocks Chat, which is how
// concurrency and cancellation get their determinism. In-flight Chat calls
// are counted so tests can assert scheduler caps.
type scriptEcho struct {
	mu      sync.Mutex
	steps   []provider.Response
	gate    chan struct{}
	running int32
	maxRun  int32

	usage provider.Usage
}

func (p *scriptEcho) Label() string { return "fake/fake" }
func (p *scriptEcho) Model() string { return "fake-model" }

func (p *scriptEcho) Chat(ctx context.Context, req provider.Request) (provider.Response, error) {
	n := atomic.AddInt32(&p.running, 1)
	for {
		h := atomic.LoadInt32(&p.maxRun)
		if n <= h || atomic.CompareAndSwapInt32(&p.maxRun, h, n) {
			break
		}
	}
	defer atomic.AddInt32(&p.running, -1)

	p.mu.Lock()
	var next provider.Response
	have := false
	if len(p.steps) > 0 {
		next = p.steps[0]
		p.steps = p.steps[1:]
		have = true
	}
	p.mu.Unlock()

	if !have && p.gate == nil {
		return p.echo(req), nil
	}
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return provider.Response{}, ctx.Err()
		case <-time.After(5 * time.Second):
			return provider.Response{}, context.DeadlineExceeded
		}
	}
	if have {
		return next, nil
	}
	return p.echo(req), nil
}

// echo builds the final answer from the newest tool observation, so a test can
// require that the delegate produced evidence before reporting.
func (p *scriptEcho) echo(req provider.Request) provider.Response {
	var last string
	for _, m := range req.Messages {
		if m.Role == provider.RoleTool {
			last = m.Text
		}
	}
	return provider.Response{Text: "REPORT: " + last, Usage: p.usage}
}

// record replays a script step: one tool call.
func toolCallResponse(name string, args map[string]any) provider.Response {
	b, _ := json.Marshal(args)
	return provider.Response{
		ToolCalls: []provider.ToolCall{{
			ID:   "call_1",
			Name: name,
			Args: b,
		}},
	}
}

// testRig is a full delegation stack on a temp dir.
type testRig struct {
	st       *store.Store
	registry *tools.Registry
	root     string
	engine   *policy.Engine
}

func newRig(t *testing.T) *testRig {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("the note body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := runner.NewManager(st)
	t.Cleanup(func() { jobs.StopAll(); st.Close() })
	engine := policy.NewEngine("yolo", true, false, strings.NewReader(""), io.Discard)
	env := &tools.Env{Workspace: root, Store: st, Jobs: jobs, Policy: engine, Session: "s_root", Budget: 400}
	reg := tools.New(env)
	return &testRig{st: st, registry: reg, root: root, engine: engine}
}

func newManager(t *testing.T, rig *testRig, prov provider.Provider, opts *Options) *Manager {
	o := Options{
		Workspace: rig.root,
		Store:     rig.st,
		Registry:  rig.registry,
		Fallback:  prov,
		ShellName: "testshell",
	}
	if opts != nil {
		o.MaxDepth = opts.MaxDepth
		o.Concurrency = opts.Concurrency
		o.MaxPerRoot = opts.MaxPerRoot
		o.MaxTotalPerRoot = opts.MaxTotalPerRoot
		o.MaxSteps = opts.MaxSteps
		o.OutputTokens = opts.OutputTokens
		o.Hooks = opts.Hooks
	}
	m := New(rig.st, o)
	if err := m.Attach(rig.registry, 0, "s_root"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Stop)
	return m
}

func TestRootOfTerminatesOnCyclicParents(t *testing.T) {
	rig := newRig(t)
	prov := &scriptEcho{}
	m := newManager(t, rig, prov, nil)
	m.mu.Lock()
	m.records["cyc-a"] = &Record{DelegateRecord: store.DelegateRecord{ID: "cyc-a", Parent: "cyc-b"}}
	m.records["cyc-b"] = &Record{DelegateRecord: store.DelegateRecord{ID: "cyc-b", Parent: "cyc-a"}}
	m.mu.Unlock()

	done := make(chan string, 1)
	go func() { done <- m.rootOf("cyc-a") }()
	select {
	case id := <-done:
		if id == "" {
			t.Fatal("rootOf returned empty id")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rootOf hung on cyclic parents")
	}
}

func TestDelegateRunsToolsAndReturnsOnlyReport(t *testing.T) {
	rig := newRig(t)
	prov := &scriptEcho{usage: provider.Usage{In: 91, Out: 13}}
	prov.steps = []provider.Response{toolCallResponse("read", map[string]any{"path": "note.txt"})}
	m := newManager(t, rig, prov, nil)

	stop := func() {}
	_, stop = m.Subscribe(64)
	defer stop()

	rec, err := m.Spawn(context.Background(), "s_root", Spec{Task: "read the note and report"})
	if err != nil {
		t.Fatal(err)
	}
	rec, err = m.Wait(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != "done" {
		t.Fatalf("status = %q (err %s): expected done", rec.Status, rec.Error)
	}
	if !strings.Contains(rec.Result, "the note body") {
		t.Fatalf("report missing tool evidence: %q", rec.Result)
	}
	if rec.Steps < 1 {
		t.Fatalf("steps = %d, want >= 1", rec.Steps)
	}
	if rec.Usage.In+rec.Usage.Out == 0 {
		t.Fatalf("usage not recorded: %+v", rec.Usage)
	}
	if rec.Handle != rec.ID {
		t.Fatalf("handle = %q, want the id %q", rec.Handle, rec.ID)
	}
	// The full report is an artifact under the delegate's own handle.
	raw, err := rig.st.ReadArtifact(rec.ID)
	if err != nil || !strings.Contains(raw, "the note body") {
		t.Fatalf("artifact missing: %v %q", err, raw)
	}

	// Event journal: spawn, start, tool evidence, done — persisted in order.
	var kinds []string
	for _, ev := range rig.st.ListDelegateEvents(0, 0) {
		if ev.Kind == "spawn" || ev.Kind == "start" || ev.Kind == "tool" || ev.Kind == "done" {
			kinds = append(kinds, ev.Kind)
		}
	}
	if !contains(kinds, "spawn") || !contains(kinds, "start") || !contains(kinds, "done") {
		t.Fatalf("journal missing lifecycle events: %v", kinds)
	}
	if !contains(kinds, "tool") {
		t.Fatalf("journal missing tool events: %v", kinds)
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func TestDelegateDenyListBlocksTool(t *testing.T) {
	rig := newRig(t)
	prov := &scriptEcho{}
	prov.steps = []provider.Response{toolCallResponse("read", map[string]any{"path": "note.txt"})}
	m := newManager(t, rig, prov, nil)

	rec, err := m.Spawn(context.Background(), "s_root", Spec{
		Task: "probe", Deny: []string{"read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ = m.Wait(context.Background(), rec.ID)
	if rec.Status != "done" {
		t.Fatalf("status %q: %s", rec.Status, rec.Error)
	}
	if !strings.Contains(rec.Result, "unknown tool") {
		t.Fatalf("denied tool did not produce an error result: %q", rec.Result)
	}
}

func TestDelegateReadOnlySpawnCannotWrite(t *testing.T) {
	rig := newRig(t)
	prov := &scriptEcho{}
	prov.steps = []provider.Response{toolCallResponse("write", map[string]any{
		"path": "evil.txt", "content": "boom",
	})}
	m := newManager(t, rig, prov, nil)

	rec, err := m.Spawn(context.Background(), "s_root", Spec{Task: "try write", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ = m.Wait(context.Background(), rec.ID)
	if rec.Status != "done" {
		t.Fatalf("status %q: %s", rec.Status, rec.Error)
	}
	if !strings.Contains(rec.Result, "unknown tool") {
		t.Fatalf("read-only spawn used write: %q", rec.Result)
	}
	if _, err := os.Stat(filepath.Join(rig.root, "evil.txt")); !os.IsNotExist(err) {
		t.Fatalf("read-only delegate wrote a file")
	}
}

func TestDelegateDepthLimit(t *testing.T) {
	rig := newRig(t)
	prov := &scriptEcho{}
	// Root spawns a delegate (depth 1, allowed at MaxDepth=1); that delegate
	// tries to spawn again and must be refused by the depth rule.
	prov.steps = []provider.Response{toolCallResponse("delegate", map[string]any{
		"task": "inner task", "can_spawn": true,
	})}
	m := newManager(t, rig, prov, &Options{MaxDepth: 1})

	rec, err := m.Spawn(context.Background(), "s_root", Spec{Task: "outer task", CanSpawn: true})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ = m.Wait(context.Background(), rec.ID)
	if rec.Status != "done" {
		t.Fatalf("status %q: %s", rec.Status, rec.Error)
	}
	if !strings.Contains(rec.Result, "depth limit") {
		t.Fatalf("depth denial missing from the inner delegate run: %q", rec.Result)
	}
}

func TestDelegateConcurrencyCap(t *testing.T) {
	rig := newRig(t)
	gate := make(chan struct{})
	prov := &scriptEcho{gate: gate}
	m := newManager(t, rig, prov, &Options{Concurrency: 2})

	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		rec, err := m.Spawn(context.Background(), "s_root", Spec{Task: "parallel work"})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = rec.ID
	}
	// Prove the cap: after the first two take both slots, the third must be
	// queued (never reaching Chat) until a slot frees.
	deadline := time.Now().Add(2 * time.Second)
	sawQueued := false
	for time.Now().Before(deadline) {
		q := false
		for _, id := range ids {
			if r := m.Get(id); r != nil && r.Status == "queued" {
				q = true
			}
		}
		if q && atomic.LoadInt32(&prov.maxRun) <= 2 {
			sawQueued = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !sawQueued {
		t.Fatalf("third delegate never queued (maxRun=%d, statuses=%v)", atomic.LoadInt32(&prov.maxRun), delegateStatuses(m, ids))
	}
	if got := atomic.LoadInt32(&prov.maxRun); got > 2 {
		t.Fatalf("concurrency cap broken: %d ran simultaneously", got)
	}
	close(gate)
	for _, id := range ids {
		r, err := m.Wait(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != "done" {
			t.Fatalf("delegate %s status %q (err %s)", id, r.Status, r.Error)
		}
	}
	if got := atomic.LoadInt32(&prov.maxRun); got > 2 {
		t.Fatalf("concurrency cap broken after release: %d ran simultaneously", got)
	}
}

func delegateStatuses(m *Manager, ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if r := m.Get(id); r != nil {
			out = append(out, r.ID[10:12]+":"+r.Status)
		}
	}
	return out
}

func TestDelegateCancelWhileBlocked(t *testing.T) {
	rig := newRig(t)
	gate := make(chan struct{})
	prov := &scriptEcho{gate: gate}
	m := newManager(t, rig, prov, nil)

	rec, err := m.Spawn(context.Background(), "s_root", Spec{Task: "long haul"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := m.Cancel(rec.ID); err != nil {
		t.Fatal(err)
	}
	r, _ := m.Wait(context.Background(), rec.ID)
	if r.Status != "canceled" {
		t.Fatalf("status = %q, want canceled", r.Status)
	}
	// Unblocking must not resurrect it.
	close(gate)
	time.Sleep(50 * time.Millisecond)
	r = m.Get(rec.ID)
	if r2 := m.Get(rec.ID); r2.Status != "canceled" {
		t.Fatalf("canceled delegate moved to %q", r2.Status)
	}
	_ = r
}

func TestRefusalDoesNotConsumeLifetimeCap(t *testing.T) {
	rig := newRig(t)
	gate := make(chan struct{})
	prov := &scriptEcho{gate: gate}
	m := newManager(t, rig, prov, &Options{MaxPerRoot: 1, MaxTotalPerRoot: 2})

	a, err := m.Spawn(context.Background(), "s_root", Spec{Task: "first"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond) // let the first delegate take the slot

	// Refused by the ACTIVE cap. This refusal must not eat a lifetime slot.
	_, err = m.Spawn(context.Background(), "s_root", Spec{Task: "second"})
	if err == nil || !strings.Contains(err.Error(), "active delegate cap") {
		t.Fatalf("second spawn error = %v, want the active-cap message", err)
	}
	close(gate)
	if r, err := m.Wait(context.Background(), a.ID); err != nil || r.Status != "done" {
		t.Fatalf("first delegate should finish, got %q (%v)", r.Status, err)
	}
	// One lifetime slot remains (2 − 1 used by first) — the refused second
	// must not have consumed it, so exactly one more spawn is possible.
	if _, err := m.Spawn(context.Background(), "s_root", Spec{Task: "third"}); err != nil {
		t.Fatalf("third spawn should be accepted, budget must not be drained by refusals: %v", err)
	}
	if _, err := m.Spawn(context.Background(), "s_root", Spec{Task: "fourth"}); err == nil {
		t.Fatal("fourth spawn should hit the lifetime cap")
	}
}

func TestSpawnCaps(t *testing.T) {
	rig := newRig(t)
	prov := &scriptEcho{}
	m := newManager(t, rig, prov, &Options{MaxPerRoot: 1})

	one, err := m.Spawn(context.Background(), "s_root", Spec{Task: "a"})
	if err != nil {
		t.Fatal(err)
	}
	capErr := struct{ msg string }{}
	_, spawnErr := m.Spawn(context.Background(), "s_root", Spec{Task: "b"})
	if spawnErr == nil {
		t.Fatal("expected active-cap refusal")
	}
	capErr.msg = spawnErr.Error()
	if !strings.Contains(capErr.msg, "cap") {
		t.Fatalf("wrong cap message: %q", capErr.msg)
	}

	m.Stop()
	// Lifetime cap on a fresh manager.
	m2rig := newRig(t)
	blocked := &scriptEcho{gate: make(chan struct{})}
	m2 := newManager(t, m2rig, blocked, &Options{MaxTotalPerRoot: 1})
	if _, err := m2.Spawn(context.Background(), "s_root", Spec{Task: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Spawn(context.Background(), "s_root", Spec{Task: "two"}); err == nil {
		t.Fatal("expected lifetime cap refusal")
	}
	_ = one
}

func TestHooksGateDelegation(t *testing.T) {
	rig := newRig(t)
	prov := &scriptEcho{}
	hs := &stubHooks{denyDelegateTask: "forbidden"}
	m := newManager(t, rig, prov, &Options{Hooks: hs})

	if _, err := m.Spawn(context.Background(), "s_root", Spec{Task: "forbidden"}); err == nil {
		t.Fatal("expected hook denial")
	} else if !strings.Contains(err.Error(), "refused by hook") {
		t.Fatalf("wrong denial message: %v", err)
	}

	ok, err := m.Spawn(context.Background(), "s_root", Spec{Task: "allowed"})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait(context.Background(), ok.ID)
}

func TestHooksRewriteDelegateTask(t *testing.T) {
	rig := newRig(t)
	prov := &scriptEcho{}
	// A hook appends a marker to the task; the spawned session's first user
	// message must carry it.
	hs := rewriteDelegate{marker: "TAG42"}
	m := newManager(t, rig, prov, &Options{Hooks: hs})

	rec, err := m.Spawn(context.Background(), "s_root", Spec{Task: "do the thing"})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := m.Wait(context.Background(), rec.ID)
	if r.Status != "done" {
		t.Fatalf("status %q: %s", r.Status, r.Error)
	}
	sess, err := rig.st.LoadSession(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Messages) == 0 || !strings.Contains(sess.Messages[0].Text, "TAG42") {
		t.Fatalf("rewritten task did not reach the delegate prompt: %+v", sess.Messages)
	}
}

// rewriteDelegate rewrites every delegate task to include a marker.
type rewriteDelegate struct {
	marker string
}

func (h rewriteDelegate) BeforeTool(name string, args json.RawMessage) (json.RawMessage, error) {
	return args, nil
}

func (h rewriteDelegate) AfterTool(name, result string, err error) string { return "" }

func (h rewriteDelegate) BeforeDelegate(opts map[string]any) (map[string]any, error) {
	task, _ := opts["task"].(string)
	opts["task"] = task + " [" + h.marker + "]"
	return opts, nil
}

func (h rewriteDelegate) AfterDelegate(task, summary string) string { return "" }

func TestManagerMarksOrphansFromDisk(t *testing.T) {
	rig := newRig(t)
	stale := store.DelegateRecord{
		ID: "d_stale", Task: "from a dead process", Status: "running", Depth: 1,
		OwnerPID: 2147483647, // almost certainly unused; OrphanDelegates should treat it as dead
		Created:  time.Now().Add(-49 * time.Hour),
	}
	if err := rig.st.SaveDelegate(stale); err != nil {
		t.Fatal(err)
	}
	prov := &scriptEcho{}
	_ = New(rig.st, Options{Workspace: rig.root, Fallback: prov})
	got, err := rig.st.LoadDelegate("d_stale")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "orphaned" {
		t.Fatalf("orphan not marked: %q", got.Status)
	}
	if !strings.Contains(got.Error, "interrupted") {
		t.Fatalf("orphan reason missing: %q", got.Error)
	}
}

func TestSpecOptMapRoundTrip(t *testing.T) {
	spec := Spec{Task: "x", Name: "n", ReadOnly: true, MaxSteps: 5, OutputTokens: 42}
	got, err := spec.optsMap()
	if err != nil {
		t.Fatal(err)
	}
	if got["task"] != "x" || got["name"] != "n" || got["read_only"] != true || got["max_steps"] != float64(5) || got["output_tokens"] != float64(42) {
		t.Fatalf("optsMap mismatch: %+v", got)
	}
}

func TestDelegateClosesBoardTask(t *testing.T) {
	rig := newRig(t)
	brd := board.New(rig.st, board.Options{
		Root:   rig.root,
		Jobs:   runner.NewManager(rig.st),
		Policy: rig.engine,
	})
	task, _, err := brd.Add(board.AddArgs{Title: "fix the loop", Verify: "echo ok"})
	if err != nil {
		t.Fatalf("board add: %v", err)
	}

	prov := &scriptEcho{}
	opts := Options{
		Workspace: rig.root,
		Store:     rig.st,
		Registry:  rig.registry,
		Fallback:  prov,
		Providers: func(slug, model string) (provider.Provider, error) {
			// The rig has a provider resolver; only real slugs resolve.
			if slug == "" {
				return prov, nil
			}
			return nil, errors.New("no such backend " + slug)
		},
		ShellName: "testshell",
		Tasks:     brd,
	}
	m := New(rig.st, opts)
	if err := m.Attach(rig.registry, 0, "s_root"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Stop)

	ctx := context.Background()
	rec, err := m.Spawn(ctx, "s_root", Spec{Task: "make the loop green", TaskID: task.ID})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	rec, err = m.Wait(ctx, rec.ID)
	if err != nil || rec.Status != "done" {
		t.Fatalf("delegate should finish done, got %s (%v)", rec.Status, err)
	}
	// The oracle was still applied by the board even though the delegate closed it.
	loaded, err := rig.st.LoadTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != board.StatusDone {
		t.Fatalf("task should be done, got %s", loaded.Status)
	}
	var claim, oracle bool
	for _, ev := range loaded.Evidence {
		if strings.Contains(ev.Text, "delegate report") {
			claim = true
		}
		if ev.Kind == "cmd" && strings.Contains(ev.Text, "exited 0") {
			oracle = true
		}
	}
	if !claim || !oracle {
		t.Fatalf("expected delegate receipt and oracle receipt, got %+v", loaded.Evidence)
	}

	// A delegate that cannot even start must FLAG the bound task instead of
	// leaving the plan stranded.
	other, _, err := brd.Add(board.AddArgs{Title: "the stranded step"})
	if err != nil {
		t.Fatalf("board add 2: %v", err)
	}
	rec2, err := m.Spawn(ctx, "s_root", Spec{Task: "impossible", TaskID: other.ID, Provider: "ghost-provider"})
	if err != nil {
		t.Fatalf("spawn 2: %v", err)
	}
	rec2, err = m.Wait(ctx, rec2.ID)
	if err != nil || rec2.Status != "failed" {
		t.Fatalf("ghost provider should fail, got %s (%v)", rec2.Status, err)
	}
	loaded2, err := rig.st.LoadTask(other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded2.Status != board.StatusPending || len(loaded2.Notes) != 1 {
		t.Fatalf("failed delegate must flag the task, got %s %+v", loaded2.Status, loaded2.Notes)
	}
}

// A toolset profile matching zero tools must fail fast with a clear error —
// never burn a delegate's whole budget running zero-tool steps.
func TestDelegateRefusesEmptyToolset(t *testing.T) {
	rig := newRig(t)
	prov := &scriptEcho{}
	m := newManager(t, rig, prov, nil)

	rec, err := m.Spawn(context.Background(), "s_root", Spec{Task: "do the thing", Tools: []string{"definitely_not_a_tool_anywhere"}})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	r, err := m.Wait(context.Background(), rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "failed" {
		t.Fatalf("delegate with no matching tools must fail, got %q", r.Status)
	}
	if !strings.Contains(r.Error, "matches no tool") {
		t.Fatalf("error should name the cause, got %q", r.Error)
	}
	if got := atomic.LoadInt32(&prov.maxRun); got != 0 {
		t.Fatalf("provider must never be consulted for an empty toolset, got %d calls", got)
	}
}

// A delegate bound to a board task that never starts (blocked behind a busy
// slot, then canceled) must flag the task instead of stranding it forever —
// this was the missing half of the bound-task closure contract.
func TestDelegateCancelBeforeStartFlagsBoundTask(t *testing.T) {
	rig := newRig(t)
	brd := board.New(rig.st, board.Options{
		Root:   rig.root,
		Jobs:   runner.NewManager(rig.st),
		Policy: rig.engine,
	})
	blockedTask, _, err := brd.Add(board.AddArgs{Title: "the preempted step", Verify: "echo ok"})
	if err != nil {
		t.Fatal(err)
	}

	gate := make(chan struct{})
	prov := &scriptEcho{gate: gate}
	opts := Options{
		Workspace: rig.root,
		Store:     rig.st,
		Registry:  rig.registry,
		Fallback:  prov,
		Providers: func(slug, model string) (provider.Provider, error) {
			if slug == "" {
				return prov, nil
			}
			return nil, errors.New("no such backend " + slug)
		},
		ShellName:   "testshell",
		Concurrency: 1,
		Tasks:       brd,
	}
	m := New(rig.st, opts)
	if err := m.Attach(rig.registry, 0, "s_root"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		m.Stop()
	})

	blocker, err := m.Spawn(context.Background(), "s_root", Spec{Task: "long haul"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond) // let the blocker take the slot

	bound, err := m.Spawn(context.Background(), "s_root", Spec{Task: "short haul", TaskID: blockedTask.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Cancel(bound.ID); err != nil {
		t.Fatal(err)
	}
	r, err := m.Wait(context.Background(), bound.ID)
	if err != nil || r.Status != "canceled" {
		t.Fatalf("bound delegate should be canceled, got %q (%v)", r.Status, err)
	}
	loaded, err := rig.st.LoadTask(blockedTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range loaded.Notes {
		if strings.Contains(strings.ToLower(n.Text), "canceled") {
			found = true
		}
	}
	if !found {
		t.Fatalf("bound task must be flagged on early cancel, got notes %+v", loaded.Notes)
	}

	// Let the blocker finish so the manager exits cleanly.
	close(gate)
	r, err = m.Wait(context.Background(), blocker.ID)
	if err != nil {
		t.Fatal(err)
	}
}
