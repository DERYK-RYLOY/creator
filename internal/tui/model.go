package tui

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/DERYK-RYLOY/creator/internal/provider"

	tea "github.com/charmbracelet/bubbletea"
)

type options struct {
	Creator          string
	Provider         string
	Model            string
	BaseURL          string
	Policy           string
	AllowCritical    bool
	DataDir          string
	NoKnowledge      bool
	WorkspacePlugins bool
	Verbose          bool
	Theme            string
	Version          string
}

type approvalMode int

const (
	approvalManual approvalMode = iota
	approvalAcceptEdits
	approvalBypass
)

var approvalNames = []string{"manual permissions", "accept edits", "bypass permissions"}

type overlayMode int

const (
	overlayNone overlayMode = iota
	overlayProviders
	overlayProviderKey
	overlayModels
	overlaySessions
	overlayTasks
	overlayTheme
	overlayHelp
)

type modelRow struct {
	Provider string
	Model    string
	Free     bool
	Note     string
	Source   string
}

type sessionRow struct {
	ID        string
	Updated   string
	Model     string
	Workspace string
	Turns     int
}

type providerStepMsg struct {
	step provider.Step
	gen  int // connect generation; stale generations are dropped
}
type providerVerifyMsg struct {
	result provider.VerifyResult
	err    error
	gen    int // connect generation; stale generations are dropped
}
type modelsDoneMsg struct {
	rows    []modelRow
	sources []string
	gen     int // scan generation; stale generations are dropped
}
type modelBatchMsg int

type blockKind string

const (
	kindWelcome   blockKind = "welcome"
	kindUser      blockKind = "user"
	kindAssistant blockKind = "assistant"
	kindThinking  blockKind = "thinking"
	kindTool      blockKind = "tool"
	kindNotice    blockKind = "notice"
	kindResult    blockKind = "result"
	kindCommand   blockKind = "command"
	kindSwarm     blockKind = "swarm"
)

type block struct {
	kind      blockKind
	title     string
	body      string
	meta      string
	ok        bool
	running   bool
	streaming bool
	expanded  bool
	tail      string
	bytes     int64
	at        time.Time
}

type selectPoint struct {
	line int
	col  int
}

// renderCache memoizes the fully rendered, lens-filtered transcript lines
// (pre-scroll). Scrolling only slices the cached slice, so mouse-wheel
// frames no longer re-render every block.
type blockRange struct{ start, end, idx int }

type renderCache struct {
	valid  bool
	bodyW  int
	gen    int64
	lens   int
	search string // active search query; "" when search is closed or query empty
	theme  string
	lines  []string
	ranges []blockRange
}

type model struct {
	opts   options
	exe    string
	width  int
	height int
	cwd    string

	gitBranch      string // detected once at startup from .git/HEAD
	sidebar        bool   // right-hand sidebar visibility
	sidebarTouched bool   // user pressed ctrl+b — stop auto-toggling on resize

	input      string
	history    []string
	historyIdx int

	transcript []block
	queued     []string
	fromQueue  bool

	running                   bool
	runCancel                 context.CancelFunc
	interrupted               bool
	runStarted                time.Time
	runTokensIn, runTokensOut int
	toolCount                 int
	lastTool                  string
	sessionID                 string
	finalText                 string
	streamed                  bool

	approval approvalMode

	palette     bool
	paletteQ    string
	paletteIdx  int
	paletteView []commandItem

	scroll int
	gen    int64 // render generation — bumped whenever transcript rendering could change

	// highlight lens — 35 one-keystroke filters for the conversation
	lensIdx    int // 0 = off, 1..len(lensCatalog) = active (1 = lensCatalog[0])
	lensHits   int // occurrences across visible blocks (set on render)
	lensHidden int // non-user blocks hidden by the active lens

	// in-transcript search (ctrl+f)
	searchActive bool
	searchQuery  string
	matchIdx     int
	searchTotal  int
	matchLines   []int // line index per match ordinal, rebuilt with the cache

	// slash-command frecency
	recentCmds []string

	renderCache renderCache

	// last completed run stats (for /usage)
	lastRunTools     int
	lastRunTokensIn  int
	lastRunTokensOut int
	lastRunDuration  time.Duration

	overlay      overlayMode
	overlayQuery string
	overlayIdx   int
	overlayOff   int

	pendingProvider provider.Descriptor
	keyInput        string
	keyBusy         bool
	keySteps        []string
	keyError        string

	modelRows     []modelRow
	modelsBusy    bool
	modelsDone    atomic.Int64
	modelsTotal   int
	modelsSources []string
	modelsScanGen int
	verifyGen     int
	verifyCancel  context.CancelFunc

	runDone     chan struct{}
	sessionRows []sessionRow
	taskRows    []taskBook
	themeRows   []ThemeRow
	themeBefore string

	selecting bool
	selMoved  bool
	selStart  selectPoint
	selCur    selectPoint

	notice string
	err    string

	program *tea.Program

	animT    float64   // animation clock, advanced on every tick
	morph    float64   // 0 = idle wordmark, 1 = thinking wave (eased)
	lastTick time.Time // timestamp of the previous tick
}
