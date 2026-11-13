package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func keysIn(t *testing.T, dir string) string {
	t.Helper()
	return filepath.Join(dir, "keys.json")
}

func TestSaveAndLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREATOR_DATA_DIR", dir)

	if err := SaveCred("openai", Cred{Key: "sk-1"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := SaveCred("anthropic", Cred{Key: "sk-2", BaseURL: "https://x"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	m, err := SavedCreds()
	if err != nil {
		t.Fatalf("saved: %v", err)
	}
	if len(m) != 2 || m["openai"].Key != "sk-1" || m["anthropic"].BaseURL != "https://x" {
		t.Fatalf("roundtrip mismatch: %v", m)
	}
	c, ok, err := LoadCred("openai")
	if err != nil || !ok || c.Key != "sk-1" {
		t.Fatalf("load openai = %v, %v, %v", c, ok, err)
	}
	if err := DeleteCred("openai"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := LoadCred("openai"); ok {
		t.Fatal("openai should be gone")
	}
	m, _ = SavedCreds()
	if _, ok := m["anthropic"]; !ok {
		t.Fatal("anthropic must be untouched by deleting openai")
	}
}

// A corrupt keys.json previously caused SaveCred/DeleteCred to silently
// overwrite the file, wiping every other saved credential. Saves must now
// fail closed and leave the file byte-identical.
func TestCorruptKeysFileNeverWiped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREATOR_DATA_DIR", dir)

	f := keysIn(t, dir)
	corrupt := []byte("{not valid json")
	if err := os.WriteFile(f, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SaveCred("openai", Cred{Key: "sk-new"}); err == nil {
		t.Fatal("save over corrupt keys.json must error, not silently wipe")
	}
	if err := DeleteCred("openai"); err == nil {
		t.Fatal("delete over corrupt keys.json must error")
	}
	if _, err := SavedCreds(); err == nil {
		t.Fatal("SavedCreds over corrupt keys.json must error")
	}
	if _, _, err := LoadCred("openai"); err == nil {
		t.Fatal("LoadCred over corrupt keys.json must error")
	}
	got, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(corrupt) {
		t.Fatalf("corrupt file must be left untouched; got %q", got)
	}
}

func TestMissingOrEmptyKeysFileIsEmptyNotError(t *testing.T) {
	for name := range map[string]struct{}{"missing": {}, "empty": {}} {
		dir := t.TempDir()
		t.Setenv("CREATOR_DATA_DIR", dir)
		if name == "empty" {
			if err := os.WriteFile(keysIn(t, dir), []byte(""), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		m, err := SavedCreds()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(m) != 0 {
			t.Fatalf("%s: want empty map, got %v", name, m)
		}
	}
}

func TestAtomicWriteNoTempLitterAndBackup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREATOR_DATA_DIR", dir)

	if err := SaveCred("a", Cred{Key: "1"}); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".keys-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temp litter left behind: %v (%v)", matches, err)
	}
	if err := SaveCred("a", Cred{Key: "2"}); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(keysIn(t, dir) + ".bak")
	if err != nil {
		t.Fatalf("expected keys.json.bak after second write: %v", err)
	}
	if !strings.Contains(string(bak), `"1"`) {
		t.Fatalf("bak should hold the previous good file, got %q", bak)
	}
	m, err := SavedCreds()
	if err != nil {
		t.Fatal(err)
	}
	if m["a"].Key != "2" {
		t.Fatalf("current file should hold the new value: %v", m)
	}
}
