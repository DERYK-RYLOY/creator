// Package config holds Creator's runtime configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"

	"github.com/creatorcli/creator/internal/reduce"
)

// Config is the fully resolved configuration for a Creator invocation.
type Config struct {
	// Provider is the registry slug of the model backend (openai, anthropic,
	// google, ollama, ...).
	Provider string `json:"provider"`
	// Model overrides the provider's default model.
	Model string `json:"model"`
	// BaseURL overrides the provider's endpoint (useful for proxies and
	// self-hosted OpenAI-compatible servers).
	BaseURL string `json:"base_url"`
	// APIKey overrides the provider's environment variable.
	APIKey string `json:"api_key,omitempty"`

	// MaxTokens is the per-response completion cap.
	MaxTokens int `json:"max_tokens"`
	// Temperature is forwarded verbatim to the provider (including 0). A
	// value < 0 is treated as unset — the provider's own default applies.
	Temperature float64 `json:"temperature"`
	// Stream asks providers for incremental responses so text and reasoning
	// appear as they are generated. Turn off with --no-stream.
	Stream bool `json:"stream"`
	// ThinkingTokens requests extended reasoning from backends that support it
	// (Anthropic thinking). Zero means off. The model's reasoning is captured
	// either way when it produces any.
	ThinkingTokens int `json:"thinking_tokens"`

	// OutputTokens is the token budget for a single tool observation. Anything
	// larger is digested and paged through the `out` tool.
	OutputTokens int `json:"output_tokens"`
	// ContextTokens is the model's context window, used to size adaptive
	// output paging. Zero means "use the provider default (128k)".
	ContextTokens int `json:"context_tokens"`
	// MaxSteps bounds tool-calling rounds per turn.
	MaxSteps int `json:"max_steps"`
	// BudgetTokens bounds total (input + output) tokens for one session. Zero
	// means unlimited.
	BudgetTokens int `json:"budget_tokens"`

	// PolicyMode is one of "strict", "balanced" or "yolo".
	PolicyMode string `json:"policy_mode"`
	// AllowCritical permits commands classified as critical without a prompt.
	AllowCritical bool `json:"allow_critical"`

	// Delegate tree limits. Zero values take the built-in defaults:
	// concurrency 8 concurrent delegates, depth 2 levels, 64 active per root,
	// 24 tool rounds and a 300-token report budget per delegate.
	DelegateConcurrency  int `json:"delegate_concurrency"`
	MaxDelegateDepth     int `json:"max_delegate_depth"`
	DelegateMaxPerRoot   int `json:"delegate_max_per_root"`
	DelegateMaxSteps     int `json:"delegate_max_steps"`
	DelegateOutputTokens int `json:"delegate_output_tokens"`

	// Shell is the interpreter used by the shell tool: auto, bash, zsh, sh,
	// pwsh, powershell or cmd.
	Shell string `json:"shell"`

	// DisableKnowledge stops Creator from loading repository instruction files
	// (AGENTS.md, CLAUDE.md, CREATOR.md, .creator/knowledge.md) into the prompt.
	DisableKnowledge bool `json:"disable_knowledge"`

	// DataDir stores sessions, artifacts and checkpoints.
	DataDir string `json:"data_dir"`
	// Theme selects the built-in Creator theme. Can be overridden with
	// CREATOR_THEME or `{"theme": "slug"}`.
	Theme string `json:"theme,omitempty"`
	// WorkspacePlugins allows Lua plugins from <workspace>/.creator/plugins to
	// load. Off by default because a repository must not get code execution
	// merely by being opened.
	WorkspacePlugins bool `json:"workspace_plugins,omitempty"`

	// JSON emits machine readable NDJSON events.
	JSON bool `json:"-"`
	// Quiet suppresses human readable progress output.
	Quiet bool `json:"-"`
	// Verbose prints request diagnostics to stderr.
	Verbose bool `json:"-"`
	// StreamIsSet marks that an explicit --stream/--no-stream choice arrived,
	// so the override beats the config file and the default.
	StreamIsSet bool `json:"-"`
	// Workspace is the root tools are allowed to touch.
	Workspace string `json:"-"`
}

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		Provider:      "openai",
		MaxTokens:     4096,
		Temperature:   -1, // <0 => unset, send provider default (0° explicitly chosen is 0)
		Stream:        true,
		OutputTokens:  0,
		ContextTokens: 128000,
		MaxSteps:      40,
		BudgetTokens:  0,
		PolicyMode:    "balanced",
		Shell:         "auto",
		Theme:         "volcanic-ember-glacial-slate",
	}
}

// Dir returns the default data directory (~/.creator). When the home
// directory is undeterminable, a cwd-relative .creator is used — but never
// silently: cwd is unpredictable and a foreign folder would be read.
func Dir() string {
	if v := os.Getenv("CREATOR_DATA_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		abs, aerr := filepath.Abs(".creator")
		if aerr == nil {
			fmt.Fprintf(os.Stderr, "creator config: cannot determine home directory; falling back to %s — set CREATOR_DATA_DIR to override\n", abs)
			return abs
		}
		return ".creator"
	}
	return filepath.Join(home, ".creator")
}

// PathIn returns the config file location for a given data directory.
func PathIn(dir string) string { return filepath.Join(dir, "config.json") }

// Path returns the config file location for the current data directory.
func Path() string { return PathIn(Dir()) }

// Load builds a configuration from defaults, the config file, environment
// variables and finally the supplied overrides (which may be zero values).
// The overrides' data_dir (if set) is honored for both reading and writing
// config.json instead of being silently dropped.
func Load(overrides Config) (Config, error) {
	cfg := Default()
	if overrides.DataDir != "" {
		cfg.DataDir = overrides.DataDir
	} else {
		cfg.DataDir = Dir()
	}

	if err := mergeFile(&cfg, PathIn(cfg.DataDir)); err != nil {
		return cfg, err
	}
	tightenConfigFile(PathIn(cfg.DataDir))
	mergeEnv(&cfg)

	// Overrides win only when they carry a non-zero value.
	if overrides.Provider != "" {
		cfg.Provider = overrides.Provider
	}
	if overrides.Model != "" {
		cfg.Model = overrides.Model
	}
	if overrides.BaseURL != "" {
		cfg.BaseURL = overrides.BaseURL
	}
	if overrides.APIKey != "" {
		cfg.APIKey = overrides.APIKey
	}
	if overrides.MaxTokens != 0 {
		cfg.MaxTokens = overrides.MaxTokens
	}
	if overrides.Temperature != -1 {
		cfg.Temperature = overrides.Temperature
	}
	if overrides.ThinkingTokens != 0 {
		cfg.ThinkingTokens = overrides.ThinkingTokens
	}
	// Stream is a tri-state override: only --no-stream turns it off.
	if overrides.StreamIsSet {
		cfg.Stream = overrides.Stream
	}
	if overrides.OutputTokens != 0 {
		cfg.OutputTokens = overrides.OutputTokens
	}
	if overrides.MaxSteps != 0 {
		cfg.MaxSteps = overrides.MaxSteps
	}
	if overrides.BudgetTokens != 0 {
		cfg.BudgetTokens = overrides.BudgetTokens
	}
	if overrides.PolicyMode != "" {
		cfg.PolicyMode = overrides.PolicyMode
	}
	if overrides.Shell != "" {
		cfg.Shell = overrides.Shell
	}
	if overrides.DataDir != "" {
		cfg.DataDir = overrides.DataDir
	}
	cfg.AllowCritical = cfg.AllowCritical || overrides.AllowCritical
	cfg.DisableKnowledge = cfg.DisableKnowledge || overrides.DisableKnowledge
	cfg.WorkspacePlugins = cfg.WorkspacePlugins || overrides.WorkspacePlugins
	cfg.JSON = cfg.JSON || overrides.JSON
	cfg.Quiet = cfg.Quiet || overrides.Quiet
	cfg.Verbose = cfg.Verbose || overrides.Verbose

	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	if cfg.Provider == "" {
		cfg.Provider = "openai"
	}
	switch cfg.PolicyMode {
	case "strict", "balanced", "yolo":
	default:
		return cfg, errors.New(`policy mode must be "strict", "balanced" or "yolo"`)
	}
	if cfg.Workspace == "" {
		wd, err := os.Getwd()
		if err != nil {
			return cfg, err
		}
		cfg.Workspace = wd
	}
	if cfg.MaxSteps < 1 {
		cfg.MaxSteps = 1
	}
	// Adaptive paging: when no explicit budget was given, size the page from
	// the model's context window (Aider convention). This must run BEFORE the
	// floor clamp — clamping first made the == 0 check unreachable and pinned
	// every session at 200 tokens of tool output forever.
	if cfg.OutputTokens == 0 {
		cfg.OutputTokens = reduce.AdaptiveBudget(cfg.ContextTokens)
	}
	if cfg.OutputTokens < 200 {
		cfg.OutputTokens = 200
	}
	return cfg, nil
}

func mergeFile(cfg *Config, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, cfg); err != nil {
		return errors.New("invalid config at " + path + ": " + err.Error())
	}
	// Typo'd keys in a hand-written config must not pass silently: warn
	// (non-fatal) about any key this schema doesn't know.
	raw := map[string]any{}
	if err := json.Unmarshal(b, &raw); err == nil {
		for k := range raw {
			if !knownConfigKeys[k] {
				fmt.Fprintf(os.Stderr, "creator config: ignoring unknown key %q in %s\n", k, path)
			}
		}
	}
	return nil
}

// knownConfigKeys is the set of json keys understood by Config, derived from
// the struct tags so omitempty-false fields are still "known".
var knownConfigKeys = func() map[string]bool {
	keys := map[string]bool{}
	t := reflect.TypeOf(Config{})
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}()

func mergeEnv(cfg *Config) {
	// Precedence is defaults < config file < environment < flags. DataDir was
	// resolved before mergeFile and never re-applied here, so a data_dir
	// written by `creator init` silently beat CREATOR_DATA_DIR forever.
	if v := os.Getenv("CREATOR_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	str := func(key string, dst *string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	num := func(key string, dst *int) {
		if v := os.Getenv(key); v != "" {
			trimmed := strings.TrimSpace(v)
			n, err := strconv.Atoi(trimmed)
			if err != nil || n < 0 {
				fmt.Fprintf(os.Stderr, "creator config: ignoring %s=%q (expected a non-negative integer)\n", key, v)
				return
			}
			*dst = n
		}
	}
	str("CREATOR_PROVIDER", &cfg.Provider)
	str("CREATOR_MODEL", &cfg.Model)
	str("CREATOR_BASE_URL", &cfg.BaseURL)
	str("CREATOR_API_KEY", &cfg.APIKey)
	str("CREATOR_POLICY", &cfg.PolicyMode)
	str("CREATOR_SHELL", &cfg.Shell)
	num("CREATOR_MAX_TOKENS", &cfg.MaxTokens)
	num("CREATOR_OUTPUT_TOKENS", &cfg.OutputTokens)
	num("CREATOR_CONTEXT_TOKENS", &cfg.ContextTokens)
	num("CREATOR_MAX_STEPS", &cfg.MaxSteps)
	num("CREATOR_BUDGET_TOKENS", &cfg.BudgetTokens)
	num("CREATOR_THINKING_TOKENS", &cfg.ThinkingTokens)
	num("CREATOR_DELEGATE_CONCURRENCY", &cfg.DelegateConcurrency)
	num("CREATOR_MAX_DELEGATE_DEPTH", &cfg.MaxDelegateDepth)
	num("CREATOR_DELEGATE_MAX_PER_ROOT", &cfg.DelegateMaxPerRoot)
	num("CREATOR_DELEGATE_MAX_STEPS", &cfg.DelegateMaxSteps)
	num("CREATOR_DELEGATE_OUTPUT_TOKENS", &cfg.DelegateOutputTokens)
	// Tri-state bool envs: an explicit 0/false/off lowers a true written in
	// the config file — security toggles must be raisable AND lowerable.
	boolFlag := func(key string, dst *bool) {
		if v := os.Getenv(key); v != "" {
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "1", "true", "yes", "on":
				*dst = true
			case "0", "false", "no", "off":
				*dst = false
			default:
				fmt.Fprintf(os.Stderr, "creator config: ignoring %s=%q (expected true or false)\n", key, v)
			}
		}
	}
	boolFlag("CREATOR_ALLOW_CRITICAL", &cfg.AllowCritical)
	boolFlag("CREATOR_NO_KNOWLEDGE", &cfg.DisableKnowledge)
	str("CREATOR_THEME", &cfg.Theme)
	boolFlag("CREATOR_WORKSPACE_PLUGINS", &cfg.WorkspacePlugins)
	boolFlag("CREATOR_JSON", &cfg.JSON)
	if v := os.Getenv("CREATOR_STREAM"); v != "" {
		if v == "0" || strings.EqualFold(v, "false") || strings.EqualFold(v, "off") || strings.EqualFold(v, "no") {
			cfg.Stream = false
		} else {
			cfg.Stream = true
		}
	}
}

// Save writes the configuration to the config file, creating the directory.
// API keys are stripped: credentials live in keys.json or the environment,
// never implicitly duplicated into config.json. Unknown keys already present
// in the file (e.g. hand-written entries this schema doesn't know about) are
// preserved; the struct round-trip would otherwise silently destroy them.
func Save(cfg Config) error {
	dir := cfg.DataDir
	if dir == "" {
		dir = Dir()
	}
	path := PathIn(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	cfg.APIKey = ""
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil && len(old) > 0 {
		known := map[string]any{}
		if err := json.Unmarshal(b, &known); err != nil {
			return err
		}
		raw := map[string]any{}
		if err := json.Unmarshal(old, &raw); err == nil {
			for k, v := range known {
				raw[k] = v
			}
			b, err = json.MarshalIndent(raw, "", "  ")
			if err != nil {
				return err
			}
		}
	}
	// 0600: the file may carry an API key override (api_key field). WriteFile
	// only applies the mode at creation, so tighten any pre-existing weak
	// perms file too — but never silently.
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "creator config: %s written but could not be tightened to 0600: %v\n", path, err)
	}
	return nil
}

// tightenConfigFile audits the config file's permissions at load time: it
// tightens to 0600 (best effort) and reports what it changed or could not
// change, so a 0644 config carrying a key never hides in silence.
func tightenConfigFile(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	perm := info.Mode().Perm()
	if perm == 0o600 {
		return
	}
	// Unix only: on Windows, os.Chmod cannot strip group/other access — it
	// only toggles a read-only flag, and os.Stat keeps reporting 0666 no
	// matter what. Printing "tightened to 0600" here would claim a security
	// property that does not exist. The file inherits its ACL from the
	// user profile folder (already private to this account); hardening it
	// further is a docs/icacls matter, not a load-time behavior.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "creator config: %s had mode %04o and could not be tightened: %v\n", path, perm, err)
			return
		}
		fmt.Fprintf(os.Stderr, "creator config: %s had mode %04o; tightened to 0600\n", path, perm)
	}
}
