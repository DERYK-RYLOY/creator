package runner

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/creatorcli/creator/internal/store"
)

func TestDetachedJobSurvivesStopAll(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := NewManager(st)

	command := "echo hi; sleep 0.8; echo bye"
	if runtime.GOOS == "windows" {
		// The runner resolves auto to powershell on this machine and passes
		// -NoProfile -NonInteractive -Command, so keep the payload native.
		command = "Write-Output hi; Start-Sleep -Milliseconds 800; Write-Output bye"
	}
	job, err := m.Start(context.Background(), Spec{Command: command, Detach: true})
	if err != nil {
		t.Fatalf("start detached: %v", err)
	}
	if job == nil || job.ID == "" {
		t.Fatal("no job returned")
	}
	artifact := st.ArtifactPath(job.ID)

	waitFor := func(needle string, timeout time.Duration) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %q in the artifact", needle)
			}
			b, _ := os.ReadFile(artifact)
			if strings.Contains(string(b), needle) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitFor("hi", 15*time.Second)
	m.StopAll() // must not touch the detached child
	waitFor("bye", 15*time.Second)
}

func newManager(t *testing.T) (*Manager, func()) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	m := NewManager(st)
	return m, func() {
		m.StopAll()
		st.Close()
	}
}

func TestRunsCommandAndCapturesOutput(t *testing.T) {
	m, cleanup := newManager(t)
	defer cleanup()

	job, err := m.Start(context.Background(), Spec{Command: "echo hello-creator", Timeout: 20 * time.Second})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := job.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
	snap := job.Snapshot()
	if snap.Status != StatusSuccess {
		t.Fatalf("status = %s, want success", snap.Status)
	}
	if snap.ExitCode != 0 {
		t.Fatalf("exit code = %d", snap.ExitCode)
	}
	out, err := job.Output()
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	if !strings.Contains(out, "hello-creator") {
		t.Fatalf("missing output: %q", out)
	}
}

func TestNonZeroExit(t *testing.T) {
	m, cleanup := newManager(t)
	defer cleanup()

	job, err := m.Start(context.Background(), Spec{Command: "exit 7", Timeout: 20 * time.Second})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = job.Wait(context.Background())
	snap := job.Snapshot()
	if snap.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", snap.Status)
	}
	if snap.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", snap.ExitCode)
	}
}

func TestTimeoutKillsJob(t *testing.T) {
	m, cleanup := newManager(t)
	defer cleanup()

	job, err := m.Start(context.Background(), Spec{Command: "sleep 30", Timeout: 400 * time.Millisecond})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-job.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("timed out job never finished")
	}
	if snap := job.Snapshot(); snap.Status != StatusTimeout {
		t.Fatalf("status = %s, want timeout", snap.Status)
	}
}

func TestBackgroundJobKeepsRunning(t *testing.T) {
	m, cleanup := newManager(t)
	defer cleanup()

	job, err := m.Start(context.Background(), Spec{Command: "sleep 2 ; echo done", Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	snap := job.Snapshot()
	if snap.Status != StatusRunning {
		t.Fatalf("start should return while running, got %s", snap.Status)
	}
	if len(m.Running()) != 1 {
		t.Fatalf("expected the job to be listed as running")
	}
	if err := job.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
	out, _ := job.Output()
	if !strings.Contains(out, "done") {
		t.Fatalf("missing output after background completion: %q", out)
	}
}

func TestKill(t *testing.T) {
	m, cleanup := newManager(t)
	defer cleanup()

	job, err := m.Start(context.Background(), Spec{Command: "sleep 20", Timeout: time.Minute})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := m.Kill(job.ID); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case <-job.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("killed job never finished")
	}
	if snap := job.Snapshot(); snap.Status != StatusKilled {
		t.Fatalf("status = %s, want killed", snap.Status)
	}
}

func TestPTYFallsBackGracefully(t *testing.T) {
	m, cleanup := newManager(t)
	defer cleanup()

	job, err := m.Start(context.Background(), Spec{
		Command: "echo pty-smoke",
		Timeout: 20 * time.Second,
		PTY:     true,
	})
	if err != nil {
		t.Fatalf("start with PTY: %v", err)
	}
	if err := job.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
	snap := job.Snapshot()
	if snap.Status != StatusSuccess {
		t.Fatalf("status = %s, want success", snap.Status)
	}
	out, _ := job.Output()
	if !strings.Contains(out, "pty-smoke") {
		t.Fatalf("output missing: %q", out)
	}
}
