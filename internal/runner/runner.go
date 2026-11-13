// Package runner executes shell commands as tracked jobs.
//
// Every execution gets an id, an artifact file containing the full output, and
// a terminal state. Long commands can be left running while the agent does
// something else, which is the point: nothing blocks the loop.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/creatorcli/creator/internal/id"
	"github.com/creatorcli/creator/internal/store"
)

// Status is a job's lifecycle state.
type Status string

const (
	StatusRunning Status = "running"
	StatusSuccess Status = "success"
	StatusFailed  Status = "failed"
	StatusTimeout Status = "timeout"
	StatusKilled  Status = "killed"
)

const defaultMaxBytes = 64 << 20

// Spec describes one command to run.
type Spec struct {
	Command string
	Dir     string
	Timeout time.Duration
	Shell   string
	// PTY forks the command inside a real pseudo-terminal so interactive
	// programs (vim, htop, REPLs) that need a TTY keep working. Output then
	// contains raw ANSI escape sequences; callers that want clean text should
	// leave it off (the default).
	PTY bool
	// Detach runs the command with its stdio wired straight to the artifact
	// file, unparented: it outlives this process and is never tracked, not
	// even to reap it. Used by --background. The store record stays
	// "running"; the next Creator startup marks it orphaned.
	Detach bool
}

// Snapshot is a race-free view of a job.
type Snapshot struct {
	ID      string    `json:"id"`
	Command string    `json:"command"`
	Dir     string    `json:"dir"`
	Shell   string    `json:"shell"`
	Status  Status    `json:"status"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitempty"`
	// Duration is in-process only (nanoseconds internally, arithmetic-friendly).
	// JSON consumers see DurationMs under duration_ms; previously Duration was
	// marshaled raw under duration_ms, which exposed nanoseconds as "ms".
	Duration   time.Duration `json:"-"`
	DurationMs int64         `json:"duration_ms"`
	ExitCode   int           `json:"exit_code"`
	Bytes      int64         `json:"bytes"`
	Truncated  bool          `json:"truncated"`
}

// Job is a running or finished command.
type Job struct {
	mu sync.Mutex

	ID        string
	Command   string
	Dir       string
	Shell     string
	Status    Status
	Started   time.Time
	Ended     time.Time
	ExitCode  int
	Bytes     int64
	Truncated bool

	artifact string
	proc     *os.Process
	done     chan struct{}

	// detached marks jobs started with Spec.Detach: unparented, never
	// supervised, never killed by StopAll/cancel — they outlive us on purpose.
	detached bool
	timedOut bool
	cancel   context.CancelFunc
}

// Done is closed when the process has been reaped and the job finalized.
func (j *Job) Done() <-chan struct{} { return j.done }

// Snapshot returns a consistent copy of the job state.
func (j *Job) Snapshot() Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := Snapshot{
		ID:        j.ID,
		Command:   j.Command,
		Dir:       j.Dir,
		Shell:     j.Shell,
		Status:    j.Status,
		Started:   j.Started,
		Ended:     j.Ended,
		ExitCode:  j.ExitCode,
		Bytes:     j.Bytes,
		Truncated: j.Truncated,
	}
	var d time.Duration
	if !j.Ended.IsZero() {
		d = j.Ended.Sub(j.Started)
	} else {
		d = time.Since(j.Started)
	}
	s.Duration = d
	s.DurationMs = d.Milliseconds()
	return s
}

// Output reads the full captured output.
func (j *Job) Output() (string, error) {
	b, err := os.ReadFile(j.artifact)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Tail returns the last maxBytes of captured output, prefixed with an
// ellipsis when truncated. It exists so TUIs can show live process output
// without slurping a multi-megabyte artifact.
func (j *Job) Tail(maxBytes int64) (string, error) {
	if maxBytes <= 0 {
		maxBytes = 4096
	}
	f, err := os.Open(j.artifact)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := info.Size()
	start := int64(0)
	truncated := false
	if size > maxBytes {
		start = size - maxBytes
		truncated = true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	if truncated {
		return "…\n" + string(b), nil
	}
	return string(b), nil
}

// Wait blocks until the job finishes or ctx is cancelled.
func (j *Job) Wait(ctx context.Context) error {
	select {
	case <-j.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Manager owns the jobs of one Creator process.
type Manager struct {
	store     *store.Store
	mu        sync.Mutex
	jobs      map[string]*Job
	maxBytes  int64
	healthMu  sync.Mutex
	healthErr error // last persistence or kill failure; surfaced via Health()
}

// NewManager builds a job manager backed by st.
func NewManager(st *store.Store) *Manager {
	return &Manager{store: st, jobs: map[string]*Job{}, maxBytes: defaultMaxBytes}
}

// noteFailure records a background operation failure so it is not lost.
func (m *Manager) noteFailure(what string, err error) {
	m.healthMu.Lock()
	m.healthErr = fmt.Errorf("%s: %w", what, err)
	m.healthMu.Unlock()
}

// Health reports the most recent background failure (a job record that could
// not be persisted, or a process tree that refused to die), or nil.
func (m *Manager) Health() error {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	return m.healthErr
}

// Start launches a command and returns immediately. The job keeps running when
// ctx is backgrounded; pass a timeout in the spec to bound it.
func (m *Manager) Start(ctx context.Context, spec Spec) (*Job, error) {
	command := strings.TrimSpace(spec.Command)
	if command == "" {
		return nil, errors.New("empty command")
	}
	sh, err := ResolveShell(spec.Shell)
	if err != nil {
		return nil, err
	}
	dir := spec.Dir
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return nil, err
		}
	}
	info, statErr := os.Stat(dir)
	if statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("working directory %q does not exist", dir)
	}

	jobID := id.New("j")
	artifact := m.store.ArtifactPath(jobID)
	file, err := m.store.OpenArtifact(jobID)
	if err != nil {
		return nil, err
	}

	// One cancel for exactly one context; reassigning the pair leaked the
	// first context (a go vet lost-cancel).
	var runCtx context.Context
	var cancel context.CancelFunc
	if spec.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}

	j := &Job{
		ID:       jobID,
		Command:  command,
		Dir:      dir,
		Shell:    sh.name,
		Status:   StatusRunning,
		Started:  time.Now(),
		artifact: artifact,
		done:     make(chan struct{}),
		cancel:   cancel,
	}

	args := append(append([]string{}, sh.args...), command)
	cmd := exec.Command(sh.path, args...)
	cmd.Dir = dir
	writer := &cappedWriter{file: file, limit: m.maxBytes}

	// Detached jobs (--background) write straight to the artifact file and
	// are unparented: no pipes for this process to drain, no watchdog to
	// reap them. Their store record stays "running"; the next Creator
	// startup marks it orphaned. StopAll never kills them because
	// configureProc releases the parent's grip (own process group on Unix).
	if spec.Detach {
		cmd.Stdout = file
		cmd.Stderr = file
		configureProc(cmd)
		if err := cmd.Start(); err != nil {
			_ = file.Close()
			cancel()
			return nil, fmt.Errorf("start %s: %w", sh.name, err)
		}
		// The child inherited the artifact handle; the parent does not use
		// it (persist runs with nil), so hand it over completely.
		_ = file.Close()
		j.proc = cmd.Process
		j.detached = true
		m.mu.Lock()
		m.jobs[jobID] = j
		m.mu.Unlock()
		m.persist(j, nil)
		m.evictFinishedJobs()
		cancel() // the parent stops supervising; the child is on its own
		return j, nil
	}

	// PTY path: fork inside a real pseudo-terminal. Interactive programs that
	// probe isatty() then work instead of failing with "not a tty".
	var ptmx *os.File
	// readerDone is closed by the PTY reader goroutine when the slave hits
	// EOF or errors. finish() waits on it (bounded) so the tail of the
	// stream is flushed to the artifact before the byte accounting is read.
	readerDone := make(chan struct{})
	readerActive := false
	if spec.PTY {
		p, err := pty.Start(cmd)
		if err != nil {
			// creack/pty returns ErrUnsupported on platforms without a
			// native PTY (notably Windows before ConPTY plumbing). Fall back
			// to pipes instead of failing the whole command, and record the
			// degradation in the captured output so callers notice.
			ptmx = nil
			_, _ = writer.Write([]byte("[pty unavailable on this platform; running in pipe mode]\n"))
			cmd.Stdout = writer
			cmd.Stderr = writer
			// pty.Start already wired cmd.Stdin to the slave's fd when it
			// set up the cmd; that fd is now closed. Reset cmd.Stdin to nil
			// (EOF) so the next cmd.Start() doesn't inherit a dangling fd.
			cmd.Stdin = nil
			configureProc(cmd)
			if err := cmd.Start(); err != nil {
				file.Close()
				cancel()
				return nil, fmt.Errorf("start %s: %w", sh.name, err)
			}
		} else {
			ptmx = p
			readerActive = true
			go func() {
				defer close(readerDone)
				buf := make([]byte, 4096)
				for {
					n, rerr := ptmx.Read(buf)
					if n > 0 {
						writer.Write(buf[:n])
					}
					if rerr != nil {
						return
					}
				}
			}()
		}
	} else {
		cmd.Stdout = writer
		cmd.Stderr = writer
		configureProc(cmd)
		if err := cmd.Start(); err != nil {
			file.Close()
			cancel()
			return nil, fmt.Errorf("start %s: %w", sh.name, err)
		}
	}
	j.proc = cmd.Process

	m.mu.Lock()
	m.jobs[jobID] = j
	m.mu.Unlock()
	m.evictFinishedJobs()
	m.persist(j, writer)

	go func() {
		waitErr := cmd.Wait()
		if readerActive {
			// The child exited; give the pty reader a bounded drain window
			// so the artifact gets the final output and the byte accounting
			// is settled before finish() reads it. An orphaned grandchild
			// holding the slave open is what bounds the wait — never hang.
			select {
			case <-readerDone:
			case <-time.After(2 * time.Second):
			}
		}
		if ptmx != nil {
			ptmx.Close()
		}
		file.Close()
		code := 0
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				code = exitErr.ExitCode()
			} else {
				code = -1
			}
		}
		j.finish(code, writer, runCtx)
		close(j.done)
		cancel()
		m.persist(j, writer)
		m.evictFinishedJobs()
	}()

	go func() {
		select {
		case <-runCtx.Done():
			// The run context is closed on normal completion too — the select
			// wake path is random between both cases. If j.done is already
			// closed the command finished cleanly: never kill a reaped pid
			// (a reused process-group id would take innocents with it).
			select {
			case <-j.done:
				return
			default:
			}
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				j.mu.Lock()
				j.timedOut = true
				j.mu.Unlock()
			} else {
				// Parent context cancelled (Ctrl+C, session teardown): this
				// is a kill, not a failure — record it as such.
				j.mu.Lock()
				if j.Status == StatusRunning {
					j.Status = StatusKilled
				}
				j.mu.Unlock()
			}
			if err := killTree(cmd.Process); err != nil {
				m.noteFailure("kill job "+j.ID, err)
			}
			if ptmx != nil {
				ptmx.Close()
			}
		case <-j.done:
		}
	}()

	return j, nil
}

func (j *Job) finish(code int, w *cappedWriter, ctx context.Context) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.ExitCode = code
	j.Bytes = w.written
	j.Truncated = w.dropped > 0
	j.Ended = time.Now()
	switch {
	case j.timedOut || errors.Is(ctx.Err(), context.DeadlineExceeded):
		j.Status = StatusTimeout
	case j.Status == StatusKilled || errors.Is(ctx.Err(), context.Canceled):
		// Parent cancelled or StopAll killed the tree: never misreport as failed.
		j.Status = StatusKilled
	case code == 0:
		j.Status = StatusSuccess
	default:
		j.Status = StatusFailed
	}
}

func (m *Manager) persist(j *Job, w *cappedWriter) {
	s := j.Snapshot()
	rec := store.JobRecord{
		OwnerPID:  os.Getpid(), // this manager owns every job it started
		ID:        s.ID,
		Command:   s.Command,
		Dir:       s.Dir,
		Shell:     s.Shell,
		Status:    string(s.Status),
		Started:   s.Started,
		Ended:     s.Ended,
		ExitCode:  s.ExitCode,
		Bytes:     s.Bytes,
		Truncated: s.Truncated,
	}
	if err := m.store.SaveJob(rec); err != nil {
		// One retry: bbolt may be briefly busy. A permanent failure is then
		// recorded in Health() instead of vanishing.
		if err2 := m.store.SaveJob(rec); err2 != nil {
			m.noteFailure("save job "+s.ID, err2)
		}
	}
}

// Get returns a live job by id.
func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}

// Running lists jobs still executing in this process.
func (m *Manager) Running() []Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Snapshot
	for _, j := range m.jobs {
		s := j.Snapshot()
		if s.Status == StatusRunning {
			out = append(out, s)
		}
	}
	return out
}

// maxTerminalJobs bounds how many finished jobs the in-memory map keeps.
// The store is the durable record; the map only exists for live jobs and
// recent results. Without this, a long-lived process accumulates one Job
// per command forever.
const maxTerminalJobs = 256

// evictFinishedJobs drops the oldest terminal jobs from the in-memory map
// once more than maxTerminalJobs have finished. Running jobs are always
// kept. Best effort: eviction failure just means the map stays big.
func (m *Manager) evictFinishedJobs() {
	m.mu.Lock()
	type slot struct {
		id    string
		ended time.Time
	}
	var done []slot
	for id, j := range m.jobs {
		s := j.Snapshot()
		if s.Status == StatusRunning {
			continue
		}
		ended := s.Ended
		if ended.IsZero() {
			ended = s.Started
		}
		done = append(done, slot{id: id, ended: ended})
	}
	if len(done) <= maxTerminalJobs {
		m.mu.Unlock()
		return
	}
	sort.Slice(done, func(i, k int) bool { return done[i].ended.Before(done[k].ended) })
	for i := 0; i < len(done)-maxTerminalJobs; i++ {
		delete(m.jobs, done[i].id)
	}
	m.mu.Unlock()
}

// Kill terminates a live job and its children.
func (m *Manager) Kill(jobID string) error {
	j, ok := m.Get(jobID)
	if !ok {
		return fmt.Errorf("no live job %q", jobID)
	}
	j.mu.Lock()
	if j.Status != StatusRunning {
		status := j.Status
		j.mu.Unlock()
		return fmt.Errorf("job %s already %s", jobID, status)
	}
	j.Status = StatusKilled
	proc := j.proc
	j.mu.Unlock()
	return killTree(proc)
}

// StopAll kills every running job; called on shutdown.
func (m *Manager) StopAll() {
	m.mu.Lock()
	jobs := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.mu.Unlock()
	for _, j := range jobs {
		if j.detached {
			// Detached jobs outlive the process on purpose; killing them
			// defeats the entire flag.
			continue
		}
		j.mu.Lock()
		running := j.Status == StatusRunning
		if running {
			select {
			case <-j.done:
				// Reaped already — never signal a possibly-reused pid.
				running = false
			default:
				j.Status = StatusKilled
			}
		}
		j.mu.Unlock()
		if running {
			if err := killTree(j.proc); err != nil {
				m.noteFailure("kill job "+j.ID, err)
			}
		}
	}
}

// cappedWriter writes to the artifact file until limit bytes, then counts the
// dropped bytes without failing the child process.
type cappedWriter struct {
	mu      sync.Mutex
	file    *os.File
	limit   int64
	written int64
	dropped int64
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.limit - w.written
	if remaining <= 0 {
		w.dropped += int64(len(p))
		return len(p), nil
	}
	chunk := p
	if int64(len(chunk)) > remaining {
		chunk = chunk[:remaining]
		w.dropped += int64(len(p) - len(chunk))
	}
	n, err := w.file.Write(chunk)
	w.written += int64(n)
	// A short write leaves a remainder that was neither persisted nor
	// dropped — account for it or the written+dropped invariant leaks.
	if remainder := int64(len(chunk)) - int64(n); remainder > 0 {
		w.dropped += remainder
	}
	if err != nil {
		// The unwritten tail counts as dropped so callers see honest
		// totals even on a failing disk.
		return n, err
	}
	return len(p), nil
}
