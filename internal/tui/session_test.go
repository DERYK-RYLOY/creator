package tui

import (
	"testing"

	"github.com/creatorcli/creator/internal/provider"
	"github.com/creatorcli/creator/internal/store"
)

func TestAttachSessionRestoresTranscriptAndActiveModel(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSession(store.Session{ID: "s_resume", Provider: "test-provider", Model: "test-model", Workspace: "/ws"}); err != nil {
		t.Fatal(err)
	}
	msgs := []provider.Message{
		{Role: provider.RoleUser, Text: "hello"},
		{Role: provider.RoleAssistant, Text: "world"},
	}
	for i, msg := range msgs {
		if err := st.AppendMessage("s_resume", i, msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	m := &model{opts: tui_dummyOptions(dir), cwd: "/ws"}
	m.attachSession("s_resume")
	if m.sessionID != "s_resume" {
		t.Fatalf("session id = %q", m.sessionID)
	}
	if m.opts.Provider != "test-provider" || m.opts.Model != "test-model" {
		t.Fatalf("active provider/model = %s/%s", m.opts.Provider, m.opts.Model)
	}
	if len(m.transcript) == 0 {
		t.Fatal("resumed session should restore the transcript")
	}
	foundAssistant := false
	for _, block := range m.transcript {
		if block.kind == kindAssistant && block.body == "world" {
			foundAssistant = true
		}
	}
	if !foundAssistant {
		t.Fatalf("assistant message not restored: %+v", m.transcript)
	}
}

func tui_dummyOptions(dir string) options {
	return options{DataDir: dir}
}
