// Command creator is a terminal agent that never floods its own context.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creatorcli/creator/internal/agent"
	"github.com/creatorcli/creator/internal/board"
	"github.com/creatorcli/creator/internal/config"
	"github.com/creatorcli/creator/internal/delegate"
	"github.com/creatorcli/creator/internal/id"
	"github.com/creatorcli/creator/internal/knowledge"
	"github.com/creatorcli/creator/internal/mcp"
	"github.com/creatorcli/creator/internal/plugin"
	"github.com/creatorcli/creator/internal/policy"
	"github.com/creatorcli/creator/internal/provider"
	"github.com/creatorcli/creator/internal/reduce"
	"github.com/creatorcli/creator/internal/runner"
	"github.com/creatorcli/creator/internal/store"
	"github.com/creatorcli/creator/internal/tools"
	"github.com/creatorcli/creator/internal/tui"

	"golang.org/x/term"
)

const version = "0.1.0"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := dispatch(ctx, os.Args[1:])
	if err == nil {
		return
	}
	if errors.Is(err, context.Canceled) {
		os.Exit(130)
	}
	var te *tui.ExitError
	if errors.As(err, &te) {
		// The flag layer already printed the reason; exit with its code.
		os.Exit(te.Code)
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintln(os.Stderr, ee.msg)
		}
		os.Exit(ee.code)
	}
	fmt.Fprintln(os.Stderr, "creator: "+err.Error())
	os.Exit(1)
}

type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// ---------------------------------------------------------------------------
// Flags
// ---------------------------------------------------------------------------

type globalFlags struct {
	provider         string
	model            string
	baseURL          string
	apiKey           string
	policyMode       string
	shell            string
	dataDir          string
	allowCritical    bool
	jsonOut          bool
	quiet            bool
	verbose          bool
	outputTokens     int
	maxSteps         int
	budget           int
	noStream         bool
	thinking         int
	noKnowledge      bool
	workspacePlugins bool
}

func (f *globalFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.provider, "provider", "", "model backend (openai, anthropic, google, xai, deepseek, mistral, groq, openrouter, together, fireworks, cerebras, perplexity, moonshot, zhipu, ollama, lmstudio, vllm, custom)")
	fs.StringVar(&f.model, "model", "", "model name; defaults to the provider's first model")
	fs.StringVar(&f.baseURL, "base-url", "", "override the provider endpoint")
	fs.StringVar(&f.apiKey, "api-key", "", "API key (otherwise read from the provider's environment variable)")
	fs.StringVar(&f.policyMode, "policy", "", "command policy: strict, balanced (default) or yolo")
	fs.StringVar(&f.shell, "shell", "", "shell interpreter: auto, bash, zsh, sh, pwsh, powershell, cmd")
	fs.StringVar(&f.dataDir, "data-dir", "", "state directory (default ~/.creator)")
	fs.BoolVar(&f.allowCritical, "allow-critical", false, "permit commands classified as critical without an interactive prompt")
	fs.BoolVar(&f.jsonOut, "json", false, "emit NDJSON events instead of human output")
	fs.BoolVar(&f.quiet, "quiet", false, "suppress progress output")
	fs.BoolVar(&f.verbose, "verbose", false, "print diagnostics to stderr")
	fs.IntVar(&f.outputTokens, "output-tokens", 0, "token budget for a single tool observation (0 = adaptive, based on context window)")
	fs.IntVar(&f.maxSteps, "max-steps", 0, "maximum tool-calling rounds per turn (default 40)")
	fs.IntVar(&f.budget, "budget", 0, "total token budget for the session (0 = unlimited)")
	fs.BoolVar(&f.noStream, "no-stream", false, "disable streaming responses (batch mode)")
	fs.IntVar(&f.thinking, "thinking", 0, "extended thinking budget in tokens (Anthropic models; 0 = off)")
	fs.BoolVar(&f.noKnowledge, "no-knowledge", false, "do not load repository instruction files (AGENTS.md, CLAUDE.md, ...) into the prompt")
	fs.BoolVar(&f.workspacePlugins, "workspace-plugins", false, "allow plugins from <workspace>/.creator/plugins to load (user plugins always load)")
}

func (f *globalFlags) overrides() config.Config {
	over := config.Config{
		Provider:         f.provider,
		Model:            f.model,
		BaseURL:          f.baseURL,
		APIKey:           f.apiKey,
		PolicyMode:       f.policyMode,
		Shell:            f.shell,
		DataDir:          f.dataDir,
		AllowCritical:    f.allowCritical,
		JSON:             f.jsonOut,
		Quiet:            f.quiet,
		Verbose:          f.verbose,
		OutputTokens:     f.outputTokens,
		MaxSteps:         f.maxSteps,
		BudgetTokens:     f.budget,
		ThinkingTokens:   f.thinking,
		DisableKnowledge: f.noKnowledge,
		WorkspacePlugins: f.workspacePlugins,
	}
	if f.noStream {
		over.Stream = false
		over.StreamIsSet = true
	}
	return over
}

func (f *globalFlags) resolve() (config.Config, error) {
	cfg, err := config.Load(f.overrides())
	if err != nil {
		return cfg, err
	}
	if wd, err := os.Getwd(); err == nil {
		cfg.Workspace = wd
	}
	return cfg, nil
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

func dispatch(ctx context.Context, args []string) error {
	if len(args) == 0 {
		if isInteractive() && stdoutIsTTY() && os.Getenv("TERM") != "dumb" && os.Getenv("CREATOR_NO_TUI") == "" {
			return tui.Run(ctx, nil, os.Stdout, os.Stderr)
		}
		printUsage(os.Stdout)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "tui":
		return tui.Run(ctx, rest, os.Stdout, os.Stderr)
	case "run":
		return cmdRun(ctx, rest)
	case "shell", "sh":
		return cmdShell(ctx, rest)
	case "jobs":
		return cmdJobs(rest)
	case "delegates":
		return cmdDelegates(ctx, rest)
	case "tasks":
		return cmdTasks(rest)
	case "out":
		return cmdOut(rest)
	case "connect":
		return cmdConnect(ctx, rest)
	case "install":
		return cmdInstall(rest)
	case "mcp":
		return cmdMCP(ctx, rest)
	case "knowledge":
		return cmdKnowledge(rest)
	case "profiles":
		return cmdProfiles(rest)
	case "providers":
		return cmdProviders()
	case "plugins":
		return cmdPlugins(rest)
	case "sessions":
		return cmdSessions(rest)
	case "revert":
		return cmdRevert(rest)
	case "init":
		return cmdInit(rest)
	case "version", "--version":
		fmt.Printf("creator %s\n", version)
		return nil
	case "help", "--help", "-h":
		printUsage(os.Stdout)
		return nil
	}
	// Keep the accidental-command trap from silently becoming an agentic turn:
	// a bare lowercase word that is not a subcommand is almost certainly a typo
	// (`creator tui`), so refuse and say how to ask. Flags and multi-word
	// prompts still go through as a run.
	if isBareWord(cmd) {
		fmt.Fprintf(os.Stderr, "creator: unknown command %q — did you mean `creator run %q`?\n", cmd, cmd)
		fmt.Fprintf(os.Stderr, "creator: run `creator help` for the command list\n")
		return &exitError{code: 2}
	}
	// Anything else with spaces or punctuation is a prompt.
	return cmdRun(ctx, args)
}

func isBareWord(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 && !(r >= 'a' && r <= 'z') {
			return false
		}
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, `creator %s — a terminal agent that never floods its own context

USAGE
  creator                             open mission control (or usage when not interactive)
  creator tui [flags]                 open mission control, the full-screen terminal IDE
  creator connect [--provider slug]   verify an API key against the live API, list its
                                      models (free first) and save it
  creator install                     install creator and ctr on your PATH
  creator run [flags] "prompt"        run one agent turn (prompt may come from stdin)
  creator shell [flags] <command>     run a command through policy + digest, standalone
  creator jobs [id]                   list jobs, or inspect one
  creator delegates [id]              list delegates, inspect one, follow the event journal
  creator tasks [id]                  the task board: plan, tick with evidence, flags, blockers
  creator out <handle> [flags]        page stored output (grep, range, tail, head, full)
  creator mcp                         serve the toolset over MCP on stdio
  creator providers                   list model backends, status and free tiers
  creator knowledge [--print]         show the repository instruction files Creator loads
  creator profiles                    list delegate agent profiles (explorer, planner, ...)
  creator plugins                     list Lua plugins (workspace plugins stay untrusted)
  creator sessions                    list saved sessions
  creator revert <checkpoint>         restore a file checkpoint
  creator init                        write ~/.creator/config.json
  creator version | help

ALIASES
  ctr is the same program as creator - "creator install" puts both on your PATH

GLOBAL FLAGS (run, shell, jobs, delegates, tasks, out, mcp, ...)
  --provider slug       model backend (openai, anthropic, ...)
  --model name          model name; defaults to the provider's first model
  --base-url url        override the provider endpoint
  --api-key key         API key (otherwise read from the provider's environment variable)
  --policy MODE         strict, balanced (default) or yolo
  --shell NAME          auto, bash, zsh, sh, pwsh, powershell, cmd
  --data-dir dir        state directory (default ~/.creator)
  --allow-critical      permit commands classified as critical without an interactive prompt
  --json                emit NDJSON events instead of human output
  --quiet               suppress progress output
  --verbose             print diagnostics to stderr
  --output-tokens N     token budget per tool observation (0 = adaptive)
  --max-steps N         maximum tool-calling rounds per turn (default 40)
  --budget N            total token budget for the session (0 = unlimited)
  --no-stream           disable streaming responses (batch mode)
  --thinking N          extended thinking budget in tokens (0 = off)
  --no-knowledge        do not load repository instruction files into the prompt
  --workspace-plugins   allow plugins from <workspace>/.creator/plugins to load

EXAMPLES
  creator run "why is the build failing?"
  creator run --provider ollama --model qwen3-coder "add tests for internal/reduce"
  creator run --continue "now fix the bug you found"
  creator shell --allow-critical "go test ./..."
  creator out j_01h9x --grep "FAIL" --ignore-case
  creator mcp | jq .

ENVIRONMENT
  CREATOR_PROVIDER, CREATOR_MODEL, CREATOR_BASE_URL, CREATOR_API_KEY
  CREATOR_POLICY, CREATOR_SHELL, CREATOR_OUTPUT_TOKENS, CREATOR_MAX_STEPS,
  CREATOR_BUDGET_TOKENS, CREATOR_THINKING_TOKENS, CREATOR_STREAM,
  CREATOR_ALLOW_CRITICAL, CREATOR_NO_KNOWLEDGE, CREATOR_WORKSPACE_PLUGINS,
  CREATOR_DATA_DIR, CREATOR_PRUNE_DAYS, CREATOR_PRUNE_EVENTS
  Provider keys: OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY, XAI_API_KEY,
  DEEPSEEK_API_KEY, MISTRAL_API_KEY, GROQ_API_KEY, OPENROUTER_API_KEY, POLLINATIONS_API_KEY, and more.

HOW IT DIFFERS
  Every command's output is stored under a handle and summarized to a token
  budget. The agent pages detail with out() instead of dumping output into the
  context. Long commands run in the background and are polled, so the loop never
  blocks. Every mutation is checkpointed. Every command is parsed to a shell AST
  and classified before it runs.
`, version)
}

// ---------------------------------------------------------------------------
// run
// ---------------------------------------------------------------------------

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f globalFlags
	f.bind(fs)
	sessionID := fs.String("session", "", "resume this session id")
	cont := fs.Bool("continue", false, "resume the most recent session")
	printSystem := fs.Bool("print-system", false, "print the system prompt and exit")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	cfg, err := f.resolve()
	if err != nil {
		return err
	}

	know := knowledgeSection(cfg)
	if *printSystem {
		fmt.Println(agent.SystemPromptWithKnowledge(cfg.Workspace, shellDisplayName(cfg.Shell), know))
		return nil
	}

	// Go's flag parser stops at the first positional, so a flag typed AFTER
	// the prompt would silently become prompt text (and get answered).
	// Detect known flags in prompt position and say so instead.
	{
		known := map[string]bool{}
		fs.VisitAll(func(fl *flag.Flag) { known[fl.Name] = true })
		for _, a := range fs.Args() {
			if !strings.HasPrefix(a, "--") {
				continue
			}
			name := strings.SplitN(strings.TrimPrefix(a, "--"), "=", 2)[0]
			if known[name] {
				return fmt.Errorf("flag --%s appears after the prompt and would become part of it; put flags before the prompt: creator run [flags] \"<prompt>\"", name)
			}
		}
	}

	prompt := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(prompt) == "" {
		if isInteractive() {
			return errors.New(`no prompt given

  creator run "what should I work on?"
  echo "explain this repo" | creator run`)
		}
		data, err := io.ReadAll(io.LimitReader(os.Stdin, int64(4<<20)+1))
		if err != nil {
			return err
		}
		if int64(len(data)) > 4<<20 {
			// Never truncate the prompt silently — the agent would act on
			// half a task without knowing.
			return errors.New("stdin prompt exceeds 4 MiB; shorten it or split the work")
		}
		prompt = string(data)
	}
	if strings.TrimSpace(prompt) == "" {
		return errors.New("empty prompt")
	}

	prov, err := resolveProvider(cfg)
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	pruneStale(st, cfg.Verbose)

	jobs := runner.NewManager(st)
	defer jobs.StopAll()

	engine := policy.NewEngine(cfg.PolicyMode, cfg.AllowCritical, isInteractive(), os.Stdin, os.Stderr)
	auditLog(engine, cfg.Verbose)
	engine.Shell = shellDisplayName(cfg.Shell)

	sess := store.Session{
		ID:        id.New("s"),
		Created:   time.Now(),
		Workspace: cfg.Workspace,
		Provider:  cfg.Provider,
		Model:     prov.Model(),
	}
	switch {
	case *sessionID != "":
		if sess, err = st.LoadSession(*sessionID); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "resuming session %s (%d messages)\n", sess.ID, len(sess.Messages))
	case *cont:
		latest, ok := st.LatestSession()
		if !ok {
			return errors.New("no previous session to continue")
		}
		if sess, err = st.LoadSession(latest); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "continuing session %s (%d messages)\n", sess.ID, len(sess.Messages))
	default:
		fmt.Fprintf(os.Stderr, "session %s\n", sess.ID)
	}
	sess.Provider = cfg.Provider
	sess.Model = prov.Model()

	printer := newPrinter(cfg)
	var usage provider.Usage
	printMu := &sync.Mutex{}
	emitEvent := func(ev agent.Event) {
		printMu.Lock()
		defer printMu.Unlock()
		if ev.Usage != nil {
			usage = *ev.Usage
		}
		printer(ev)
	}
	env := &tools.Env{
		Workspace: cfg.Workspace,
		Store:     st,
		Jobs:      jobs,
		Policy:    engine,
		Session:   sess.ID,
		Budget:    cfg.OutputTokens,
		Shell:     cfg.Shell,
	}
	if isInteractive() {
		env.Ask = askOnTerminal
	}
	env.OnProgress = func(name, stage, text string, meta map[string]string) {
		emitEvent(agent.Event{Type: "tool_progress", Tool: name, Status: stage, Text: text, Meta: meta})
	}
	registry := tools.New(env)
	env.Hooks = loadPlugins(cfg, registry)

	brd := board.New(st, board.Options{
		Root:   cfg.Workspace,
		Jobs:   jobs,
		Policy: engine,
		Shell:  cfg.Shell,
	})
	env.OnCommandExit = func(command string, code int, jobID, dir string) {
		if closed, _ := brd.AutoCloseByVerify(command, jobID, dir); len(closed) > 0 && cfg.Verbose {
			fmt.Fprintf(os.Stderr, "creator: closed %s (verify exit 0)\n", strings.Join(closed, ", "))
		}
	}
	if err := brd.Attach(registry, sess.ID); err != nil {
		fmt.Fprintf(os.Stderr, "creator: %v\n", err)
	}

	mgr := delegate.New(st, delegateOptions(cfg, st, registry, prov, brd, know))
	defer mgr.Stop()
	if err := mgr.Attach(registry, 0, sess.ID); err != nil {
		fmt.Fprintf(os.Stderr, "creator: %v\n", err)
	}

	a := agent.New(agent.Options{
		Provider:     prov,
		Tools:        registry,
		Store:        st,
		Session:      sess,
		MaxSteps:     cfg.MaxSteps,
		BudgetTokens: cfg.BudgetTokens,
		MaxTokens:    cfg.MaxTokens,
		Temperature:  cfg.Temperature,
		Thinking:     cfg.ThinkingTokens,
		ShellName:    shellDisplayName(cfg.Shell),
		Knowledge:    know,
		OnEvent:      emitEvent,
	})
	answer, err := a.Run(ctx, prompt)
	if err != nil {
		return err
	}
	if cfg.JSON {
		enc := json.NewEncoder(os.Stdout)
		_ = enc.Encode(map[string]any{"type": "result", "text": answer, "session": sess.ID})
	} else {
		fmt.Println(answer)
		// Token totals, plain numbers — no box.
		if !cfg.Quiet && usage.In+usage.Out > 0 {
			fmt.Fprintf(os.Stderr, "%d in · %d out · %d total\n", usage.In, usage.Out, usage.In+usage.Out)
		}
	}
	return nil
}

// delegateOptions wires the delegation subsystem: the root registry is
// filtered per spawn, the provider may be overridden per delegate, the tree
// limits come from config, and board-bound tasks close through a delegate's
// report.
func delegateOptions(cfg config.Config, st *store.Store, registry *tools.Registry, prov provider.Provider, tasks delegate.TaskCloser, know string) delegate.Options {
	return delegate.Options{
		Workspace: cfg.Workspace,
		Store:     st,
		Registry:  registry,
		Providers: func(slug, model string) (provider.Provider, error) {
			if slug == "" && model == "" {
				return prov, nil
			}
			if slug == "" {
				slug = cfg.Provider
			}
			baseURL, apiKey := cfg.BaseURL, cfg.APIKey
			if slug != cfg.Provider {
				// base-url/api-key were meant for the root backend only.
				baseURL, apiKey = "", ""
			}
			return provider.Resolve(slug, model, baseURL, apiKey)
		},
		Fallback:     prov,
		ShellName:    shellDisplayName(cfg.Shell),
		Knowledge:    know,
		MaxTokens:    cfg.MaxTokens,
		Temperature:  cfg.Temperature,
		Thinking:     cfg.ThinkingTokens,
		MaxDepth:     cfg.MaxDelegateDepth,
		Concurrency:  cfg.DelegateConcurrency,
		MaxPerRoot:   cfg.DelegateMaxPerRoot,
		MaxSteps:     cfg.DelegateMaxSteps,
		OutputTokens: cfg.DelegateOutputTokens,
		Tasks:        tasks,
		OnEvent:      nil,
	}
}

func resolveProvider(cfg config.Config) (provider.Provider, error) {
	// Nothing configured yet: pick up a provider from the environment or from
	// a credential the connect flow already saved.
	if cfg.Provider == "" {
		if slug, ok := provider.Detect(); ok {
			if p, err := provider.Resolve(slug, cfg.Model, cfg.BaseURL, cfg.APIKey); err == nil {
				return p, nil
			}
		}
		return nil, provider.ErrNoProvider
	}
	p, err := provider.Resolve(cfg.Provider, cfg.Model, cfg.BaseURL, cfg.APIKey)
	if err == nil {
		return p, nil
	}
	// An unknown slug is a usage error: surface it verbatim instead of
	// papering over it with the "no provider configured" hint.
	if _, ok := provider.Lookup(cfg.Provider); !ok {
		return nil, err
	}
	// If the configured backend is unusable, fall back to whichever provider
	// actually has credentials instead of failing outright — but never
	// replace the real reason the configured one failed.
	if slug, ok := provider.Detect(); ok && slug != cfg.Provider {
		if fallback, ferr := provider.Resolve(slug, "", "", ""); ferr == nil {
			fmt.Fprintf(os.Stderr, "creator: %s; using %s instead\n", err, fallback.Label())
			return fallback, nil
		}
	}
	// Fallback was not possible, fall back to the real failure reason.
	return nil, err
}

func newPrinter(cfg config.Config) func(agent.Event) {
	enc := json.NewEncoder(os.Stdout)
	return func(ev agent.Event) {
		if cfg.JSON {
			_ = enc.Encode(ev)
			return
		}
		if cfg.Quiet {
			return
		}
		switch ev.Type {
		case "assistant":
			fmt.Println(ev.Text)
		case "assistant_delta":
			// streamed prose; write raw so users watch it appear
			fmt.Print(ev.Text)
		case "reasoning_delta":
			fmt.Printf("  · %s\n", ev.Text)
		case "tool_start":
			fmt.Printf("\n→ %s %s\n", ev.Tool, compactJSON(ev.Args, 160))
		case "tool_end":
			first := firstLine(ev.Result)
			if ev.Status == "error" {
				fmt.Printf("✗ %s: %s\n", ev.Tool, first)
			} else {
				fmt.Printf("  ↳ %s\n", first)
			}
		case "usage":
			if cfg.Verbose {
				fmt.Fprintf(os.Stderr, "  [tokens %d in / %d out, %d total]\n", ev.Usage.In, ev.Usage.Out, ev.Usage.In+ev.Usage.Out)
			}
		case "notice":
			if cfg.Verbose {
				fmt.Fprintln(os.Stderr, ev.Text)
			}
		}
	}
}

// shellDisplayName resolves the shell preference to a concrete interpreter name
// for display and policy diagnostics.
func shellDisplayName(preference string) string {
	sh, err := runner.ResolveShell(preference)
	if err != nil {
		return "unknown"
	}
	return sh.Name()
}

// pruneStale removes old raw artifacts, checkpoints and journal events so a
// long Creator history cannot eat the disk. Artifact/checkpoint age comes from
// CREATOR_PRUNE_DAYS (default 7); journal events keep CREATOR_PRUNE_EVENTS
// newest entries (default 10000). Zero disables each.
func pruneStale(st *store.Store, verbose bool) {
	if removed := st.OrphanJobs(); removed > 0 && verbose {
		fmt.Fprintf(os.Stderr, "creator: marked %d stale running job(s) orphaned\n", removed)
	}
	age := 7 * 24 * time.Hour
	pruneDays := true
	if v := os.Getenv("CREATOR_PRUNE_DAYS"); v != "" {
		if d, err := strconv.ParseInt(v, 10, 64); err == nil && d >= 0 {
			// clamp to avoid int64 overflow when d is huge: d*24h must fit in a Duration.
			const maxSafeDays = int64(^uint64(0)>>1) / int64(24*time.Hour)
			if d > maxSafeDays {
				d = maxSafeDays
			}
			age = time.Duration(d) * 24 * time.Hour
			if age == 0 {
				pruneDays = false
			}
		} else if err != nil {
			// Garbage input: refuse to prune everything by accident. Surface
			// a clear error and do no pruning.
			_, _ = fmt.Fprintf(os.Stderr, "creator: invalid CREATOR_PRUNE_DAYS=%q (ignored): %v\n", v, err)
			return
		}
	}
	if pruneDays {
		if removed := st.PruneArtifacts(age); removed > 0 && verbose {
			fmt.Fprintf(os.Stderr, "creator: pruned %d stale artifact(s)\n", removed)
		}
		if removed := st.PruneCheckpoints(age); removed > 0 && verbose {
			fmt.Fprintf(os.Stderr, "creator: pruned %d stale checkpoint(s)\n", removed)
		}
	}
	keep := 10000
	if v := os.Getenv("CREATOR_PRUNE_EVENTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			keep = n
		}
	}
	if keep > 0 {
		if removed := st.TrimDelegateEvents(keep); removed > 0 && verbose {
			fmt.Fprintf(os.Stderr, "creator: pruned %d old journal event(s)\n", removed)
		}
	}
}

// auditLog wires the policy audit hook to stderr in verbose mode. It logs the
// verdict and decision, not a hash of the command: the logs are local and the
// command is already going to be parsed and executed by the same process.
// sensitiveArgPatterns mask credential-looking substrings in command lines.
// Best-effort and deliberately broad: an adversarial command can obfuscate
// a secret past any static filter, but the common shapes must never land in
// a log.
var sensitiveArgPatterns = []struct {
	re     *regexp.Regexp
	mask   string
}{
	{regexp.MustCompile(`(?i)(authorization:\s*(bearer|basic|token)\s+)[^\s"']+`), "${1}[REDACTED]"},
	{regexp.MustCompile(`(?i)(bearer\s+)[^\s"']+`), "${1}[REDACTED]"},
	{regexp.MustCompile(`(?i)((api[_-]?key|apikey|token|secret|password|passwd|auth[_-]?token)\s*[=: ]\s*)[^\s"']+`), "${1}[REDACTED]"},
	{regexp.MustCompile(`(?i)([A-Za-z0-9_-]*(key|token|secret)[A-Za-z0-9_-]*\s*=\s*)[^\s"']+`), "${1}[REDACTED]"},
	{regexp.MustCompile(`sk-[A-Za-z0-9_\-]{8,}`), "[REDACTED]"},
	{regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{8,}`), "[REDACTED]"},
	{regexp.MustCompile(`(?i)(x-api-key["']?\s*:?\s*)[^\s"']+`), "${1}[REDACTED]"},
}

func redactSecrets(s string) string {
	for _, p := range sensitiveArgPatterns {
		s = p.re.ReplaceAllString(s, p.mask)
	}
	return s
}

func auditLog(engine *policy.Engine, verbose bool) {
	if !verbose || engine == nil {
		return
	}
	engine.Log = func(cmd string, v policy.Verdict, d policy.Decision) {
		fmt.Fprintf(os.Stderr, "policy[%s/%s]: %s\n", v.Tier, d, firstLine(redactSecrets(cmd)))
	}
}

// cutRunes truncates s to at most max runes. Counting runes instead of bytes
// keeps multi-byte UTF-8 from being sliced mid-character into garbage.
func cutRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func compactJSON(raw json.RawMessage, max int) string {
	if len(raw) == 0 {
		return ""
	}
	return cutRunes(strings.TrimSpace(string(raw)), max)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return cutRunes(s, 200)
}

// commitProvider persists a verified credential and switches the active
// provider. It returns the provider slug and the resolved model.
func commitProvider(slug, key, baseURL, model string) (string, string, error) {
	d, ok := provider.Lookup(slug)
	if !ok {
		return "", "", fmt.Errorf("unknown provider %q", slug)
	}
	wroteCred := false
	var prevCred provider.Cred
	hadPrev := false
	if key != "" || baseURL != "" {
		if p, h, perr := provider.LoadCred(slug); perr == nil {
			prevCred, hadPrev = p, h
		}
		if err := provider.SaveCred(slug, provider.Cred{Key: key, BaseURL: baseURL}); err != nil {
			return "", "", err
		}
		wroteCred = true
	}
	// Roll the credential back if the provider switch doesn't commit —
	// otherwise keys sit saved for a provider the config doesn't use.
	// (If the rollback itself fails, say so: the user must know.)
	rollback := func() {
		if !wroteCred {
			return
		}
		var rbErr error
		if hadPrev {
			rbErr = provider.SaveCred(slug, prevCred)
		} else {
			rbErr = provider.DeleteCred(slug)
		}
		if rbErr != nil {
			fmt.Fprintf(os.Stderr, "warning: credential rollback for %q failed: %v\n", slug, rbErr)
		}
	}
	if model == "" {
		model = d.DefaultModel()
	}
	cfg, err := config.Load(config.Config{})
	if err != nil {
		rollback()
		return "", "", err
	}
	cfg.Provider = slug
	cfg.Model = model
	if err := config.Save(cfg); err != nil {
		rollback()
		return "", "", err
	}
	return slug, model, nil
}

// providerSlugs lists every registered backend for error hints.
func providerSlugs() string {
	var out []string
	for _, d := range provider.Descriptors() {
		out = append(out, d.Slug)
	}
	return strings.Join(out, ", ")
}

// cmdConnect connects a provider. With a key on the command line it verifies
// against the live API, saves the credential and prints every model the
// account can use (free first).
func cmdConnect(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: creator connect [provider] [key] [--provider slug] [--model name] [--base-url url]")
		fmt.Fprintln(os.Stderr, "\nWith a key it verifies against the live API, saves it, and lists the models")
		fmt.Fprintln(os.Stderr, "the account can use.")
		fs.PrintDefaults()
	}
	provFlag := fs.String("provider", "", "provider slug (openai, anthropic, groq, ...)")
	modelFlag := fs.String("model", "", "model to use after connecting")
	baseFlag := fs.String("base-url", "", "override the API base URL")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	rest := fs.Args()
	slug := strings.TrimSpace(*provFlag)
	if slug == "" && len(rest) > 0 {
		if _, ok := provider.Lookup(rest[0]); ok {
			slug, rest = rest[0], rest[1:]
		}
	}
	key := ""
	if len(rest) > 0 {
		key = rest[0]
		rest = rest[1:]
	}
	if len(rest) > 0 {
		// Never drop extra arguments silently — the user typed two (or
		// more) values and expects both to be used.
		return fmt.Errorf("unexpected argument(s) %q — pass a single key (connect one provider at a time)", strings.Join(rest, " "))
	}
	if slug == "" {
		if len(rest) > 1 {
			return fmt.Errorf("unknown provider %q (try: %s)", rest[0], providerSlugs())
		}
		if key != "" {
			return errors.New("which provider? run: creator connect --provider <slug> <key>")
		}
		return errors.New("usage: creator connect <provider> <key>")
	}
	d, ok := provider.Lookup(slug)
	if !ok {
		return fmt.Errorf("unknown provider %q (try: %s)", slug, providerSlugs())
	}
	// Key resolution: a positional key is convenient but leaks into shell
	// history and the process list; prefer the no-echo terminal prompt or
	// piped stdin.
	if key != "" {
		fmt.Fprintf(os.Stderr, "warning: passing the key as an argument exposes it in shell history and the process list; prefer `creator connect %s` (no-echo prompt) or `echo KEY | creator connect %s`\n", slug, slug)
	} else if !d.Local {
		if isInteractive() {
			fmt.Fprintf(os.Stderr, "%s API key (input hidden): ", d.Name)
			raw, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return fmt.Errorf("read key: %w", err)
			}
			key = strings.TrimSpace(string(raw))
		} else {
			data, err := io.ReadAll(io.LimitReader(os.Stdin, int64(4096)+1))
			if err != nil {
				return err
			}
			if int64(len(data)) > 4096 {
				return errors.New("stdin key exceeds 4 KiB")
			}
			key = strings.TrimSpace(string(data))
		}
		if key == "" {
			return fmt.Errorf("usage: creator connect %s <key> (or pipe it on stdin)", slug)
		}
	}
	res, err := provider.Verify(ctx, d, key, *baseFlag, *modelFlag, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", d.Name, err)
	}
	if _, _, err := commitProvider(slug, key, *baseFlag, *modelFlag); err != nil {
		return err
	}
	fmt.Printf("connected to %s · %s\n", d.Name, res.Endpoint)
	for _, s := range res.Steps {
		mark := "✓"
		if !s.OK {
			mark = "✗"
		}
		fmt.Printf("  %s %s — %s\n", mark, s.Name, s.Detail)
	}
	if len(res.Models) == 0 {
		fmt.Println("\nno model list from this endpoint — pick one with --model")
		return nil
	}
	fmt.Printf("\n%d models, free first:\n", len(res.Models))
	for _, wantFree := range []bool{true, false} {
		for _, m := range res.Models {
			if m.Free != wantFree {
				continue
			}
			note := ""
			if m.Free {
				note = "  free"
				if m.Note != "" {
					note += " — " + m.Note
				}
			}
			fmt.Printf("  %s%s\n", m.ID, note)
		}
	}
	return nil
}

// cmdInstall builds creator and ctr and links them into a PATH
// directory so both work from any folder.
func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", defaultBinDir(), "directory to install into")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	root, err := findModuleRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	for _, target := range []struct{ pkg, name string }{
		{"./cmd/creator", "creator"},
		{"./cmd/creator-tui", "creator-tui"},
		{"./cmd/ctr", "ctr"},
	} {
		name := target.name
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		out := filepath.Join(*dir, name)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", out, target.pkg)
		cmd.Dir = root
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("building %s: %w", target.name, err)
		}
		fmt.Printf("installed %s\n", out)
	}
	if _, err := exec.LookPath("creator"); err != nil {
		fmt.Printf("\nadd %s to your PATH, then creator, creator-tui and ctr work from any folder\n", *dir)
	}
	return nil
}

// findModuleRoot locates the source tree the binaries were built from.
func findModuleRoot() (string, error) {
	if wd, err := os.Getwd(); err == nil {
		if dir, ok := walkForGoMod(wd); ok {
			return dir, nil
		}
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		if dir, ok := walkForGoMod(filepath.Dir(file)); ok {
			return dir, nil
		}
	}
	return "", errors.New("not inside the creator source tree — run creator install from a checkout of the repo")
}

func walkForGoMod(start string) (string, bool) {
	for dir := start; ; {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// defaultBinDir is where a user-level install lands.
func defaultBinDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "bin")
	}
	return "bin"
}

func cmdShell(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f globalFlags
	f.bind(fs)
	timeout := fs.Int("timeout", 120, "kill the command after N seconds")
	background := fs.Bool("background", false, "start the job and return immediately")
	raw := fs.Bool("raw", false, "print the raw output instead of a digest")
	ptyFlag := fs.Bool("pty", false, "run inside a pseudo-terminal (for vim, htop, REPLs)")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	command := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(command) == "" {
		return errors.New("usage: creator shell [flags] <command>")
	}
	cfg, err := f.resolve()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	pruneStale(st, cfg.Verbose)
	jobs := runner.NewManager(st)
	// --background detaches the job; StopAll must not kill it on return.
	detach := *background
	defer func() {
		if !detach {
			jobs.StopAll()
		}
	}()

	engine := policy.NewEngine(cfg.PolicyMode, cfg.AllowCritical, isInteractive(), os.Stdin, os.Stderr)
	auditLog(engine, cfg.Verbose)
	engine.Shell = shellDisplayName(cfg.Shell)
	if _, err := engine.Authorize(ctx, command); err != nil {
		return &exitError{code: 3, msg: err.Error()}
	}
	job, err := jobs.Start(ctx, runner.Spec{
		Command: command,
		Dir:     cfg.Workspace,
		Timeout: time.Duration(*timeout) * time.Second,
		Shell:   cfg.Shell,
		PTY:     *ptyFlag && !*background, // detached jobs have no terminal
		Detach:  *background,
	})
	if err != nil {
		return err
	}
	if *background {
		fmt.Printf("%s\n", job.ID)
		fmt.Fprintf(os.Stderr, "creator: detached job %s is running in the background; track it with `creator jobs %s`\n", job.ID, job.ID)
		return nil
	}
	_ = job.Wait(ctx)
	snap := job.Snapshot()
	output, err := job.Output()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s · %s · exit %d · %s\n", snap.ID, snap.Status, snap.ExitCode, snap.Duration.Round(time.Millisecond))
	if cfg.JSON {
		// Machine-readable: one JSON object on stdout (stderr keeps the
		// status line). Output is the full captured artifact.
		type shellJSON struct {
			Type       string `json:"type"`
			ID         string `json:"id"`
			Status     string `json:"status"`
			ExitCode   int    `json:"exit_code"`
			DurationMs int64  `json:"duration_ms"`
			Truncated  bool   `json:"truncated"`
			Output     string `json:"output"`
		}
		_ = json.NewEncoder(os.Stdout).Encode(shellJSON{
			Type:       "result",
			ID:         snap.ID,
			Status:     string(snap.Status),
			ExitCode:   snap.ExitCode,
			DurationMs: snap.Duration.Milliseconds(),
			Truncated:  snap.Truncated,
			Output:     output,
		})
	} else if *raw {
		fmt.Print(output)
	} else {
		res := reduce.Reduce(snap.ID, output, reduce.Options{Budget: cfg.OutputTokens, Kind: reduce.KindAuto})
		fmt.Println(res.Text)
	}
	if ctx.Err() != nil {
		// Interrupted (e.g. Ctrl+C): the shell convention is 128+SIGINT.
		// main() maps context.Canceled to 130.
		return context.Canceled
	}
	if snap.Status != runner.StatusSuccess {
		return &exitError{code: 1}
	}
	return nil
}

// ---------------------------------------------------------------------------
// jobs / out
// ---------------------------------------------------------------------------

func cmdJobs(args []string) error {
	fs := flag.NewFlagSet("jobs", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f globalFlags
	f.bind(fs)
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	cfg, err := f.resolve()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()

	if id := fs.Arg(0); id != "" {
		rec, err := st.LoadJob(id)
		if err != nil {
			return err
		}
		dur := "—"
		if !rec.Ended.IsZero() {
			dur = rec.Ended.Sub(rec.Started).Round(time.Millisecond).String()
		}
		status := "exit " + fmt.Sprint(rec.ExitCode)
		if runner.Status(rec.Status) == runner.StatusRunning || rec.Ended.IsZero() {
			status = "running"
		}
		fmt.Printf("%s · %s · %s · %s\n$ %s\n\n", rec.ID, rec.Status, status, dur, rec.Command)
		data, err := os.ReadFile(st.ArtifactPath(rec.ID))
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Printf("artifact missing at %s (the job may still be running, or it was pruned)\n", st.ArtifactPath(rec.ID))
			} else {
				fmt.Printf("artifact unreadable at %s: %v\n", st.ArtifactPath(rec.ID), err)
			}
			return nil
		}
		page := reduce.Page(string(data), "tail", "", 0, 0, 40, false, cfg.OutputTokens)
		fmt.Println(page)
		return nil
	}

	recs := st.ListJobs(20)
	if err := st.DBHealth(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: the job database reported errors (%v) — output above may be incomplete\n", err)
	}
	if len(recs) == 0 {
		fmt.Println("no jobs recorded")
		return nil
	}
	fmt.Printf("%-22s %-8s %-4s %-10s %s\n", "ID", "STATUS", "EXIT", "DURATION", "COMMAND")
	for _, r := range recs {
		dur := time.Duration(0)
		if !r.Ended.IsZero() {
			dur = r.Ended.Sub(r.Started)
		}
		cmd := strings.ReplaceAll(r.Command, "\n", " ")
		if n := len([]rune(cmd)); n > 70 {
			// Rune-safe truncation: byte-slicing splits multi-byte glyphs.
			cmd = string([]rune(cmd)[:70]) + "…"
		}
		fmt.Printf("%-22s %-8s %-4d %-10s %s\n", r.ID, r.Status, r.ExitCode, dur.Round(time.Millisecond), cmd)
	}
	return nil
}

func cmdOut(args []string) error {
	fs := flag.NewFlagSet("out", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f globalFlags
	f.bind(fs)
	mode := fs.String("mode", "head", "head, tail, range, grep or full")
	grep := fs.String("grep", "", "shorthand for --mode grep")
	rangeSpec := fs.String("range", "", "shorthand for --mode range, e.g. 100:250")
	tail := fs.Int("tail", 0, "shorthand for --mode tail with N lines")
	head := fs.Int("head", 0, "shorthand for --mode head with N lines")
	full := fs.Bool("full", false, "shorthand for --mode full")
	ignoreCase := fs.Bool("ignore-case", false, "case-insensitive grep")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	handle := fs.Arg(0)
	if handle == "" {
		return errors.New("usage: creator out <handle> [--grep PATTERN [--ignore-case] | --range A:B | --tail N | --head N | --full]")
	}
	cfg, err := f.resolve()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()

	data, err := st.ReadArtifact(handle)
	if err != nil {
		return err
	}
	start, end, n := 0, 0, 0
	switch {
	case *grep != "":
		*mode = "grep"
	case *rangeSpec != "":
		*mode = "range"
		parts := strings.SplitN(*rangeSpec, ":", 2)
		if len(parts) != 2 {
			return errors.New("--range expects START:END")
		}
		start, err = strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return fmt.Errorf("--range start %q is not a number", strings.TrimSpace(parts[0]))
		}
		end, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return fmt.Errorf("--range end %q is not a number", strings.TrimSpace(parts[1]))
		}
		if start < 1 || end < 1 {
			return errors.New("--range lines are 1-based and must be positive")
		}
		if start > end {
			return fmt.Errorf("--range start %d is after end %d", start, end)
		}
	case *tail != 0:
		if *tail < 0 {
			return fmt.Errorf("--tail must be a positive number (got %d)", *tail)
		}
		*mode, n = "tail", *tail
	case *head != 0:
		if *head < 0 {
			return fmt.Errorf("--head must be a positive number (got %d)", *head)
		}
		*mode, n = "head", *head
	case *full:
		*mode = "full"
	}
	pattern := *grep
	fmt.Println(reduce.Page(data, *mode, pattern, start, end, n, *ignoreCase, cfg.OutputTokens))
	return nil
}

// ---------------------------------------------------------------------------
// mcp
// ---------------------------------------------------------------------------

func cmdMCP(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f globalFlags
	f.bind(fs)
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	cfg, err := f.resolve()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	pruneStale(st, cfg.Verbose)
	jobs := runner.NewManager(st)
	defer jobs.StopAll()

	// A provider is required for delegation from MCP mode; without one the
	// delegate tools still register and report the missing backend politely.
	prov, _ := resolveProvider(cfg)

	engine := policy.NewEngine(cfg.PolicyMode, cfg.AllowCritical, false, os.Stdin, os.Stderr)
	auditLog(engine, cfg.Verbose)
	engine.Shell = shellDisplayName(cfg.Shell)
	env := &tools.Env{
		Workspace: cfg.Workspace,
		Store:     st,
		Jobs:      jobs,
		Policy:    engine,
		Session:   id.New("mcp"),
		Budget:    cfg.OutputTokens,
		Shell:     cfg.Shell,
	}
	registry := tools.New(env)
	env.Hooks = loadPlugins(cfg, registry)

	brd := board.New(st, board.Options{
		Root:   cfg.Workspace,
		Jobs:   jobs,
		Policy: engine,
		Shell:  cfg.Shell,
	})
	env.OnCommandExit = func(command string, code int, jobID, dir string) {
		_, _ = brd.AutoCloseByVerify(command, jobID, dir)
	}
	if err := brd.Attach(registry, env.Session); err != nil {
		fmt.Fprintf(os.Stderr, "creator: %v\n", err)
	}

	mgr := delegate.New(st, delegateOptions(cfg, st, registry, prov, brd, knowledgeSection(cfg)))
	defer mgr.Stop()
	if err := mgr.Attach(registry, 0, env.Session); err != nil {
		fmt.Fprintf(os.Stderr, "creator: %v\n", err)
	}
	server := &mcp.Server{
		Tools:   registry,
		Version: version,
		In:      os.Stdin,
		Out:     os.Stdout,
		Logf: func(format string, a ...any) {
			if cfg.Verbose {
				fmt.Fprintf(os.Stderr, format+"\n", a...)
			}
		},
	}
	return server.Serve(ctx)
}

// ---------------------------------------------------------------------------
// task board
// ---------------------------------------------------------------------------

// cmdTasks renders the persisted board for a human (the model sees the same
// rendering through the tasks tool). A single id prints that task's full
// detail: notes, receipts, acceptance criteria.
func cmdTasks(args []string) error {
	fs := flag.NewFlagSet("tasks", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f globalFlags
	f.bind(fs)
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	cfg, err := f.resolve()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()

	if idv := fs.Arg(0); idv != "" {
		t, err := st.LoadTask(idv)
		if err != nil {
			return err
		}
		fmt.Println(board.TaskDetail(t, taskDeps(st, t), time.Now()))
		return nil
	}
	recs := st.ListTasks(0)
	fmt.Println(board.RenderBoard(recs, time.Now()))
	fmt.Println("\nmodel view: the tasks tool (add/start/done/flag/cancel/dep/list) · journal: creator delegates --events")
	return nil
}

// taskDeps resolves a task's dependencies for the CLI detail view.
func taskDeps(st *store.Store, t store.TaskRecord) map[string]string {
	out := map[string]string{}
	for _, d := range t.DependsOn {
		if dv, err := st.LoadTask(d); err == nil {
			out[dv.ID] = dv.Status
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// providers / sessions / revert / init
// ---------------------------------------------------------------------------

// pluginDirs returns the plugin directories to load. User-level plugins in
// the data directory always load. Workspace plugins are included only after
// an explicit opt-in (--workspace-plugins or CREATOR_WORKSPACE_PLUGINS=1),
// because opening a repository must not confer code execution.
func pluginDirs(cfg config.Config) []string {
	dirs := []string{}
	ws := filepath.Join(cfg.Workspace, ".creator", "plugins")
	if cfg.WorkspacePlugins {
		dirs = append(dirs, ws)
	} else if st, err := os.Stat(ws); err == nil && st.IsDir() {
		fmt.Fprintf(os.Stderr, "creator: ignoring workspace plugins in %s (pass --workspace-plugins to opt in)\n", ws)
	}
	dirs = append(dirs, filepath.Join(cfg.DataDir, "plugins"))
	return dirs
}

// loadPlugins registers Lua tools. The host stays alive as long as the
// registered handlers reference it; its hook set is returned so the caller can
// splice it into the tool environment.
func loadPlugins(cfg config.Config, registry *tools.Registry) tools.Hooks {
	host, warnings, err := plugin.Load(pluginDirs(cfg))
	if err != nil {
		fmt.Fprintf(os.Stderr, "creator: plugins: %v\n", err)
		return nil
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "creator: plugin skipped: %s\n", w)
	}
	for _, tool := range host.Tools() {
		if err := registry.Add(tool); err != nil {
			fmt.Fprintf(os.Stderr, "creator: plugin tool rejected: %v\n", err)
		}
	}
	return host.Hooks()
}

func cmdPlugins(args []string) error {
	fs := flag.NewFlagSet("plugins", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f globalFlags
	f.bind(fs)
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	cfg, err := f.resolve()
	if err != nil {
		return err
	}
	dirs := pluginDirs(cfg)
	fmt.Printf("plugin directories:\n")
	for _, d := range dirs {
		fmt.Printf("  %s\n", d)
	}
	fmt.Println()
	host, warnings, err := plugin.Load(dirs)
	if err != nil {
		return err
	}
	defer host.Close()
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	if files := host.Files(); len(files) > 0 {
		fmt.Println("loaded files:")
		for _, f := range files {
			fmt.Printf("  %s\n", f)
		}
	}
	if names := host.Names(); len(names) > 0 {
		fmt.Println("tools:")
		for _, n := range names {
			fmt.Printf("  %s\n", n)
		}
	} else {
		fmt.Println("no plugin tools found")
	}
	return nil
}

// ---------------------------------------------------------------------------
// delegates
// ---------------------------------------------------------------------------

// normalizeBareFlag rewrites a value-taking flag used bare (e.g. the
// documented `creator delegates --events`) into `--flag=<default>`, so the
// stdlib flag parser doesn't demand an argument the user was never told to
// provide. Tokens already carrying a value are left alone.
func normalizeBareFlag(args []string, name, def string) []string {
	for i, a := range args {
		if a != "--"+name && a != "-"+name {
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			return args // a value follows
		}
		out := make([]string, 0, len(args)+1)
		out = append(out, args[:i]...)
		out = append(out, "--"+name+"="+def)
		out = append(out, args[i+1:]...)
		return out
	}
	return args
}

func cmdDelegates(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("delegates", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f globalFlags
	f.bind(fs)
	events := fs.Int("events", 0, "print the last N journal events and exit (default 20)")
	follow := fs.Bool("follow", false, "live-tail the delegation event journal (Ctrl+C ends)")
	limit := fs.Int("limit", 20, "how many delegates to list")
	args = normalizeBareFlag(args, "events", "20")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	cfg, err := f.resolve()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	st.OrphanDelegates() // stale "running" state belongs to dead processes

	if *follow {
		return followDelegateEvents(ctx, st)
	}
	if *events > 0 {
		for _, ev := range st.LatestDelegateEvents(*events) {
			fmt.Println(renderDelegateEvent(ev))
		}
		return nil
	}

	if idv := fs.Arg(0); idv != "" {
		rec, err := st.LoadDelegate(idv)
		if err != nil {
			return err
		}
		dur := ""
		if !rec.Ended.IsZero() {
			dur = " · " + rec.Ended.Sub(rec.Started).Round(time.Millisecond).String()
		} else if rec.Status == "running" {
			dur = " · " + time.Since(rec.Started).Round(time.Second).String()
		}
		fmt.Printf("%s · %s · depth %d · %d steps · %s in/%s out%s\n",
			rec.ID, rec.Status, rec.Depth, rec.Steps, commaIt(rec.Usage.In), commaIt(rec.Usage.Out), dur)
		fmt.Printf("parent: %s · name: %s\n", orElseStr(rec.Parent, "-"), orElseStr(rec.Name, "-"))
		fmt.Printf("task: %s\n", rec.Task)
		if rec.Error != "" {
			fmt.Printf("error: %s\n", rec.Error)
		}
		if rec.Result != "" {
			data, rerr := st.ReadArtifact(rec.ID)
			if rerr != nil {
				data = rec.Result
			}
			page := reduce.Page(data, "tail", "", 0, 0, 40, false, cfg.OutputTokens)
			fmt.Println(strings.TrimSpace(page))
		}
		return nil
	}

	recs := st.ListDelegates(*limit)
	if len(recs) == 0 {
		fmt.Println("no delegation records yet (spawned with the delegate tool)")
		return nil
	}
	fmt.Printf("%-24s %-9s %-6s %-5s %-10s %-16s %s\n", "ID", "STATUS", "DEPTH", "STEPS", "TOKENS", "NAME", "TASK")
	for _, r := range recs {
		fmt.Printf("%-24s %-9s %-6d %-5d %-10s %-16s %s\n",
			r.ID, r.Status, r.Depth, r.Steps, commaIt(r.Usage.In+r.Usage.Out), r.Name, r.Task)
	}
	fmt.Println("\ninspect: creator delegates <id> · journal: creator delegates --events 50 --follow")
	return nil
}

// followDelegateEvents replays the existing journal, then live-tails it. The
// front-end shipped later reads exactly this substrate; the CLI is its
// reference consumer.
func followDelegateEvents(ctx context.Context, st *store.Store) error {
	existing := st.LatestDelegateEvents(30)
	var watermark uint64
	for _, ev := range existing {
		fmt.Println(renderDelegateEvent(ev))
		watermark = ev.Seq
	}
	fmt.Println("(following — new delegation events appear here; Ctrl+C to stop)")
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			for _, ev := range st.ListDelegateEvents(watermark, 200) {
				fmt.Println(renderDelegateEvent(ev))
				watermark = ev.Seq
			}
		}
	}
}

// renderDelegateEvent prints one journal line compactly.
func renderDelegateEvent(ev store.DelegateEvent) string {
	ts := ev.Time.Format("15:04:05")
	name := orElseStr(ev.Name, "")
	tag := orElseStr(name, orElseStr(ev.ID, "-"))
	parts := []string{ts, fmt.Sprintf("%3d", ev.Seq), ev.Kind, tag}
	if ev.Depth > 0 {
		parts = append(parts, fmt.Sprintf("d%d", ev.Depth))
	}
	if ev.Tool != "" {
		parts = append(parts, ev.Tool)
	}
	if ev.Status != "" {
		parts = append(parts, ev.Status)
	}
	if ev.Usage.In+ev.Usage.Out > 0 {
		parts = append(parts, commaIt(ev.Usage.In)+"in/"+commaIt(ev.Usage.Out)+"out")
	}
	detail := cutLine(ev.Detail, 110)
	if detail != "" {
		parts = append(parts, "·", detail)
		return strings.Join(parts, " ")
	}
	if task := cutLine(ev.Task, 80); task != "" {
		parts = append(parts, "·", task)
	}
	return strings.Join(parts, " ")
}

// cutLine keeps a bounded chunk of text for journal rendering.
func cutLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return cutRunes(s, max)
}

func commaIt(n int) string {
	s := strconv.Itoa(n)
	var out []string
	for len(s) > 3 {
		out = append([]string{s[len(s)-3:]}, out...)
		s = s[:len(s)-3]
	}
	out = append([]string{s}, out...)
	return strings.Join(out, ",")
}

func orElseStr(v, d string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return d
}

func cmdProviders() error {
	fmt.Printf("%-12s %-26s %-10s %-32s %-6s %s\n", "SLUG", "NAME", "PROTOCOL", "DEFAULT MODEL", "FREE", "STATUS")
	for _, d := range provider.Descriptors() {
		status := "ready"
		switch {
		case d.Local:
			status = "local"
		case provider.MissingKey(d):
			status = "missing " + strings.Join(d.APIKeyEnv, "/")
		}
		free := ""
		if d.Free != "" {
			free = "yes"
		}
		fmt.Printf("%-12s %-26s %-10s %-32s %-6s %s\n", d.Slug, d.Name, d.Protocol, d.DefaultModel(), free, status)
	}
	fmt.Println("\nconnect a key to list its live models (free first): creator connect <provider> <key>")
	fmt.Println("or use a local backend: creator run --provider ollama --model qwen3-coder \"...\"")
	return nil
}

func cmdSessions(args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f globalFlags
	f.bind(fs)
	limit := fs.Int("limit", 20, "how many sessions to list")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	cfg, err := f.resolve()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	metas := st.ListSessions(*limit)
	if len(metas) == 0 {
		fmt.Println("no sessions yet")
		return nil
	}
	fmt.Printf("%-24s %-18s %-28s %s\n", "ID", "UPDATED", "MODEL", "TOKENS IN/OUT")
	for _, m := range metas {
		fmt.Printf("%-24s %-18s %-28s %d/%d\n", m.ID, m.Updated.Format("2006-01-02 15:04:05"), m.Model, m.Usage.In, m.Usage.Out)
	}
	fmt.Println("\nresume with: creator run --session <id> \"...\"")
	return nil
}

func cmdRevert(args []string) error {
	fs := flag.NewFlagSet("revert", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f globalFlags
	f.bind(fs)
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	cpID := fs.Arg(0)
	if cpID == "" {
		return errors.New("usage: creator revert <checkpoint-id>")
	}
	cfg, err := f.resolve()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	cp, err := st.GetCheckpoint(cpID)
	if err != nil {
		return err
	}
	data, err := st.ReadCheckpoint(cp)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cp.Path), 0o700); err != nil {
		return err
	}
	perm := os.FileMode(0o600)
	if cp.Mode != 0 {
		perm = os.FileMode(cp.Mode).Perm()
	}
	// Never overwrite through a symlink: a checkpoint's path may have been
	// replaced by a link pointing outside the workspace since it was saved.
	// (Residual check-to-write window remains, as with all std-lib-only
	// no-follow handling — same class as the workspace write tools.)
	if fi, err := os.Lstat(cp.Path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; refusing to overwrite through it (restore to a fresh path instead)", cp.Path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(cp.Path, data, perm); err != nil {
		return err
	}
	_ = os.Chmod(cp.Path, perm)
	fmt.Printf("restored %s from %s (%d bytes, saved %s)\n", cp.RelPath, cp.ID, len(data), cp.Created.Format(time.RFC3339))
	return nil
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f globalFlags
	f.bind(fs)
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	cfg, err := config.Load(f.overrides())
	if err != nil {
		return err
	}
	if cfg.Provider == "" {
		cfg.Provider = "openai"
	}
	cfg.Workspace = ""
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n\nprovider: %s\n", config.PathIn(cfg.DataDir), cfg.Provider)
	if d, ok := provider.Lookup(cfg.Provider); ok {
		if d.Local {
			fmt.Printf("%s runs locally; start it and Creator will connect to %s\n", d.Name, d.BaseURL)
		} else {
			fmt.Printf("paste a key: creator connect %s <key>\n", cfg.Provider)
			fmt.Printf("or set %s, then run: creator run \"...\"\n", strings.Join(d.APIKeyEnv, " or "))
		}
	}
	return nil
}

// knowledgeSection discovers the repository instruction files unless the user
// turned them off. Discovery is cheap and root-only; the section is bounded by
// the knowledge package.
func knowledgeSection(cfg config.Config) string {
	if cfg.DisableKnowledge {
		return ""
	}
	return knowledge.Read(cfg.Workspace)
}

// cmdKnowledge shows which instruction files the agent will load, and what it
// will see. It is the inspectable half of an otherwise invisible prompt input.
func cmdKnowledge(args []string) error {
	fs := flag.NewFlagSet("knowledge", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f globalFlags
	f.bind(fs)
	printText := fs.Bool("print", false, "print the rendered prompt section")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	cfg, err := f.resolve()
	if err != nil {
		return err
	}

	sec := knowledge.Discover(cfg.Workspace)
	if cfg.JSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"workspace": cfg.Workspace,
			"searched":  knowledge.Names,
			"files":     sec.Files,
			"section":   sec.Text,
		})
	}
	if sec.Empty() {
		fmt.Printf("no knowledge files in %s\n", cfg.Workspace)
		fmt.Printf("looked for: %s\n", strings.Join(knowledge.Names, ", "))
		fmt.Println("create AGENTS.md to give the agent project instructions")
		return nil
	}
	fmt.Printf("%d knowledge file(s) in %s\n", len(sec.Files), cfg.Workspace)
	for _, file := range sec.Files {
		note := ""
		if file.Truncated {
			note = " (truncated)"
		}
		fmt.Printf("  %-28s %7d bytes%s\n", file.Path, file.Bytes, note)
	}
	if *printText {
		fmt.Print(sec.Text)
	}
	return nil
}

// cmdProfiles lists the named delegate roles the model can ask for.
func cmdProfiles(args []string) error {
	fs := flag.NewFlagSet("profiles", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOut := fs.Bool("json", false, "machine readable output")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	ps := delegate.Profiles()
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(ps)
	}
	fmt.Printf("%-9s %-9s %-10s %-6s %s\n", "profile", "access", "tools", "steps", "description")
	for _, p := range ps {
		access := "write"
		if p.ReadOnly {
			access = "read-only"
		}
		tools := "inherited"
		if len(p.Tools) > 0 {
			tools = fmt.Sprintf("%d", len(p.Tools))
		}
		fmt.Printf("%-9s %-9s %-10s %-6d %s\n", p.Name, access, tools, p.MaxSteps, p.Description)
	}
	fmt.Println("\nuse: delegate(task=\"...\", profile=\"explorer\") — explicit tools/deny narrow a profile, never widen it")
	return nil
}

// askOnTerminal is the ask_user front-end: prompt on stderr so it cannot be
// mistaken for model output, and read the answer without a buffered reader of
// its own so it cannot swallow bytes another prompt already buffered.
func askOnTerminal(ctx context.Context, req tools.AskRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "\n? %s\n", req.Question)
	for i, opt := range req.Options {
		fmt.Fprintf(os.Stderr, "  %d) %s\n", i+1, opt)
	}
	fmt.Fprint(os.Stderr, "> ")
	line, err := readLineUnbuffered(os.Stdin)
	if errors.Is(err, io.EOF) {
		// The channel closed before any answer arrived — the user never
		// answered. Never report that as "successful empty answer".
		return "", errors.New("no answer on stdin (EOF) — ask_user needs an interactive terminal or piped input")
	}
	if err != nil {
		return "", err
	}
	answer := strings.TrimSpace(line)
	if answer != "" && len(req.Options) > 0 {
		if n, convErr := strconv.Atoi(answer); convErr == nil && n >= 1 && n <= len(req.Options) {
			return req.Options[n-1], nil
		}
	}
	return answer, nil
}

// readLineUnbuffered reads up to a newline one byte at a time. Interactivity
// is rare and answers are short, so the cost is irrelevant; sharing the policy
// engine's buffered reader would be far more fragile.
func readLineUnbuffered(r io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			switch buf[0] {
			case '\n':
				return b.String(), nil
			case '\r':
			default:
				b.WriteByte(buf[0])
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && b.Len() > 0 {
				return b.String(), nil
			}
			return b.String(), err
		}
	}
}

func isInteractive() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func stdoutIsTTY() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
