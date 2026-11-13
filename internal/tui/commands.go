package tui

import "strings"

type commandItem struct {
	Name string
	Desc string
	Run  func(*model) string // returns status notice
}

func commandCatalog() []commandItem {
	return []commandItem{
		{"/help", "open the command map", func(m *model) string { m.palette = false; return "press F1 for the command map" }},
		{"/new", "start a fresh session and clear the feed", func(m *model) string {
			m.transcript = nil
			m.sessionID = ""
			m.finalText = ""
			return "new session ready"
		}},
		{"/clear", "clear the transcript", func(m *model) string { m.transcript = nil; return "feed cleared" }},
		{"/sessions", "list and resume saved sessions (Ctrl+R)", func(m *model) string { m.openSessions(""); return "" }},
		{"/continue", "resume the most recent session", func(m *model) string { m.attachSession("latest"); return "" }},
		{"/session", "resume a specific session id", func(m *model) string { return "type /session <id>" }},
		{"/jobs", "list background jobs and recent commands", func(m *model) string { m.runCLI([]string{"jobs"}); return "jobs" }},
		{"/todo", "open the session to-do list", func(m *model) string { m.openTasks(); return "" }},
		{"/tasks", "open the project task board (alias of /todo)", func(m *model) string { m.openTasks(); return "" }},
		{"/providers", "browse the full provider registry and connect one", func(m *model) string { m.openProviders(""); return "" }},
		{"/theme", "browse and switch the color theme", func(m *model) string { m.openTheme(""); return "" }},
		{"/models", "list models on connected providers (free first)", func(m *model) string { m.openModels(); return "" }},
		{"/profiles", "list delegate profiles", func(m *model) string { m.runCLI([]string{"profiles"}); return "profiles" }},
		{"/plugins", "list Lua plugins (workspace plugins stay untrusted)", func(m *model) string { m.runCLI([]string{"plugins"}); return "plugins" }},
		{"/knowledge", "show the repository instructions Creator loads", func(m *model) string { m.runCLI([]string{"knowledge"}); return "knowledge" }},
		{"/delegates", "list delegates and live event journal", func(m *model) string { m.runCLI([]string{"delegates", "--events", "30"}); return "delegates" }},
		{"/mcp", "serve the toolset over MCP on stdio", func(m *model) string { return "run: creator mcp | jq ." }},
		{"/init", "write ~/.creator/config.json", func(m *model) string { m.runCLI([]string{"init"}); return "init" }},
		{"/install", "build creator and ctr into ~/.local/bin", func(m *model) string { m.runCLI([]string{"install"}); return "install" }},
		{"/revert", "restore a checkpoint: /revert <checkpoint-id>", func(m *model) string { return "type /revert <id>" }},
		{"/copy", "copy the full transcript to the clipboard", func(m *model) string { m.copyFullTranscript(); return "" }},
		{"/quit", "quit mission control", func(m *model) string { return "quit" }},
	}
}

func filterCommands(q string) []commandItem {
	q = strings.TrimSpace(strings.ToLower(q))
	items := commandCatalog()
	if q == "" {
		return items
	}
	out := make([]commandItem, 0, len(items))
	for _, it := range items {
		if strings.Contains(strings.ToLower(it.Name+" "+it.Desc), q) {
			out = append(out, it)
		}
	}
	return out
}
