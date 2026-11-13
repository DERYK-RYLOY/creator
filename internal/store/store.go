// Package store persists sessions, raw job output and file checkpoints.
//
// Metadata lives in a bbolt database; bulky payloads (job output, checkpoint
// bodies) live beside it as files so the database stays small and mmap friendly.
package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/creatorcli/creator/internal/id"
	"github.com/creatorcli/creator/internal/provider"
)

var (
	bucketSessions    = []byte("sessions")
	bucketMessages    = []byte("messages")
	bucketCheckpoints = []byte("checkpoints")
	bucketJobs        = []byte("jobs")
	bucketMeta        = []byte("meta")
	bucketDelegates   = []byte("delegates")
	bucketEvents      = []byte("devents")
	bucketTasks       = []byte("tasks")
)

// msgKey builds a bucket key for one message: session id, 0x00 separator,
// 8-byte big-endian sequence. Lexicographic order equals conversation order.
func msgKey(sessionID string, seq uint64) []byte {
	key := make([]byte, 0, len(sessionID)+9)
	key = append(key, []byte(sessionID)...)
	key = append(key, 0x00)
	var s [8]byte
	be := uint64(seq)
	for i := 7; i >= 0; i-- {
		s[i] = byte(be)
		be >>= 8
	}
	return append(key, s[:]...)
}

// Store is the on-disk state of a Creator installation.
type Store struct {
	db    *bolt.DB
	dir   string
	errMu sync.Mutex
	dbErr error
}

// Session is a resumable conversation.
type Session struct {
	ID        string             `json:"id"`
	Created   time.Time          `json:"created"`
	Updated   time.Time          `json:"updated"`
	Provider  string             `json:"provider"`
	Model     string             `json:"model"`
	Workspace string             `json:"workspace"`
	Turns     int                `json:"turns,omitempty"`
	Messages  []provider.Message `json:"messages,omitempty"`
	Usage     provider.Usage     `json:"usage"`
}

// SessionMeta is a lightweight session listing row.
type SessionMeta struct {
	ID      string         `json:"id"`
	Updated time.Time      `json:"updated"`
	Model   string         `json:"model"`
	Turns   int            `json:"turns"`
	Usage   provider.Usage `json:"usage"`
}

// JobRecord is the persisted state of a command execution.
type JobRecord struct {
	ID        string    `json:"id"`
	Command   string    `json:"command"`
	Dir       string    `json:"dir"`
	Shell     string    `json:"shell"`
	Status    string    `json:"status"`
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended,omitempty"`
	ExitCode  int       `json:"exit_code"`
	Bytes     int64     `json:"bytes"`
	Truncated bool      `json:"truncated"`
	OwnerPID  int       `json:"owner_pid,omitempty"`
}

func idForCheckpoint() string { return id.New("cp") }

// Checkpoint records a snapshot of a file taken before Creator modified it.
type Checkpoint struct {
	ID      string    `json:"id"`
	Session string    `json:"session"`
	Path    string    `json:"path"`
	RelPath string    `json:"rel_path"`
	Tool    string    `json:"tool"`
	Created time.Time `json:"created"`
	Bytes   int       `json:"bytes"`
	Mode    uint32    `json:"mode,omitempty"` // original permission bits, restored by revert
}

// Open creates the data directory and opens the database.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700) // tighten pre-existing installs too
	for _, sub := range []string{"out", "checkpoints"} {
		p := filepath.Join(dir, sub)
		if err := os.MkdirAll(p, 0o700); err != nil {
			return nil, err
		}
		_ = os.Chmod(p, 0o700)
	}
	dbPath := filepath.Join(dir, "creator.db")
	db, err := bolt.Open(dbPath, 0o600, &bolt.Options{Timeout: 3 * time.Second})
	if err != nil {
		if bolt.ErrTimeout == err {
			return nil, fmt.Errorf("open database: %w - is another Creator process already running?", err)
		}
		return nil, fmt.Errorf("open database: %w", err)
	}
	// bolt's 0o600 only applies at creation; tighten a pre-existing weak
	// perms database too, and record the failure if we cannot.
	chmodErr := os.Chmod(dbPath, 0o600)
	s := &Store{db: db, dir: dir}
	if chmodErr != nil {
		s.noteDBErr(fmt.Errorf("tighten %s permissions: %w", dbPath, chmodErr))
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketSessions, bucketMessages, bucketCheckpoints, bucketJobs, bucketMeta, bucketDelegates, bucketEvents, bucketTasks} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
// view/update wrap s.db.View and s.db.Update so a transaction failure is
// recorded on the Store instead of silently producing empty results. The
// wrappers never change control flow — getters stay best-effort — but DB
// corruption can no longer hide.
func (s *Store) view(fn func(*bolt.Tx) error) error {
	err := s.db.View(fn)
	if err != nil {
		s.noteDBErr(err)
	}
	return err
}

func (s *Store) update(fn func(*bolt.Tx) error) error {
	err := s.db.Update(fn)
	if err != nil {
		s.noteDBErr(err)
	}
	return err
}

// noteDBErr records the most recent database failure.
func (s *Store) noteDBErr(err error) {
	if err == nil {
		return
	}
	s.errMu.Lock()
	s.dbErr = err
	s.errMu.Unlock()
}

// DBHealth reports the most recent database failure (a transaction error or a
// corrupted record), or nil when the database is healthy. Callers that show
// background-work status should surface this so persistence problems are not
// mistaken for "nothing to report".
func (s *Store) DBHealth() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.dbErr
}

func (s *Store) Close() error { return s.db.Close() }

// Dir returns the data directory.
func (s *Store) Dir() string { return s.dir }

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

// SaveSession writes the session header. Conversation messages live in the
// messages bucket (written incrementally by AppendMessage); embedding history
// here again would rewrite the whole transcript on every save, which is
// quadratic over a long session.
func (s *Store) SaveSession(sess Session) error {
	if sess.ID == "" {
		return errors.New("session needs an id")
	}
	sess.Updated = time.Now()
	if sess.Created.IsZero() {
		sess.Created = sess.Updated
	}
	sess.Messages = nil
	// Count and write in ONE transaction so the header's Turns cannot race
	// a concurrent AppendMessage.
	return s.db.Update(func(tx *bolt.Tx) error {
		sess.Turns = countMessagesTx(tx, sess.ID)
		payload, err := json.Marshal(sess)
		if err != nil {
			return err
		}
		return tx.Bucket(bucketSessions).Put([]byte(sess.ID), payload)
	})
}

// AppendMessage durably adds one message to a session's log. Because it writes
// only that message, a torn turn never loses earlier messages and persistence
// stays constant-cost per message.
// AppendMessage adds msg at position index; index must not be negative.
func (s *Store) AppendMessage(sessionID string, index int, msg provider.Message) error {
	if sessionID == "" {
		return errors.New("append needs a session id")
	}
	if index < 0 {
		return fmt.Errorf("append needs a non-negative index (got %d)", index)
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketMessages).Put(msgKey(sessionID, uint64(index)), payload)
	})
}

// CountMessages counts a session's message log.
// countMessagesTx counts a session's message log inside an open transaction
// so callers that write in the same tx see a consistent snapshot.
func countMessagesTx(tx *bolt.Tx, sessionID string) int {
	count := 0
	c := tx.Bucket(bucketMessages).Cursor()
	prefix := append([]byte(sessionID), 0x00)
	for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
		count++
	}
	return count
}

// loadMessages reads a session's message log in order.
func (s *Store) loadMessages(sessionID string) ([]provider.Message, error) {
	var msgs []provider.Message
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketMessages).Cursor()
		prefix := append([]byte(sessionID), 0x00)
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var m provider.Message
			if err := json.Unmarshal(v, &m); err == nil {
				msgs = append(msgs, m)
			} else {
				s.noteDBErr(err)
			}
		}
		return nil
	})
	return msgs, err
}

// LoadSession reads a session: header plus message log. Legacy sessions stored
// their history inside the header document; those are migrated into the log
// exactly once.
func (s *Store) LoadSession(id string) (Session, error) {
	var sess Session
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketSessions).Get([]byte(id))
		if raw == nil {
			return fmt.Errorf("no session %q", id)
		}
		return json.Unmarshal(raw, &sess)
	})
	if err != nil {
		return sess, err
	}
	msgs, err := s.loadMessages(id)
	if err != nil {
		return sess, err
	}
	if len(msgs) > 0 {
		sess.Messages = msgs
		sess.Turns = len(msgs)
		return sess, nil
	}
	if len(sess.Messages) > 0 {
		// Legacy layout: header carries messages. Migrate them into the log
		// so future appends stay incremental, then strip them from the header.
		legacy := sess.Messages
		err = s.db.Update(func(tx *bolt.Tx) error {
			b := tx.Bucket(bucketMessages)
			for i, m := range legacy {
				payload, merr := json.Marshal(m)
				if merr != nil {
					return merr
				}
				if err := b.Put(msgKey(id, uint64(i)), payload); err != nil {
					return err
				}
			}
			headerRaw := tx.Bucket(bucketSessions).Get([]byte(id))
			var header Session
			if err := json.Unmarshal(headerRaw, &header); err != nil {
				return err
			}
			header.Messages = nil
			header.Turns = len(legacy)
			stripped, jerr := json.Marshal(header)
			if jerr != nil {
				return jerr
			}
			return tx.Bucket(bucketSessions).Put([]byte(id), stripped)
		})
		if err != nil {
			sess.Messages = legacy
			return sess, err
		}
		sess.Messages = legacy
		sess.Turns = len(legacy)
	}
	return sess, nil
}

// LatestSession returns the most recently updated session id.
func (s *Store) LatestSession() (string, bool) {
	metas := s.ListSessions(1)
	if len(metas) == 0 {
		return "", false
	}
	return metas[0].ID, true
}

// ListSessions returns sessions newest first.
func (s *Store) ListSessions(limit int) []SessionMeta {
	var out []SessionMeta
	_ = s.view(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSessions).ForEach(func(_, v []byte) error {
			var sess Session
			if err := json.Unmarshal(v, &sess); err != nil {
				s.noteDBErr(err)
				return nil
			}
			out = append(out, SessionMeta{
				ID:      sess.ID,
				Updated: sess.Updated,
				Model:   sess.Model,
				Turns:   sess.Turns,
				Usage:   sess.Usage,
			})
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ---------------------------------------------------------------------------
// Job records
// ---------------------------------------------------------------------------

// SaveJob persists job metadata.
//
// SaveJob never fabricates ownership: a record with no OwnerPID belongs to an
// unknown owner (a legacy or foreign record re-saved by this process is NOT
// this process's work). Callers that own the job stamp OwnerPID explicitly at
// creation time; untracked records fall back to the 24h liveness rule.
func (s *Store) SaveJob(rec JobRecord) error {
	payload, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketJobs).Put([]byte(rec.ID), payload)
	})
}

// LoadJob reads job metadata.
func (s *Store) LoadJob(id string) (JobRecord, error) {
	var rec JobRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketJobs).Get([]byte(id))
		if raw == nil {
			return fmt.Errorf("no job %q", id)
		}
		return json.Unmarshal(raw, &rec)
	})
	return rec, err
}

// ListJobs returns job records newest first.
func (s *Store) ListJobs(limit int) []JobRecord {
	var out []JobRecord
	_ = s.view(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketJobs).ForEach(func(_, v []byte) error {
			var rec JobRecord
			if err := json.Unmarshal(v, &rec); err == nil {
				out = append(out, rec)
			} else {
				s.noteDBErr(err)
			}
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ---------------------------------------------------------------------------
// Artifacts
// ---------------------------------------------------------------------------

// ArtifactPath returns where a handle's raw output lives.
func (s *Store) ArtifactPath(handle string) string {
	return filepath.Join(s.dir, "out", filepath.Base(handle)+".log")
}

// ReadArtifact loads raw output for a handle.
func (s *Store) ReadArtifact(handle string) (string, error) {
	b, err := os.ReadFile(s.ArtifactPath(handle))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// The raw OS error leaks the internal layout and reads like a
			// crash; tell the user how to find a real handle instead.
			return "", fmt.Errorf("unknown handle %q — list them with `creator jobs` and `creator delegates`", handle)
		}
		return "", fmt.Errorf("unknown handle %q: %w", handle, err)
	}
	return string(b), nil
}

// OpenArtifact creates the artifact file for writing.
func (s *Store) OpenArtifact(handle string) (*os.File, error) {
	return os.OpenFile(s.ArtifactPath(handle), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}

// jobPossiblyLive reports whether a running job may still be producing
// output: its owning process is alive, or (legacy records without an owner)
// it is younger than 24h. Used by orphan-marking and artifact pruning alike
// so both agree on liveness.
func (s *Store) jobPossiblyLive(rec JobRecord) bool {
	if rec.Status != "running" {
		return false
	}
	if rec.OwnerPID != 0 {
		return processAlive(rec.OwnerPID, jobApproxStart(rec))
	}
	return time.Since(rec.Started) < 24*time.Hour
}

// PruneArtifacts deletes artifacts older than age. Artifacts of possibly
// live jobs are never removed — a job that started long ago must not lose
// its output file from under it.
func (s *Store) PruneArtifacts(age time.Duration) int {
	dir := filepath.Join(s.dir, "out")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-age)
	removed := 0
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		if jobID := strings.TrimSuffix(e.Name(), ".log"); jobID != e.Name() {
			if rec, err := s.LoadJob(jobID); err == nil && s.jobPossiblyLive(rec) {
				continue // a live job still owns this artifact
			}
		}
		if info.ModTime().Before(cutoff) {
			if os.Remove(filepath.Join(dir, e.Name())) == nil {
				removed++
			}
		}
	}
	return removed
}

// PruneCheckpoints deletes checkpoint bodies and their metadata older than age.
func (s *Store) PruneCheckpoints(age time.Duration) int {
	cutoff := time.Now().Add(-age)
	var stale []Checkpoint
	_ = s.view(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketCheckpoints).ForEach(func(_, v []byte) error {
			var cp Checkpoint
			if err := json.Unmarshal(v, &cp); err != nil {
				s.noteDBErr(err)
				return nil
			}
			if cp.Created.Before(cutoff) {
				stale = append(stale, cp)
			}
			return nil
		})
	})
	removed := 0
	for _, cp := range stale {
		// DB row first: a failure here leaves both halves intact, never a
		// metadata row pointing at a missing body.
		if err := s.db.Update(func(tx *bolt.Tx) error {
			return tx.Bucket(bucketCheckpoints).Delete([]byte(cp.ID))
		}); err != nil {
			continue
		}
		// Row is gone; the body is now unreferenced. A removal failure
		// leaves an orphan file that a later sweep can reap — it can never
		// be confused for a live checkpoint.
		_ = os.Remove(filepath.Join(s.dir, "checkpoints", filepath.Base(cp.ID)))
		removed++
	}
	return removed
}

// ---------------------------------------------------------------------------
// Checkpoints
// ---------------------------------------------------------------------------

// saveCheckpointMaxBody bounds checkpoint bodies: a checkpoint is a full
// duplicate of the file, so checkpointing giant generated files would double
// disk usage and memory. Callers surface the error to the user.
const saveCheckpointMaxBody = 16 << 20

// SaveCheckpoint stores a copy of path's current contents and returns the
// checkpoint metadata.
func (s *Store) SaveCheckpoint(session, path, relPath, tool string) (Checkpoint, error) {
	if info, err := os.Stat(path); err == nil && info.Size() > saveCheckpointMaxBody {
		return Checkpoint{}, fmt.Errorf("file is %d bytes; exceeds the %d MiB checkpoint cap", info.Size(), saveCheckpointMaxBody>>20)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return Checkpoint{}, err
	}
	var mode os.FileMode
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	cp := Checkpoint{
		ID:      idForCheckpoint(),
		Session: session,
		Path:    path,
		RelPath: relPath,
		Tool:    tool,
		Created: time.Now(),
		Bytes:   len(content),
		Mode:    uint32(mode),
	}
	if err := os.WriteFile(filepath.Join(s.dir, "checkpoints", filepath.Base(cp.ID)), content, 0o600); err != nil {
		return Checkpoint{}, err
	}
	payload, err := json.Marshal(cp)
	if err != nil {
		_ = os.Remove(filepath.Join(s.dir, "checkpoints", filepath.Base(cp.ID)))
		return Checkpoint{}, err
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketCheckpoints).Put([]byte(cp.ID), payload)
	}); err != nil {
		// Don't strand the body file with no metadata row referencing it.
		_ = os.Remove(filepath.Join(s.dir, "checkpoints", filepath.Base(cp.ID)))
		return Checkpoint{}, err
	}
	return cp, nil
}

// GetCheckpoint reads checkpoint metadata by id (or unique prefix).
func (s *Store) GetCheckpoint(id string) (Checkpoint, error) {
	var exact *Checkpoint
	var matches []Checkpoint
	_ = s.view(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketCheckpoints).ForEach(func(k, v []byte) error {
			key := string(k)
			if key != id && !strings.HasPrefix(key, id) {
				return nil
			}
			var cp Checkpoint
			if err := json.Unmarshal(v, &cp); err != nil {
				s.noteDBErr(err)
				return nil
			}
			c := cp
			if key == id {
				exact = &c
				return nil
			}
			matches = append(matches, c)
			return nil
		})
	})
	switch {
	case exact != nil:
		return *exact, nil
	case len(matches) == 1:
		return matches[0], nil
	case len(matches) > 1:
		ids := make([]string, 0, len(matches))
		for _, cp := range matches {
			ids = append(ids, cp.ID)
		}
		sort.Strings(ids)
		return Checkpoint{}, fmt.Errorf("checkpoint prefix %q is ambiguous (%s)", id, strings.Join(ids, ", "))
	default:
		return Checkpoint{}, fmt.Errorf("no checkpoint %q", id)
	}
}

// ReadCheckpoint returns the saved contents of a checkpoint.
func (s *Store) ReadCheckpoint(cp Checkpoint) ([]byte, error) {
	// Base the id before joining: the write and prune paths already do, so
	// a hand-edited or legacy id cannot escape the checkpoints dir here.
	return os.ReadFile(filepath.Join(s.dir, "checkpoints", filepath.Base(cp.ID)))
}

// ListCheckpoints returns checkpoints newest first.
func (s *Store) ListCheckpoints(limit int) []Checkpoint {
	var out []Checkpoint
	_ = s.view(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketCheckpoints).ForEach(func(_, v []byte) error {
			var cp Checkpoint
			if err := json.Unmarshal(v, &cp); err == nil {
				out = append(out, cp)
			} else {
				s.noteDBErr(err)
			}
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ---------------------------------------------------------------------------
// Delegates
// ---------------------------------------------------------------------------

// DelegateRecord is the persisted state of a delegated sub-agent.
type DelegateRecord struct {
	ID       string         `json:"id"`
	Parent   string         `json:"parent,omitempty"`
	Name     string         `json:"name,omitempty"`
	Task     string         `json:"task"`
	Status   string         `json:"status"` // queued|running|done|failed|canceled|orphaned|depth|rejected
	Depth    int            `json:"depth"`
	Created  time.Time      `json:"created"`
	Started  time.Time      `json:"started,omitempty"`
	Ended    time.Time      `json:"ended,omitempty"`
	Steps    int            `json:"steps"`
	Usage    provider.Usage `json:"usage"`
	Result   string         `json:"result,omitempty"` // reduced final report
	Error    string         `json:"error,omitempty"`
	Session  string         `json:"session,omitempty"`
	Handle   string         `json:"handle,omitempty"` // artifact holding the full report
	OwnerPID int            `json:"owner_pid,omitempty"`
}

// SaveDelegate persists delegate metadata.
// SaveDelegate never fabricates ownership, same contract as SaveJob: an
// untracked delegate keeps OwnerPID == 0 and falls back to the 24h liveness
// rule instead of being claimed by the current process.
func (s *Store) SaveDelegate(rec DelegateRecord) error {
	payload, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketDelegates).Put([]byte(rec.ID), payload)
	})
}

// LoadDelegate reads one delegate record.
func (s *Store) LoadDelegate(id string) (DelegateRecord, error) {
	var rec DelegateRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketDelegates).Get([]byte(id))
		if raw == nil {
			return fmt.Errorf("no delegate %q", id)
		}
		return json.Unmarshal(raw, &rec)
	})
	return rec, err
}

// ListDelegates returns delegate records newest first.
func (s *Store) ListDelegates(limit int) []DelegateRecord {
	var out []DelegateRecord
	_ = s.view(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketDelegates).ForEach(func(_, v []byte) error {
			var rec DelegateRecord
			if err := json.Unmarshal(v, &rec); err == nil {
				out = append(out, rec)
			} else {
				s.noteDBErr(err)
			}
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// delegateApproxStart returns the delegate record's best-known start time
// for the PID-reuse guard. Falls back to Created.
func delegateApproxStart(rec DelegateRecord) time.Time {
	if !rec.Started.IsZero() {
		return rec.Started
	}
	return rec.Created
}

// jobApproxStart returns the job record's start time for the PID-reuse
// guard. May be zero for legacy records, in which case the guard is skipped.
func jobApproxStart(rec JobRecord) time.Time {
	return rec.Started
}

// OrphanDelegates marks queued/running records whose owning Creator process
// is gone (or which predate owner tracking) as orphaned. Live Creator
// processes may safely share one database file, so a record is never
// orphaned while its owner is still running.
func (s *Store) OrphanDelegates() int {
	count := 0
	// One transaction: read + mark + write are atomic, so a record a
	// concurrent Creator just finished (queued/running → done/failed)
	// can never be clobbered back to orphaned by this pass.
	_ = s.update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketDelegates)
		return b.ForEach(func(k, v []byte) error {
			var rec DelegateRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				s.noteDBErr(err)
				return nil
			}
			if rec.Status != "queued" && rec.Status != "running" {
				return nil
			}
			if rec.OwnerPID != 0 {
				if processAlive(rec.OwnerPID, delegateApproxStart(rec)) {
					return nil
				}
			} else {
				created := rec.Created
				if !rec.Started.IsZero() {
					created = rec.Started
				}
				if time.Since(created) < 24*time.Hour {
					return nil
				}
			}
			orphan := rec
			orphan.Status = "orphaned"
			orphan.Ended = time.Now()
			if orphan.Error == "" {
				orphan.Error = "interrupted: creator process exited"
			}
			payload, err := json.Marshal(orphan)
			if err != nil {
				return nil
			}
			if err := b.Put(k, payload); err != nil {
				return nil
			}
			count++
			return nil
		})
	})
	return count
}

// OrphanJobs marks running jobs whose owning Creator process is gone (or
// which predate owner tracking) as orphaned so `creator jobs` does not show
// them live forever.
func (s *Store) OrphanJobs() int {
	count := 0
	_ = s.update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		return b.ForEach(func(k, v []byte) error {
			var rec JobRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				s.noteDBErr(err)
				return nil
			}
			if s.jobPossiblyLive(rec) {
				return nil
			}
			orphan := rec
			orphan.Status = "orphaned"
			orphan.Ended = time.Now()
			payload, err := json.Marshal(orphan)
			if err != nil {
				return nil
			}
			if err := b.Put(k, payload); err != nil {
				return nil
			}
			count++
			return nil
		})
	})
	return count
}

// ---------------------------------------------------------------------------
// Delegate event journal
// ---------------------------------------------------------------------------

// DelegateEvent is one entry in the persistent delegation journal. It is the
// complete state change feed for a delegation tree, in order: the substrate a
// live viewer (web front-end) replays and then follows.
type DelegateEvent struct {
	Seq    uint64         `json:"seq"`
	Time   time.Time      `json:"time"`
	Kind   string         `json:"kind"` // spawn|start|step|tool|usage|done|fail|cancel|orphan|notice
	ID     string         `json:"id,omitempty"`
	Parent string         `json:"parent,omitempty"`
	Name   string         `json:"name,omitempty"`
	Depth  int            `json:"depth,omitempty"`
	Tool   string         `json:"tool,omitempty"`
	Status string         `json:"status,omitempty"`
	Steps  int            `json:"steps,omitempty"`
	Usage  provider.Usage `json:"usage,omitempty"`
	Task   string         `json:"task,omitempty"` // trimmed to a display line
	Detail string         `json:"detail,omitempty"`
}

// eventKey renders a monotonic sequence as a big-endian sortable key.
func eventKey(seq uint64) []byte {
	var b [8]byte
	be := seq
	for i := 7; i >= 0; i-- {
		b[i] = byte(be)
		be >>= 8
	}
	return b[:]
}

// AppendDelegateEvent assigns the next sequence number and appends one event,
// returning it. Ordering is total: seq order is creation order.
func (s *Store) AppendDelegateEvent(ev DelegateEvent) (DelegateEvent, error) {
	var err error
	err = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketEvents)
		ev.Seq, err = b.NextSequence()
		if err != nil {
			return err
		}
		ev.Time = time.Now()
		full, merr := json.Marshal(ev)
		if merr != nil {
			return merr
		}
		return b.Put(eventKey(ev.Seq), full)
	})
	return ev, err
}

// ListDelegateEvents returns events in ascending sequence order, optionally
// starting after a sequence (0 = from the beginning). A sequence of ^uint64(0)
// has no successor, so it yields no events rather than wrapping to 0 and
// replaying the whole log.
func (s *Store) ListDelegateEvents(after uint64, limit int) []DelegateEvent {
	var out []DelegateEvent
	if after == ^uint64(0) {
		return out
	}
	_ = s.view(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketEvents).Cursor()
		start := eventKey(after + 1)
		if after == 0 {
			start = eventKey(0)
		}
		for k, v := c.Seek(start); k != nil; k, v = c.Next() {
			var ev DelegateEvent
			if err := json.Unmarshal(v, &ev); err == nil {
				out = append(out, ev)
			} else {
				s.noteDBErr(err)
			}
			if limit > 0 && len(out) >= limit {
				return nil
			}
		}
		return nil
	})
	return out
}

// LatestDelegateEvents returns the newest n events, ascending.
func (s *Store) LatestDelegateEvents(n int) []DelegateEvent {
	if n <= 0 {
		return nil
	}
	var rev []DelegateEvent
	_ = s.view(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketEvents).Cursor()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			var ev DelegateEvent
			if err := json.Unmarshal(v, &ev); err == nil {
				rev = append(rev, ev)
			} else {
				s.noteDBErr(err)
			}
			if len(rev) >= n {
				return nil
			}
		}
		return nil
	})
	out := make([]DelegateEvent, len(rev))
	for i, ev := range rev {
		out[len(rev)-1-i] = ev
	}
	return out
}

// TrimDelegateEvents keeps at most the newest keep events.
func (s *Store) TrimDelegateEvents(keep int) int {
	if keep <= 0 {
		return 0
	}
	removed := 0
	_ = s.update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketEvents)
		count := b.Sequence()
		if int(count) <= keep {
			return nil
		}
		c := b.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			seq := binary.BigEndian.Uint64(k)
			if seq+uint64(keep) < count {
				if c.Delete() == nil {
					removed++
				}
			} else {
				break
			}
		}
		return nil
	})
	return removed
}

// ---------------------------------------------------------------------------
// Task board
// ---------------------------------------------------------------------------

// TaskEvidence is one receipt attached to a finished task. Three kinds exist:
// "claim" (the agent's own stated proof), "cmd" (a verify command that was run
// and its job id), and "delegate" (a sub-agent report that closed the task).
type TaskEvidence struct {
	Kind string    `json:"kind"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// TaskNote is a flag: a human- or machine-spotted imperfection on a task that
// is not fatal enough to reopen it. Surfaced in every render as an OFF signal.
type TaskNote struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// TaskRecord is one row of the persistent task board.
type TaskRecord struct {
	ID     string `json:"id"` // "t7" — sequence-based, stable across processes
	Seq    int64  `json:"seq"`
	Parent string `json:"parent,omitempty"` // parent task for hierarchy
	Title  string `json:"title"`            // imperative: "run the test suite"
	Doing  string `json:"doing,omitempty"`  // present form: "running the test suite"
	Detail string `json:"detail,omitempty"`
	// Status is pending | in_progress | done | cancelled. A pending task whose
	// dependencies are unfinished is *displayed* as blocked; the state is
	// always derived from DependsOn at render time, never stored, so no cleanup
	// pass is needed when a dependency completes.
	Status string `json:"status"`
	// Priority is high | med | low (empty = normal).
	Priority string `json:"priority,omitempty"`
	// DependsOn holds task ids this task cannot finish before.
	DependsOn []string `json:"depends_on,omitempty"`
	// VerifyMode is "command" when Verify holds a command whose exit code
	// decides completion (the completion oracle). Empty Verify means the
	// agent's own evidence claim is accepted (with a note).
	VerifyMode string         `json:"verify_mode,omitempty"`
	Verify     string         `json:"verify,omitempty"`
	Evidence   []TaskEvidence `json:"evidence,omitempty"`
	Notes      []TaskNote     `json:"notes,omitempty"`
	// DelegatedTo is the delegate id (d_...) bound to this task, if any.
	DelegatedTo string    `json:"delegated_to,omitempty"`
	Session     string    `json:"session,omitempty"`
	Created     time.Time `json:"created"`
	Started     time.Time `json:"started,omitempty"`
	Updated     time.Time `json:"updated"`
	Ended       time.Time `json:"ended,omitempty"`
}

// SaveTask persists a task. Fresh tasks (empty ID) are assigned the next
// monotonic sequence number and the id "t<seq>"; existing tasks keep theirs.
func (s *Store) SaveTask(t TaskRecord) (TaskRecord, error) {
	if t.ID == "" {
		err := s.db.Update(func(tx *bolt.Tx) error {
			seq, err := tx.Bucket(bucketTasks).NextSequence()
			if err != nil {
				return err
			}
			t.Seq = int64(seq)
			t.ID = fmt.Sprintf("t%d", seq)
			if t.Created.IsZero() {
				t.Created = time.Now()
			}
			t.Updated = time.Now()
			payload, merr := json.Marshal(t)
			if merr != nil {
				return merr
			}
			return tx.Bucket(bucketTasks).Put([]byte(t.ID), payload)
		})
		return t, err
	}
	t.Updated = time.Now()
	if t.Seq == 0 {
		t.Seq = taskSeqOf(t.ID)
	}
	payload, err := json.Marshal(t)
	if err != nil {
		return t, err
	}
	return t, s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTasks).Put([]byte(t.ID), payload)
	})
}

// LoadTask reads one task; the id accepts the strict form ("t3") or the bare
// number ("3").
func (s *Store) LoadTask(idv string) (TaskRecord, error) {
	var t TaskRecord
	if seq := taskSeqOf(idv); seq <= 0 {
		return t, fmt.Errorf("no task %q", idv)
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketTasks).Get([]byte(fmt.Sprintf("t%d", taskSeqOf(idv))))
		if raw == nil {
			return fmt.Errorf("no task %q", idv)
		}
		return json.Unmarshal(raw, &t)
	})
	return t, err
}

// taskSeqOf parses a task id: "t7" or "7" -> 7, anything else -> 0.
func taskSeqOf(raw string) int64 {
	n, err := strconv.ParseInt(strings.TrimPrefix(raw, "t"), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// ListTasks returns every task in board order (sequence ascending, which is
// also creation order).
func (s *Store) ListTasks(limit int) []TaskRecord {
	var out []TaskRecord
	_ = s.view(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTasks).ForEach(func(_, v []byte) error {
			var t TaskRecord
			if err := json.Unmarshal(v, &t); err == nil {
				out = append(out, t)
			} else {
				s.noteDBErr(err)
			}
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
