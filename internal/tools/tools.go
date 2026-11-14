// Package tools implements the capabilities Creator exposes to a model.
//
// The registry is shared by the agent loop and the MCP server, so a tool added
// here becomes available to both a local model and any MCP client.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/DERYK-RYLOY/creator/internal/id"
	"github.com/DERYK-RYLOY/creator/internal/policy"
	"github.com/DERYK-RYLOY/creator/internal/provider"
	"github.com/DERYK-RYLOY/creator/internal/reduce"
	"github.com/DERYK-RYLOY/creator/internal/runner"
	"github.com/DERYK-RYLOY/creator/internal/store"
)

// Env is the runtime a tool executes against.
type Env struct {
	Workspace string
	Store     *store.Store
	Jobs      *runner.Manager
	Policy    *policy.Engine
	Session   string
	Budget    int    // observation token budget
	Shell     string // shell preference for the shell tool (auto, bash, powershell, ...)
	// Hooks intercept every tool call and delegation. May be nil.
	Hooks Hooks
	// Ask lets the ask_user tool reach a human. Nil in headless, MCP and
	// delegate runs; the tool then tells the model to proceed on an
	// assumption instead of blocking forever.
	Ask AskFunc
	// OnCommandExit is called after a shell command completes with its exit
	// code and job id. Used to auto-close board tasks whose verify command
	// just passed. Nil disables the hook.
	OnCommandExit func(command string, exitCode int, jobID string, dir string)
	// OnProgress streams live tool output while a tool runs. It is used by the
	// TUI to render a process as it happens. Nil disables progress events.
	OnProgress func(name, stage, text string, meta map[string]string)
	// ResolveProvider builds a model backend on demand, used by the delegate
	// system when a spawn asks for a specific provider or model. May be nil;
	// the delegate manager then requires an explicit provider.
	ResolveProvider func(providerSlug, model string) (provider.Provider, error)
}

// Tool is one callable capability.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	ReadOnly    bool
	Handler     func(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry holds tools in registration order.
type Registry struct {
	env   *Env
	tools map[string]*Tool
	order []string
}

// New builds the default toolset.
func New(env *Env) *Registry {
	if env.Budget <= 0 {
		env.Budget = reduce.DefaultBudget
	}
	r := &Registry{env: env, tools: map[string]*Tool{}}
	r.register(readTool())
	r.register(writeTool())
	r.register(editTool())
	r.register(listTool())
	r.register(globTool())
	r.register(grepTool())
	r.register(hashTool())
	r.register(stringsTool())
	r.register(netFetchTool())
	r.register(webFetchTool())
	if env.Ask != nil {
		r.register(askUserTool())
	}
	r.register(hexdumpTool())
	r.register(entropyTool())
	r.register(xorDecodeTool())
	r.register(peInfoTool())
	r.register(elfInfoTool())
	r.register(rot13Tool())
	r.register(hexToAsciiTool())
	r.register(base64DecodeTool())
	r.register(caesarShiftTool())
	r.register(writeFileTool())
	r.register(outlineTool())
	r.register(readSymbolTool())
	r.register(appendTool())
	r.register(insertTool())
	r.register(patchTool())
	r.register(diffTool())
	r.register(reviewTool())
	r.register(searchFilesTool())
	r.register(treeTool())
	r.register(todosTool())
	r.register(findDefTool())
	r.register(repoStatsTool())
	r.register(shellTool())
	r.register(jobsTool())
	r.register(outTool())
	return r
}

func (r *Registry) register(t Tool) {
	if _, exists := r.tools[t.Name]; exists {
		panic("duplicate tool " + t.Name)
	}
	r.tools[t.Name] = &t
	r.order = append(r.order, t.Name)
}

// Add registers an externally supplied tool (a Lua plugin, for example).
func (r *Registry) Add(t Tool) error {
	if _, exists := r.tools[t.Name]; exists {
		return fmt.Errorf("tool %q is already registered", t.Name)
	}
	r.register(t)
	return nil
}

// Env returns the tool environment.
func (r *Registry) Env() *Env { return r.env }

// Specs converts the registry into provider tool specs.
func (r *Registry) Specs() []provider.ToolSpec {
	out := make([]provider.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		out = append(out, provider.ToolSpec{
			Name:        t.Name,
			Description: t.Description,
			Schema:      t.Schema,
		})
	}
	return out
}

// Names lists registered tool names.
func (r *Registry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Call invokes a tool, converting panics into errors so a bad model payload
// cannot take down the process. Hook interception wraps both entry and exit,
// so agent, MCP and manual CLI calls share one policy spine.
func (r *Registry) Call(ctx context.Context, name string, args json.RawMessage) (result string, err error) {
	// Last-resort safety net: any panic above the hook layers (or inside one
	// that escapes its own wrapper) is converted into a tool error instead
	// of crashing the process.
	defer func() {
		if rec := recover(); rec != nil {
			result = ""
			err = fmt.Errorf("tool %s panicked: %v", name, rec)
		}
	}()

	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q (available: %s)", name, strings.Join(r.order, ", "))
	}
	if h := r.env.Hooks; h != nil {
		args2, hookErr := beforeToolSafe(h, name, args)
		if hookErr != nil {
			return "", hookErr
		}
		args = args2
	}
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("tool %s panicked: %v", name, rec)
		}
		if h := r.env.Hooks; h != nil {
			if out := afterToolSafe(h, name, result, err); out != "" {
				result = out
			}
		}
	}()
	return t.Handler(withEnv(ctx, r.env), args)
}

// Reduce returns a new registry sharing this one's environment but carrying
// only the tools for which keep returns true. Sub-agent toolsets are built
// this way: the delegate manager filters the root registry per spawn. The
// original registry is untouched.
func (r *Registry) Reduce(keep func(t Tool) bool) *Registry {
	out := &Registry{env: r.env, tools: map[string]*Tool{}}
	for _, name := range r.order {
		t := *r.tools[name]
		if keep(t) {
			out.tools[name] = &t
			out.order = append(out.order, name)
		}
	}
	return out
}

type envKey struct{}

func withEnv(ctx context.Context, env *Env) context.Context {
	return context.WithValue(ctx, envKey{}, env)
}

func currentEnv(ctx context.Context) *Env {
	env, ok := ctx.Value(envKey{}).(*Env)
	if !ok {
		panic("tools: environment missing from context")
	}
	return env
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// resolvedRoot returns the workspace root with symlinks resolved once, so
// path verification (and the post-open re-check) reason about the same base.
func (e *Env) resolvedRoot() (string, error) {
	abs, err := filepath.Abs(e.Workspace)
	if err != nil {
		return "", err
	}
	return evalSymlinksPath(abs)
}

// resolve maps a possibly relative path into the workspace and refuses anything
// outside it. Symlinks are resolved as far as they exist, so a workspace
// symlink to /etc does not launder writes outside the sandbox.
func (e *Env) resolve(p, wantDir string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("path is required")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(e.Workspace, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	root, err := e.resolvedRoot()
	if err != nil {
		return "", err
	}
	// Component-wise verification: every existing component (including
	// dangling symlinks) is resolved and required to stay inside root.
	// This closes the tail-reattachment escape where a dangling symlink in
	// the path made lexical containment lie.
	resolved, err := verifyWithin(root, abs, 0)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace (%s)", p, root)
	}
	abs = resolved
	if wantDir != "" {
		info, err := os.Stat(abs)
		if err != nil {
			if wantDir == "file" && os.IsNotExist(err) {
				return "", fmt.Errorf("%s: %w", p, err)
			}
			return "", fmt.Errorf("%s: %w", p, err)
		}
		if wantDir == "dir" && !info.IsDir() {
			return "", fmt.Errorf("%s is not a directory", p)
		}
		if wantDir == "file" && info.IsDir() {
			return "", fmt.Errorf("%s is a directory", p)
		}
	}
	return abs, nil
}

// insideWorkspace reports whether path resolves — following every symlink in
// it — to somewhere under root. root is the workspace; path may be absolute
// or relative to the current directory's view of root. Walk-based tools use
// it per file, because their containment check on the walk root proves
// nothing about the files it finds.
func insideWorkspace(root, path string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	rootRes, err := evalSymlinksPath(absRoot)
	if err != nil {
		return false
	}
	_, err = verifyWithin(rootRes, path, 0)
	return err == nil
}

// verifyWithin resolves every component of path (absolute, possibly with a
// non-existent tail) starting from root, following symlinks component by
// component and requiring every redirect to stay inside root. Unlike
// filepath.EvalSymlinks it tolerates non-existent tails, and unlike the
// attach-the-tail logic it closes dangling-symlink escapes: a symlink in
// the tail (e.g. one that points at /etc/passwd or a not-yet-created path
// outside the workspace) is followed and re-verified against root,
// recursively. Symlink cycles are capped.
func verifyWithin(root, path string, depth int) (string, error) {
	if depth > 40 {
		return "", fmt.Errorf("too many symlink hops resolving %q", path)
	}
	slashRel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("path %q is outside the workspace root %q", path, root)
	}
	slashRel = filepath.ToSlash(slashRel)
	if slashRel == ".." || strings.HasPrefix(slashRel, "../") {
		return "", fmt.Errorf("path %q is outside the workspace root %q", path, root)
	}
	cur := root
	parts := strings.Split(relSplit(slashRel), string(filepath.Separator))
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		cand := filepath.Join(cur, part)
		li, err := os.Lstat(cand)
		if err != nil {
			// Nothing on disk at this component — the tail cannot launder a
			// write through a symlink, so the lexical continuation is fine.
			cur = cand
			continue
		}
		if li.Mode()&os.ModeSymlink == 0 {
			cur = cand
			continue
		}
		dest, err := os.Readlink(cand)
		if err != nil {
			return "", fmt.Errorf("read symlink %q: %w", cand, err)
		}
		if !filepath.IsAbs(dest) {
			dest = filepath.Join(filepath.Dir(cand), dest)
		}
		dest = filepath.Clean(dest)
		tail := filepath.Join(parts[i+1:]...)
		joined := dest
		if tail != "." && tail != "" {
			joined = filepath.Join(dest, tail)
		}
		return verifyWithin(root, joined, depth+1)
	}
	return cur, nil
}

// relSplit converts a ToSlash-style relative path back into the OS dialect.
func relSplit(slashRel string) string {
	if filepath.Separator == '/' {
		return slashRel
	}
	return filepath.FromSlash(slashRel)
}

// evalSymlinksPath behaves like filepath.EvalSymlinks but also works for a
// path that does not exist yet: the deepest existing parent is resolved and
// the remaining components are reattached.
func evalSymlinksPath(path string) (string, error) {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Abs(resolved)
	}
	var tail []string
	cur := path
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("%s: cannot resolve symlinks", path)
		}
		tail = append(tail, filepath.Base(cur))
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			base := resolved
			for i := len(tail) - 1; i >= 0; i-- {
				base = filepath.Join(base, tail[i])
			}
			return filepath.Abs(base)
		}
		cur = parent
	}
}

// checkpointForMutation snapshots path if it exists and returns a short note
// naming the checkpoint. Missing files are new writes and need no checkpoint.
func checkpointForMutation(env *Env, path, tool string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	cp, err := env.Store.SaveCheckpoint(env.Session, path, env.rel(path), tool)
	if err != nil {
		// A failed checkpoint means this mutation cannot be rolled back.
		// Never proceed silently — the caller's output carries the warning.
		return " (WARNING: checkpoint failed — " + err.Error() + " — this change cannot be reverted)"
	}
	return fmt.Sprintf(" (previous version saved as %s)", cp.ID)
}

// openChecked opens path with flag and then re-verifies the file descriptor
// against a fresh resolution of the path. A symlink swapped into the path
// between resolve() and open() therefore fails the check instead of being
// truncated or appended to. For newly created files the caller passes
// O_CREATE|O_EXCL so a racer's file is never opened at all.
//
// Note: Go's stdlib exposes no race-free path walk (openat2 with
// RESOLVE_BENEATH on Linux, or reparse-point inspection on Windows), so a
// *stable* attacker swap is indistinguishable from the legitimate target.
// This check closes the realistic window — a transient swap that persists
// from check to open — but cannot out-invent kernel primitives.
func openChecked(e *Env, path string, flag int, perm os.FileMode) (*os.File, error) {
	root, err := e.resolvedRoot()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	resolved, err := verifyWithin(root, path, 0)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("re-verify %s: %w", path, err)
	}
	opened, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	current, err := os.Stat(resolved)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("re-verify %s: %w", resolved, err)
	}
	if !os.SameFile(opened, current) {
		f.Close()
		return nil, fmt.Errorf("%s changed between resolve and open (symlink swapped in) — refusing to write", path)
	}
	return f, nil
}

// writeFilePreserveMode writes data to path, keeping the file's existing
// permission bits when it exists and using a restrictive default otherwise.
// The write is identity-checked against a fresh resolution, and new files
// are created with O_EXCL, so a racer cannot redirect the write.
func writeFilePreserveMode(e *Env, path string, data []byte) error {
	perm := os.FileMode(0o600)
	existed := false
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
		existed = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	flag := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if existed {
		flag = os.O_WRONLY // truncate only after the identity check
	}
	f, err := openChecked(e, path, flag, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	if existed {
		if err := f.Truncate(0); err != nil {
			return err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Chmod(perm); err != nil {
		return err
	}
	return nil
}

// rel turns an absolute path back into a workspace-relative one for display.
func (e *Env) rel(abs string) string {
	root, err := filepath.Abs(e.Workspace)
	if err != nil {
		return abs
	}
	if r, err := filepath.Rel(root, abs); err == nil {
		return filepath.ToSlash(r)
	}
	return abs
}

// digest stores raw output under a fresh handle and returns the reduced form.
func (e *Env) digest(raw string, kind reduce.Kind) (string, reduce.Result) {
	handle := id.New("h")
	if err := os.WriteFile(e.Store.ArtifactPath(handle), []byte(raw), 0o600); err != nil {
		// Losing the artifact is survivable: return the raw text instead of
		// failing the tool call.
		return raw, reduce.Result{Handle: handle, Kind: kind}
	}
	res := reduce.Reduce(handle, raw, reduce.Options{Budget: e.Budget, Kind: kind})
	return res.Text, res
}

// summarize reduces an existing artifact (used for jobs, whose output already
// lives at the job id).
func (e *Env) summarize(handle, raw string, kind reduce.Kind) (string, reduce.Result) {
	res := reduce.Reduce(handle, raw, reduce.Options{Budget: e.Budget, Kind: kind})
	return res.Text, res
}

// numbered renders lines with 1-based line numbers.
// maxLineChars caps a single displayed line so a minified file or a
// 10000-char string cannot blow up the terminal or jumble the output.
const maxLineChars = 400

func numbered(lines []string, from int) []string {
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if len(l) > maxLineChars {
			l = l[:maxLineChars] + fmt.Sprintf("... (%d more chars)", len(l)-maxLineChars)
		}
		out = append(out, fmt.Sprintf("%6d  %s", from+i, l))
	}
	return out
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// ignoredDirs are never walked by glob or grep; they are noise for a model and
// ruinously large in most repositories.
var ignoredDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true, "vendor": true,
	".venv": true, "venv": true, "__pycache__": true, ".mypy_cache": true,
	".pytest_cache": true, ".next": true, ".nuxt": true, "dist": true,
	"build": true, "target": true, ".idea": true, ".gradle": true,
	".terraform": true, "coverage": true, ".cache": true,
}

var limits = struct {
	ReadBytes   int64
	GrepFileMax int64
	GlobResults int
	GrepResults int
	CodeReadMax int64
}{
	ReadBytes:   4 << 20,
	GrepFileMax: 2 << 20,
	GlobResults: 4000,
	GrepResults: 400,
	CodeReadMax: 32 << 20, // generous cap for code tools; unbounded reads OOM'd the CLI
}

// beforeToolSafe converts a BeforeTool hook panic into an ordinary error so
// a broken interception layer can never take the caller down with it.
func beforeToolSafe(h Hooks, name string, args json.RawMessage) (result json.RawMessage, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			result = nil
			err = fmt.Errorf("BeforeTool hook panicked: %v", rec)
		}
	}()
	return h.BeforeTool(name, args)
}

// afterToolSafe converts an AfterTool hook panic into a "no replacement text"
// result so the tool call's own outcome survives a broken hook.
func afterToolSafe(h Hooks, name, result string, err error) (out string) {
	defer func() {
		_ = recover()
	}()
	return h.AfterTool(name, result, err)
}
