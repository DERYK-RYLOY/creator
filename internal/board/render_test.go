package board

import (
	"strings"
	"testing"
	"time"

	"github.com/creatorcli/creator/internal/store"
)

func TestRenderBoardNestsToAnyDepth(t *testing.T) {
	recs := []store.TaskRecord{
		{ID: "t1", Title: "root work", Status: StatusPending, Session: "s"},
		{ID: "t2", Title: "child work", Parent: "t1", Status: StatusPending, Session: "s"},
		{ID: "t3", Title: "grandchild work", Parent: "t2", Status: StatusPending, Session: "s"},
	}
	out := RenderBoard(recs, time.Now())
	for _, want := range []string{"root work", "child work", "grandchild work"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
	if i, j := strings.Index(out, "root work"), strings.Index(out, "child work"); i == -1 || j < i {
		t.Fatalf("nesting order wrong:\n%s", out)
	}
	if j, k := strings.Index(out, "child work"), strings.Index(out, "grandchild work"); j == -1 || k < j {
		t.Fatalf("nesting order wrong:\n%s", out)
	}
}

func TestRenderBoardShowsManyChildrenTruncation(t *testing.T) {
	recs := []store.TaskRecord{{ID: "t1", Title: "root", Status: StatusPending, Session: "s"}}
	for i, name := range []string{"c1", "c2", "c3", "c4", "c5", "c6"} {
		recs = append(recs, store.TaskRecord{ID: "child" + name, Title: "kid " + name, Parent: "t1", Status: StatusPending, Session: "s", Seq: int64(10 + i)})
	}
	out := RenderBoard(recs, time.Now())
	if !strings.Contains(out, "kid c1") || !strings.Contains(out, "kid c4") {
		t.Fatalf("first children missing:\n%s", out)
	}
	if !strings.Contains(out, "more") {
		t.Fatalf("truncation marker missing:\n%s", out)
	}
	if strings.Contains(out, "kid c6") {
		t.Fatalf("children beyond the cap must be collapsed:\n%s", out)
	}
}

func TestRenderBoardKeepsCycleRowsVisibleAsRoots(t *testing.T) {
	recs := []store.TaskRecord{
		{ID: "a", Title: "cycle a", Parent: "b", Status: StatusPending, Session: "s"},
		{ID: "b", Title: "cycle b", Parent: "a", Status: StatusPending, Session: "s"},
	}
	out := RenderBoard(recs, time.Now())
	if !strings.Contains(out, "cycle a") || !strings.Contains(out, "cycle b") {
		t.Fatalf("cycle members must remain visible:\n%s", out)
	}
}
