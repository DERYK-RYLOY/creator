package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/creatorcli/creator/internal/provider"
)

func TestFilteredProvidersFindsGoogle(t *testing.T) {
	m := &model{overlayQuery: "google"}
	list := m.filteredProviders()
	if len(list) == 0 {
		t.Fatal("expected google provider")
	}
	found := false
	for _, d := range list {
		if d.Slug == "google" {
			found = true
		}
	}
	if !found {
		t.Fatalf("google missing from matches: %+v", list)
	}
}

func TestBeginProviderConnectStartsKeyFlow(t *testing.T) {
	var google provider.Descriptor
	for _, d := range provider.Descriptors() {
		if d.Slug == "google" {
			google = d
			break
		}
	}
	if google.Slug == "" {
		t.Fatal("google provider not found in registry")
	}
	m := &model{}
	m.beginProviderConnect(google)
	if m.overlay != overlayProviderKey {
		t.Fatalf("overlay = %d, want provider key flow", m.overlay)
	}
	if m.pendingProvider.Slug != "google" {
		t.Fatalf("pending provider = %q", m.pendingProvider.Slug)
	}
}

func TestProviderMatchIgnoresCase(t *testing.T) {
	m := &model{overlayQuery: "GROQ"}
	list := m.filteredProviders()
	if len(list) == 0 {
		t.Fatal("case-insensitive provider search returned nothing")
	}
	joined := ""
	for _, d := range list {
		joined += d.Slug + " " + strings.ToLower(d.Name)
	}
	if !strings.Contains(joined, "groq") {
		t.Fatalf("groq missing from %q", joined)
	}
}

func TestFilteredModelRowsOnlyShowsFree(t *testing.T) {
	m := &model{modelRows: []modelRow{
		{Provider: "nvidia", Model: "free-model", Free: true},
		{Provider: "nvidia", Model: "paid-model", Free: false},
		{Provider: "pollinations", Model: "openai-fast", Free: true},
	}}
	filtered := m.filteredModelRows()
	if len(filtered) != 2 {
		t.Fatalf("expected only free rows, got %+v", filtered)
	}
	for _, r := range filtered {
		if !r.Free {
			t.Fatalf("paid model leaked into the list: %+v", r)
		}
	}
}

// A cancelled-then-superseded model scan must not re-open its overlay or
// hijack the overlay the user moved to (e.g. typing an API key).
func TestStaleModelScanDoesNotReopenOverlay(t *testing.T) {
	m := &model{
		opts:          options{},
		overlay:       overlayProviderKey,
		keyInput:      "sk-ant-draft-do-not-lose",
		modelsBusy:    false, // cancelled
		modelsScanGen: 7,
	}
	out, _ := m.Update(modelsDoneMsg{
		rows:    []modelRow{{Provider: "x", Model: "y"}},
		sources: []string{"x: test"},
		gen:     6, // stale: older than the scan that was cancelled
	})
	tm := out.(*model)
	if tm.overlay != overlayProviderKey {
		t.Fatalf("stale scan must not steal the provider-key overlay, got overlay %d", tm.overlay)
	}
	if tm.keyInput != "sk-ant-draft-do-not-lose" {
		t.Fatalf("key input must be preserved, got %q", tm.keyInput)
	}
}

func TestFreshModelScanOpensModelsOverlay(t *testing.T) {
	m := &model{
		opts:          options{},
		overlay:       overlayProviderKey,
		keyInput:      "sk-ant-draft",
		modelsBusy:    true,
		modelsScanGen: 7,
	}
	out, _ := m.Update(modelsDoneMsg{
		rows:    []modelRow{{Provider: "x", Model: "y"}},
		sources: []string{"x: test"},
		gen:     7,
	})
	tm := out.(*model)
	if tm.overlay != overlayModels {
		t.Fatalf("a fresh scan should open the models overlay, got %d", tm.overlay)
	}
	if tm.modelsBusy {
		t.Fatal("busy flag must clear when the fresh scan completes")
	}
}

// Paste must not register control combos or other invisible junk in the
// API-key box — only printable, non-space ASCII survives.
func TestProviderKeyBoxDropsControlBytes(t *testing.T) {
	m := &model{overlay: overlayProviderKey}
	mk := func(r rune) tea.KeyMsg {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
	}
	paste := "AIzaSyCleanKey-123"
	for _, r := range paste {
		m.Update(mk(r))
	}
	for _, r := range []rune{0x00, 0x16, 0x7F, 0x03, 0x19} {
		m.Update(mk(r))
	}
	m.Update(mk('X'))
	if m.keyInput != paste+"X" {
		t.Fatalf("control bytes should be dropped; keyInput=%q", m.keyInput)
	}
}

// A Ctrl+V key event must never enter the API-key box.
func TestProviderKeyBoxIgnoresCtrlVKeyType(t *testing.T) {
	m := &model{overlay: overlayProviderKey}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if m.keyInput != "" {
		t.Fatalf("KeyCtrlV must not enter the key box, got %q", m.keyInput)
	}
}
