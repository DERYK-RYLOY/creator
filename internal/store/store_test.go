package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/DERYK-RYLOY/creator/internal/provider"
)

func newStoreAt(t *testing.T, dir string) *Store {
	t.Helper()
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestAppendMessagesRoundTrip(t *testing.T) {
	st := newStoreAt(t, filepath.Join(t.TempDir(), "creator"))
	if err := st.SaveSession(Session{ID: "s_1", Model: "m", Workspace: "/tmp/ws"}); err != nil {
		t.Fatalf("save header: %v", err)
	}
	msgs := []provider.Message{
		{Role: provider.RoleUser, Text: "fix the build"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "edit", Args: json.RawMessage(`{}`)}}},
		{Role: provider.RoleTool, ToolCallID: "c1", Text: "ok"},
		{Role: provider.RoleAssistant, Text: "done"},
	}
	for i, m := range msgs {
		if err := st.AppendMessage("s_1", i, m); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	loaded, err := st.LoadSession("s_1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Messages) != len(msgs) {
		t.Fatalf("messages = %d, want %d", len(loaded.Messages), len(msgs))
	}
	for i := range msgs {
		if loaded.Messages[i].Text != msgs[i].Text || loaded.Messages[i].Role != msgs[i].Role {
			t.Fatalf("message %d drifted: %+v", i, loaded.Messages[i])
		}
	}
	if loaded.Turns != len(msgs) {
		t.Fatalf("turn count = %d, want %d", loaded.Turns, len(msgs))
	}
}

func TestLegacySessionMigratesToMessageLog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "creator")
	st := newStoreAt(t, dir)

	// Seed a legacy document with embedded messages, bypassing SaveSession
	// (which strips them) exactly the way old versions wrote it.
	legacy := Session{
		ID:        "s_legacy",
		Model:     "m",
		Workspace: "/tmp/ws",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Text: "old turn"},
			{Role: provider.RoleAssistant, Text: "old answer"},
		},
	}
	raw, _ := json.Marshal(legacy)
	if err := st.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSessions).Put([]byte("s_legacy"), raw)
	}); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	loaded, err := st.LoadSession("s_legacy")
	if err != nil {
		t.Fatalf("load legacy: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("legacy messages = %d, want 2", len(loaded.Messages))
	}
	if loaded.Turns != 2 {
		t.Fatalf("turn count after migration = %d, want 2", loaded.Turns)
	}
}

func TestLockedDatabaseGetsFriendlyError(t *testing.T) {
	// Not every platform locks quickly enough for a deterministic test; assert
	// the friendly message only when a real lock error occurs, but always
	// verify the error mentions the database.
	dir := filepath.Join(t.TempDir(), "creator")
	first, err := Open(dir)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	defer first.Close()
	second, err := Open(dir)
	if err == nil {
		second.Close()
		return
	}
	if !strings.Contains(err.Error(), "database") {
		t.Errorf("expected the error to mention the database, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "another") {
		t.Errorf("expected the friendly lock hint, got %q", err.Error())
	}
}

func TestGetCheckpointExactPrefixAndAmbiguous(t *testing.T) {
	st := newStoreAt(t, filepath.Join(t.TempDir(), "creator"))
	src := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(src, []byte("v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := st.SaveCheckpoint("s", src, "note.txt", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = st.SaveCheckpoint("s", src, "note.txt", "test")
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCheckpoint(first.ID)
	if err != nil || got.ID != first.ID {
		t.Fatalf("exact get = %+v, %v", got, err)
	}
	// Prefix matching should be unambiguous only when exactly one stored id
	// starts with the prefix; otherwise it must fail.
	if err := st.db.Update(func(tx *bolt.Tx) error {
		for _, id := range []string{"cp_amb_one", "cp_amb_two"} {
			cp := Checkpoint{ID: id, Session: "s", Path: src, RelPath: "note.txt", Tool: "seed", Created: time.Now()}
			payload, _ := json.Marshal(cp)
			if err := tx.Bucket(bucketCheckpoints).Put([]byte(id), payload); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCheckpoint("cp_amb"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous prefix should fail with a clear error, got %v", err)
	}
	if got, err := st.GetCheckpoint("cp_amb_one"); err != nil || got.ID != "cp_amb_one" {
		t.Fatalf("full id should still resolve exactly, got %+v, %v", got, err)
	}
	longTypo := first.ID + "_typo_suffix_that_runs_long"
	if _, err := st.GetCheckpoint(longTypo); err == nil {
		t.Fatal("long typo should not match any checkpoint")
	}
}

func TestOrphanDelegatesOnlyMarksDeadOrOld(t *testing.T) {
	st := newStoreAt(t, filepath.Join(t.TempDir(), "creator"))
	recent := DelegateRecord{ID: "d_recent", Task: "recent anonymous", Status: "running", Created: time.Now()}
	if err := st.db.Update(func(tx *bolt.Tx) error {
		payload, _ := json.Marshal(recent)
		return tx.Bucket(bucketDelegates).Put([]byte(recent.ID), payload)
	}); err != nil {
		t.Fatal(err)
	}
	old := recent
	old.ID = "d_old"
	old.Created = time.Now().Add(-49 * time.Hour)
	if err := st.db.Update(func(tx *bolt.Tx) error {
		payload, _ := json.Marshal(old)
		return tx.Bucket(bucketDelegates).Put([]byte(old.ID), payload)
	}); err != nil {
		t.Fatal(err)
	}
	if n := st.OrphanDelegates(); n != 1 {
		t.Fatalf("orphans = %d, want 1", n)
	}
	got, err := st.LoadDelegate("d_recent")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" {
		t.Fatalf("recent anonymous delegate was orphaned: %q", got.Status)
	}
	got, err = st.LoadDelegate("d_old")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "orphaned" {
		t.Fatalf("old anonymous delegate not orphaned: %q", got.Status)
	}
}

func TestProcessAliveBasicPid(t *testing.T) {
	if !processAlive(os.Getpid(), time.Now()) {
		t.Fatal("own pid must report alive")
	}
	if processAlive(0, time.Now()) {
		t.Fatal("pid 0 must report dead")
	}
	if processAlive(2147483646, time.Now()) {
		t.Fatal("huge (unused) pid must report dead")
	}
}

func TestOrphanDelegatesNeverClobbersDoneRecords(t *testing.T) {
	st := newStoreAt(t, filepath.Join(t.TempDir(), "creator"))
	done := DelegateRecord{ID: "d_done", Task: "x", Status: "done", OwnerPID: 2147483647, Created: time.Now().Add(-49 * time.Hour)}
	if err := st.db.Update(func(tx *bolt.Tx) error {
		payload, _ := json.Marshal(done)
		return tx.Bucket(bucketDelegates).Put([]byte(done.ID), payload)
	}); err != nil {
		t.Fatal(err)
	}
	if n := st.OrphanDelegates(); n != 0 {
		t.Fatalf("orphans = %d, want 0 for terminal records", n)
	}
	got, err := st.LoadDelegate("d_done")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "done" {
		t.Fatalf("terminal record clobbered: %q", got.Status)
	}
	if got.OwnerPID != 2147483647 {
		t.Fatalf("orphan pass re-stamped OwnerPID to %d", got.OwnerPID)
	}
}

func TestPruneCheckpointsDeletesOld(t *testing.T) {
	st := newStoreAt(t, filepath.Join(t.TempDir(), "creator"))
	src := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(src, []byte("v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cp, err := st.SaveCheckpoint("s", src, "note.txt", "test")
	if err != nil {
		t.Fatal(err)
	}
	if cp.Mode == 0 {
		t.Fatal("checkpoint should record original permission bits")
	}
	if err := st.db.Update(func(tx *bolt.Tx) error {
		cp.Created = time.Now().Add(-49 * time.Hour)
		payload, _ := json.Marshal(cp)
		return tx.Bucket(bucketCheckpoints).Put([]byte(cp.ID), payload)
	}); err != nil {
		t.Fatal(err)
	}
	if n := st.PruneCheckpoints(24 * time.Hour); n != 1 {
		t.Fatalf("pruned = %d, want 1", n)
	}
	if _, err := st.GetCheckpoint(cp.ID); err == nil {
		t.Fatal("checkpoint should be gone after prune")
	}
}
