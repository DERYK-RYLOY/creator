// Package policy classifies shell commands before they run.
//
// Creator parses the command into a shell AST (via mvdan.cc/sh) instead of
// matching regular expressions, so quoting, pipelines, substitutions and
// redirections are understood structurally. The resulting tier drives whether a
// command runs unattended, needs a confirmation, or is refused.
package policy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Mode selects how much Creator may run without asking.
type Mode string

const (
	// ModeStrict prompts for anything that is not read-only.
	ModeStrict Mode = "strict"
	// ModeBalanced runs safe and moderate commands, gates critical ones.
	ModeBalanced Mode = "balanced"
	// ModeYolo runs everything it is allowed to run.
	ModeYolo Mode = "yolo"
)

// Tier is a risk classification.
type Tier int

const (
	TierSafe Tier = iota
	TierModerate
	TierCritical
)

func (t Tier) String() string {
	switch t {
	case TierSafe:
		return "safe"
	case TierModerate:
		return "moderate"
	default:
		return "critical"
	}
}

// Verdict is the classification of one command string.
type Verdict struct {
	Tier     Tier
	Reasons  []string
	Commands []string
	Dynamic  bool
}

// Decision is what the engine permits.
type Decision string

const (
	Allow   Decision = "allow"
	Confirm Decision = "confirm"
	Deny    Decision = "deny"
)

// Engine applies a Mode to verdicts and can prompt the user.
type Engine struct {
	Mode          Mode
	AllowCritical bool
	Interactive   bool
	In            io.Reader
	Out           io.Writer
	reader        *bufio.Reader // persistent: one per engine, so buffered answers reach later prompts
	// Shell is the name of the interpreter commands will actually run under
	// (bash, powershell, cmd, ...). It only shapes diagnostics: classification
	// always parses bash because most agent-written commands are bash-shaped.
	Shell string
	// Log, when set, receives every verdict for auditing.
	Log func(cmd string, v Verdict, d Decision)
}

// NewEngine builds an engine for a mode.
func NewEngine(mode string, allowCritical, interactive bool, in io.Reader, out io.Writer) *Engine {
	m := ModeBalanced
	switch Mode(strings.ToLower(mode)) {
	case ModeStrict:
		m = ModeStrict
	case ModeYolo:
		m = ModeYolo
	case ModeBalanced, "":
		m = ModeBalanced
	}
	return &Engine{
		Mode:          m,
		AllowCritical: allowCritical,
		Interactive:   interactive,
		In:            in,
		Out:           out,
	}
}

// Authorize classifies cmd and enforces the engine's policy. It returns a
// non-nil error when the command must not run.
func (e *Engine) Authorize(ctx context.Context, cmd string) (Verdict, error) {
	v := Classify(cmd)
	decision := e.Decide(v)
	if e.Log != nil {
		e.Log(cmd, v, decision)
	}
	switch decision {
	case Allow:
		return v, nil
	case Confirm:
		if !e.Interactive {
			return v, fmt.Errorf(
				"refusing %s command in a non-interactive session: %s\nhint: rerun with --allow-critical or --policy yolo if this is intended",
				v.Tier, describe(v))
		}
		ok, err := e.prompt(cmd, v)
		if err != nil {
			return v, err
		}
		if !ok {
			return v, errors.New("command declined by user")
		}
		return v, nil
	default:
		reasons := describe(v)
		return v, fmt.Errorf("blocked %s command: %s", v.Tier, reasons)
	}
}

// Decide applies the mode without prompting.
func (e *Engine) Decide(v Verdict) Decision {
	if e.Mode == ModeYolo {
		return Allow
	}
	switch v.Tier {
	case TierSafe:
		return Allow
	case TierModerate:
		if e.Mode == ModeStrict {
			return Confirm
		}
		return Allow
	default:
		if e.AllowCritical {
			return Allow
		}
		return Deny
	}
}

func describe(v Verdict) string {
	var parts []string
	parts = append(parts, v.Reasons...)
	if len(parts) == 0 {
		parts = append(parts, "policy match")
	}
	return strings.Join(parts, "; ")
}

func (e *Engine) prompt(cmd string, v Verdict) (bool, error) {
	// A fresh bufio per prompt would buffer ahead on a pipe and swallow the
	// answers destined for later prompts; keep one reader for the engine.
	if e.reader == nil {
		e.reader = bufio.NewReader(e.In)
	}
	reader := e.reader
	if e.Out != nil {
		fmt.Fprintf(e.Out, "\n  creator blocks this %s command\n  $ %s\n  why: %s\n  run it? [y/N] ",
			v.Tier, truncateCmd(cmd), describe(v))
	}
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func truncateCmd(s string) string {
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	if r := []rune(s); len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return s
}

// canonicalDangerPath normalizes a path for the dangerous-path checks before
// they compare. Symlinks are resolved when possible, the path is cleaned
// (“/x/../y“ → “/y“), separators are normalized, the extended-length
// prefix (“\\?\“) is stripped, and drive letters are lowercased.
// cleanTextPath collapses redundant separators and resolves "." and ".."
// textually, preserving UNC roots (//server/share) and leading POSIX
// slashes. It deliberately avoids filepath (which is OS-specific and would
// leave Windows paths unrecognized on Linux).
func cleanTextPath(p string) string {
	abs := strings.HasPrefix(p, "/")
	unc := false
	if strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "///") &&
		!strings.HasPrefix(p, "//?") && !strings.HasPrefix(p, "//.") {
		unc = true
		p = p[2:]
		abs = false
	}
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' })
	out := []string{}
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if n := len(out); n > 0 && out[n-1] != ".." {
				out = out[:n-1]
				continue
			}
			if !abs && !unc {
				out = append(out, "..")
			}
			continue
		default:
			out = append(out, part)
		}
	}
	res := strings.Join(out, "/")
	switch {
	case unc:
		if res == "" {
			return "//"
		}
		return "//" + res
	case abs:
		return "/" + res
	default:
		return res
	}
}

func canonicalDangerPath(p string) string {
	t := strings.TrimSpace(p)
	if t == "" {
		return ""
	}
	// Resolve symlinks where the path exists; fall back to the textual
	// normalization below when it doesn't.
	if resolved, err := filepath.EvalSymlinks(t); err == nil {
		t = resolved
	}
	// Normalize separators first so Windows paths classify identically on
	// every host OS; then clean textually (filepath is OS-specific).
	t = strings.ReplaceAll(t, `\`, "/")
	t = strings.TrimPrefix(t, `//?/`)
	t = strings.TrimPrefix(t, `//./`)
	t = cleanTextPath(t)
	t = strings.ToLower(t)
	return strings.TrimSuffix(t, "/")
}

// ---------------------------------------------------------------------------
// Classification tables
// ---------------------------------------------------------------------------

var safeCommands = map[string]bool{
	"ls": true, "dir": true, "pwd": true, "cd": true, "echo": true, "cat": true,
	"head": true, "tail": true, "wc": true, "grep": true, "egrep": true, "rg": true,
	"find": true, "fd": true, "tree": true, "stat": true, "file": true, "du": true,
	"df": true, "date": true, "whoami": true, "hostname": true, "uname": true,
	"env": true, "printenv": true, "which": true, "where": true, "type": true,
	"go": true, "node": true, "python": true, "python3": true, "cargo": true,
	"rustc": true, "javac": true, "java": true, "dotnet": true, "mvn": true,
	"gradle": true, "pytest": true, "make": true, "cmake": true, "tsc": true,
	"eslint": true, "prettier": true, "gofmt": true, "goimports": true,
	"git":  true, // filtered further below
	"diff": true, "sort": true, "uniq": true, "cut": true, "jq": true,
	"sed": true, // filtered: -i escalates
	"awk": true, "basename": true, "dirname": true, "realpath": true,
	"sha256sum": true, "shasum": true, "md5sum": true, "cksum": true,
	"sleep": true, "true": true, "test": true, "[": true, "printf": true,
	"tar": true, "unzip": true, "zip": true, "less": true, "more": true,
}

var moderateCommands = map[string]bool{
	"rm": true, "rmdir": true, "mv": true, "cp": true, "touch": true,
	"mkdir": true, "chmod": true, "chown": true, "chgrp": true, "ln": true,
	"truncate": true, "tee": true, "kill": true, "pkill": true, "killall": true,
	"taskkill": true, "curl": true, "wget": true, "ssh": true, "scp": true,
	"rsync": true, "docker": true, "podman": true, "kubectl": true, "helm": true,
	"systemctl": true, "service": true, "launchctl": true, "brew": true,
	"apt": true, "apt-get": true, "yum": true, "dnf": true, "pacman": true,
	"choco": true, "winget": true, "scoop": true, "pip": true, "pip3": true,
	"gem": true, "composer": true, "sudo": true, "doas": true, "su": true,
	"patch": true, "npm": true, "pnpm": true, "yarn": true, "bun": true, "deno": true,
}

var criticalCommands = map[string]bool{
	"mkfs": true, "mkfs.ext4": true, "mkfs.xfs": true, "wipefs": true,
	"shred": true, "fdisk": true, "parted": true, "diskpart": true,
	"bcdedit": true, "shutdown": true, "reboot": true, "halt": true,
	"poweroff": true, "init": true, "format": true, "cipher": true,
	"dd": true, "reg": true, "netsh": true, "iptables": true, "nft": true,
}

// Classify inspects a command string and returns a verdict, assuming a
// bash-compatible target shell for diagnostics.
func Classify(command string) Verdict {
	return classifyShell(command, "")
}

// inlineEvalFlags maps interpreters to the flags that carry an inline code
// payload the shell AST cannot verify. These invocations are never
// safe-by-default: strict mode prompts, and the reasons say why.
var inlineEvalFlags = map[string][]string{
	"python": {"-c"}, "python2": {"-c"}, "python3": {"-c"}, "pypy": {"-c"}, "pypy3": {"-c"},
	"node": {"-e", "--eval", "-p", "--print"},
	"deno": {"-e", "--eval", "--print"},
	"bun":  {"-e", "--eval"},
	"perl": {"-e"}, "ruby": {"-e"}, "php": {"-r"}, "lua": {"-e"},
}

// classifyShell is Classify with shell-aware diagnostics: when the command
// cannot be parsed as bash and the runtime shell is something else (PowerShell,
// cmd), the reason says so instead of leaving a mystery failure for the model.
func classifyShell(command, shell string) Verdict {
	return classifyShellWithDepth(command, shell, 0)
}

// classifyShellWithDepth is classifyShell with a nesting depth. The engine
// uses it when it re-parses a payload nested inside the command it is
// deciding on (the string after sh -c, an eval argument, …); depth is capped
// so adversarial nesting terminates instead of recursing forever.
func classifyShellWithDepth(command, shell string, depth int) Verdict {
	v := Verdict{Tier: TierSafe}
	command = strings.TrimSpace(command)
	if command == "" {
		v.Tier = TierModerate
		v.Reasons = append(v.Reasons, "empty command")
		return v
	}

	lower := strings.ToLower(command)
	if forkBomb(lower) {
		v.Tier = TierCritical
		v.Reasons = append(v.Reasons, "fork bomb pattern")
	}

	parser := syntax.NewParser(syntax.Variant(syntax.LangBash), syntax.KeepComments(false))
	file, err := parser.Parse(strings.NewReader(command), "<cmd>")
	if err != nil {
		// Fail closed for bash-like shells: an unanalyzable command must
		// never run unattended. For other shells the bash-grammar parser
		// rejects valid native syntax, so extend only the ordinary caution.
		switch shell {
		case "", "bash", "sh", "zsh", "dash", "ksh", "ash":
			v.Tier = maxTier(v.Tier, TierCritical)
			v.Reasons = append(v.Reasons, "command could not be parsed as a shell program; refusing to certify it safe")
			return v
		}
		v.Tier = maxTier(v.Tier, TierModerate)
		switch shell {
		case "", "bash", "sh", "zsh":
			v.Reasons = append(v.Reasons, "command could not be parsed as a shell program")
		default:
			v.Reasons = append(v.Reasons,
				fmt.Sprintf("command could not be parsed as a shell program (bash form; the active shell is %s, so bash-only syntax like «for i in $(seq 1 10); do ...; done» will fail there)", shell))
		}
		return v
	}

	hasSubst := false
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.CmdSubst:
			hasSubst = true
		case *syntax.ProcSubst:
			hasSubst = true
		case *syntax.CallExpr:
			args := literalArgs(n)
			if len(args) == 0 {
				if len(n.Assigns) > 0 {
					// A bare assignment (FOO=bar): mutates env, never runs a
					// command. Moderate — not a computed command name.
					v.Tier = maxTier(v.Tier, TierModerate)
					v.Reasons = append(v.Reasons, "assignment only (no command runs)")
					return true
				}
				// Purely dynamic command name, e.g. `$CMD`.
				v.Tier = maxTier(v.Tier, TierCritical)
				v.Reasons = append(v.Reasons, "command name is computed at runtime")
				return true
			}
			v.Commands = append(v.Commands, args[0])
			classifyCall(&v, args, n, depth)
		case *syntax.Redirect:
			classifyRedirect(&v, n)
		case *syntax.BinaryCmd:
			if n.Op == syntax.Pipe {
				classifyPipeline(&v, n)
			}
		}
		return true
	})
	if hasSubst {
		v.Dynamic = true
		v.Tier = maxTier(v.Tier, TierModerate)
		v.Reasons = append(v.Reasons, "contains command substitution")
	}
	if v.Tier > TierSafe {
		v.Reasons = dedupe(v.Reasons)
	}
	return v
}

func classifyCall(v *Verdict, args []string, call *syntax.CallExpr, depth int) {
	// Lowercase first, then strip executable suffixes to a fixpoint so
	// "FORMAT.EXE", "RM.BAT", "DISKPART.PS1" and mixed-case combos match
	// the command tables (the old code trimmed ".exe" before lowercasing,
	// so "FORMAT.EXE" matched nothing; ".bat"/".ps1"/".com" never matched).
	base := filepath.Base(args[0])
	name := strings.ToLower(base)
	for changed := true; changed; {
		changed = false
		for _, ext := range []string{".exe", ".com", ".scr", ".pif", ".cmd", ".bat", ".ps1"} {
			if strings.HasSuffix(name, ext) {
				name = strings.TrimSuffix(name, ext)
				changed = true
			}
		}
	}
	words := argWords(call)

	if len(call.Assigns) > 0 {
		// `FOO=bar cmd` can rewrite the environment of a destructive call.
		v.Tier = maxTier(v.Tier, TierModerate)
	}

	rest := args[1:]

	// Dangerous even when the binary is normally harmless.
	// Interpreters with inline-code flags carry a payload the shell AST
	// cannot verify; they are never safe-by-default.
	if flags := inlineEvalFlags[name]; len(flags) > 0 {
		for _, a := range rest {
			la := strings.ToLower(a)
			for _, f := range flags {
				if la == f || strings.HasPrefix(la, f+"=") {
					v.Tier = maxTier(v.Tier, TierModerate)
					v.Reasons = append(v.Reasons, name+" runs inline code via "+f+"; payload is not inspectable")
					break
				}
			}
		}
	}

	if criticalCommands[name] {
		v.Tier = maxTier(v.Tier, TierCritical)
		v.Reasons = append(v.Reasons, "uses "+name+" which can destroy data or the system")
		return
	}

	switch name {
	case "rm", "rmdir", "unlink", "shred", "rd", "del", "erase", "remove-item":
		classifyDelete(v, name, words)
	case "dd":
		for _, a := range rest {
			if strings.HasPrefix(a, "of=/dev/") {
				v.Tier = maxTier(v.Tier, TierCritical)
				v.Reasons = append(v.Reasons, "writes raw device node")
				return
			}
		}
		v.Tier = maxTier(v.Tier, TierModerate)
	case "base64", "base32", "base16", "uudecode":
		// Precise check on the actual command and its own flags — the old
		// whole-command substring scan ("base64" + "-d" anywhere) fired on
		// unrelated commands.
		decodes := name == "uudecode" // uudecode has no non-decoding mode
		for _, a := range rest {
			la := strings.ToLower(strings.TrimSpace(a))
			switch {
			case la == "-d" || la == "--decode" || la == "-di":
				decodes = true
			case strings.HasPrefix(la, "--decode="):
				decodes = true
			case strings.HasPrefix(la, "-") && !strings.HasPrefix(la, "--") && strings.ContainsRune(la, 'd'):
				decodes = true // bundled short flags, e.g. -di (decode + ignore-garbage)
			}
		}
		if decodes {
			v.Tier = maxTier(v.Tier, TierModerate)
			v.Reasons = append(v.Reasons, "decodes base64/encoded content")
		}
	case "git":
		classifyGit(v, rest)
	case "sudo", "doas", "su":
		v.Tier = maxTier(v.Tier, TierCritical)
		v.Reasons = append(v.Reasons, "requests elevated privileges")
	case "chmod", "chown", "chgrp":
		words := argWords(call)
		recursive, _, literalTargets, unverifiable := splitDeleteArgs(name, words[1:])
		danger := ""
		for _, t := range literalTargets {
			if dangerousPath(t) {
				danger = t
				break
			}
		}
		permissive := chmodModePermissive(rest)
		switch {
		case recursive && danger != "":
			v.Tier = maxTier(v.Tier, TierCritical)
			v.Reasons = append(v.Reasons, "recursive permission change at protected path "+danger)
			return
		case recursive && len(unverifiable) > 0:
			v.Tier = maxTier(v.Tier, TierCritical)
			v.Reasons = append(v.Reasons, "recursive permission change of unverifiable target(s): "+strings.Join(unverifiable, ", "))
			return
		case danger != "" && permissive:
			v.Tier = maxTier(v.Tier, TierCritical)
			v.Reasons = append(v.Reasons, "changes permissions insecurely at "+danger)
			return
		}
		v.Tier = maxTier(v.Tier, TierModerate)
		v.Reasons = append(v.Reasons, "changes permissions")
	case "sed":
		if hasAny(rest, "-i") || hasAny(rest, "--in-place") {
			v.Tier = maxTier(v.Tier, TierModerate)
			v.Reasons = append(v.Reasons, "edits files in place")
		}
	case "find":
		joined := strings.Join(rest, " ")
		findRoot := func() string {
			for _, a := range rest {
				if a == "-H" || a == "-L" || a == "-P" {
					continue
				}
				if strings.HasPrefix(a, "-") {
					return ""
				}
				return a
			}
			return ""
		}()
		switch {
		case strings.Contains(joined, "-delete"):
			if findRoot != "" && dangerousDeletePath(findRoot) {
				v.Tier = maxTier(v.Tier, TierCritical)
				v.Reasons = append(v.Reasons, "find -delete on protected path "+findRoot)
			} else {
				v.Tier = maxTier(v.Tier, TierModerate)
				v.Reasons = append(v.Reasons, "deletes matched files")
			}
		case strings.Contains(joined, "-exec"), strings.Contains(joined, "-execdir"):
			payload := findExecPayload(rest)
			if len(payload) > 0 {
				sub := classifyShellWithDepth(quotePayloadArgs(payload), "", depth+1)
				if sub.Tier > v.Tier {
					v.Tier = sub.Tier
				}
				for _, r := range sub.Reasons {
					v.Reasons = append(v.Reasons, "find -exec: "+r)
				}
				if findRoot != "" && dangerousDeletePath(findRoot) {
					v.Tier = maxTier(v.Tier, TierCritical)
					v.Reasons = append(v.Reasons, "find -exec on protected path "+findRoot)
				}
				if sub.Dynamic {
					v.Dynamic = true
				}
			} else {
				v.Tier = maxTier(v.Tier, TierModerate)
				v.Reasons = append(v.Reasons, "executes commands on matches")
			}
		}
	case "xargs":
		// xargs re-executes its inner command once per stdin item; the inner
		// command must be classified like any other. Targets pulled from
		// stdin are unverifiable — a bare delete verb gets its targets from
		// stdin, which we cannot audit, so it is always critical.
		toks := xargsInnerTokens(rest)
		if len(toks) == 0 {
			v.Tier = maxTier(v.Tier, TierModerate)
			v.Reasons = append(v.Reasons, "xargs with no verifiable inner command")
			return
		}
		switch toks[0] {
		case "rm", "rmdir", "shred", "unlink", "del", "erase", "rd":
			// A delete verb with no inline targets gets them from stdin,
			// which can never be inspected — always critical.
			hasNonFlagTarget := false
			for _, t := range toks[1:] {
				if !strings.HasPrefix(t, "-") {
					hasNonFlagTarget = true
					break
				}
			}
			if !hasNonFlagTarget {
				v.Tier = maxTier(v.Tier, TierCritical)
				v.Reasons = append(v.Reasons, "xargs feeds unverifiable stdin targets to "+toks[0])
				return
			}
		}
		sub := classifyShellWithDepth(quotePayloadArgs(toks), "", depth+1)
		if sub.Tier > v.Tier {
			v.Tier = sub.Tier
		}
		for _, r := range sub.Reasons {
			v.Reasons = append(v.Reasons, "xargs: "+r)
		}
		if sub.Dynamic {
			v.Dynamic = true
		}
	case "npm", "pnpm", "yarn":
		sub := ""
		if len(rest) > 0 {
			sub = strings.ToLower(rest[0])
		}
		switch sub {
		case "install", "i", "add", "remove", "uninstall", "publish", "update", "upgrade", "link":
			v.Tier = maxTier(v.Tier, TierModerate)
			v.Reasons = append(v.Reasons, "changes dependencies or publishes")
		case "exec", "dlx":
			v.Tier = maxTier(v.Tier, TierModerate)
			v.Reasons = append(v.Reasons, "runs arbitrary remote packages")
		}
	case "go":
		if len(rest) > 0 {
			switch strings.ToLower(rest[0]) {
			case "install", "get", "clean":
				v.Tier = maxTier(v.Tier, TierModerate)
				v.Reasons = append(v.Reasons, "mutates the module or install cache")
			}
		}
	case "cargo":
		if len(rest) > 0 && strings.ToLower(rest[0]) == "install" {
			v.Tier = maxTier(v.Tier, TierModerate)
			v.Reasons = append(v.Reasons, "installs binaries")
		}
	case "pip", "pip3":
		if len(rest) > 0 {
			switch strings.ToLower(rest[0]) {
			case "install", "uninstall":
				v.Tier = maxTier(v.Tier, TierModerate)
				v.Reasons = append(v.Reasons, "changes Python packages")
			}
		}
	case "powershell", "pwsh":
		for _, a := range rest {
			la := strings.ToLower(a)
			// Exact parameter forms for an encoded payload — prefix matching
			// used to swallow -ErrorAction, -Encoding, -ea, etc.
			switch la {
			case "-e", "-ec", "-en", "-enc", "-encodedcommand", "-encodedcommandfile":
				v.Tier = maxTier(v.Tier, TierCritical)
				v.Reasons = append(v.Reasons, "runs an encoded PowerShell payload")
				return
			}
			if strings.Contains(la, "encodedcommand") {
				v.Tier = maxTier(v.Tier, TierCritical)
				v.Reasons = append(v.Reasons, "runs an encoded PowerShell payload")
				return
			}
		}
		joined := strings.ToLower(strings.Join(rest, " "))
		if strings.Contains(joined, "invoke-expression") || strings.Contains(joined, "iex ") || strings.Contains(joined, "downloadstring") {
			v.Tier = maxTier(v.Tier, TierCritical)
			v.Reasons = append(v.Reasons, "downloads and evaluates code")
			return
		}
		v.Tier = maxTier(v.Tier, TierModerate)
	case "sh", "bash", "zsh", "dash", "fish", "ksh", "ash", "csh":
		// `sh -c '<payload>'` (combined short flags like -lc included): the
		// payload is a command line of its own and gets its own verdict.
		reclassifyShellPayload(v, rest, depth)
	case "eval":
		// eval concatenates its arguments into one command line.
		classifyNestedPayload(v, strings.Join(rest, " "), depth)
	case "command", "builtin", "exec":
		// ARGV array: re-emit each argument shell-quoted so the payload
		// parses back into exactly the same words.
		classifyNestedPayload(v, quotePayloadArgs(rest), depth)
	case "env":
		i := skipEnvPrefix(rest)
		classifyNestedPayload(v, quotePayloadArgs(rest[i:]), depth)
	case "timeout", "nice", "nohup", "stdbuf", "ionice":
		// Wrappers run another command (ARGV array); verify the wrapped
		// command too.
		i := skipWrapperPrefix(name, rest)
		classifyNestedPayload(v, quotePayloadArgs(rest[i:]), depth)
	}

	if moderateCommands[name] {
		v.Tier = maxTier(v.Tier, TierModerate)
		v.Reasons = append(v.Reasons, "command can modify state or reach the network")
	}
	if name == "" {
		v.Tier = maxTier(v.Tier, TierCritical)
		v.Reasons = append(v.Reasons, "empty command name")
	}
	if v.Tier == TierSafe && !safeCommands[name] && !wrapperCommands[name] {
		// Fail closed: a command we don't recognize is not safe by default.
		// Strict mode prompts; balanced treats it as moderate.
		v.Tier = TierModerate
		v.Reasons = append(v.Reasons, "unrecognized command; defaulting to caution")
	}
}

// wrapperCommands are inert when their payload verifies safe — the safety
// was established by classifying the wrapped command, so the unrecognized
// command catch-all must not re-flag them. Shells are deliberately absent:
// they are never provably inert.
var wrapperCommands = map[string]bool{
	"command": true, "builtin": true, "exec": true,
	"env": true, "timeout": true, "nice": true,
	"nohup": true, "stdbuf": true, "ionice": true, "xargs": true,
}

// xargsInnerTokens skips xargs' leading flags and returns the inner
// command and its arguments. -i/-I/-n/-P/-s/-d/-E and -a consume a separate
// operand; every other leading -token is treated as a self-contained flag.
func xargsInnerTokens(rest []string) []string {
	twoOperand := map[string]bool{"-i": true, "-I": true, "-n": true, "-P": true, "-s": true, "-d": true, "-E": true, "-a": true}
	i := 0
	for i < len(rest) {
		a := rest[i]
		if a == "--" {
			return rest[i+1:]
		}
		if !strings.HasPrefix(a, "-") {
			break
		}
		if twoOperand[a] && i+1 < len(rest) {
			i += 2
			continue
		}
		i++
	}
	return rest[i:]
}

// findExecPayload extracts the command between -exec (or -execdir) and the
// `;` terminator. Returns nil when not present.
func findExecPayload(rest []string) []string {
	for i := 0; i < len(rest); i++ {
		if rest[i] == "-exec" || rest[i] == "-execdir" {
			for j := i + 1; j < len(rest); j++ {
				if rest[j] == ";" || rest[j] == `\;` {
					return rest[i+1 : j]
				}
			}
			return rest[i+1:]
		}
	}
	return nil
}

// maxNestedDepth caps how deeply the engine re-parses nested payloads
// (sh -c 'sh -c ...') so adversarial nesting terminates.
const maxNestedDepth = 8

// classifyNestedPayload re-parses a nested command line (the payload of
// `sh -c …`, an eval argument, a wrapper's trailing command) with the same
// engine and merges the verdict into v. An unparseable payload comes back
// Moderate from the engine, so nesting never lowers a tier.
func classifyNestedPayload(v *Verdict, payload string, depth int) {
	if strings.TrimSpace(payload) == "" {
		v.Tier = maxTier(v.Tier, TierModerate)
		v.Reasons = append(v.Reasons, "wrapper command with no payload to run")
		return
	}
	if depth >= maxNestedDepth {
		v.Tier = maxTier(v.Tier, TierModerate)
		v.Reasons = append(v.Reasons, "nested commands deeper than 8 levels; refusing to certify safe")
		return
	}
	sub := classifyShellWithDepth(payload, "", depth+1)
	if sub.Tier > v.Tier {
		v.Tier = sub.Tier
	}
	for _, r := range sub.Reasons {
		v.Reasons = append(v.Reasons, "nested shell: "+r)
	}
	if sub.Dynamic {
		v.Dynamic = true
	}
}

// reclassifyShellPayload finds a shell's inline payload (`sh -c '…'`,
// `bash -lc '…'`) and verifies it with the same engine. A shell with no
// verifiable inline payload — a script file, or the interactive shell — is
// at least Moderate.
func reclassifyShellPayload(v *Verdict, rest []string, depth int) {
	for i, a := range rest {
		la := strings.ToLower(a)
		if a == "-c" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsRune(la, 'c')) {
			if i+1 < len(rest) {
				// The payload is exactly one argument; anything after it is
				// positional parameters, not more script.
				classifyNestedPayload(v, rest[i+1], depth)
				// A shell is never provably inert — it may source startup
				// files, expand aliases and functions. The payload verdict
				// tightens what runs, but the floor stays moderate.
				v.Tier = maxTier(v.Tier, TierModerate)
				return
			}
			v.Tier = maxTier(v.Tier, TierModerate)
			v.Reasons = append(v.Reasons, "shell -c with a non-literal payload")
			return
		}
	}
	v.Tier = maxTier(v.Tier, TierModerate)
	v.Reasons = append(v.Reasons, "shell invocation with no verifiable inline payload")
}

// skipEnvPrefix returns the index in rest where env's command word begins,
// skipping leading options (operand-consuming forms like -u NAME included)
// and VAR=value assignments.
func skipEnvPrefix(rest []string) int {
	i := 0
	for i < len(rest) {
		a := rest[i]
		if a == "--" {
			return i + 1
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			switch a {
			case "-u", "-C", "-S", "-a":
				if i+1 < len(rest) {
					i += 2
					continue
				}
			}
			// attached operand forms: -uNAME, -C DIR is the two-arg one above,
			// -uNAME and friends are single tokens.
			if len(a) > 2 && (strings.HasPrefix(a, "-u") || strings.HasPrefix(a, "-C") || strings.HasPrefix(a, "-S") || strings.HasPrefix(a, "-a")) {
				i++
				continue
			}
			i++
			continue
		}
		if isAssignmentWord(a) {
			i++
			continue
		}
		break
	}
	return i
}

// skipWrapperPrefix skips leading options of a wrapper command (timeout,
// nice, nohup, stdbuf, ionice). For timeout the first operand is the
// duration, so it is consumed as well. Returns the index where the wrapped
// command's word begins; if there is nothing left it returns len(rest).
func skipWrapperPrefix(name string, rest []string) int {
	i := 0
	twoOperand := map[string]bool{"-k": true, "-s": true, "-n": true, "-i": true, "-o": true, "-e": true, "-c": true}
	for i < len(rest) {
		a := rest[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			if twoOperand[a] && i+1 < len(rest) {
				i += 2
				continue
			}
			i++
			continue
		}
		if name == "timeout" {
			i++ // duration operand
		}
		break
	}
	return i
}

// isAssignmentWord reports whether a looks like VAR=value.
func isAssignmentWord(a string) bool {
	eq := strings.IndexByte(a, '=')
	if eq <= 0 {
		return false
	}
	for j := 0; j < eq; j++ {
		r := a[j]
		alnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (j > 0 && r >= '0' && r <= '9')
		if !alnum && r != '_' {
			return false
		}
	}
	return true
}

// shellQuote re-emits one ARGV element single-quoted so re-parsing it with
// the bash parser returns the element byte-for-byte. Used to verify wrapper
// commands (env, timeout, nice, …) whose payloads are argument vectors, not
// command-line strings.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, `'`, `'\''`) + "'"
}

// quotePayloadArgs shell-quotes each argument and joins them into one parseable command line.
func quotePayloadArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

func classifyGit(v *Verdict, rest []string) {
	// git's leading options may consume a separate value; treating that value
	// as the subcommand hid destructive operations behind option parsing
	// (`git -C /repo push --force`). Skip option/value pairs properly.
	valueTaking := map[string]bool{
		"-C": true, "-c": true,
		"--git-dir": true, "--work-tree": true, "--namespace": true,
		"--exec-path": true, "--html-path": true, "--man-path": true,
		"--info-path": true, "--attr-source": true,
	}
	sub := ""
	subIdx := -1
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if !strings.HasPrefix(a, "-") {
			sub = strings.ToLower(a)
			subIdx = i
			break
		}
		if strings.Contains(a, "=") {
			// --opt=value is self-contained.
			continue
		}
		if valueTaking[a] && i+1 < len(rest) {
			if strings.EqualFold(a, "-c") {
				// `git -c key=value` config entries are part of this
				// invocation — exec-sink keys must escalate, not be skipped.
				scanGitConfigKeyValue(v, rest[i+1])
			}
			i++ // skip the option's value
			continue
		}
		// boolean option: nothing more to skip
	}
	// Guard the attached -c form too (`git -ckey=value`).
	end := subIdx
	if end < 0 {
		end = len(rest)
	}
	for _, a := range rest[:end] {
		la := strings.ToLower(a)
		if strings.HasPrefix(la, "-c") && len(a) > 3 && strings.Contains(a, "=") {
			scanGitConfigKeyValue(v, a[2:])
		}
	}
	switch sub {
	case "", "status", "diff", "log", "show", "blame", "branch", "remote", "rev-parse", "describe", "tag", "ls-files":
		return
	case "config":
		// A bare `config` subcommand is not inherently safe: exec-sink keys
		// (core.sshCommand & friends) run arbitrary programs on the next op.
		if subIdx >= 0 && subIdx+1 < len(rest) {
			classifyGitConfig(v, rest[subIdx+1:])
		}
		return
	case "push":
		// Match force indicators as whole tokens or token prefixes. The old
		// substring probe flagged ordinary branches (fix-foo, my-fix) and
		// --follow-tags as force-pushes, denying them in every default policy.
		force := false
		for _, a := range rest {
			la := strings.ToLower(a)
			switch {
			case strings.HasPrefix(la, "+"): // force refspec: +main:main
				force = true
			case strings.HasPrefix(la, "--force"), strings.HasPrefix(la, "--mirror"):
				force = true
			case strings.HasPrefix(la, "-") && !strings.HasPrefix(la, "--") && strings.ContainsRune(la, 'f'):
				force = true // short bundle such as -f or -fu
			}
			if force {
				break
			}
		}
		if force {
			v.Tier = maxTier(v.Tier, TierCritical)
			v.Reasons = append(v.Reasons, "force-pushes remote history")
			return
		}
		v.Tier = maxTier(v.Tier, TierModerate)
		v.Reasons = append(v.Reasons, "pushes to a remote")
	case "reset":
		if hasAny(rest, "--hard") {
			v.Tier = maxTier(v.Tier, TierModerate)
			v.Reasons = append(v.Reasons, "discards working tree changes")
		}
	case "clean":
		v.Tier = maxTier(v.Tier, TierModerate)
		v.Reasons = append(v.Reasons, "deletes untracked files")
	case "checkout", "switch", "restore", "stash", "merge", "rebase", "cherry-pick", "revert", "commit", "add", "rm", "mv", "apply", "am", "submodule":
		v.Tier = maxTier(v.Tier, TierModerate)
		v.Reasons = append(v.Reasons, "mutates the repository")
	}
}

// gitConfigSink reports whether key names a git configuration entry whose
// value the git binary executes (or delegates to) on its next operation:
// hook injection and friends. Writing to these keys — even "harmlessly" —
// installs an arbitrary command into the repo.
func gitConfigSink(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	switch k {
	case "core.sshcommand", "core.askpass", "core.gitproxy",
		"core.pager", "core.editor", "editor", "sequence.editor",
		"core.sequenceeditor", "core.hookspath", "core.fsmonitor",
		"credential.helper", "diff.external", "diff.astextplain",
		"merge.guitool", "gpg.program":
		return true
	}
	for _, p := range []string{"filter.", "pager.", "alias.", "diff.", "difftool.", "mergetool.", "credential."} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// scanGitConfigKeyValue escalates an exec-sink `git -c key=value` entry.
func scanGitConfigKeyValue(v *Verdict, kv string) {
	key, _, _ := strings.Cut(kv, "=")
	if key == "" {
		return
	}
	if gitConfigSink(key) {
		v.Tier = maxTier(v.Tier, TierCritical)
		v.Reasons = append(v.Reasons, "git -c sets exec-sink key "+strings.ToLower(key)+" (hook injection)")
	}
}

// classifyGitConfig handles the arguments of `git config` (after the
// subcommand). Reads stay safe; writes to non-sink keys are moderate; writes
// to exec-sink keys fail closed to critical.
func classifyGitConfig(v *Verdict, args []string) {
	mutating := false
	read := false
	unset := false
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		la := strings.ToLower(a)
		switch {
		case la == "--get" || la == "--list" || la == "-l" || la == "--get-all" || la == "--get-regexp" || la == "--get-urlmatch" || la == "--name-only":
			read = true
		case la == "--add" || la == "--replace-all":
			mutating = true
		case la == "--unset" || la == "--unset-all":
			mutating = true // removal is a write, but never installs a program
			unset = true
		case la == "--remove-section" || la == "--rename-section":
			mutating = true
		case la == "-f" || la == "--file":
			i++ // value flag: skip the file path
			continue
		case strings.HasPrefix(la, "--file="):
			continue
		case strings.HasPrefix(a, "-"):
			// --global, --local, --system, --worktree, --type, -z, ... : harmless
			continue
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) == 0 {
		// No key named: `git config --list` style — nothing to set.
		return
	}
	key := positional[0]
	writing := mutating || (!read && len(positional) >= 2)
	if !writing {
		return // bare read of one key
	}
	if gitConfigSink(key) {
		// Clearing a sink (`--unset`, empty value) removes rather than
		// installs — moderate, never critical.
		if unset || (len(positional) >= 2 && positional[1] == "") {
			v.Tier = maxTier(v.Tier, TierModerate)
			v.Reasons = append(v.Reasons, "git config clears exec-sink key "+strings.ToLower(key))
			return
		}
		v.Tier = maxTier(v.Tier, TierCritical)
		v.Reasons = append(v.Reasons, "git config sets exec-sink key "+strings.ToLower(key)+" (hook injection)")
		return
	}
	v.Tier = maxTier(v.Tier, TierModerate)
	v.Reasons = append(v.Reasons, "git config rewrites git settings")
}

func classifyRedirect(v *Verdict, r *syntax.Redirect) {
	target := ""
	if r.Word != nil && len(r.Word.Parts) > 0 {
		// A redirect target like > "/etc/passwd" parses as DblQuoted, not a
		// bare Lit — the old probe only understood *syntax.Lit and treated
		// quoted system paths as "computed". literalWord handles all-quoted
		// and mixed Lit/quote words (e.g. /e""tc/passwd), so quoted-but-
		// fully-known targets reach the system-location check.
		if lit, ok := literalWord(r.Word); ok {
			target = lit
		}
	}
	switch r.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrInOut, syntax.RdrClob, syntax.AppClob,
		syntax.RdrAll, syntax.AppAll, syntax.RdrAllClob, syntax.AppAllClob:
		// Formerly only >, >>, &>, &>> were classified — `<>` (read+write
		// truncate) and the explicit clobber forms (>|, >>|, &>|, &>>|) write
		// the same bytes to the same places.
	case syntax.DplOut:
		// `>&2` is an fd reference, never a file write — ignore it. `>&file`
		// writes both streams to a path and is classified below.
		if isFDReference(target) {
			return
		}
	default:
		return
	}
	{
		// A target built from variables or substitutions cannot be inspected;
		// treat it like the rest of the dynamic-input family (moderate).
		if r.Word != nil && len(r.Word.Parts) > 0 {
			// A target built from variables or substitutions cannot be
			// inspected; treat it like the rest of the dynamic-input family.
			if _, ok := literalWord(r.Word); !ok {
				v.Tier = maxTier(v.Tier, TierModerate)
				v.Reasons = append(v.Reasons, "redirect target is computed at runtime")
				return
			}
		}
		lower := canonicalDangerPath(target)
		isRawDisk := strings.HasPrefix(lower, "/dev/sd") || strings.HasPrefix(lower, "/dev/hd") ||
			strings.HasPrefix(lower, "/dev/vd") || strings.HasPrefix(lower, "/dev/nvme") ||
			strings.HasPrefix(lower, "/dev/mmcblk") || strings.HasPrefix(lower, "/dev/loop") ||
			strings.HasPrefix(lower, "/dev/md") || strings.HasPrefix(lower, "/dev/mapper") ||
			strings.HasPrefix(lower, `\\.\physicaldrive`) || strings.HasPrefix(lower, `\\?\physicaldrive`)
		if strings.HasPrefix(lower, "/etc/") || strings.HasPrefix(lower, "/boot/") ||
			isRawDisk || strings.HasPrefix(lower, "c:\\windows") ||
			strings.HasPrefix(lower, "c:/windows") {
			v.Tier = maxTier(v.Tier, TierCritical)
			v.Reasons = append(v.Reasons, "writes into a system location: "+target)
			return
		}
		if target != "" && !strings.HasPrefix(target, "/dev/null") && !strings.HasPrefix(target, "/dev/stderr") && !strings.HasPrefix(target, "/dev/stdout") {
			v.Tier = maxTier(v.Tier, TierModerate)
			v.Reasons = append(v.Reasons, "writes output to "+target)
		}
	}
}

// isFDReference reports whether s is a bare file-descriptor reference used
// by `>&`/`<&` redirection: digits (`>&2`) or `-` (`>&-`, closes the fd).
func isFDReference(s string) bool {
	if s == "-" {
		return true
	}
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// flattenPipe expands a possibly-nested pipeline into its segment command
// names. Pipeline nesting lives on Stmt.Cmd (*syntax.BinaryCmd with
// Op == Pipe); both associativities are handled.
func flattenPipe(stmt *syntax.Stmt, out *[]string) {
	if stmt == nil || stmt.Cmd == nil {
		return
	}
	if bc, ok := stmt.Cmd.(*syntax.BinaryCmd); ok && bc.Op == syntax.Pipe {
		flattenPipe(bc.X, out)
		flattenPipe(bc.Y, out)
		return
	}
	*out = append(*out, firstCommandName(stmt))
}

// classifyPipeline escalates when content from a fetcher or a decoder/
// transform reaches an interpreter anywhere downstream in the pipe —
// not just in an adjacent segment. `curl … | tee f | sh` and
// `iwr … | iex` are the classic gaps.
func classifyPipeline(v *Verdict, bc *syntax.BinaryCmd) {
	var segs []string
	flattenPipe(bc.X, &segs)
	flattenPipe(bc.Y, &segs)
	if len(segs) == 0 {
		return
	}
	fetchers := map[string]bool{"curl": true, "wget": true, "fetch": true, "iwr": true, "irm": true, "invoke-webrequest": true, "invoke-restmethod": true}
	// Decoders and text transforms: piping their output into an interpreter
	// hands the interpreter material whose origin cannot be audited, so it
	// fails closed to critical even without an explicit -d flag.
	decodeTransforms := map[string]bool{
		"base64": true, "base32": true, "base16": true, "xxd": true, "uudecode": true,
		"zcat": true, "gunzip": true, "gzip": true, "xz": true, "unxz": true,
		"lz4": true, "zstd": true, "openssl": true, "gpg": true, "iconv": true,
		"tr": true, "rev": true, "sed": true, "awk": true, "jq": true, "cut": true,
	}
	interpreters := map[string]bool{
		"sh": true, "bash": true, "zsh": true, "dash": true, "fish": true, "ksh": true,
		"pwsh": true, "powershell": true, "cmd": true,
		"iex": true, "invoke-expression": true, "icm": true,
		"python": true, "python2": true, "python3": true, "node": true, "deno": true, "bun": true,
		"perl": true, "ruby": true, "php": true, "lua": true,
	}
	sawFetcher, sawDecoder := false, false
	for _, s := range segs {
		if fetchers[s] {
			sawFetcher = true
		}
		if decodeTransforms[s] {
			sawDecoder = true
		}
	}
	if !sawFetcher && !sawDecoder {
		// echo/printf into an interpreter: generated shell input is
		// at least moderate (unchanged behavior).
		if len(segs) == 2 && interpreters[segs[1]] && (segs[0] == "echo" || segs[0] == "printf") {
			v.Tier = maxTier(v.Tier, TierModerate)
			v.Reasons = append(v.Reasons, "executes generated shell input")
		}
		return
	}
	for _, s := range segs {
		if interpreters[s] {
			v.Tier = maxTier(v.Tier, TierCritical)
			if sawFetcher {
				v.Reasons = append(v.Reasons, "pipes downloaded content into "+s)
			} else {
				v.Reasons = append(v.Reasons, "pipes decoded or transformed content into "+s)
			}
			return
		}
	}
}

func firstCommandName(node syntax.Node) string {
	switch n := node.(type) {
	case *syntax.Stmt:
		if n.Cmd == nil {
			return ""
		}
		return firstCommandName(n.Cmd)
	case *syntax.BinaryCmd:
		return firstCommandName(n.Y)
	case *syntax.CallExpr:
		args := literalArgs(n)
		if len(args) == 0 {
			return ""
		}
		return strings.ToLower(filepath.Base(args[0]))
	}
	return ""
}

// argWord is one argument of a call: either the literal value of a word that
// can be fully reduced to static text, or the raw source text of a computed
// one ($VAR, ${VAR}, "$DIR"/prod, $(cmd), ...). Keeping the two apart lets
// destructive commands fail closed on targets they cannot verify instead of
// silently dropping them from the analysis.
type argWord struct {
	value   string // literal value; empty when computed
	raw     string // source text, for diagnostics
	literal bool
}

func argWords(call *syntax.CallExpr) []argWord {
	out := make([]argWord, 0, len(call.Args))
	for _, w := range call.Args {
		if s, ok := literalWord(w); ok {
			out = append(out, argWord{value: s, raw: s, literal: true})
			continue
		}
		out = append(out, argWord{raw: rawWord(w)})
	}
	return out
}

func rawWord(w *syntax.Word) string {
	var sb strings.Builder
	if err := syntax.NewPrinter().Print(&sb, w); err != nil {
		return "<computed>"
	}
	return sb.String()
}

// classifyDelete handles rm/rmdir/unlink/shred and the Windows/PowerShell
// equivalents (rd, del, erase, Remove-Item). It fails closed: any argument it
// cannot reduce to a literal string is unverifiable, and unverifiable targets
// combined with recursive deletion produce critical instead of moderate.
func classifyDelete(v *Verdict, name string, words []argWord) {
	if len(words) == 0 {
		return
	}
	recursive, force, literalTargets, unverifiable := splitDeleteArgs(name, words[1:])
	kind := "recursive"
	if force {
		kind = "recursive force"
	}
	danger := ""
	for _, t := range literalTargets {
		if dangerousDeletePath(t) {
			danger = t
			break
		}
	}
	switch {
	case recursive && len(unverifiable) > 0:
		v.Tier = maxTier(v.Tier, TierCritical)
		v.Reasons = append(v.Reasons, kind+" delete of unverifiable target(s): "+strings.Join(unverifiable, ", "))
		return
	case recursive && danger != "":
		v.Tier = maxTier(v.Tier, TierCritical)
		v.Reasons = append(v.Reasons, kind+" delete of "+danger)
		return
	case danger != "":
		// A single file at a protected location is as dangerous as it
		// gets without -r — don't require the recursive flag to fire the gate.
		v.Tier = maxTier(v.Tier, TierCritical)
		v.Reasons = append(v.Reasons, "delete of protected path: "+danger)
		return
	case len(unverifiable) > 0:
		// Not necessarily catastrophic in blast radius, but the target cannot
		// be verified, so this must never look safe — and must stay at least
		// moderate with an explicit reason.
		v.Tier = maxTier(v.Tier, TierModerate)
		v.Reasons = append(v.Reasons, "delete target is computed at runtime; cannot verify safety: "+strings.Join(unverifiable, ", "))
	default:
		v.Tier = maxTier(v.Tier, TierModerate)
		v.Reasons = append(v.Reasons, "deletes files")
	}
}

// splitDeleteArgs parses the argument vector of a delete-like command into
// (recursive, force) plus literal targets and the raw text of computed
// arguments. It understands POSIX flags (-r, -rf, --recursive, --force),
// Windows cmd flags (/s, /q, /f) and PowerShell parameter prefixes
// (-Recurse, -Rec, -Force, -Fo, ...).
func splitDeleteArgs(name string, words []argWord) (recursive, force bool, literalTargets, unverifiable []string) {
	windowsSlash := name == "rd" || name == "del" || name == "erase" || name == "rmdir"
	powerShell := name == "remove-item"
	for _, w := range words {
		if !w.literal {
			// A computed word could be a flag or a path; either way it cannot
			// be accounted for, so it is reported as unverifiable.
			unverifiable = append(unverifiable, w.raw)
			continue
		}
		a := strings.ToLower(w.value)
		if windowsSlash && isShortSlashFlag(a) {
			for _, r := range a[1:] {
				switch r {
				case 's':
					recursive = true
				case 'f':
					force = true
				}
			}
			continue
		}
		if powerShell && strings.HasPrefix(a, "-") {
			t := strings.TrimLeft(a, "-")
			if i := strings.Index(t, ":"); i >= 0 {
				t = t[:i] // -Confirm:$false style
			}
			if t != "" && (strings.HasPrefix("recurse", t) || t == "recursive") {
				recursive = true
				continue
			}
			if t != "" && strings.HasPrefix("force", t) {
				force = true
				continue
			}
			continue
		}
		if strings.HasPrefix(a, "--") {
			t := strings.TrimPrefix(a, "--")
			if i := strings.Index(t, "="); i >= 0 {
				t = t[:i]
			}
			switch t {
			case "recursive", "recurse":
				recursive = true
			case "force":
				force = true
			}
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" && a != "--" {
			for _, r := range a[1:] {
				switch r {
				case 'r', 'R':
					recursive = true
				case 'f', 'F':
					force = true
				}
			}
			continue
		}
		if hasGlobMeta(w.value) && !dangerousDeletePath(w.value) {
			// The shell expands this at runtime: the target set is unknown to
			// static analysis — unless it already matched a protected root,
			// in which case the protected-path verdict stands.
			unverifiable = append(unverifiable, w.raw)
			continue
		}
		literalTargets = append(literalTargets, w.value)
	}
	return recursive, force, literalTargets, unverifiable
}

// hasGlobMeta reports whether a word carries shell glob metacharacters
// (glob expansion happens at run time, never statically).
func hasGlobMeta(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// isShortSlashFlag reports whether a is a cmd-style flag bundle such as
// /s, /q or /f (leading slash, only letters after it). Paths such as /etc
// exceed the length limit, and drive paths such as C:\ never start with
// a slash, so real targets are left alone.
func isShortSlashFlag(a string) bool {
	if len(a) < 2 || a[0] != '/' {
		return false
	}
	letters := a[1:]
	if len(letters) > 3 {
		return false
	}
	for _, r := range letters {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// dangerousDeletePath reports whether p names a root or system location that
// must never be recursively deleted. It understands POSIX roots, any
// Windows drive root (C:\, D:\, extended-length \\?\C:\), UNC share roots,
// well-known Windows system directories (any drive letter), unexpanded
// environment-variable roots, and the POSIX system paths in dangerousPath.
func dangerousDeletePath(p string) bool {
	if dangerousPath(p) {
		return true
	}
	t := canonicalDangerPath(p)
	t = strings.TrimSuffix(t, "*")
	if t == "" {
		return true // was nothing but slashes/wildcards
	}
	// Drive root of any letter, e.g. "c:" after normalization.
	if len(t) == 2 && t[1] == ':' && isASCIILetter(t[0]) {
		return true
	}
	// UNC share root (\\server\share or //server/share after slash normalization).
	if strings.HasPrefix(t, `\\`) || strings.HasPrefix(t, "//") {
		parts := strings.FieldsFunc(strings.Trim(t, `/\`), func(r rune) bool {
			return r == '\\' || r == '/'
		})
		if len(parts) <= 2 {
			return true
		}
	}
	// Windows system directories on any drive: <d>:\Windows[\...],
	// <d>:\Program Files[\...], <d>:\ProgramData[\...], <d>:\Users[\...].
	if len(t) >= 3 && t[1] == ':' && isASCIILetter(t[0]) && (t[2] == '\\' || t[2] == '/') {
		rest := t[3:]
		first := rest
		if i := strings.IndexAny(rest, `\/`); i >= 0 {
			first = rest[:i]
		}
		switch first {
		case "windows", "program files", "program files (x86)", "programdata", "users":
			return true
		}
	}
	// Unexpanded environment-variable roots (written literally by the user).
	envRoots := []string{
		`%systemroot%`, `%windir%`, `%systemdrive%`, `%programfiles%`, `%programfiles(x86)%`, `%programdata%`, `%userprofile%`, `%homedrive%`,
		`$env:systemroot`, `$env:windir`, `$env:systemdrive`, `$env:programfiles`, `$env:programfiles(x86)`, `$env:programdata`, `$env:userprofile`, `$env:homedrive`,
		`${env:systemroot}`, `${env:windir}`, `${env:systemdrive}`, `${env:programfiles}`, `${env:programdata}`, `${env:userprofile}`,
	}
	for _, r := range envRoots {
		if t == r || strings.HasPrefix(t, r+`\`) || strings.HasPrefix(t, r+"/") || strings.HasPrefix(t, r+"*") {
			return true
		}
	}
	// POSIX system locations and user data: subtrees of these are never
	// benign cleanup targets (e.g. /usr/bin, /etc/ssh, /var/lib/dpkg,
	// /home/<user>). /var/tmp is exempt (scratch space).
	posixRoots := []string{
		"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64",
		"/boot", "/dev", "/sys", "/proc", "/run", "/opt",
		"/root", "/home", "/users", "/volumes", "/system", "/library",
		"/mnt", "/media",
	}
	for _, r := range posixRoots {
		if t == r || strings.HasPrefix(t, r+"/") {
			return true
		}
	}
	if (t == "/var" || strings.HasPrefix(t, "/var/")) &&
		t != "/var/tmp" && !strings.HasPrefix(t, "/var/tmp/") {
		return true
	}
	return false
}

func literalArgs(call *syntax.CallExpr) []string {
	var args []string
	for _, w := range call.Args {
		s, ok := literalWord(w)
		if !ok {
			continue
		}
		args = append(args, s)
	}
	return args
}

func literalWord(w *syntax.Word) (string, bool) {
	var b strings.Builder
	for _, p := range w.Parts {
		switch part := p.(type) {
		case *syntax.Lit:
			b.WriteString(part.Value)
		case *syntax.SglQuoted:
			b.WriteString(part.Value)
		case *syntax.DblQuoted:
			for _, dp := range part.Parts {
				lit, ok := dp.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

func collectTargets(args []string) []string {
	var out []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			out = append(out, a)
		}
	}
	return out
}

func dangerousPath(p string) bool {
	p = canonicalDangerPath(p)
	switch p {
	case "/", "", ".", "..", "*", "/*", "~", "~/", "~/*", "$home", "${home}",
		"c:", `c:\`, "c:/", `c:\*`,
		"/etc", "/usr", "/var", "/bin", "/lib", "/boot", "/dev", "/home", "/users",
		"/sbin", "/lib64", "/opt", "/root", "/library", "/system":
		return true
	}
	// POSIX subtrees (see dangerousDeletePath): /var/tmp exempt.
	posixRoots := []string{
		"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64",
		"/boot", "/dev", "/sys", "/proc", "/run", "/opt",
		"/root", "/home", "/users", "/volumes", "/system", "/library",
		"/mnt", "/media",
	}
	for _, r := range posixRoots {
		if strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	if strings.HasPrefix(p, "/var/") && !strings.HasPrefix(p, "/var/tmp/") {
		return true
	}
	if strings.Trim(p, `/`) == "" {
		return true
	}
	return false
}

func forkBomb(lower string) bool {
	compact := strings.Join(strings.Fields(lower), "")
	return strings.Contains(compact, ":(){:|:&};:")
}

// chmodModePermissive reports whether the mode arguments make the tree
// world-writable or world-executable anywhere (o+w, o+x, a+rwx, +w, 777,
// 666…). It is token-based, so a filename containing the digits "777" no
// longer forces the escalation, and 666/o+rwx cases that used to slip through
// do fire it now.
func chmodModePermissive(args []string) bool {
	for _, tok := range args {
		lt := strings.ToLower(tok)
		switch lt {
		case "777", "666", "0777", "0666", "o+rwx", "a+rwx", "u+rwx", "g+rwx",
			"o=rwx", "a=rwx", "o+rw", "a+rw", "o=rw", "a=rw", "o+tx":
			return true
		}
		// Symbolic form: [ugoa]*[+-=][rwxXst]+ — flag world-write or
		// world-exec letters applied to other/all/none.
		i := 0
		for i < len(lt) && strings.ContainsRune("ugoa", rune(lt[i])) {
			i++
		}
		subj := lt[:i]
		rest := lt[i:]
		op := strings.IndexAny(rest, "+-=")
		if op < 0 {
			continue
		}
		letters := rest[op+1:]
		// "rwx"-bearing permissions where world/all can write or exec.
		if strings.ContainsAny(letters, "wx") {
			if subj == "" || strings.ContainsAny(subj, "oa") {
				return true
			}
		}
	}
	return false
}

func hasAny(args []string, needles ...string) bool {
	for _, a := range args {
		for _, n := range needles {
			if strings.EqualFold(a, n) || strings.Contains(strings.ToLower(a), strings.ToLower(n)) {
				return true
			}
		}
	}
	return false
}

func maxTier(a, b Tier) Tier {
	if a > b {
		return a
	}
	return b
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
