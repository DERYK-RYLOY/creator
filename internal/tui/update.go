package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/creatorcli/creator/internal/provider"
)

type tickMsg struct{ at time.Time }
type childLineMsg struct{ line string }
type childErrLineMsg struct{ line string }
type childDoneMsg struct{ err error }
type commandDoneMsg struct {
	args []string
	out  string
	err  error
}

func (m *model) Init() tea.Cmd {
	m.lastTick = time.Now()
	return tick(900 * time.Millisecond)
}

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg{at: t} })
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		now := msg.at
		if m.lastTick.IsZero() {
			m.lastTick = now.Add(-70 * time.Millisecond)
		}
		dt := now.Sub(m.lastTick).Seconds()
		if dt <= 0 || dt > 0.5 {
			dt = 0.07
		}
		m.lastTick = now
		m.animT += dt
		target := 0.0
		if m.running {
			target = 1.0
		}
		step := dt / 0.9
		if m.morph < target {
			m.morph += step
			if m.morph > 1 {
				m.morph = 1
			}
		} else if m.morph > target {
			m.morph -= step
			if m.morph < 0 {
				m.morph = 0
			}
		}
		interval := 900 * time.Millisecond
		if m.running {
			interval = 70 * time.Millisecond
		}
		if diff := target - m.morph; diff > 0.001 || diff < -0.001 {
			// mid-morph: fast cadence so the ease stays smooth even when
			// the idle loop was on its slow tick
			if interval > 40*time.Millisecond {
				interval = 40 * time.Millisecond
			}
		}
		return m, tick(interval)
	case childLineMsg:
		m.keepReadingPosition(func() { m.consumeEvent(msg.line, false) })
		return m, nil
	case childErrLineMsg:
		m.keepReadingPosition(func() { m.consumeEvent(msg.line, true) })
		return m, nil
	case childDoneMsg:
		m.keepReadingPosition(func() { m.finishRun(msg.err) })
		return m, nil
	case commandDoneMsg:
		m.keepReadingPosition(func() { m.appendCommandResult(msg) })
		return m, nil
	case providerStepMsg:
		// Drop steps from a superseded or cancelled connect.
		if msg.gen != m.verifyGen {
			return m, nil
		}
		m.keySteps = append(m.keySteps, formatProviderStep(msg.step))
		return m, nil
	case providerVerifyMsg:
		// A verify that finished after the user cancelled (or after a
		// newer connect began) must not touch the UI.
		if msg.gen != m.verifyGen {
			return m, nil
		}
		m.finishProviderVerify(msg)
		return m, nil
	case modelsDoneMsg:
		// Drop scans that finished after the user cancelled, or that were
		// superseded by a newer scan: never re-open or re-hijack the overlay
		// on a stale generation.
		if !m.modelsBusy || msg.gen != m.modelsScanGen {
			return m, nil
		}
		m.modelRows = msg.rows
		m.modelsSources = msg.sources
		m.modelsBusy = false
		m.overlay = overlayModels
		return m, nil
	case tea.MouseMsg:
		m.handleMouse(msg)
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	default:
		return m, nil
	}
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (m *model) keepReadingPosition(f func()) {
	var before int
	if m.width > 0 {
		before = m.transcriptLineCount(m.width)
	}
	f()
	if m.width <= 0 {
		return
	}
	after := m.transcriptLineCount(m.width)
	if m.scroll > 0 {
		m.scroll += after - before
		if m.scroll < 0 {
			m.scroll = 0
		}
		if after > m.bodyHeight() {
			if maxScroll := after - m.bodyHeight(); m.scroll > maxScroll {
				m.scroll = maxScroll
			}
		} else {
			m.scroll = 0
		}
	}
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.overlay != overlayNone {
		return m.handleOverlayKey(msg)
	}
	if m.palette {
		return m.handlePaletteKey(msg)
	}
	switch msg.String() {
	case "ctrl+c":
		if m.running {
			m.interrupt("ctrl+c")
		}
		return m, tea.Quit
	case "esc":
		if m.running {
			m.interrupt("esc")
			return m, nil
		}
		if m.input != "" {
			m.input = ""
			return m, nil
		}
		return m, nil
	case "f1", "ctrl+p":
		m.openPalette(m.paletteQ)
		return m, nil
	case "ctrl+y":
		m.copyFullTranscript()
		return m, nil
	case "ctrl+l":
		m.transcript = nil
		m.scroll = 0
		return m, nil
	case "ctrl+n":
		m.newSession()
		return m, nil
	case "ctrl+r":
		m.openSessions("")
		return m, nil
	case "shift+tab":
		m.approval = (m.approval + 1) % 3
		m.notice = approvalNames[m.approval]
		return m, nil
	case "tab":
		m.toggleExpanded()
		return m, nil
	case "home":
		m.scroll = m.transcriptLineCount(m.width) // clamps to top once layout is recomputed
		return m, nil
	case "end":
		m.scroll = 0
		return m, nil
	case "pgup", "ctrl+u":
		m.scroll += 10
		return m, nil
	case "pgdn", "ctrl+d":
		m.scroll -= 10
		if m.scroll < 0 {
			m.scroll = 0
		}
		return m, nil
	case "ctrl+o":
		m.toggleExpanded()
		return m, nil
	case "up":
		m.scroll++
		return m, nil
	case "down":
		m.scroll--
		if m.scroll < 0 {
			m.scroll = 0
		}
		return m, nil
	case "ctrl+up", "alt+up":
		m.historyPrev()
		return m, nil
	case "ctrl+down", "alt+down":
		m.historyNext()
		return m, nil
	case "enter":
		return m, m.submit()
	case "ctrl+j", "alt+enter":
		m.input += "\n"
		return m, nil
	case "backspace":
		m.backspace()
		return m, nil
	default:
		if msg.Type == tea.KeyRunes {
			m.insertRunes(msg.Runes)
		}
		return m, nil
	}
}

func (m *model) handleMouse(msg tea.MouseMsg) {
	if m.palette {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if m.paletteIdx > 0 {
				m.paletteIdx--
			}
		case tea.MouseButtonWheelDown:
			if m.paletteIdx < len(m.paletteView)-1 {
				m.paletteIdx++
			}
		}
		return
	}
	if m.overlay != overlayNone && m.overlay != overlayTheme {
		return // overlays own the full screen; never fall through to transcript
	}
	if m.overlay == overlayTheme {
		switch msg.Button {
		case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
			m.moveThemeOverlay(msg.Button == tea.MouseButtonWheelUp)
		}
		return
	}
	switch msg.Button {
	case tea.MouseButtonLeft:
		m.handleMouseLeft(msg)
	case tea.MouseButtonWheelUp:
		m.scroll += 2
	case tea.MouseButtonWheelDown:
		m.scroll -= 2
		if m.scroll < 0 {
			m.scroll = 0
		}
	}
}

func (m *model) handleMouseLeft(msg tea.MouseMsg) {
	if msg.Action == tea.MouseActionPress {
		if msg.Y >= 0 && msg.Y < m.bodyHeight() {
			m.selecting = true
			m.selMoved = false
			m.selStart = selectPoint{line: msg.Y, col: msg.X}
			m.selCur = m.selStart
		}
		return
	}
	if msg.Action == tea.MouseActionMotion && m.selecting {
		if absInt(msg.Y-m.selCur.line) > 0 || absInt(msg.X-m.selCur.col) > 1 {
			m.selCur = selectPoint{line: msg.Y, col: msg.X}
			if dx, dy := absInt(msg.X-m.selStart.col), absInt(msg.Y-m.selStart.line); dx > 2 || dy > 0 {
				m.selMoved = true
			}
		}
		return
	}
	if msg.Action != tea.MouseActionRelease {
		return
	}
	if !m.selecting {
		return
	}
	m.selCur = selectPoint{line: msg.Y, col: msg.X}
	if !m.selMoved {
		if idx := m.hitExpandableAtY(msg.Y); idx >= 0 && idx < len(m.transcript) {
			m.toggleExpandedAt(idx)
		}
		m.selecting = false
		return
	}
	lines := m.transcriptLines(m.width, m.bodyHeight(), nil)
	text := copySelectionText(lines, m.selStart, m.selCur)
	m.selecting = false
	if err := copyToClipboard(text); err != nil {
		m.notice = "clipboard unavailable: " + err.Error()
		return
	}
	m.notice = fmt.Sprintf("copied selection (%d characters)", len([]rune(text)))
}

func (m *model) toggleExpanded() {
	for i := len(m.transcript) - 1; i >= 0; i-- {
		switch m.transcript[i].kind {
		case kindTool, kindCommand, kindAssistant, kindThinking:
			m.toggleExpandedAt(i)
			return
		}
	}
}

func (m *model) toggleExpandedAt(idx int) {
	if idx < 0 || idx >= len(m.transcript) {
		return
	}
	before := m.transcriptLineCount(m.width)
	m.transcript[idx].expanded = !m.transcript[idx].expanded
	after := m.transcriptLineCount(m.width)
	delta := after - before
	if delta > 0 {
		m.scroll += delta
	} else if delta < 0 {
		m.scroll += delta
		if m.scroll < 0 {
			m.scroll = 0
		}
	}
	h := m.bodyHeight()
	if m.scroll < 0 {
		m.scroll = 0
	}
	if after > h {
		if maxScroll := after - h; m.scroll > maxScroll {
			m.scroll = maxScroll
		}
	} else {
		m.scroll = 0
	}
}

func (m *model) transcriptLineCount(width int) int {
	total := 0
	for _, block := range m.transcript {
		total += len(renderBlockZen(block, width))
	}
	return total
}

func (m *model) handlePaletteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc":
		m.palette = false
		m.paletteQ = ""
		if strings.HasPrefix(m.input, "/") {
			m.input = ""
		}
		return m, nil
	case "up":
		if m.paletteIdx > 0 {
			m.paletteIdx--
		}
		return m, nil
	case "down":
		if m.paletteIdx < len(m.paletteView)-1 {
			m.paletteIdx++
		}
		return m, nil
	case "enter":
		if len(m.paletteView) == 0 {
			return m, nil
		}
		item := m.paletteView[m.paletteIdx]
		m.palette = false
		return m, m.execPaletteItem(item)
	case "backspace":
		if len(m.paletteQ) > 0 {
			m.paletteQ = m.paletteQ[:len([]rune(m.paletteQ))-1]
			m.refreshPalette()
		}
		return m, nil
	default:
		if msg.Type == tea.KeyRunes {
			m.paletteQ += string(msg.Runes)
			m.refreshPalette()
		}
		return m, nil
	}
}

func (m *model) insertRunes(rs []rune) {
	s := string(rs)
	beforeSlash := strings.HasPrefix(m.input, "/")
	m.input += s
	if !m.palette && !beforeSlash && strings.HasPrefix(m.input, "/") {
		m.openPalette(m.input[1:])
		return
	}
	if m.palette {
		m.paletteQ = strings.TrimPrefix(m.input, "/")
		m.refreshPalette()
	}
}

func (m *model) backspace() {
	if m.input == "" {
		return
	}
	r := []rune(m.input)
	m.input = string(r[:len(r)-1])
	if m.palette {
		m.paletteQ = strings.TrimPrefix(m.input, "/")
		m.refreshPalette()
	}
}

func (m *model) submit() tea.Cmd {
	prompt := strings.TrimSpace(m.input)
	if prompt == "" {
		return nil
	}
	// Treat notices as ephemeral: any new user action replaces the previous
	// transient message so the idle status line is never stuck on it.
	m.notice = ""
	m.input = ""
	if !m.fromQueue {
		m.history = append(m.history, prompt)
		m.historyIdx = len(m.history)
	}
	m.fromQueue = false
	if m.running {
		m.queued = append(m.queued, prompt)
		m.notice = fmt.Sprintf("queued %d message(s) — running now", len(m.queued))
		return nil
	}
	if strings.HasPrefix(prompt, "/") {
		return m.execSlashCommand(prompt)
	}
	return m.startRun(prompt)
}

func (m *model) execSlashCommand(raw string) tea.Cmd {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return nil
	}
	name := strings.ToLower(fields[0])
	switch name {
	case "/help":
		m.palette = true
		m.paletteQ = ""
		m.refreshPalette()
	case "/copy":
		m.copyFullTranscript()
	case "/quit", "/exit":
		// Same as the palette: actually leave the TUI.
		return tea.Quit
	case "/clear":
		m.transcript = nil
	case "/new":
		m.newSession()
	case "/revert":
		if len(fields) < 2 {
			m.notice = "usage: /revert <checkpoint-id>"
			return nil
		}
		m.runCLI([]string{"revert", fields[1]})
	case "/workspace":
		if len(fields) > 1 {
			m.notice = "workspace is fixed for this process; restart with a new cwd"
		} else {
			m.notice = m.cwd
		}
	case "/sessions":
		m.openSessions(strings.Join(fields[1:], " "))
	case "/continue", "/resume":
		m.attachSession("latest")
	case "/session", "/attach":
		if len(fields) < 2 {
			m.openSessions("")
			return nil
		}
		m.attachSession(fields[1])
	case "/tasks", "/todo":
		m.openTasks()
	case "/theme":
		args := strings.Join(fields[1:], " ")
		if args != "" {
			if theme, ok := ThemeBySlug(args); ok {
				ApplyTheme(theme.Slug)
				if err := saveActiveTheme(theme.Slug); err == nil {
					m.notice = ""
					return nil
				}
				m.notice = "applied for this session (config save failed)"
				return nil
			}
		}
		m.openTheme(args)
	case "/providers":
		m.openProviders(strings.Join(fields[1:], " "))
	case "/models":
		m.openModels()
	case "/connect":
		if len(fields) < 2 {
			m.openProviders("")
			return nil
		}
		slug := strings.ToLower(fields[1])
		for _, d := range provider.Descriptors() {
			if d.Slug == slug {
				m.beginProviderConnect(d)
				return nil
			}
		}
		m.notice = "unknown provider " + slug
		m.openProviders(slug)
	case "/policy":
		if len(fields) > 1 {
			m.notice = "policy flags are set at launch; shift+tab cycles permissions"
		} else {
			m.notice = approvalNames[m.approval]
		}
	default:
		args := fields[1:]
		m.runCLI(append([]string{strings.TrimPrefix(name, "/")}, args...))
	}
	return nil
}

func (m *model) execPaletteItem(item commandItem) tea.Cmd {
	if item.Name == "/quit" {
		return tea.Quit
	}
	before := m.transcript
	_ = before
	msg := item.Run(m)
	if msg == "quit" {
		return tea.Quit
	}
	if msg != "" {
		m.notice = msg
	}
	return nil
}

func (m *model) newSession() {
	m.transcript = nil
	m.sessionID = ""
	m.finalText = ""
	m.scroll = 0
	m.notice = "new session ready"
}

func (m *model) openPalette(q string) {
	m.palette = true
	m.paletteQ = q
	m.paletteIdx = 0
	m.refreshPalette()
}

func (m *model) refreshPalette() {
	m.paletteView = filterCommands(m.paletteQ)
	if m.paletteIdx >= len(m.paletteView) {
		m.paletteIdx = len(m.paletteView) - 1
	}
	if m.paletteIdx < 0 {
		m.paletteIdx = 0
	}
}

func (m *model) runCLI(args []string) {
	go func() {
		out, err := exec.Command(m.exe, args...).CombinedOutput()
		m.program.Send(commandDoneMsg{args: args, out: string(out), err: err})
	}()
}

func (m *model) appendCommandResult(msg commandDoneMsg) {
	title := "$ creator " + strings.Join(msg.args, " ")
	body := strings.TrimRight(msg.out, "\n")
	if body == "" {
		body = "(no output)"
	}
	m.transcript = append(m.transcript, block{kind: kindCommand, title: title, body: body, ok: msg.err == nil, at: time.Now()})
	m.scroll = 0
	if msg.err != nil {
		m.notice = strings.Join(msg.args, " ") + " exited with an error"
	}
}

func (m *model) insertText(s string) { m.input += s }

func (m *model) historyPrev() {
	if len(m.history) == 0 {
		return
	}
	if m.historyIdx <= 0 {
		m.historyIdx = 0
	} else {
		m.historyIdx--
	}
	m.input = m.history[m.historyIdx]
}
func (m *model) historyNext() {
	if len(m.history) == 0 {
		return
	}
	if m.historyIdx >= len(m.history) {
		m.historyIdx = len(m.history)
		m.input = ""
		return
	}
	m.historyIdx++
	if m.historyIdx < len(m.history) {
		m.input = m.history[m.historyIdx]
	} else {
		m.input = ""
	}
}

func (m *model) startRun(prompt string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.running = true
	m.runCancel = cancel
	m.interrupted = false
	m.runStarted = time.Now()
	m.runTokensIn, m.runTokensOut = 0, 0
	m.toolCount = 0
	m.lastTool = ""
	m.finalText = ""
	m.streamed = false
	m.runDone = make(chan struct{})
	m.transcript = append(m.transcript, block{kind: kindUser, title: "YOU", body: prompt, at: time.Now()})
	m.transcript = append(m.transcript, block{kind: kindThinking, title: "thinking", streaming: true, at: time.Now()})
	m.scroll = 0

	args := []string{"run", "--json"}
	args = append(args, m.policyArgs()...)
	if m.opts.Provider != "" {
		args = append(args, "--provider", m.opts.Provider)
	}
	if m.opts.Model != "" {
		args = append(args, "--model", m.opts.Model)
	}
	if m.opts.BaseURL != "" {
		args = append(args, "--base-url", m.opts.BaseURL)
	}
	if m.opts.DataDir != "" {
		args = append(args, "--data-dir", m.opts.DataDir)
	}
	if m.opts.NoKnowledge {
		args = append(args, "--no-knowledge")
	}
	if m.opts.WorkspacePlugins {
		args = append(args, "--workspace-plugins")
	}
	if m.sessionID != "" {
		args = append(args, "--session", m.sessionID)
	}
	args = append(args, "--") // prompt is sent over stdin; argv is tiny and Windows-safe

	cmd := exec.CommandContext(ctx, m.exe, args...)
	cmd.Dir = m.cwd
	cmd.Stdin = strings.NewReader(prompt)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		m.failStart(cancel, err)
		return nil
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		m.failStart(cancel, err)
		return nil
	}
	if err := cmd.Start(); err != nil {
		m.failStart(cancel, err)
		return nil
	}

	stdoutDone := make(chan struct{})
	stderrDone := make(chan struct{})
	go drainLines(stdout, stdoutDone, func(line string) { m.program.Send(childLineMsg{line: line}) })
	go drainLines(stderr, stderrDone, func(line string) { m.program.Send(childErrLineMsg{line: line}) })
	go func() {
		<-stdoutDone
		<-stderrDone
		err := cmd.Wait()
		m.program.Send(childDoneMsg{err: err})
	}()
	return tick(70 * time.Millisecond)
}

func (m *model) failStart(cancel context.CancelFunc, err error) {
	m.running = false
	m.runCancel = nil
	cancel()
	m.transcript = append(m.transcript, block{kind: kindNotice, title: "engine", body: err.Error(), ok: false, at: time.Now()})
}

func drainLines(r io.Reader, done chan<- struct{}, fn func(string)) {
	defer close(done)
	scanLines(r, fn)
}

func scanLines(r io.Reader, fn func(string)) {
	if fn == nil {
		_, _ = io.Copy(io.Discard, r)
		return
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 32<<20)
	for sc.Scan() {
		if line := strings.TrimRight(sc.Text(), "\r"); line != "" {
			fn(line)
		}
	}
	if err := sc.Err(); err != nil {
		// A too-long line halts the scanner and wedges the reader: report
		// it instead of going silent mid-stream.
		fn(fmt.Sprintf("[creator: output stream interrupted: %v]", err))
	}
}

func (m *model) policyArgs() []string {
	switch m.approval {
	case approvalManual:
		return []string{"--policy", "strict"}
	case approvalAcceptEdits:
		return []string{"--policy", "balanced"}
	case approvalBypass:
		args := []string{"--policy", "yolo", "--allow-critical"}
		_ = args
		return args
	default:
		return []string{"--policy", "balanced"}
	}
}

func (m *model) interrupt(source string) {
	if m.runCancel != nil {
		m.runCancel()
		m.notice = "interrupted by " + source
		m.interrupted = true
	}
}

func (m *model) finishRun(err error) {
	m.running = false
	m.runCancel = nil
	m.closeStreamingAssistant()
	if m.runDone != nil {
		close(m.runDone)
		m.runDone = nil
	}

	if err != nil && m.finalText == "" {
		if m.interrupted {
			// A deliberate interrupt is not a crash — no red engine error.
			m.transcript = append(m.transcript, block{kind: kindNotice, title: "engine", body: "interrupted — the run was stopped on request", ok: true, at: time.Now()})
		} else {
			m.transcript = append(m.transcript, block{kind: kindNotice, title: "engine", body: err.Error(), ok: false, at: time.Now()})
		}
	}
	m.interrupted = false
	m.scroll = 0
	if len(m.queued) > 0 {
		next := m.queued[0]
		m.queued = m.queued[1:]
		m.input = next
		// The queued prompt already went into history at queue time; mark
		// this run so submit() doesn't record it a second time.
		m.fromQueue = true
		m.submit()
	}
}

type jsonEvent struct {
	Type    string            `json:"type"`
	Text    string            `json:"text,omitempty"`
	Tool    string            `json:"tool,omitempty"`
	Args    json.RawMessage   `json:"args,omitempty"`
	Result  string            `json:"result,omitempty"`
	Status  string            `json:"status,omitempty"`
	Session string            `json:"session,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
	Usage   *struct {
		In  int `json:"in"`
		Out int `json:"out"`
	} `json:"usage,omitempty"`
}

func (m *model) consumeEvent(line string, fromStderr bool) {
	trim := strings.TrimSpace(line)
	if strings.HasPrefix(trim, "session ") {
		m.sessionID = strings.Fields(trim)[1]
		return
	}
	if strings.Contains(trim, "session ") && strings.HasPrefix(trim, "resuming") {
		parts := strings.Fields(trim)
		for i, p := range parts {
			if p == "session" && i+1 < len(parts) {
				m.sessionID = parts[i+1]
				return
			}
		}
	}
	var ev jsonEvent
	if err := json.Unmarshal([]byte(trim), &ev); err != nil {
		m.transcript = append(m.transcript, block{kind: kindNotice, title: "engine", body: line, ok: false, at: time.Now()})
		return
	}
	switch ev.Type {
	case "assistant_delta":
		m.appendAssistantDelta(ev.Text)
	case "reasoning_delta":
		m.appendReasoning(ev.Text)
	case "tool_start":
		m.closeStreamingAssistant()
		m.toolCount++
		m.lastTool = ev.Tool
		m.transcript = append(m.transcript, block{kind: kindTool, title: ev.Tool, body: strings.TrimSpace(string(ev.Args)), meta: "running", running: true, at: time.Now()})
	case "tool_progress":
		m.updateToolProgress(ev)
	case "tool_end":
		m.finishTool(ev)
	case "notice":
		if ev.Text == "user turn" {
			return
		}
		m.transcript = append(m.transcript, block{kind: kindNotice, title: "notice", body: ev.Text, ok: true, at: time.Now()})
	case "usage":
		if ev.Usage != nil {
			m.runTokensIn = ev.Usage.In
			m.runTokensOut = ev.Usage.Out
		}
	case "done":
		// Completion markers from the engine are transport noise; the result event is enough.
		return
	case "assistant":
		if strings.TrimSpace(ev.Text) != "" {
			m.appendAssistantFinal(ev.Text)
		}
	case "result":
		m.closeStreamingAssistant()
		if ev.Session != "" {
			m.sessionID = ev.Session
		}
		m.finalText = ev.Text
		if strings.TrimSpace(ev.Text) != "" && !m.streamed {
			m.appendAssistantFinal(ev.Text)
		}
	default:
		if ev.Type != "" {
			m.transcript = append(m.transcript, block{kind: kindNotice, title: ev.Type, body: ev.Text, at: time.Now()})
		}
	}
}

func (m *model) closeStreamingAssistant() {
	for i := range m.transcript {
		if m.transcript[i].kind == kindAssistant && m.transcript[i].streaming {
			m.transcript[i].streaming = false
		}
		if m.transcript[i].kind == kindThinking && m.transcript[i].streaming {
			m.transcript[i].streaming = false
		}
	}
}

func (m *model) updateToolProgress(ev jsonEvent) {
	for i := len(m.transcript) - 1; i >= 0; i-- {
		if m.transcript[i].kind != kindTool || !m.transcript[i].running {
			continue
		}
		if ev.Tool != "" && m.transcript[i].title != ev.Tool {
			continue
		}
		m.transcript[i].tail = ev.Text
		m.transcript[i].meta = "running"
		if ev.Meta != nil {
			if b, err := strconv.ParseInt(ev.Meta["bytes"], 10, 64); err == nil {
				m.transcript[i].bytes = b
			}
			if status := ev.Meta["status"]; status != "" && status != "running" {
				m.transcript[i].meta = status
			}
		}
		return
	}
}

func (m *model) appendAssistantDelta(s string) {
	m.streamed = true
	for i := range m.transcript {
		if m.transcript[i].kind == kindThinking && m.transcript[i].streaming {
			m.transcript[i].streaming = false
		}
	}
	for i := range m.transcript {
		if m.transcript[i].kind == kindAssistant && m.transcript[i].streaming {
			m.transcript[i].body += s
			return
		}
	}
	m.transcript = append(m.transcript, block{kind: kindAssistant, title: "AI CREATOR", body: s, streaming: true, at: time.Now()})
}

func (m *model) appendReasoning(s string) {
	for i := range m.transcript {
		if m.transcript[i].kind == kindThinking && m.transcript[i].streaming {
			m.transcript[i].body += s
			return
		}
	}
	m.transcript = append(m.transcript, block{kind: kindThinking, title: "thinking", body: s, streaming: true, at: time.Now()})
}

func (m *model) appendAssistantFinal(s string) {
	for i := range m.transcript {
		if m.transcript[i].kind == kindAssistant && m.transcript[i].streaming {
			m.transcript[i].body = strings.TrimSpace(s)
			m.transcript[i].streaming = false
			return
		}
	}
	m.transcript = append(m.transcript, block{kind: kindAssistant, title: "AI CREATOR", body: s, at: time.Now()})
}

func (m *model) finishTool(ev jsonEvent) {
	status := ev.Status
	if status == "" {
		status = "done"
	}
	for i := len(m.transcript) - 1; i >= 0; i-- {
		if m.transcript[i].kind == kindTool && m.transcript[i].running {
			m.transcript[i].running = false
			m.transcript[i].tail = ev.Result
			m.transcript[i].meta = status
			m.transcript[i].ok = status != "error"
			return
		}
	}
	m.transcript = append(m.transcript, block{kind: kindTool, title: ev.Tool, tail: ev.Result, meta: status, ok: status != "error", at: time.Now()})
}

func buildFileTree(root string, depth int, maxFiles int) string {
	type row struct {
		depth int
		label string
	}
	var rows []row
	count := 0
	ignored := map[string]bool{".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true, "target": true, ".next": true, ".cache": true}
	var walk func(dir string, d int)
	walk = func(dir string, d int) {
		if d > depth || count >= maxFiles {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if count >= maxFiles {
				return
			}
			name := e.Name()
			if ignored[name] {
				continue
			}
			prefix := strings.Repeat("  ", d)
			if e.IsDir() {
				rows = append(rows, row{depth: d, label: prefix + "▸ " + name + "/"})
				walk(filepath.Join(dir, name), d+1)
				continue
			}
			rows = append(rows, row{depth: d, label: prefix + "· " + name})
			count++
		}
	}
	rows = append(rows, row{depth: 0, label: filepath.Base(filepath.Clean(root)) + string(filepath.Separator)})
	walk(root, 1)
	if count >= maxFiles {
		rows = append(rows, row{depth: 0, label: "… truncated"})
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.label)
	}
	return strings.Join(out, "\n")
}
