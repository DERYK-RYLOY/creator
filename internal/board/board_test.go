package board

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creatorcli/creator/internal/policy"
	"github.com/creatorcli/creator/internal/runner"
	"github.com/creatorcli/creator/internal/store"
	"github.com/creatorcli/creator/internal/tools"
)

// newRig builds a board over a temp store with a real runner and a
// non-interactive balanced policy engine.
func newRig(t *testing.T) (*Board, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	b := New(st, Options{
		Root:   ".",
		Jobs:   runner.NewManager(st),
		Policy: policy.NewEngine("", false, false, nil, nil),
		Now:    func() time.Time { return time.Now() },
	})
	return b, st
}

func TestAddStartDoneWithoutOracle(t *testing.T) {
	b, _ := newRig(t)
	rec, _, err := b.Add(AddArgs{Title: "write the digest", Doing: "writing the digest"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if rec.Status != StatusPending || rec.ID != "t1" {
		t.Fatalf("unexpected record %+v", rec)
	}

	// No evidence: refused.
	if _, _, err := b.Done(context.Background(), DoneArgs{ID: rec.ID}); err == nil {
		t.Fatal("done without evidence must be refused")
	}

	if _, _, err := b.Start(rec.ID, false); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, _, err := b.Done(context.Background(), DoneArgs{ID: rec.ID, Evidence: "file digest.txt written, 12 lines"}); err != nil {
		t.Fatalf("done: %v", err)
	}
	loaded, err := ListOne(b, rec.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Status != StatusDone || len(loaded.Evidence) != 1 {
		t.Fatalf("unexpected close state %+v", loaded)
	}
	// Completing after starting leaves no drift note, but the cancelled/parallel
	// machinery must not have fired either.
	if len(loaded.Notes) != 0 {
		t.Fatalf("unexpected notes: %+v", loaded.Notes)
	}
}

func TestAutoCloseByVerifyDirAndSessionGates(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mk := func(session string) *Board {
		b := New(st, Options{Root: root, Jobs: runner.NewManager(st), Now: time.Now})
		b.session = session
		return b
	}
	bA, bB := mk("sess-a"), mk("sess-b")

	rec, _, err := bA.Add(AddArgs{Title: "green build", Doing: "building", Verify: "go test ./..."})
	if err != nil {
		t.Fatal(err)
	}
	// Same session, WRONG cwd → not closed.
	closed, err := bA.AutoCloseByVerify("go test ./...", "j1", filepath.Join(root, "elsewhere"))
	if err != nil || len(closed) != 0 {
		t.Fatalf("wrong cwd must not close: closed=%v err=%v", closed, err)
	}
	// Empty cwd → not closed (cannot prove the oracle ran).
	closed, err = bA.AutoCloseByVerify("go test ./...", "j1b", "")
	if err != nil || len(closed) != 0 {
		t.Fatalf("empty cwd must not close: closed=%v err=%v", closed, err)
	}
	// Right cwd + same session → closed.
	closed, err = bA.AutoCloseByVerify("go test ./...", "j2", root)
	if err != nil || len(closed) != 1 {
		t.Fatalf("right cwd+session must close: closed=%v err=%v", closed, err)
	}
	got, err := st.LoadTask(rec.ID)
	if err != nil || got.Status != StatusDone {
		t.Fatalf("task must be done in bA: status=%s err=%v", got.Status, err)
	}

	// Session B's task must not close on session A's exit-0.
	recB, _, err := bB.Add(AddArgs{Title: "green build B", Doing: "building", Verify: "go test ./..."})
	if err != nil {
		t.Fatal(err)
	}
	closed, err = bA.AutoCloseByVerify("go test ./...", "j3", root)
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 0 {
		t.Fatalf("session A's job must not close session B's task: %v", closed)
	}
	got, err = st.LoadTask(recB.ID)
	if err != nil || got.Status == StatusDone {
		t.Fatalf("session B's task must remain open: status=%s err=%v", got.Status, err)
	}
	// Session B's own exit-0 in its own root closes it.
	closed, err = bB.AutoCloseByVerify("go test ./...", "j4", root)
	if err != nil || len(closed) != 1 {
		t.Fatalf("session B's own oracle must close its task: closed=%v err=%v", closed, err)
	}
}

// The OnEvent hook is called by the board on done. A hook that re-enters the
// board (flag the task to bring it to the user's attention) must not deadlock
// on the board's non-re-entrant mutex — the auto-close path used to emit
// while holding it.
func TestAutoCloseByVerifyOnEventMayReenterBoard(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	hook := make(chan struct{}, 1)
	var onetime atomic.Bool
	var b *Board
	b = New(st, Options{
		Root: root,
		Jobs: runner.NewManager(st),
		Now:  time.Now,
		OnEvent: func(ev store.DelegateEvent) {
			if ev.Kind == "task" && ev.Status == "done" && strings.HasPrefix(ev.Detail, "auto") && onetime.CompareAndSwap(false, true) {
				// Re-enter the board from inside the OnEvent callback.
				_, _, err := b.Flag(ev.ID, "arrived via re-entrant hook")
				if err == nil {
					hook <- struct{}{}
				}
			}
		},
	})
	rec, _, err := b.Add(AddArgs{Title: "green build", Doing: "building", Verify: "go test ./..."})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan []string, 1)
	go func() {
		c, _ := b.AutoCloseByVerify("go test ./...", "job-1", root)
		done <- c
	}()
	select {
	case c := <-done:
		if len(c) != 1 || c[0] != rec.ID {
			t.Fatalf("auto-close result = %v", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: AutoCloseByVerify did not return within 5s (OnEvent held the board mutex)")
	}
	select {
	case <-hook:
	default:
		t.Fatal("OnEvent hook did not run or its re-entrant Flag failed")
	}
}

func TestCancelRefusesToClobberCompletedTask(t *testing.T) {
	b, _ := newRig(t)
	rec, _, err := b.Add(AddArgs{Title: "green build", Doing: "building"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Start(rec.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Done(context.Background(), DoneArgs{ID: rec.ID, Evidence: "green"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Cancel(rec.ID); err == nil {
		t.Fatal("cancel on a done task must error")
	}
	got, err := ListOne(b, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusDone {
		t.Fatalf("task must remain done, got %s", got.Status)
	}
	// Cancelling an active task still works.
	rec2, _, err := b.Add(AddArgs{Title: "later task", Doing: "setting up"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Cancel(rec2.ID); err != nil {
		t.Fatalf("cancel on a pending task must work: %v", err)
	}
	got, err = ListOne(b, rec2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusCancelled {
		t.Fatalf("pending task must be cancelled, got %s", got.Status)
	}
	// Cancelling a cancelled task errors rather than rewriting it.
	if _, _, err := b.Cancel(rec2.ID); err == nil {
		t.Fatal("cancel on a cancelled task must error")
	}
}

// ListOne is a test helper loading one task through the board store.
func ListOne(b *Board, id string) (store.TaskRecord, error) {
	var out store.TaskRecord
	err := b.withWrite(func(st *store.Store) error {
		r, err := st.LoadTask(id)
		if err != nil {
			return err
		}
		out = r
		return nil
	})
	return out, err
}

func TestOracleGatesCompletion(t *testing.T) {
	b, _ := newRig(t)
	// Critical verify commands are refused at add time.
	if _, _, err := b.Add(AddArgs{Title: "bad idea", Verify: "sudo x"}); err == nil {
		t.Fatal("critical verify must be refused at add")
	}
	fail, _, err := b.Add(AddArgs{Title: "verify that fails", Verify: "exit 1"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, _, err := b.Start(fail.ID, false); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Even with evidence text present, a failing oracle keeps the task open
	// and stamps a note.
	if _, _, err := b.Done(context.Background(), DoneArgs{ID: fail.ID, Evidence: "trust me"}); err == nil {
		t.Fatal("failing oracle must refuse the close")
	}
	loaded, _ := ListOne(b, fail.ID)
	if loaded.Status != StatusInProgress {
		t.Fatalf("task must stay open after failed oracle, got %s", loaded.Status)
	}
	if len(loaded.Notes) != 1 || !strings.Contains(loaded.Notes[0].Text, "stays open") {
		t.Fatalf("expected failure note, got %+v", loaded.Notes)
	}
	// The passing oracle closes with a machine receipt.
	pass, _, err := b.Add(AddArgs{Title: "verify that passes", Verify: "echo ok"})
	if err != nil {
		t.Fatalf("add pass: %v", err)
	}
	_, _, _ = b.Start(pass.ID, false)
	if _, _, err := b.Done(context.Background(), DoneArgs{ID: pass.ID}); err != nil {
		t.Fatalf("done with passing oracle: %v", err)
	}
	loaded, _ = ListOne(b, pass.ID)
	if loaded.Status != StatusDone || len(loaded.Evidence) != 1 || loaded.Evidence[0].Kind != "cmd" {
		t.Fatalf("unexpected receipts %+v", loaded.Evidence)
	}
	if !strings.Contains(loaded.Evidence[0].Text, "exited 0") || strings.Index(loaded.Evidence[0].Text, "j_") < 0 {
		t.Fatalf("receipt must carry the exit code and job id, got %q", loaded.Evidence[0].Text)
	}
}

func TestOracleWithoutEvidenceCloses(t *testing.T) {
	b, _ := newRig(t)
	rec, _, err := b.Add(AddArgs{Title: "verify build", Verify: "echo fine"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	_, _, _ = b.Start(rec.ID, false)
	if _, _, err := b.Done(context.Background(), DoneArgs{ID: rec.ID}); err != nil {
		t.Fatalf("oracle-only close: %v", err)
	}
}

func TestBlockingDAG(t *testing.T) {
	b, _ := newRig(t)
	t1, _, err := b.Add(AddArgs{Title: "implement the store"})
	if err != nil {
		t.Fatalf("add t1: %v", err)
	}
	// Naming a not-yet-existing dependency must fail.
	if _, _, err := b.Add(AddArgs{Title: "later thing", DependsOn: []string{"t99"}}); err == nil {
		t.Fatal("unknown dependency must be refused")
	}
	t2, _, err := b.Add(AddArgs{Title: "wire the runner", DependsOn: []string{t1.ID}})
	if err != nil {
		t.Fatalf("add t2: %v", err)
	}
	if _, _, err := b.Start(t2.ID, false); err == nil {
		t.Fatal("starting a blocked task must refuse")
	}
	if _, _, err := b.Done(context.Background(), DoneArgs{ID: t2.ID, Evidence: "x"}); err == nil {
		t.Fatal("closing a blocked task must refuse")
	}
	if _, _, err := b.Start(t1.ID, false); err != nil {
		t.Fatalf("start t1: %v", err)
	}
	if _, _, err := b.Done(context.Background(), DoneArgs{ID: t1.ID, Evidence: "store.go compiles"}); err != nil {
		t.Fatalf("done t1: %v", err)
	}
	if _, _, err := b.Start(t2.ID, false); err != nil {
		t.Fatalf("start t2 after dep done: %v", err)
	}
}

func TestDepEditsRefuseCycles(t *testing.T) {
	b, _ := newRig(t)
	t1, _, _ := b.Add(AddArgs{Title: "first"})
	t2, _, _ := b.Add(AddArgs{Title: "second", DependsOn: []string{t1.ID}})
	// t1 depending on t2 (which depends on t1) is a cycle.
	if _, _, err := b.Dep(t1.ID, []string{t2.ID}, nil); err == nil {
		t.Fatal("cycle must be refused")
	}
	// Removing a dependency is fine.
	if _, _, err := b.Dep(t2.ID, nil, []string{t1.ID}); err != nil {
		t.Fatalf("dep remove: %v", err)
	}
	if _, _, err := b.Dep(t1.ID, []string{t2.ID}, nil); err != nil {
		t.Fatalf("now acyclic, must pass: %v", err)
	}
}

func TestSingleFocusAndForceNote(t *testing.T) {
	b, _ := newRig(t)
	ta, _, _ := b.Add(AddArgs{Title: "a"})
	tb, _, _ := b.Add(AddArgs{Title: "b"})
	if _, _, err := b.Start(ta.ID, false); err != nil {
		t.Fatalf("start a: %v", err)
	}
	if _, _, err := b.Start(tb.ID, false); err == nil {
		t.Fatal("second concurrent start must refuse")
	}
	saved, _, err := b.Start(tb.ID, true)
	if err != nil {
		t.Fatalf("forced start: %v", err)
	}
	if len(saved.Notes) != 1 || !strings.Contains(saved.Notes[0].Text, "parallel") {
		t.Fatalf("forced start must record a note, got %+v", saved.Notes)
	}
}

func TestStaleAndRenderBoard(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	base := time.Now()
	b := New(st, Options{Now: func() time.Time { return base }})
	ta, _, _ := b.Add(AddArgs{Title: "long running"})
	_, _, _ = b.Start(ta.ID, false)
	_, _, _ = b.Add(AddArgs{Title: "blocked", DependsOn: []string{ta.ID}})
	// Re-read from the store: the local Add/Start return values are snapshots
	// from before the subsequent mutations.
	later := base.Add(staleAfter + time.Minute)
	recs := mustList(b)
	if len(recs) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(recs))
	}
	board := RenderBoard(recs, later)
	if !strings.Contains(board, "STALE") {
		t.Fatalf("stale task must surface, got:\n%s", board)
	}
	if !strings.Contains(board, "[B]") || !strings.Contains(board, "1 blocked") {
		t.Fatalf("blocked task must surface, got:\n%s", board)
	}
	if !strings.Contains(board, "0 done") {
		t.Fatalf("digest must count statuses, got:\n%s", board)
	}
}

func TestClosingNudge(t *testing.T) {
	b, _ := newRig(t)
	var last store.TaskRecord
	for _, title := range []string{"read the files", "write the change", "update the readme"} {
		rec, _, err := b.Add(AddArgs{Title: title})
		if err != nil {
			t.Fatalf("add %s: %v", title, err)
		}
		closed, _, err := b.Done(context.Background(), DoneArgs{ID: rec.ID, Evidence: "did the thing (no checks anywhere)"})
		if err != nil {
			t.Fatalf("done %s: %v", rec.ID, err)
		}
		last = closed
	}
	if done := countDone(mustList(b)); done != 3 {
		t.Fatalf("expected 3 done, got %d", done)
	}
	// The nudge lives in the tool result path; run the predicate it uses.
	if !closingNudge(b.st, last) {
		t.Fatal("3 closes with no verification anywhere must fire the nudge")
	}
}

func TestNudgeSuppressedWhenVerifyTaskExists(t *testing.T) {
	b, _ := newRig(t)
	if _, _, err := b.Add(AddArgs{Title: "run the test suite", Verify: "echo ok"}); err != nil {
		t.Fatalf("add verify task: %v", err)
	}
	var last store.TaskRecord
	for i := 0; i < 3; i++ {
		r, _, err := b.Add(AddArgs{Title: "step " + string(rune('x'+i))})
		if err != nil {
			t.Fatalf("add: %v", err)
		}
		closed, _, err := b.Done(context.Background(), DoneArgs{ID: r.ID, Evidence: "ok"})
		if err != nil {
			t.Fatalf("done: %v", err)
		}
		last = closed
	}
	if done := countDone(mustList(b)); done != 3 {
		t.Fatalf("expected 3 done, got %d", done)
	}
	if closingNudge(b.st, last) {
		t.Fatal("nudge must stay quiet once a check-shaped task exists on the board")
	}
}

func countDone(recs []store.TaskRecord) int {
	n := 0
	for _, r := range recs {
		if r.Status == StatusDone {
			n++
		}
	}
	return n
}

func mustList(b *Board) []store.TaskRecord {
	recs, _ := b.List()
	return recs
}

func verifyified(recs []store.TaskRecord) bool {
	for _, r := range recs {
		if r.VerifyMode == "command" || verifPattern.MatchString(r.Title) {
			return true
		}
	}
	return false
}

func TestFlagPersistAndRender(t *testing.T) {
	b, _ := newRig(t)
	rec, _, _ := b.Add(AddArgs{Title: "polish the readme"})
	if _, _, err := b.Flag(rec.ID, "the wording in section 2 is wrong"); err != nil {
		t.Fatalf("flag: %v", err)
	}
	board := RenderBoard(mustList(b), time.Now())
	if !strings.Contains(board, "1 flagged") || !strings.Contains(board, "section 2") {
		t.Fatalf("flag must render, got:\n%s", board)
	}
	// Flags survive a plain cancel too (cancel itself appends its own note).
	if _, _, err := b.Cancel(rec.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	loaded, _ := ListOne(b, rec.ID)
	if loaded.Status != StatusCancelled || len(loaded.Notes) != 2 {
		t.Fatalf("unexpected state %+v", loaded)
	}
	last := loaded.Notes[len(loaded.Notes)-1].Text
	if last != "cancelled" {
		t.Fatalf("expected cancel note, got %q", last)
	}
}

func TestJournalEventsEmit(t *testing.T) {
	b, st := newRig(t)
	rec, _, _ := b.Add(AddArgs{Title: "journaled"})
	_, _, _ = b.Start(rec.ID, false)
	evs := st.LatestDelegateEvents(4)
	if len(evs) == 0 {
		t.Fatal("task events must be journaled")
	}
	for _, want := range []string{"task"} {
		found := false
		for _, ev := range evs {
			if ev.Kind == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("journal must contain %q events, got %+v", want, evs)
		}
	}
	if evs[len(evs)-1].Detail != "start" && evs[len(evs)-1].Detail != "add" && evs[len(evs)-1].Detail != "start " {
		if evs[len(evs)-1].Status != StatusInProgress {
			t.Fatalf("last event should reflect the start, got %+v", evs[len(evs)-1])
		}
	}
}

func TestPersistenceAcrossBoards(t *testing.T) {
	b, st := newRig(t)
	rec, _, _ := b.Add(AddArgs{Title: "durable task", Detail: "acceptance: lives forever"})
	_, _, _ = b.Start(rec.ID, false)

	// A brand-new board over the same store sees the same state.
	b2 := New(st, Options{Jobs: b.opts.Jobs})
	see, err := b2.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(see) != 1 || see[0].Title != "durable task" || see[0].Status != StatusInProgress {
		t.Fatalf("state did not survive the board boundary: %+v", see)
	}
}

func TestDelegateClosureHooks(t *testing.T) {
	b, _ := newRig(t)
	rec, _, _ := b.Add(AddArgs{Title: "delegate the fix", Verify: "echo ok"})
	// A delegate that reports done must still pass the oracle.
	if err := b.CloseTask(rec.ID, "done", "all green: the fix landed and the tests pass"); err != nil {
		t.Fatalf("close via delegate: %v", err)
	}
	loaded, _ := ListOne(b, rec.ID)
	if loaded.Status != StatusDone {
		t.Fatalf("oracle passed, task should be done, got %s", loaded.Status)
	}
	// A failed delegate flags instead of closing.
	other, _, _ := b.Add(AddArgs{Title: "the impossible step"})
	if err := b.CloseTask(other.ID, "failed", "could not read the config, it does not exist"); err != nil {
		t.Fatalf("flag via delegate: %v", err)
	}
	loaded, _ = ListOne(b, other.ID)
	if loaded.Status != StatusPending || len(loaded.Notes) != 1 || !strings.Contains(loaded.Notes[0].Text, "could not read") {
		t.Fatalf("failed delegate must flag, got %+v %+v", loaded.Status, loaded.Notes)
	}
}

func TestRegistryAttachAndCall(t *testing.T) {
	b, st := newRig(t)
	defer st.Close()
	env := &tools.Env{Workspace: ".", Store: st, Jobs: runner.NewManager(st), Policy: b.opts.Policy}
	reg := tools.New(env)
	if err := b.Attach(reg, "test-session"); err != nil {
		t.Fatalf("attach: %v", err)
	}
	names := reg.Names()
	if len(names) == 0 || names[len(names)-1] != "tasks" {
		t.Fatalf("tasks tool not registered last, got %v", names)
	}
	out, err := reg.Call(context.Background(), "tasks", []byte(`{"op":"add","title":"plan step"}`))
	if err != nil {
		t.Fatalf("call add: %v", err)
	}
	if !strings.Contains(out, "added tf") && !strings.Contains(out, "added t") {
		t.Fatalf("unexpected add call output %q", out)
	}
	out, err = reg.Call(context.Background(), "tasks", []byte(`{"op":"list"}`))
	if err != nil {
		t.Fatalf("call list: %v", err)
	}
	if !strings.Contains(out, "task board ·") {
		t.Fatalf("list output missing digest, got %q", out)
	}
}

func TestAddRollsBackWhenDependencyIsInvalid(t *testing.T) {
	b, st := newRig(t)
	if _, _, err := b.Add(AddArgs{Title: "do the work", DependsOn: []string{"t999"}}); err == nil {
		t.Fatal("invalid dependency must fail")
	}
	if recs := st.ListTasks(0); len(recs) != 0 {
		t.Fatalf("orphaned task left on the board: %+v", recs)
	}
}

func TestRenderBlockedUntilEveryDependencyIsDone(t *testing.T) {
	now := time.Now()
	recs := []store.TaskRecord{
		{ID: "t1", Title: "blocked child", Status: StatusPending, DependsOn: []string{"t2", "t3"}, Seq: 1},
		{ID: "t2", Title: "done dep", Status: StatusDone, Seq: 2},
		{ID: "t3", Title: "pending dep", Status: StatusPending, Seq: 3},
	}
	out := RenderBoard(recs, now)
	if !strings.Contains(out, "[B] t1") {
		t.Fatalf("task with one pending dep must render blocked:\n%s", out)
	}
	for i := range recs {
		if recs[i].ID == "t3" {
			recs[i].Status = StatusDone
		}
	}
	out = RenderBoard(recs, now)
	if strings.Contains(out, "[B] t1") {
		t.Fatalf("task should be unblocked once all deps are done:\n%s", out)
	}
}

func TestSessionScopedBoardList(t *testing.T) {
	b, _ := newRig(t)
	b.session = "session-a"
	a1, _, err := b.Add(AddArgs{Title: "only a"})
	if err != nil {
		t.Fatal(err)
	}
	b.session = "session-b"
	b1, _, err := b.Add(AddArgs{Title: "only b"})
	if err != nil {
		t.Fatal(err)
	}
	b.session = "session-a"
	list, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != a1.ID {
		t.Fatalf("session board = %+v, want only %s", list, a1.ID)
	}
	_ = b1
}
