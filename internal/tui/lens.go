// lens.go — the highlight lenses: 35 one-keystroke filters that scan the
// conversation for the words that matter (errors, secrets, TODOs, URLs,
// envs, …), hide the blocks that contain nothing, and paint the matches in
// the lens color. Only one lens is active at a time; cycling is cheap.
package tui

import (
	"regexp"
	"strings"
)

type lensSpec struct {
	Slug    string
	Name    string
	Desc    string
	Pattern string // case-insensitive regexp
	Color   string // hex color used for the match underline+foreground
}

// lensCatalog is ordered by workflow phase: what broke → what it touches →
// how it ships. Exactly 35 entries.
var lensCatalog = []lensSpec{
	{slug("errors"), "Errors", "failures, exceptions, fatal paths", `(?i)\b(error|failed|failure|fatal|panic|exception|traceback|errno|refused|aborted)\b`, "#ff6b6b"},
	{slug("warnings"), "Warnings", "warnings and cautions", `(?i)\b(warn|warning|caution|deprecated|deprecation|soft.?fail)\b`, "#ffd43b"},
	{slug("security"), "Security", "vulnerabilities and attack surface", `(?i)\b(security|vulnerab\w*|exploit\w*|cve-?\d|auth(entic|oriz)\w*|oauth|jwt|rce|xss|csrf|injection)\b`, "#f783ac"},
	{slug("secrets"), "Secrets & Keys", "credentials that must never leak", `(?i)\b(api[-_]?key|secret|token|password|passwd|private[-_]?key|credential\w*|sk[-_]live|bearer)\b`, "#ffa94d"},
	{slug("risk"), "Risk & Care", "fragile spots and edge cases", `(?i)\b(risk\w*|fragile|edge[- ]case|careful|dangerous?|unstable|workaround|untested|volatile)\b`, "#ff8787"},
	{slug("important"), "Important", "critical callouts and hard rules", `(?i)\b(important|critical|urgent|breaking|must|never|always|priority|p0|p1)\b`, "#e599f7"},
	{slug("todos"), "TODOs & Notes", "todo / fixme / hack markers", `(?i)\b(todo|fixme|hack|xxx|note|bug)\b`, "#ffe066"},
	{slug("refactor"), "Refactor & Cleanup", "simplification and debt", `(?i)\b(refactor\w*|simplif\w*|rename\w*|clean\s?up|dead\s?code|tech\s?debt|duplicat\w+|cleanup)\b`, "#b197fc"},

	{slug("api"), "API & HTTP", "endpoints, methods, status codes", `(?i)\b(https?|endpoint|route|http|grpc|rest|graphql|webhook|payload|status\s?[1-5]\d\d)\b`, "#4dabf7"},
	{slug("network"), "Network", "sockets, DNS, timeouts", `(?i)\b(network|socket|tcp|udp|dns|tls|ssl|proxy|bandwidth|timeout|timed?\s?out|connection)\b`, "#38d9a9"},
	{slug("database"), "Database", "queries, schema, migrations", `(?i)\b(database|db|sql|query|schema|migration|transaction|postgres|mysql|sqlite|redis|index)\b`, "#9775fa"},
	{slug("crypto"), "Crypto", "hashing, ciphers, signatures", `(?i)\b(hash|cipher|encrypt\w*|decrypt\w*|signature|aes|rsa|sha-?\d|bcrypt|argon|scrypt)\b`, "#63e6be"},
	{slug("auth"), "Auth & Access", "login, roles, permissions", `(?i)\b(login|logout|session|cookie|permission\w*|role|acl|rbac|credential|access\s?control|authenticate|authorize)\b`, "#da77f2"},
	{slug("files"), "Filesystem", "paths, files, directories", `(?i)([a-z]:\\[^\s"'` + "`" + `)]+|(?:^|[\s"'` + "`" + `(])/[\w.\-/]{2,}|\bdirectory\b|\bpath\b|\bfile(s)?\b)`, "#e9c46a"},

	{slug("git"), "Git & VCS", "commits, branches, diffs", `(?i)\b(git|commit\w*|branch\w*|merge|rebase|pull\s?request|cherry-pick|stash|diff|HEAD|blame)\b`, "#f4a261"},
	{slug("testing"), "Testing", "tests, mocks, coverage", `(?i)\b(test\w*|spec\w*|mock\w*|assert\w*|expect\w*|coverage|fixture|snapshot)\b`, "#8ce99a"},
	{slug("build"), "Build & Tooling", "compiler, lint, CI", `(?i)\b(build\w*|compil\w*|compiler|lint\w*|format\w*|toolchain|ci\b|vet|prettier|eslint)\b`, "#a5d8ff"},
	{slug("deps"), "Dependencies", "packages, modules, versions", `(?i)\b(dependenc\w+|package\w*|module\w*|import\w*|require\w*|semver|registry|upgrade\w*)\b`, "#ffc078"},
	{slug("config"), "Configuration", "configs, env vars, flags", `(?i)\b(config\w*|yaml|toml|\.env|flag\w*|setting\w*|manifest|json\s?config)\b`, "#99e9f2"},
	{slug("metrics"), "Metrics & Timing", "durations, rates, percentages", `\b\d+(?:\.\d+)?(?:\s?(?:ms|s|µs|ns|%|rps|qps|tok(?:ens)?)\b)`, "#c0eb75"},
	{slug("versions"), "Versions", "semantic versions", `\bv?\d+\.\d+(?:\.\d+)?(?:[-+][\w.\-]+)?\b`, "#74c0fc"},
	{slug("timestamps"), "Timestamps & Dates", "ISO times and clock values", `\b\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2})?(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?)?\b|\b\d{1,2}:\d{2}:\d{2}\b`, "#ced4da"},
	{slug("sizes"), "Byte Sizes", "readable byte quantities", `\b\d+(?:\.\d+)?\s?(?:bytes?|kb|mb|gb|tb|kib|mib|gib|tib)\b`, "#f6d365"},

	{slug("urls"), "URLs & Links", "web addresses", `https?://[^\s"'<>)]+`, "#66d9e8"},
	{slug("paths"), "File Paths", "absolute and relative paths", `(?:^|[\s"'` + "`" + `(])(?:[a-z]:\\[^\s"'` + "`" + `)]+|/(?:[\w.\-]+/){1,}[\w.\-]+|~/[^\s"'` + "`" + `)]+|\.{1,2}/[^\s"'` + "`" + `)]+)`, "#ffd8a8"},
	{slug("envvars"), "Env Vars & Constants", "SCREAMING_SNAKE identifiers", `\b[A-Z][A-Z0-9]{2,}(?:_[A-Z0-9]+)*\b`, "#dee2e6"},
	{slug("data"), "Data & Serialization", "json, payloads, records", `(?i)\b(json|yaml|csv|xml|payload|serializ\w*|deserializ\w*|response\s?body|request\s?body|rows?\b|records?\b)\b`, "#b2f2bb"},

	{slug("memory"), "Memory", "leaks, heap, allocation", `(?i)\b(memory|heap|alloc\w*|leak|garbage\s?collect|\bgc\b|buffer|overflow|oom)\b`, "#a5d8ff"},
	{slug("concurrency"), "Concurrency", "goroutines, locks, races", `(?i)\b(goroutine\w*|thread\w*|mutex|lock\w*|channel\w*|race\s?condition|deadlock|concurren\w*|async|await|atomic)\b`, "#e9fac8"},
	{slug("processes"), "Processes", "exec, spawn, signals", `(?i)\b(process\w*|pid\b|exec\w*|spawn\w*|fork\w*|signal\w*|kill\w*|terminate|daemon\w*|zombie)\b`, "#f8f0fc"},
	{slug("encoding"), "Encoding", "base64, utf, escapes", `(?i)\b(base64|utf-?8|unicode|escape\w*|encode\w*|decode\w*|urlencode|percent-?encod)\b`, "#fff3bf"},

	{slug("perf"), "Performance", "latency, throughput, hot paths", `(?i)\b(perf|performance|latency|throughput|benchmark|optimi\w*|bottleneck|profile\w*|hot\s?path)\b`, "#63e6be"},
	{slug("docs"), "Docs & Comments", "readme, docs, changelogs", `(?i)\b(readme|docs?|documentation|comment\w*|changelog|wiki|guide|tutorial)\b`, "#e7f5ff"},
	{slug("deploy"), "Deploy & Release", "shipping, rollout, prod", `(?i)\b(deploy\w*|release\w*|publish\w*|rollout|ship\w*|changelog|production|prod\b|tag\b|hotfix)\b`, "#ffe8cc"},
	{slug("breaking"), "Breaking Changes", "API removal and migration", `(?i)\b(breaking\s?change\w*|migrat\w+\s+(?:required|guide)|incompat\w*|major\s+version|removed\s+in|deprecated\s+in)\b`, "#ff8787"},
}

func slug(s string) string { return s }

// lensCompiled holds the regex for every catalog entry, compiled once.
var lensCompiled = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(lensCatalog))
	for i, l := range lensCatalog {
		out[i] = regexp.MustCompile(l.Pattern)
	}
	return out
}()

// lensForIndex returns (spec, compiled) for i, or (zero, nil) when invalid.
func lensForIndex(i int) (*lensSpec, *regexp.Regexp) {
	if i < 0 || i >= len(lensCatalog) {
		return nil, nil
	}
	return &lensCatalog[i], lensCompiled[i]
}

// lensOff is the stored model value for "no lens": the stored lens domain
// is 1..len(lensCatalog) (1 = lensCatalog[0]) so the zero value — including
// models constructed by hand in tests — means "off".
const lensOff = 0

// lensNone is the catalog-domain "off" sentinel used by lensByQuery and
// lensForIndex. Never store it on the model — it is -1, use lensOff.
const lensNone = -1

// lensCycle advances the stored lens (1..35) by delta, wrapping around.
// Stepping forward from 0 lands on lens 1; stepping backward from 0 wraps
// to the last lens. Stored values stay in 1..len(lensCatalog).
func lensCycle(cur, delta int) int {
	n := len(lensCatalog)
	if cur <= 0 && delta < 0 {
		cur = n + 1
	}
	next := (cur+delta-1)%n + 1
	if next <= 0 {
		next += n
	}
	return next
}

// lensByQuery resolves a user-typed slug/name substring to a catalog index.
// Tokens of length ≥ 4 are also tried individually, so "/lens secret keys"
// still finds the secrets lens.
func lensByQuery(q string) int {
	q = strings.TrimSpace(strings.ToLower(q))
	if q == "" || q == "off" || q == "none" || q == "clear" {
		return lensNone
	}
	for i, l := range lensCatalog {
		if l.Slug == q {
			return i
		}
	}
	for i, l := range lensCatalog {
		if strings.Contains(l.Slug, q) || strings.Contains(strings.ToLower(l.Name), q) {
			return i
		}
	}
	for _, token := range strings.Fields(strings.ReplaceAll(q, "-", " ")) {
		if len(token) < 4 {
			continue
		}
		for i, l := range lensCatalog {
			if strings.Contains(l.Slug, token) || strings.Contains(strings.ToLower(l.Name), token) {
				return i
			}
		}
	}
	return -2 // unknown
}
