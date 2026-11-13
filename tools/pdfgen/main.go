package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/go-pdf/fpdf"
)

const (
	pageW  = 210.0
	margin = 18.0
	bottom = 274.0
	lh     = 4.9
	pad    = 2.2
)

var pdf *fpdf.Fpdf

func ensure(h float64) {
	if pdf.GetY()+h > bottom {
		pdf.AddPage()
	}
}

func title(t string) {
	pdf.SetFont("Helvetica", "B", 26)
	pdf.SetTextColor(18, 42, 84)
	pdf.MultiCell(0, 12, t, "", "L", false)
	pdf.Ln(2)
	pdf.SetDrawColor(30, 110, 200)
	pdf.SetLineWidth(1.4)
	pdf.Line(margin, pdf.GetY(), pageW-margin, pdf.GetY())
	pdf.SetLineWidth(0.2)
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(8)
}

func subtitle(t string) {
	pdf.SetFont("Helvetica", "", 12)
	pdf.SetTextColor(70, 70, 70)
	pdf.MultiCell(0, 6.5, t, "", "L", false)
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(3)
}

func h1(t string) {
	ensure(24)
	pdf.Ln(4)
	pdf.SetFont("Helvetica", "B", 15)
	pdf.SetTextColor(18, 42, 84)
	pdf.MultiCell(0, 8, t, "", "L", false)
	pdf.SetTextColor(0, 0, 0)
	pdf.SetDrawColor(30, 110, 200)
	pdf.SetLineWidth(0.6)
	pdf.Line(margin, pdf.GetY(), pageW-margin, pdf.GetY())
	pdf.SetLineWidth(0.2)
	pdf.SetDrawColor(0, 0, 0)
	pdf.Ln(5)
}

func h2(t string) {
	ensure(16)
	pdf.Ln(2)
	pdf.SetFont("Helvetica", "B", 11.5)
	pdf.SetTextColor(40, 40, 40)
	pdf.MultiCell(0, 6.5, t, "", "L", false)
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(1.5)
}

func p(t string) {
	pdf.SetFont("Helvetica", "", 9.8)
	pdf.MultiCell(0, lh, t, "", "L", false)
	pdf.Ln(2.6)
}

func bullet(t string) {
	pdf.SetFont("Helvetica", "", 9.8)
	pdf.SetX(margin + 2)
	pdf.CellFormat(4, lh, "-", "", 0, "L", false, 0, "")
	pdf.MultiCell(0, lh, t, "", "L", false)
	pdf.Ln(0.8)
}

func code(lines []string) {
	h := float64(len(lines))*4.5 + 5
	ensure(h + 4)
	y := pdf.GetY()
	x := margin + 3
	w := pageW - 2*margin - 6
	pdf.SetFillColor(244, 246, 249)
	pdf.SetDrawColor(210, 215, 222)
	pdf.Rect(x, y, w, h, "DF")
	pdf.SetFont("Courier", "", 8.5)
	pdf.SetTextColor(35, 35, 35)
	pdf.SetXY(x+3, y+2.5)
	for _, l := range lines {
		pdf.CellFormat(w-6, 4.5, l, "", 0, "L", false, 0, "")
		pdf.SetXY(x+3, pdf.GetY()+4.5)
	}
	pdf.SetTextColor(0, 0, 0)
	pdf.SetXY(margin, y+h+3)
}

type table struct {
	headers []string
	widths  []float64
	aligns  []string
	rows    [][]string
}

func (t *table) headerRow() {
	t.drawRow(t.headers, true, true)
}

func (t *table) drawRow(cells []string, bold, fill bool) {
	if bold {
		pdf.SetFont("Helvetica", "B", 8.8)
	} else {
		pdf.SetFont("Helvetica", "", 8.8)
	}
	maxLines := 1
	for i, c := range cells {
		n := len(pdf.SplitText(c, t.widths[i]-2*pad))
		if n == 0 {
			n = 1
		}
		if n > maxLines {
			maxLines = n
		}
	}
	h := float64(maxLines)*lh + 2*pad
	if os.Getenv("PDFDEBUG") != "" {
		fmt.Printf("row y=%.1f lines=%d h=%.1f first=%q\n", pdf.GetY(), maxLines, h, truncateRunes(cells[0], 30))
	}
	if pdf.GetY()+h > bottom {
		pdf.AddPage()
		t.headerRow()
	}
	x := pdf.GetX()
	y := pdf.GetY()
	if fill {
		pdf.SetFillColor(236, 242, 250)
		pdf.Rect(x, y, sum(t.widths), h, "DF")
	}
	pdf.SetDrawColor(198, 206, 216)
	for i, c := range cells {
		w := t.widths[i]
		pdf.Rect(x, y, w, h, "D")
		pdf.SetXY(x+pad, y+pad)
		pdf.MultiCell(w-2*pad, lh, c, "", t.aligns[i], false)
		x += w
	}
	pdf.SetDrawColor(0, 0, 0)
	pdf.SetXY(margin, y+h)
}

func (t *table) render() {
	pdf.SetX(margin)
	t.headerRow()
	for _, r := range t.rows {
		t.drawRow(r, false, false)
	}
	pdf.Ln(5)
}

// truncateRunes cuts s at a rune boundary — byte-slicing mid-multibyte
// produces invalid UTF-8 in the debug log.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func sum(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s
}

func tbl(headers []string, widths []float64, rows [][]string) {
	n := len(headers)
	// aligns and widths are indexed per cell: both MUST match headers.
	adjust := make([]string, n)
	for i := range adjust {
		adjust[i] = "L"
	}
	if len(widths) != n {
		// Deterministic fallback instead of a latent index-out-of-range.
		total := pageW - 2*margin
		widths = make([]float64, n)
		for i := range widths {
			widths[i] = total / float64(n)
		}
	}
	t := &table{headers: headers, widths: widths, aligns: adjust, rows: rows}
	t.render()
}

func main() {
	pdf = fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(margin, margin, margin)
	pdf.SetAutoPageBreak(true, 16)
	pdf.SetTitle("Creator v0.1 - Corrected Plan and Build Status", true)
	pdf.SetAuthor("Creator build session", true)
	pdf.SetFooterFunc(func() {
		pdf.SetY(-13)
		pdf.SetFont("Helvetica", "I", 8)
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(pageW-2*margin, 5, "Creator v0.1 - corrected plan and build status", "", 1, "L", false, 0, "")
		pdf.CellFormat(pageW-2*margin, 5, fmt.Sprintf("page %d", pdf.PageNo()), "", 0, "R", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
	})

	// ---------------------------------------------------------------- cover
	pdf.AddPage()
	pdf.Ln(24)
	title("Creator")
	subtitle("Corrected plan and build status - version 0.1")
	pdf.Ln(2)
	pdf.SetFont("Helvetica", "", 10.5)
	pdf.SetTextColor(60, 60, 60)
	for _, l := range []string{
		"Date: 29 October 2026",
		"Source: the design conversation (three pasted plans, your corrections, four decisions you made)",
		"Module: github.com/creatorcli/creator",
		"Code: 7,259 lines of Go across 29 files, 11 packages",
		"Artifact: bin/creator.exe - 9,051,648 bytes, no CGO, no runtime dependencies",
		"Status: builds clean, all tests pass, end-to-end verified against a fake model server",
	} {
		pdf.MultiCell(0, 6.2, l, "", "L", false)
	}
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(8)
	pdf.SetFillColor(236, 242, 250)
	pdf.SetDrawColor(30, 110, 200)
	w := pageW - 2*margin
	pdf.Rect(margin, pdf.GetY(), w, 34, "DF")
	pdf.SetXY(margin+5, pdf.GetY()+5)
	pdf.SetFont("Helvetica", "B", 10.5)
	pdf.MultiCell(w-10, 5.5, "One-line summary:", "", "L", false)
	pdf.SetXY(margin+5, pdf.GetY()+1)
	pdf.SetFont("Helvetica", "", 10)
	pdf.MultiCell(w-10, 5.2,
		"Creator is a single static Go binary that is an agent loop, an MCP server and a set of manual CLI tools sharing one tool registry. Its defining rule: the model never sees raw command output, only a token-budgeted digest plus a handle it can page through.",
		"", "L", false)

	// ---------------------------------------------------- 1. origin of plan
	pdf.AddPage()
	h1("1. Where the plan came from")
	p("Three separate plans were in play at the start, pasted together. They were written for three different products, and two of them contradicted each other:")
	bullet("Plan A - the Creator AI workspace CLI: an agent loop with a background job table, checkpointing, output digests instead of raw logs, an MCP server, a TUI, session journaling.")
	bullet("Plan B - a systems/OS plan: mmap zero-copy rings, a custom epoll loop, predictive page faulting, Merkle-try token compression, WASM as the operating-system shim, eBPF/ETW audit hooks, TPM and sealed enclaves.")
	bullet("Plan C - a security/EDR plan: Rowhammer mitigation, SMM inspection, power-line mesh, thermal side channels, LSASS dumping detection, malware evasion testing, TPM attestation.")
	p("A single binary cannot be all three: A is a developer tool, B is a kernel project, C is an endpoint-protection product. The first correction was therefore subtractive - decide what Creator is not, then build the rest properly.")

	h1("2. Corrections applied to the original plan")
	p("Every row below is a change made during the conversation, with the reason it was made.")
	tbl([]string{"Original plan item", "Correction applied", "Reason"}, []float64{50, 62, 62}, [][]string{
		{"Stratum III: Rowhammer, SMM, power-line mesh, thermal side-channels", "Removed entirely from scope", "No overlap with an agent CLI. It is an antivirus feature set; keeping it would have stopped the CLI from ever shipping."},
		{"LSASS dumping detection, malware evasion, TPM attestation (second paste)", "Removed entirely from scope", "Same reason. Mixing security-tooling with a workspace CLI means neither product gets built."},
		{"mmap zero-copy rings, custom epoll loops, predictive page faulting", "Removed; replaced by Go net, os/exec and buffered I/O", "Go's runtime already multiplexes non-blocking I/O. In Go this is a complexity tax that buys no measurable speed."},
		{"Merkle trie for token compression", "Replaced by digest + resumable handle", "The real win is addressing, not hashing. A handle the model can page (grep / range / tail) is about 100x simpler and gives strictly more capability."},
		{"WASM as the primary execution path / OS shim", "Demoted to an optional sandbox tier; a real shell/PTY is the main path", "A CLI that cannot run 'make' or 'npm install' is not a CLI. Sandboxing untrusted tools stays as a later tier."},
		{"eBPF/ETW audit hooks, TPM, sealed enclaves", "Declared out of v0.1 and written down as non-goals", "Platform-specific and unprovable in this environment; not needed to demonstrate the core thesis."},
		{"TUI, headless mode and MCP all in the first pass", "Headless core only (your decision, section 3)", "Ship the loop before the pixels. The TUI becomes a thin consumer of the same core later."},
		{"One or two model backends", "Two wire protocols, 17 registry entries, local models included (your decision)", "You asked for every well-known provider plus local. Two protocols cover all of them without vendor SDKs."},
		{"Token accounting as a nice-to-have", "Promoted to a budget governor inside the loop", "Without it a long tool-heavy turn silently overruns the context window and the session dies mid-task."},
	})

	h1("3. Decisions you made, and what each one changed")
	tbl([]string{"Question I put to you", "Your answer", "What it meant for the build"}, []float64{46, 54, 74}, [][]string{
		{"How much should be built in this first pass?", "Headless core only", "No TUI code was written. The deliverable is 'creator run', 'creator mcp' and the manual subcommands (shell, jobs, out, sessions, revert, providers, plugins)."},
		{"Which model backend should v0.1 ship with?", "EVERY POSSIBLE AND FAMOUS PROVIDER OUT THERE PLUS ALSO LOCAL IF THE USER WANTS TO USE A LOCAL MODEL", "internal/provider gained OpenAI-protocol and Anthropic-protocol clients plus a 17-entry registry: OpenAI, Anthropic, Azure OpenAI, OpenRouter, Groq, Together, Fireworks, Cerebras, Mistral, DeepSeek, xAI, Perplexity, Google Gemini, Cohere, Ollama, LM Studio and any custom OpenAI-shaped base URL. No vendor SDK dependencies - plain HTTP. Local defaults to http://127.0.0.1:11434/v1."},
		{"Go is not installed on this machine. How should it be installed?", "Install via winget", "winget install GoLang.Go succeeded (Go 1.27). go.mod declares go 1.26 because bbolt 1.5 requires it."},
		{"We are also using Lua, and Lua", "Read as: Lua is the extension language", "internal/plugin built on gopher-lua: user plugins in .creator/plugins/*.lua can register tools that join the same registry the agent and MCP server expose. Sandbox removes os, io, package, require, dofile, loadfile, load, loadstring, collectgarbage and the global environment. MEANING NOT YET CONFIRMED - see section 7."},
	})

	// ------------------------------------------- 4. corrected plan / scope
	pdf.AddPage()
	h1("4. The corrected v0.1 plan")
	p("One static binary, three modes, one tool registry, one contract between the model and the machine:")
	code([]string{
		"creator run \"fix the failing tests\"   headless agent: journal, budget, tools",
		"creator mcp                            MCP stdio server (protocol 2025-06-18)",
		"creator shell | jobs | out | sessions  the same tools, driven by a human",
		"         | revert | providers | plugins",
	})
	h2("The four properties that make it not a wrapper")
	bullet("Digest, not raw output. Every command writes full output to an artifact and returns a summary: diagnostics clustered by normalized message with repetition counts, search hits grouped by file, diffs reduced to per-file stats, listings reduced to directory and extension histograms. Head and tail are always preserved. The model pages detail with out(handle, mode=grep|range|tail).")
	bullet("Everything is a job. Exec runs in the background by default, gets an XID, lands in a job table, streams into a ring buffer, and can be waited, listed or killed. Nothing blocks the loop. Timeouts kill the whole process tree.")
	bullet("Every mutation is a checkpoint. write and edit snapshot the prior bytes first, so creator revert restores them. Undo is a primitive, not a feature.")
	bullet("Every command is parsed and tiered. A bash tokenizer and AST walk classify each command safe / moderate / critical; critical commands are refused in non-interactive mode and gated behind --yes interactively.")
	h2("Package map")
	tbl([]string{"Package", "Responsibility", "Status"}, []float64{38, 112, 24}, [][]string{
		{"cmd/creator", "CLI entrypoint: run, mcp, shell, jobs, out, sessions, revert, providers, plugins, config", "DONE"},
		{"internal/provider", "Provider interface, two wire protocols, 17-entry registry, HTTP transport with retries and error parsing", "DONE"},
		{"internal/reduce", "Output reducer: five output kinds, duplicate-run collapse, token estimation, digest + handle trailer", "DONE"},
		{"internal/policy", "Bash tokenizer, AST walker, three tiers, platform rules, critical-block + --yes override", "DONE"},
		{"internal/runner", "Job engine: background start, ring buffer, artifact file, timeout, process-tree kill (unix + windows)", "DONE"},
		{"internal/tools", "The tool registry and schemas: read, write, edit, glob, grep, shell, jobs, out + checkpointing", "DONE"},
		{"internal/agent", "Tool-calling loop, tool-choice steering, token budget governor, session persistence", "DONE"},
		{"internal/store", "bbolt session journal: save, load, list, resume", "DONE"},
		{"internal/mcp", "MCP stdio server: initialize, tools/list, tools/call, ping, protocol 2025-06-18", "DONE"},
		{"internal/plugin", "Lua plugin layer on gopher-lua: sandbox, config access, tool registration into the shared registry", "DONE"},
		{"internal/config, internal/id", "CREATOR_* config resolution; prefixed id generation (j_ for jobs)", "DONE"},
	})

	h1("5. What has been done")
	p("Status of every component of the corrected plan.")
	tbl([]string{"Planned item", "Delivered", "Status"}, []float64{46, 104, 24}, [][]string{
		{"Toolchain via winget", "Go 1.27 installed, builds run from the project", "DONE"},
		{"Module + config + id", "go.mod (go 1.26), internal/config, internal/id", "DONE"},
		{"Provider layer, all majors + local", "internal/provider: interface, OpenAI protocol, Anthropic protocol, 17 backends, retries, streaming-shaped response parsing", "DONE"},
		{"Output reducer with handles", "internal/reduce: auto kind detection, clustering, collapse, budget fit, handle trailer with paging instructions", "DONE"},
		{"AST command policy", "internal/policy: safe/moderate/critical, blocked patterns, platform rules, secret-scan shape", "DONE"},
		{"Exec / job engine", "internal/runner: background XIDs, artifacts, ring buffer, tree-kill timeout, exit codes, cross-platform process handling", "DONE"},
		{"Tool layer + checkpointing", "internal/tools: read, write, edit, glob, grep, shell, jobs, out with JSON schemas and snapshot-before-write", "DONE"},
		{"Agent loop + budget governor + journal", "internal/agent + internal/store: tool loop, tool-choice steering, token accounting, bbolt persistence, resume", "DONE"},
		{"MCP stdio server", "internal/mcp: full handshake, tools/list, tools/call over stdio", "DONE"},
		{"Lua extension layer", "internal/plugin: sandboxed plugins registering tools into the same registry", "DONE"},
		{"Tests", "8 packages with suites: reducer, policy, runner, tools, provider, agent, mcp, plugin", "DONE"},
		{"End-to-end proof", "Fake OpenAI server driving the real binary; MCP over real stdio; policy refusal; cross-process out/jobs", "DONE"},
		{"Docs and hygiene", "README with architecture, build, verification log, limits and non-goals; .gitignore; gofmt clean", "DONE"},
		{"TUI (deferred by your call)", "Not started - headless first", "DEFERRED"},
		{"Streaming responses", "Not started - responses arrive whole", "NOT STARTED"},
		{"Lua meaning confirmation", "Open question back to you (section 7)", "OPEN"},
	})

	h1("6. Verification - what was actually run")
	p("Not a claim of completion; the commands and their results.")
	tbl([]string{"Check", "Command", "Result"}, []float64{40, 60, 74}, [][]string{
		{"Unit and package tests", "go test ./...", "PASS - 8 packages ok (agent, mcp, plugin, policy, provider, reduce, runner, tools); 3 packages have no tests (config, id, store); 0 failures"},
		{"Static analysis", "go vet ./...", "PASS - no output"},
		{"Formatting", "gofmt -l -w", "PASS - no files left unformatted"},
		{"Build", "go build -o bin/creator.exe ./cmd/creator", "PASS - 9,051,648 bytes, static, no CGO"},
		{"Policy enforcement", "creator shell \"rm -rf /\"", "exit=3, tier=critical, blocked in non-interactive mode"},
		{"Policy permission", "creator shell \"echo hi\"", "exit=0, tier=safe, ran"},
		{"Agent end-to-end", "fake OpenAI server + creator run", "Model issued a shell tool call, Creator ran it as a job, digest returned, model produced a final answer; session persisted to bbolt"},
		{"MCP handshake", "creator mcp over stdio", "Returned protocolVersion 2025-06-18, serverInfo creator/0.1.0, 8 tools advertised"},
		{"MCP tool call", "tools/call shell over stdio", "Executed the command and returned content with isError=false"},
		{"Cross-process paging", "creator shell ... then creator jobs / creator out --tail", "Artifact written by the job engine read back by a second process"},
		{"Resume", "creator run --continue \"...\"", "Prior turns loaded from the journal and replayed into the context"},
	})

	h1("7. What is NOT done, and what I need from you")
	h2("Open question - Lua")
	p("Your message arrived as 'we are also using Lua, and Lua' with no detail. I implemented the most useful reading: Lua as the extension language, where a user plugin defines tools that join the same registry the agent and the MCP server use, in a sandbox with no file, process or package access. If you meant something else - Lua hooks around every tool call, Lua driving the agent loop itself, or Lua scripts that are allowed to shell out - say so, because the sandbox boundary is a design decision worth confirming before it hardens.")
	h2("Known limits of v0.1")
	bullet("No streaming: model responses arrive whole, so long turns look silent until they finish.")
	bullet("No TUI: deferred by your decision; the headless core is the TUI's future substrate.")
	bullet("Lua tools are pure: a plugin can compute and return JSON, but cannot yet call shell/read/write or register before/after hooks.")
	bullet("In-flight jobs are not reattached across process restarts - the job table lives in the running process, artifacts survive it.")
	bullet("No live-provider integration tests: no API keys were present, so provider code is covered by httptest-based tests only.")
	bullet("No packaging or CI: no scoop/brew formula, no Dockerfile, no GitHub Actions.")
	bullet("Non-goals, carried forward from the corrections: eBPF/ETW audit hooks, TPM/sealed enclaves, Rowhammer/SMM/power-line/thermal analysis, WASM as the primary exec path.")

	h1("8. Suggested next steps")
	bullet("Confirm the Lua intent, then extend the plugin layer with tool-calling from Lua and before/after hooks if that is what you meant.")
	bullet("Add streaming so long turns report progress, both on the CLI and as MCP notifications.")
	bullet("Build the TUI on the headless core: live job panel, digest paging, checkpoint/revert affordances.")
	bullet("Point it at a real backend (Ollama locally, or any hosted key) and run a genuine multi-step task end to end.")

	// The output anchors to the source tree (tools/pdfgen), never to the
	// caller's cwd — a relative `../../docs/...` silently wrote outside the
	// repo when invoked from elsewhere. Override with --out.
	out := flag.String("out", defaultOutPath(), "output PDF path")
	flag.Parse()
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "pdf:", err)
		os.Exit(1)
	}
	if err := pdf.OutputFileAndClose(*out); err != nil {
		fmt.Fprintln(os.Stderr, "pdf:", err)
		os.Exit(1)
	}
	abs, _ := filepath.Abs(*out)
	fmt.Println("wrote", abs)
}

// defaultOutPath resolves <repo>/docs/Creator-v0.1-Plan-and-Status.pdf from
// this source file's location, independent of cwd.
func defaultOutPath() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "Creator-v0.1-Plan-and-Status.pdf"
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	return filepath.Join(repoRoot, "docs", "Creator-v0.1-Plan-and-Status.pdf")
}
