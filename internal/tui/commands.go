package tui

import (
	"sort"
	"strings"
)

type commandItem struct {
	Name     string
	Desc     string
	Shortcut string
	Category string
	Keywords []string
	recent   bool
	Run      func(*model) string // returns status notice
}

const maxRecentCommands = 6

func commandCatalog() []commandItem {
	return []commandItem{
		{Name: "/help", Desc: "open the command map", Shortcut: "F1", Category: "System", Keywords: []string{"help", "commands", "map"}, Run: func(m *model) string {
			m.openPalette("")
			return ""
		}},
		{Name: "/search", Desc: "search the conversation", Shortcut: "ctrl+f", Category: "Edit", Keywords: []string{"find", "locate", "grep"}, Run: func(m *model) string {
			m.openSearch()
			return ""
		}},
		{Name: "/lens", Desc: "cycle the highlight lens (35 lenses)", Shortcut: "ctrl+g", Category: "Appearance", Keywords: []string{"highlight", "filter", "important", "errors", "secrets", "todos"}, Run: func(m *model) string {
			m.cycleLens(1)
			return ""
		}},
		{Name: "/lens-off", Desc: "disable the highlight lens", Shortcut: "", Category: "Appearance", Keywords: []string{"highlight", "filter", "clear"}, Run: func(m *model) string {
			m.lensIdx = 0
			return "lens off"
		}},
		{Name: "/new", Desc: "start a fresh session", Shortcut: "ctrl+n", Category: "Session", Keywords: []string{"reset", "fresh", "clear"}, Run: func(m *model) string {
			m.newSession()
			return ""
		}},
		{Name: "/clear", Desc: "clear the transcript", Shortcut: "ctrl+l", Category: "Session", Keywords: []string{"wipe", "feed"}, Run: func(m *model) string {
			m.transcript = nil
			m.scroll = 0
			m.gen++
			return "feed cleared"
		}},
		{Name: "/sessions", Desc: "list and resume saved sessions", Shortcut: "ctrl+r", Category: "Session", Keywords: []string{"resume", "history", "load", "attach"}, Run: func(m *model) string {
			m.openSessions("")
			return ""
		}},
		{Name: "/continue", Desc: "resume the most recent session", Shortcut: "", Category: "Session", Keywords: []string{"resume", "latest"}, Run: func(m *model) string {
			m.attachSession("latest")
			return ""
		}},
		{Name: "/session", Desc: "resume a session by id (/session <id>)", Shortcut: "", Category: "Session", Keywords: []string{"attach", "id"}, Run: func(m *model) string {
			return "type /session <id> to attach"
		}},
		{Name: "/revert", Desc: "restore a checkpoint (/revert <id>)", Shortcut: "", Category: "Git", Keywords: []string{"undo", "rollback", "checkpoint"}, Run: func(m *model) string {
			return "type /revert <id> to restore"
		}},
		{Name: "/branch", Desc: "show the git branch and repo info", Shortcut: "", Category: "Git", Keywords: []string{"status", "vcs", "repo"}, Run: func(m *model) string {
			return m.gitNotice()
		}},
		{Name: "/jobs", Desc: "list background jobs and processes", Shortcut: "", Category: "System", Keywords: []string{"processes", "background"}, Run: func(m *model) string {
			m.runCLI([]string{"jobs"})
			return "jobs"
		}},
		{Name: "/tasks", Desc: "open the project task board", Shortcut: "", Category: "Tasks", Keywords: []string{"todo", "board", "plan"}, Run: func(m *model) string {
			m.openTasks()
			return ""
		}},
		{Name: "/todo", Desc: "open the session to-do board (alias of /tasks)", Shortcut: "", Category: "Tasks", Keywords: []string{"board", "plan", "checklist"}, Run: func(m *model) string {
			m.openTasks()
			return ""
		}},
		{Name: "/providers", Desc: "browse providers and connect one", Shortcut: "", Category: "Models", Keywords: []string{"connect", "api", "key", "vendor"}, Run: func(m *model) string {
			m.openProviders("")
			return ""
		}},
		{Name: "/models", Desc: "list models on connected providers (free first)", Shortcut: "", Category: "Models", Keywords: []string{"browse", "choose", "model"}, Run: func(m *model) string {
			m.openModels()
			return ""
		}},
		{Name: "/theme", Desc: "browse and switch color themes (35)", Shortcut: "", Category: "Appearance", Keywords: []string{"colors", "scheme", "look"}, Run: func(m *model) string {
			m.openTheme("")
			return ""
		}},
		{Name: "/copy", Desc: "copy the full transcript to the clipboard", Shortcut: "ctrl+y", Category: "Edit", Keywords: []string{"clipboard", "transcript"}, Run: func(m *model) string {
			m.copyFullTranscript()
			return ""
		}},
		{Name: "/copy-last", Desc: "copy the last assistant reply", Shortcut: "", Category: "Edit", Keywords: []string{"clipboard", "response", "reply"}, Run: func(m *model) string {
			return m.copyLastAssistant()
		}},
		{Name: "/copy-code", Desc: "copy the last fenced code block", Shortcut: "", Category: "Edit", Keywords: []string{"clipboard", "snippet", "code"}, Run: func(m *model) string {
			return m.copyLastCode()
		}},
		{Name: "/export", Desc: "export the transcript to a markdown file", Shortcut: "", Category: "Session", Keywords: []string{"save", "file", "md", "markdown"}, Run: func(m *model) string {
			return m.exportTranscript()
		}},
		{Name: "/expand-all", Desc: "expand every tool and command box", Shortcut: "", Category: "View", Keywords: []string{"fold", "open", "uncollapse"}, Run: func(m *model) string {
			return m.setAllExpanded(true)
		}},
		{Name: "/collapse-all", Desc: "collapse every tool and command box", Shortcut: "", Category: "View", Keywords: []string{"fold", "close", "hide"}, Run: func(m *model) string {
			return m.setAllExpanded(false)
		}},
		{Name: "/sidebar", Desc: "toggle the right-hand sidebar", Shortcut: "ctrl+b", Category: "View", Keywords: []string{"panel", "layout"}, Run: func(m *model) string {
			return m.toggleSidebar()
		}},
		{Name: "/profiles", Desc: "list delegate profiles", Shortcut: "", Category: "Agent", Keywords: []string{"delegate", "agents"}, Run: func(m *model) string {
			m.runCLI([]string{"profiles"})
			return "profiles"
		}},
		{Name: "/delegates", Desc: "list delegates and live event journal", Shortcut: "", Category: "Agent", Keywords: []string{"delegate", "journal", "events"}, Run: func(m *model) string {
			m.runCLI([]string{"delegates", "--events", "30"})
			return "delegates"
		}},
		{Name: "/knowledge", Desc: "show the repository instructions loaded into the prompt", Shortcut: "", Category: "Agent", Keywords: []string{"agents.md", "claude.md", "instructions"}, Run: func(m *model) string {
			m.runCLI([]string{"knowledge"})
			return "knowledge"
		}},
		{Name: "/plugins", Desc: "list Lua plugins (workspace plugins stay untrusted)", Shortcut: "", Category: "System", Keywords: []string{"lua", "extensions", "hooks"}, Run: func(m *model) string {
			m.runCLI([]string{"plugins"})
			return "plugins"
		}},
		{Name: "/mcp", Desc: "serve the toolset over MCP on stdio", Shortcut: "", Category: "MCP", Keywords: []string{"protocol", "server"}, Run: func(m *model) string {
			return "run: creator mcp | jq ."
		}},
		{Name: "/init", Desc: "write ~/.creator/config.json", Shortcut: "", Category: "System", Keywords: []string{"setup", "configure"}, Run: func(m *model) string {
			m.runCLI([]string{"init"})
			return "init"
		}},
		{Name: "/install", Desc: "build binaries into ~/.local/bin", Shortcut: "", Category: "System", Keywords: []string{"setup", "build"}, Run: func(m *model) string {
			m.runCLI([]string{"install"})
			return "install"
		}},
		{Name: "/usage", Desc: "show tokens and tools for the last run", Shortcut: "", Category: "Session", Keywords: []string{"tokens", "cost", "stats"}, Run: func(m *model) string {
			return m.showUsage()
		}},
		{Name: "/stats", Desc: "transcript statistics (blocks, words)", Shortcut: "", Category: "Session", Keywords: []string{"words", "count", "size"}, Run: func(m *model) string {
			return m.showStats()
		}},
		{Name: "/keys", Desc: "show the keyboard cheatsheet", Shortcut: "", Category: "System", Keywords: []string{"help", "shortcuts", "cheatsheet", "keys"}, Run: func(m *model) string {
			m.openHelp()
			return ""
		}},
		{Name: "/quit", Desc: "quit mission control", Shortcut: "ctrl+c", Category: "System", Keywords: []string{"exit", "close", "bye"}, Run: func(m *model) string {
			return "quit"
		}},
	}
}

// catalogNames lets the frecency tracker reject typed commands that do not
// exist as palette entries (they still run through runCLI, but must not
// pollute the recent list).
var catalogNames = func() map[string]bool {
	m := make(map[string]bool, len(commandCatalog()))
	for _, it := range commandCatalog() {
		m[it.Name] = true
	}
	return m
}()

// recordRecent pushes a catalog command to the front of the frecency list.
func (m *model) recordRecent(name string) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	if !catalogNames[name] {
		return
	}
	out := make([]string, 0, maxRecentCommands)
	out = append(out, name)
	for _, r := range m.recentCmds {
		if r == name {
			continue
		}
		out = append(out, r)
		if len(out) >= maxRecentCommands {
			break
		}
	}
	m.recentCmds = out
}

// mergeRecent prepends frecently used catalog commands (marked recent) and
// drops them from the default tail. Empty query only.
func mergeRecent(items []commandItem, recents []string) []commandItem {
	if len(recents) == 0 {
		return items
	}
	byName := make(map[string]commandItem, len(items))
	for _, it := range items {
		byName[it.Name] = it
	}
	seen := make(map[string]bool, len(recents))
	out := make([]commandItem, 0, len(items)+len(recents))
	for _, r := range recents {
		it, ok := byName[r]
		if !ok || seen[r] {
			continue
		}
		seen[r] = true
		it.recent = true
		out = append(out, it)
	}
	for _, it := range items {
		if seen[it.Name] {
			continue
		}
		out = append(out, it)
	}
	return out
}

// subseqScore reports whether q is a rune-wise subsequence of name; the
// returned bonus rewards short, dense matches.
func subseqScore(name, q string) int {
	nr, qr := []rune(name), []rune(q)
	i := 0
	for _, r := range qr {
		found := false
		for i < len(nr) {
			if nr[i] == r {
				found = true
				i++
				break
			}
			i++
		}
		if !found {
			return -1
		}
	}
	bonus := 24 - len(nr)/2
	if bonus < 1 {
		bonus = 1
	}
	return bonus
}

// scoreItem ranks a command against the query; -1 means no match.
func scoreItem(q string, it commandItem) int {
	name := strings.ToLower(it.Name)
	nameNoSlash := strings.TrimPrefix(name, "/")
	desc := strings.ToLower(it.Desc)
	cat := strings.ToLower(it.Category)
	kw := strings.ToLower(strings.Join(it.Keywords, " "))
	switch {
	case strings.HasPrefix(nameNoSlash, q):
		return 100 - len(nameNoSlash)/8
	case strings.HasPrefix(name, q):
		return 96 - len(name)/8
	case strings.Contains(nameNoSlash, q):
		return 72 - strings.Index(nameNoSlash, q)
	case kw != "" && strings.Contains(kw, q):
		return 46
	case cat != "" && strings.HasPrefix(cat, q):
		return 40
	case strings.Contains(desc, q):
		return 30
	default:
		if s := subseqScore(nameNoSlash, q); s >= 0 {
			return s
		}
		if s := subseqScore(cat+" "+kw, q); s >= 0 {
			return s - 12
		}
		return -1
	}
}

func filterCommands(q string) []commandItem {
	q = strings.TrimSpace(strings.ToLower(q))
	items := commandCatalog()
	if q == "" {
		return items
	}
	type scored struct {
		it commandItem
		s  int
	}
	out := make([]scored, 0, len(items))
	for _, it := range items {
		if s := scoreItem(q, it); s >= 0 {
			out = append(out, scored{it, s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].s > out[j].s })
	res := make([]commandItem, len(out))
	for i, sc := range out {
		res[i] = sc.it
	}
	return res
}
