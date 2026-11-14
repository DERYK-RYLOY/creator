package board

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/DERYK-RYLOY/creator/internal/store"
)

// TaskDetail is the exported single-task render used by the CLI
// (creator tasks <id>).
func TaskDetail(t store.TaskRecord, depStatus map[string]string, now time.Time) string {
	return renderTask(t, depStatus, now)
}

// renderTask renders one task in the shared board language: one status row,
// then its detail, notes and receipts underneath. depStatus maps dependency
// ids to their status ("pending", "in_progress", "done", ...); an absent key
// means "unknown here" (durability across processes matters more than showing
// a doubtful marker).
func renderTask(t store.TaskRecord, depStatus map[string]string, now time.Time) string {
	lookup := depStatusOfID(depStatus)
	var b strings.Builder
	b.WriteString(rowIcon(t, lookup, now))
	b.WriteString("\n")
	if d := strings.TrimSpace(t.Detail); d != "" {
		b.WriteString(indentLines(d, 4))
		b.WriteString("\n")
	}
	for _, n := range t.Notes {
		b.WriteString(fmt.Sprintf("    note: %s (%s)\n", firstLine(n.Text, 160), stamp(n.At, now)))
	}
	for _, ev := range t.Evidence {
		b.WriteString(fmt.Sprintf("    receipt(%s): %s\n", ev.Kind, firstLine(ev.Text, 200)))
	}
	return strings.TrimRight(b.String(), "\n")
}

// RenderBoard renders the whole board for the model and for the CLI:
// digest header, rows grouped open-first, kids nested under parents.
func RenderBoard(recs []store.TaskRecord, now time.Time) string {
	status := map[string]store.TaskRecord{}
	for _, r := range recs {
		status[r.ID] = r
	}
	depStatus := func(id string) string {
		if r, ok := status[id]; ok {
			return r.Status
		}
		return ""
	}
	var b strings.Builder

	var open int64
	var running int64
	var doneCount int64
	var canceledCount int64
	var flagged int64
	var blockedCount int64
	var staleCount int64
	for _, r := range recs {
		switch {
		case r.Status == StatusInProgress:
			running++
			if !r.Started.IsZero() && now.Sub(r.Started) > staleAfter {
				staleCount++
			}
		case r.Status == StatusDone:
			doneCount++
		case r.Status == StatusCancelled:
			canceledCount++
		default:
			open++
			if isBlocked(r, depStatus) {
				blockedCount++
			}
		}
		if len(r.Notes) > 0 {
			flagged++
		}
	}
	b.WriteString(fmt.Sprintf(
		"task board · %d tasks · %d done · %d in progress · %d open · %d blocked · %d flagged · %d stale\n",
		len(recs), doneCount, running, open, blockedCount, flagged, staleCount))
	if len(recs) == 0 {
		b.WriteString("(empty — add tasks before working)\n")
		return b.String()
	}

	parents := map[string]bool{}
	children := map[string][]store.TaskRecord{}
	for _, r := range recs {
		_, parentKnown := status[r.Parent]
		switch {
		case r.Parent == "":
			parents[r.ID] = true // no parent: root
		case !parentKnown:
			parents[r.ID] = true // parent missing: degrade to a root row
		case hasParentCycle(r, status):
			parents[r.ID] = true // corrupt cycle: keep the row visible as a root
		default:
			children[r.Parent] = append(children[r.Parent], r)
		}
	}

	// Order roots: in_progress first, then pending (priority weight then seq),
	// then done, then cancelled. Children follow their parent, nested to any
	// depth; cyclic or disconnected islands stay visible as roots.
	roots := selectRootOrder(recs, parents)
	for _, r := range roots {
		renderRow(&b, r, 0, children, status, depStatusOf(status), now)
	}
	return b.String()
}

const maxRenderDepth = 16

func renderRow(b *strings.Builder, r store.TaskRecord, depth int, children map[string][]store.TaskRecord, byID map[string]store.TaskRecord, depStatus func(string) string, now time.Time) {
	line := rowIcon(r, depStatus, now)
	if depth == 0 {
		b.WriteString(line)
	} else {
		b.WriteString(indentLines(line, depth*4))
	}
	b.WriteString("\n")
	kids := children[r.ID]
	if len(kids) == 0 {
		return
	}
	if depth >= maxRenderDepth {
		fmt.Fprintf(b, "%s└ … %d deeper task(s) hidden\n", strings.Repeat("    ", depth+1), len(kids))
		return
	}
	for k, c := range kids {
		if k == 4 {
			fmt.Fprintf(b, "%s└ … %d more\n", strings.Repeat("    ", depth+1), len(kids)-4)
			break
		}
		renderRow(b, c, depth+1, children, byID, depStatus, now)
	}
}

// hasParentCycle reports whether walking r's parent chain revisits a task.
// Such rows are kept visible as roots instead of vanishing from both the
// root list and every parent's child list.
func hasParentCycle(r store.TaskRecord, byID map[string]store.TaskRecord) bool {
	seen := map[string]bool{r.ID: true}
	cur := r.Parent
	for cur != "" {
		if seen[cur] {
			return true
		}
		seen[cur] = true
		p, ok := byID[cur]
		if !ok {
			return false
		}
		cur = p.Parent
		if len(seen) > len(byID)+1 {
			return true
		}
	}
	return false
}

// rowIcon is one board row.
func rowIcon(t store.TaskRecord, depStatus func(string) string, now time.Time) string {
	icon := "[ ]"
	hasDeps := len(t.DependsOn) > 0
	blocked := hasDeps && isBlocked(t, depStatus)
	switch t.Status {
	case StatusInProgress:
		icon = "[>]"
	case StatusDone:
		icon = "[x]"
	case StatusCancelled:
		icon = "[-]"
	default:
		if blocked {
			icon = "[B]"
		}
	}

	parts := []string{
		fmt.Sprintf("%s %s", icon, t.ID),
		t.Title,
	}
	if t.Status == StatusInProgress {
		parts = append(parts, "running "+stamp(t.Started, now))
	}
	if t.Status == StatusInProgress && !t.Started.IsZero() && now.Sub(t.Started) > staleAfter {
		parts = append(parts, "STALE")
	}
	if t.Doing != "" {
		parts = append(parts, "doing: "+firstLine(t.Doing, 60))
	}
	if t.Priority == "high" {
		parts = append(parts, "HIGH")
	}
	if t.Verify != "" {
		parts = append(parts, "verify: "+firstLine(t.Verify, 60))
	}
	if t.Status != StatusDone && t.Status != StatusCancelled && hasDeps {
		var notDone []string
		for _, d := range t.DependsOn {
			st := depStatus(d)
			if st == "" {
				notDone = append(notDone, d+" (?)")
			} else if st != StatusDone {
				notDone = append(notDone, d+" ("+st+")")
			}
		}
		if len(notDone) > 0 {
			parts = append(parts, "deps: "+strings.Join(notDone, ", "))
		}
	}
	if len(t.Notes) > 0 {
		tag := "note"
		if t.Status == StatusInProgress || t.Status == StatusDone {
			tag = "OFF"
		}
		parts = append(parts, fmt.Sprintf("%s: %s", tag, firstLine(t.Notes[len(t.Notes)-1].Text, 100)))
	}
	if t.Status == StatusDone {
		kinds := map[string]bool{}
		for _, ev := range t.Evidence {
			kinds[ev.Kind] = true
		}
		var kindList []string
		for _, k := range []string{"cmd", "verify", "delegate", "claim"} {
			if kinds[k] {
				kindList = append(kindList, k)
			}
		}
		if len(kindList) > 0 {
			parts = append(parts, "receipts: "+strings.Join(kindList, "+"))
		}
	}
	if t.DelegatedTo != "" {
		parts = append(parts, "delegated→"+t.DelegatedTo)
	}
	return strings.Join(parts, " · ")
}

// selectRootOrder orders displayed roots: running, then pending by priority
// (then seq), then done, then cancelled. Children never appear here.
func selectRootOrder(recs []store.TaskRecord, parents map[string]bool) []store.TaskRecord {
	var roots []store.TaskRecord
	for _, r := range recs {
		if parents[r.ID] {
			roots = append(roots, r)
		}
	}
	group := func(r store.TaskRecord) int {
		switch r.Status {
		case StatusInProgress:
			return 0
		case StatusDone:
			return 2
		case StatusCancelled:
			return 3
		default:
			return 1
		}
	}
	sort.SliceStable(roots, func(i, j int) bool {
		gi, gj := group(roots[i]), group(roots[j])
		if gi != gj {
			return gi < gj
		}
		if gi == 1 && priorityWeight(roots[i].Priority) != priorityWeight(roots[j].Priority) {
			return priorityWeight(roots[i].Priority) < priorityWeight(roots[j].Priority)
		}
		return roots[i].Seq < roots[j].Seq
	})
	return roots
}

func isBlocked(r store.TaskRecord, depStatus func(string) string) bool {
	if len(r.DependsOn) == 0 {
		return false
	}
	if depStatus == nil {
		return true
	}
	for _, d := range r.DependsOn {
		if s := depStatus(d); s != StatusDone {
			return true
		}
	}
	return false
}

func depStatusOf(status map[string]store.TaskRecord) func(string) string {
	return func(id string) string {
		if r, ok := status[id]; ok {
			return r.Status
		}
		return ""
	}
}

// depStatusOfID adapts an id→status map into the rowIcon lookup.
func depStatusOfID(m map[string]string) func(string) string {
	return func(id string) string { return m[id] }
}

// stamp renders a duration-since timestamp ("5m") for display.
func stamp(from, now time.Time) string {
	d := now.Sub(from)
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

func indentLines(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

func priorityWeight(p string) int {
	switch p {
	case "high":
		return 0
	case "low":
		return 2
	default:
		return 1
	}
}
