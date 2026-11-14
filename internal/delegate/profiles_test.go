package delegate

import (
	"context"
	"strings"
	"testing"

	"github.com/DERYK-RYLOY/creator/internal/tools"
)

func TestLookupProfile(t *testing.T) {
	for _, name := range ProfileNames() {
		if _, ok := LookupProfile(name); !ok {
			t.Fatalf("ProfileNames lists %q but LookupProfile misses it", name)
		}
	}
	if _, ok := LookupProfile("  EXPLORER "); !ok {
		t.Fatal("profile lookup should be case- and space-insensitive")
	}
	if _, ok := LookupProfile("wizard"); ok {
		t.Fatal("unknown profile must not resolve")
	}
	// The four roles the delegation contract promises.
	for _, want := range []string{"editor", "explorer", "planner", "reviewer"} {
		if _, ok := LookupProfile(want); !ok {
			t.Fatalf("missing built-in profile %q", want)
		}
	}
}

func TestApplyProfileNarrowsAndCannotWiden(t *testing.T) {
	p, ok := LookupProfile("explorer")
	if !ok {
		t.Fatal("explorer missing")
	}
	spec := Spec{
		Task:     "map the retry paths",
		Tools:    []string{"read", "write", "shell", "grep"},
		CanSpawn: false,
	}
	applyProfile(&spec, p)

	if !spec.ReadOnly {
		t.Fatal("a read-only profile must force ReadOnly")
	}
	if spec.CanSpawn {
		t.Fatal("explorer must not gain spawning because the spec asked for it")
	}
	if spec.Name != "explorer" {
		t.Fatalf("name = %q, want the profile name", spec.Name)
	}
	if spec.role == "" {
		t.Fatal("profile guidance not attached")
	}
	if spec.MaxSteps != p.MaxSteps || spec.OutputTokens != p.OutputTokens {
		t.Fatalf("profile defaults not applied: steps=%d tokens=%d", spec.MaxSteps, spec.OutputTokens)
	}
	got := strings.Join(spec.Tools, ",")
	if got != "read,grep" {
		t.Fatalf("allowlist = %q, want the intersection read,grep", got)
	}
}

func TestApplyProfileKeepsExplicitBudget(t *testing.T) {
	p, _ := LookupProfile("planner")
	spec := Spec{Task: "plan it", MaxSteps: 3, OutputTokens: 900}
	applyProfile(&spec, p)
	if spec.MaxSteps != 3 || spec.OutputTokens != 900 {
		t.Fatalf("explicit budgets must win: steps=%d tokens=%d", spec.MaxSteps, spec.OutputTokens)
	}
}

func TestApplyProfileEditorInheritsTools(t *testing.T) {
	p, _ := LookupProfile("editor")
	spec := Spec{Task: "fix it"}
	applyProfile(&spec, p)
	if spec.Tools != nil {
		t.Fatalf("editor should inherit the toolset, got %v", spec.Tools)
	}
	if spec.ReadOnly {
		t.Fatal("editor must be able to write")
	}
}

func TestRegistryPredicateHonoursProfile(t *testing.T) {
	p, _ := LookupProfile("explorer")
	spec := Spec{Task: "look around"}
	applyProfile(&spec, p)
	pred := registryPredicate(spec)

	cases := map[string]bool{
		"read": true, "grep": true, "outline": true, "out": true,
		"write": false, "shell": false, "web_fetch": false,
		"delegate": false,
	}
	for name, want := range cases {
		if got := pred(tools.Tool{Name: name, ReadOnly: name != "write" && name != "shell"}); got != want {
			t.Errorf("predicate(%s) = %v, want %v", name, got, want)
		}
	}
}

func TestProfileGuidanceReachesPrompt(t *testing.T) {
	p, _ := LookupProfile("reviewer")
	spec := Spec{Task: "review the diff"}
	applyProfile(&spec, p)
	m := &Manager{opts: Options{OutputTokens: 300}}
	got := m.prompt("d_test", spec)
	if !strings.Contains(got, "review the diff") {
		t.Fatal("task missing from the prompt")
	}
	if !strings.Contains(got, "Role: reviewer") {
		t.Fatalf("profile guidance missing from the prompt: %q", got)
	}
}

func TestSpawnRejectsUnknownProfile(t *testing.T) {
	rig := newRig(t)
	m := newManager(t, rig, &scriptEcho{}, nil)
	_, err := m.Spawn(context.Background(), "s_root", Spec{Task: "do something", Profile: "wizard"})
	if err == nil {
		t.Fatal("expected an unknown-profile error")
	}
	if !strings.Contains(err.Error(), "available:") {
		t.Fatalf("error should list the available profiles: %v", err)
	}
}

func TestSpawnAppliesProfileToRecord(t *testing.T) {
	rig := newRig(t)
	m := newManager(t, rig, &scriptEcho{}, nil)
	rec, err := m.Spawn(context.Background(), "s_root", Spec{Task: "map the note", Profile: "explorer"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Name != "explorer" {
		t.Fatalf("record name = %q, want explorer", rec.Name)
	}
	// Let the delegate finish so test cleanup is clean and the record settles.
	if _, err := m.Wait(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}
}
