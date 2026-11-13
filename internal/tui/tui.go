package tui

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/creatorcli/creator/internal/config"
)

const version = "0.1.0"

// Run starts mission control. args are the arguments after `creator tui`.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := options{}
	fs.StringVar(&opts.Creator, "creator", "", "path to the creator engine binary (default: beside the TUI or on PATH)")
	fs.StringVar(&opts.Provider, "provider", "", "model backend slug")
	fs.StringVar(&opts.Model, "model", "", "model name")
	fs.StringVar(&opts.BaseURL, "base-url", "", "override the provider endpoint")
	fs.StringVar(&opts.Policy, "policy", "", "manual|accept edits|bypass permissions, or strict|balanced|yolo")
	fs.BoolVar(&opts.AllowCritical, "allow-critical", false, "permit critical commands")
	fs.StringVar(&opts.DataDir, "data-dir", "", "state directory (default ~/.creator)")
	fs.StringVar(&opts.Theme, "theme", "", "theme slug (default from ~/.creator/config.json)")
	fs.BoolVar(&opts.NoKnowledge, "no-knowledge", false, "do not load repository instruction files")
	fs.BoolVar(&opts.WorkspacePlugins, "workspace-plugins", false, "allow plugins from <workspace>/.creator/plugins")
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose engine diagnostics")
	ver := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return &ExitError{Code: 2}
	}
	_ = opts
	opts.Version = version
	if *ver {
		fmt.Fprintf(stdout, "creator-tui %s\n", version)
		return nil
	}

	invalidTheme := false
	if opts.Theme == "" {
		if cfg, err := config.Load(config.Config{}); err == nil && cfg.Theme != "" {
			opts.Theme = cfg.Theme
		} else {
			opts.Theme = CurrentThemeSlug()
		}
	}
	if opts.Theme != "" && !ApplyTheme(opts.Theme) {
		opts.Theme = CurrentThemeSlug()
		invalidTheme = true
	} else if opts.Theme == "" {
		opts.Theme = CurrentThemeSlug()
	}
	exe, err := discoverEngine(opts.Creator)
	if err != nil {
		return err
	}
	opts.Policy = normalizePolicy(opts.Policy, opts.AllowCritical)

	m := &model{opts: opts, exe: exe, cwd: mustGetwd(), approval: policyToApproval(opts.Policy), historyIdx: 0}
	if invalidTheme {
		m.notice = "unknown theme slug; using " + CurrentThemeSlug()
		m.transcript = append(m.transcript, block{kind: kindNotice, title: "theme", body: "unknown theme slug; using " + CurrentThemeSlug(), ok: false, at: time.Now()})
	}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	m.program = p
	_, runErr := p.Run()
	m.shutdownEngine()
	return runErr
}

// ExitError is returned for flag/usage failures that should stop with a
// specific exit code instead of a stack-looking crash.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string { return fmt.Sprintf("exit %d", e.Code) }

func (m *model) shutdownEngine() {
	if m.running {
		m.interrupt("tui exit")
	}
	if m.runDone == nil {
		return
	}
	// The tea program has exited, so finishRun() can never run and nobody
	// will ever close m.runDone. Close it ourselves instead of taxing the
	// exit path on a "wait for a goroutine that can't finish" timer.
	close(m.runDone)
	m.runDone = nil
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func discoverEngine(override string) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err == nil {
			return override, nil
		}
		if p, err := exec.LookPath(override); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("creator engine not found at %q", override)
	}
	if self, err := os.Executable(); err == nil {
		dir := filepath.Dir(self)
		for _, name := range []string{"creator.exe", "creator"} {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}
	if wd, err := os.Getwd(); err == nil {
		for _, name := range []string{filepath.Join(wd, "creator.exe"), filepath.Join(wd, "bin", "creator.exe")} {
			if _, err := os.Stat(name); err == nil {
				return name, nil
			}
		}
	}
	if p, err := exec.LookPath("creator"); err == nil {
		return p, nil
	}
	return "", errors.New("creator engine not found — build ./cmd/creator or pass --creator /path/to/creator")
}

func normalizePolicy(p string, allowCritical bool) string {
	switch p {
	case "manual", "manual permissions":
		return "strict"
	case "accept edits", "accept":
		return "balanced"
	case "bypass", "bypass permissions", "yolo":
		return "yolo"
	case "strict", "balanced":
		return p
	case "":
		return "balanced"
	default:
		return p
	}
}

func policyToApproval(p string) approvalMode {
	switch p {
	case "strict":
		return approvalManual
	case "yolo":
		return approvalBypass
	default:
		return approvalAcceptEdits
	}
}
