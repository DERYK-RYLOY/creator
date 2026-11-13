package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadPrecedence checks defaults < file < env < overrides in one table.
func TestLoadPrecedence(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"provider":"anthropic","model":"claude-sonnet-4-5","max_tokens":2048,"stream":false,"policy_mode":"strict"}`)
	t.Setenv("CREATOR_DATA_DIR", dir)
	t.Setenv("CREATOR_POLICY", "yolo")

	// 1: file values, then env on top (env beats file for policy).
	t.Run("file then env", func(t *testing.T) {
		cfg, err := Load(Config{})
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.Provider != "anthropic" || cfg.Model != "claude-sonnet-4-5" {
			t.Fatalf("file fields lost: provider=%q model=%q", cfg.Provider, cfg.Model)
		}
		if cfg.MaxTokens != 2048 {
			t.Fatalf("max_tokens = %d, want 2048 from file", cfg.MaxTokens)
		}
		if cfg.Stream {
			t.Fatalf("stream = true, want file's false; default must not clobber an explicit file value")
		}
		if cfg.PolicyMode != "yolo" {
			t.Fatalf("env must beat file: policy = %q", cfg.PolicyMode)
		}
	})

	// 2: flags beat everything.
	t.Run("overrides win", func(t *testing.T) {
		cfg, err := Load(Config{PolicyMode: "balanced", MaxTokens: 512})
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.PolicyMode != "balanced" || cfg.MaxTokens != 512 {
			t.Fatalf("overrides ignored: %+v", cfg)
		}
	})
}

// TestStreamTriState: stream is on by default; only StreamIsSet may turn it
// off; CREATOR_STREAM can too.
func TestStreamTriState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREATOR_DATA_DIR", dir)

	cfg, err := Load(Config{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Stream {
		t.Fatal("stream should default to on")
	}

	cfg, err = Load(Config{Stream: false, StreamIsSet: true})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Stream {
		t.Fatal("--no-stream must win over the default")
	}

	t.Setenv("CREATOR_STREAM", "0")
	cfg, err = Load(Config{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Stream {
		t.Fatal("CREATOR_STREAM=0 must disable streaming")
	}
}

// TestThinkingTokensEnv checks the new knob is wired through mergeEnv.
func TestThinkingTokensEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREATOR_DATA_DIR", dir)
	t.Setenv("CREATOR_THINKING_TOKENS", "2048")

	cfg, err := Load(Config{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ThinkingTokens != 2048 {
		t.Fatalf("thinking_tokens = %d, want 2048", cfg.ThinkingTokens)
	}
}

// TestBadPolicyRejected keeps the guardrail.
func TestBadPolicyRejected(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"policy_mode":"chaos"}`)
	t.Setenv("CREATOR_DATA_DIR", dir)
	_, err := Load(Config{})
	if err == nil || !strings.Contains(err.Error(), "policy mode") {
		t.Fatalf("expected a policy-mode error, got %v", err)
	}
}

func writeConfig(t *testing.T, dir string, j string) string {
	t.Helper()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(j), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSaveStripsAPIKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREATOR_DATA_DIR", dir)
	cfg := Default()
	cfg.Provider = "openai"
	cfg.APIKey = "sk-should-not-be-written"
	if err := Save(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-should-not-be-written") {
		t.Fatalf("config.json leaked the API key:\n%s", data)
	}
	if !strings.Contains(string(data), "openai") {
		t.Fatalf("provider was not saved:\n%s", data)
	}
}

func TestSavePreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREATOR_DATA_DIR", dir)
	seed := `{"provider":"openai","custom_field":"hand-written","\u005ffuture":"future-field"}`
	writeConfig(t, dir, seed)
	cfg, err := Load(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"custom_field"`, "hand-written", `"_future"`, "future-field"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("unknown key %q not preserved in saved config:\n%s", want, b)
		}
	}
}

func TestLoadAndSaveHonorDataDirOverride(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"provider":"openai","model":"gpt-test"}`)
	cfg, err := Load(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "gpt-test" {
		t.Fatalf("data_dir override not used for loading: model = %q", cfg.Model)
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatal("Save did not write to the override data_dir: ", err)
	}
}

func TestWorkspacePluginsOptInEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREATOR_DATA_DIR", dir)
	t.Setenv("CREATOR_WORKSPACE_PLUGINS", "true")
	cfg, err := Load(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.WorkspacePlugins {
		t.Fatal("CREATOR_WORKSPACE_PLUGINS=true should opt into workspace plugins")
	}
}
