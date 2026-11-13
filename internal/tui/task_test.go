package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/creatorcli/creator/internal/store"
)

func TestTaskViewRendersChecklist(t *testing.T) {
	m := &model{
		width:  80,
		height: 30,
		taskRows: []taskBook{
			{Rec: store.TaskRecord{ID: "t1", Title: "prove build passes", Status: "in_progress", Priority: "high", Verify: "go test ./...", Doing: "running the suite", Seq: 1, Created: time.Now(), Updated: time.Now()}},
			{Rec: store.TaskRecord{ID: "t2", Title: "review the patch", Status: "pending", Priority: "med", Seq: 2, Created: time.Now(), Updated: time.Now()}},
			{Rec: store.TaskRecord{ID: "t3", Title: "write the docs", Status: "done", Priority: "low", Verify: "test -f README.md", Seq: 3, Created: time.Now(), Updated: time.Now()}},
		},
	}
	got := m.taskView(80, 30)
	for _, want := range []string{"to do", "[>] t1", "[ ] t2", "[x] t3", "1/3 done", "1 verified"} {
		if !strings.Contains(got, want) {
			t.Fatalf("task view missing %q:\n%s", want, got)
		}
	}
}
