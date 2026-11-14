// explore.go — the "understand the codebase fast" tools. These answer the
// questions an agent asks before it touches anything: what is here (tree),
// what is flagged (todos), where is this defined (find_def), how big is it
// (repo_stats). All are read-only, workspace-sandboxed and budget-clipped.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/DERYK-RYLOY/creator/internal/reduce"
)

func treeTool() Tool {
	return Tool{
		Name:     "tree",
		ReadOnly: true,
		Description: "Print a depth-capped directory tree of the workspace (or a subdirectory), with counts. " +
			"Skips .git, node_modules, vendor and build output. Cheaper than reading many list calls.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Subdirectory to print from (default workspace root)."},
    "depth": {"type": "integer", "description": "Max depth below path (default 3, hard cap 6)."},
    "max_files": {"type": "integer", "description": "Hard cap on file rows (default 200, hard cap 500)."}
  }
}`),
		Handler: handleTree,
	}
}

type treeArgs struct {
	Path     string `json:"path"`
	Depth    int    `json:"depth"`
	MaxFiles int    `json:"max_files"`
}

func handleTree(ctx context.Context, raw json.RawMessage) (string, error) {
	var a treeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Depth <= 0 {
		a.Depth = 3
	}
	if a.Depth > 6 {
		a.Depth = 6
	}
	if a.MaxFiles <= 0 {
		a.MaxFiles = 200
	}
	if a.MaxFiles > 500 {
		a.MaxFiles = 500
	}
	env := currentEnv(ctx)
	root := env.Workspace
	var err error
	if strings.TrimSpace(a.Path) != "" {
		if root, err = env.resolve(a.Path, "dir"); err != nil {
			return "", err
		}
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(filepath.Base(filepath.Clean(rootAbs)) + string(filepath.Separator) + "\n")
	files, dirs, truncated := 0, 0, false
	walkErr := filepath.WalkDir(rootAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are reported via counts, not by failing the call
		}
		if p != rootAbs && d.IsDir() && ignoredDirs[d.Name()] {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(rootAbs, p)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		depth := len(strings.Split(filepath.ToSlash(rel), "/"))
		if depth > a.Depth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			truncated = true
			return nil
		}
		if !insideWorkspace(rootAbs, p) {
			truncated = true
			return nil
		}
		indent := strings.Repeat("  ", depth)
		if d.IsDir() {
			dirs++
			b.WriteString(indent + "▸ " + d.Name() + "/\n")
			return nil
		}
		files++
		if files > a.MaxFiles {
			truncated = true
			return filepath.SkipAll
		}
		b.WriteString(indent + "· " + d.Name() + "\n")
		return nil
	})
	_ = walkErr
	if truncated {
		b.WriteString("… truncated (depth/file cap reached)\n")
	}
	fmt.Fprintf(&b, "— %d dirs · %d files", dirs, files)
	return clip(env, b.String(), reduce.KindGeneric), nil
}

func todosTool() Tool {
	return Tool{
		Name:     "todos",
		ReadOnly: true,
		Description: "Scan the workspace for TODO / FIXME / HACK / XXX / BUG / NOTE markers with file and line. " +
			"Returns counts per kind and capped rows. Cheaper than grep for triage; skips binaries and ignored directories.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Subdirectory to scan (default workspace root)."},
    "marker": {"type": "string", "description": "Only this marker, e.g. FIXME (default: all of TODO, FIXME, HACK, XXX, BUG, NOTE)."},
    "max_results": {"type": "integer", "description": "Hard cap on rows (default 80, hard cap 300)."}
  }
}`),
		Handler: handleTodos,
	}
}

type todosArgs struct {
	Path       string `json:"path"`
	Marker     string `json:"marker"`
	MaxResults int    `json:"max_results"`
}

var todoMarkerRe = regexp.MustCompile(`\b(TODO|FIXME|HACK|XXX|BUG|NOTE)\b`)

func handleTodos(ctx context.Context, raw json.RawMessage) (string, error) {
	var a todosArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.MaxResults <= 0 {
		a.MaxResults = 80
	}
	if a.MaxResults > 300 {
		a.MaxResults = 300
	}
	marker := strings.ToUpper(strings.TrimSpace(a.Marker))
	if marker != "" && todoMarkerRe.FindString(marker) == "" {
		return "", fmt.Errorf("unknown marker %q (want one of TODO, FIXME, HACK, XXX, BUG, NOTE)", a.Marker)
	}
	env := currentEnv(ctx)
	root := env.Workspace
	var err error
	if strings.TrimSpace(a.Path) != "" {
		if root, err = env.resolve(a.Path, "dir"); err != nil {
			return "", err
		}
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	type row struct{ file, line, kind, text string }
	var rows []row
	counts := map[string]int{}
	scanned := 0
	filepath.WalkDir(rootAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != rootAbs && ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if scanned >= limits.GrepResults*4 || len(rows) > a.MaxResults*4 {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil || info.Size() > limits.GrepFileMax {
			return nil
		}
		if !insideWorkspace(rootAbs, p) || !looksTextFile(p) {
			return nil
		}
		data, err := readFileCapped(p, limits.GrepFileMax)
		if err != nil {
			return nil
		}
		scanned++
		rel, _ := filepath.Rel(rootAbs, p)
		rel = filepath.ToSlash(rel)
		for i, line := range strings.Split(string(data), "\n") {
			m := todoMarkerRe.FindString(line)
			if m == "" {
				continue
			}
			if marker != "" && m != marker {
				continue
			}
			counts[m]++
			if len(rows) < a.MaxResults {
				text := strings.TrimSpace(line)
				if len(text) > 140 {
					text = text[:140] + "…"
				}
				rows = append(rows, row{rel, fmt.Sprint(i + 1), m, text})
			}
		}
		return nil
	})
	total := 0
	for _, n := range counts {
		total += n
	}
	var b strings.Builder
	if marker != "" {
		fmt.Fprintf(&b, "%s markers: %d\n", marker, counts[marker])
	} else {
		parts := []string{}
		for _, k := range []string{"TODO", "FIXME", "HACK", "XXX", "BUG", "NOTE"} {
			if counts[k] > 0 {
				parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
			}
		}
		if len(parts) == 0 {
			b.WriteString("no markers found — the tree is clean\n")
			return clip(env, b.String(), reduce.KindGeneric), nil
		}
		fmt.Fprintf(&b, "%d markers (%s)\n", total, strings.Join(parts, " · "))
	}
	for _, r := range rows {
		fmt.Fprintf(&b, "%s:%s [%s] %s\n", r.file, r.line, r.kind, r.text)
	}
	if total > len(rows) {
		fmt.Fprintf(&b, "… %d more rows (narrow with marker= or path=)\n", total-len(rows))
	}
	return clip(env, b.String(), reduce.KindGeneric), nil
}

func findDefTool() Tool {
	return Tool{
		Name:     "find_def",
		ReadOnly: true,
		Description: "Find where a symbol is defined (function, type, class, interface, const) across the workspace. " +
			"Matches keyword-then-name patterns in Go, Python, JS/TS, Rust and shell; falls back to bare occurrences when nothing matches.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "symbol": {"type": "string", "description": "The symbol to locate, e.g. NewClient or parseConfig."},
    "path": {"type": "string", "description": "Subdirectory to search (default workspace root)."},
    "max_results": {"type": "integer", "description": "Hard cap on rows (default 40, hard cap 120)."}
  },
  "required": ["symbol"]
}`),
		Handler: handleFindDef,
	}
}

type findDefArgs struct {
	Symbol     string `json:"symbol"`
	Path       string `json:"path"`
	MaxResults int    `json:"max_results"`
}

func handleFindDef(ctx context.Context, raw json.RawMessage) (string, error) {
	var a findDefArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	symbol := strings.TrimSpace(a.Symbol)
	if symbol == "" {
		return "", fmt.Errorf("symbol is required")
	}
	if len(symbol) > 128 {
		symbol = symbol[:128]
	}
	if a.MaxResults <= 0 {
		a.MaxResults = 40
	}
	if a.MaxResults > 120 {
		a.MaxResults = 120
	}
	quoted := regexp.QuoteMeta(symbol)
	defRe := regexp.MustCompile(`(?i)(func|function|def|fn|class|type|struct|interface|enum|trait|const|let|var)\s+\(?\s*\*?` + quoted + `\b`)
	bareRe := regexp.MustCompile(`\b` + quoted + `\b`)

	env := currentEnv(ctx)
	root := env.Workspace
	var err error
	if strings.TrimSpace(a.Path) != "" {
		if root, err = env.resolve(a.Path, "dir"); err != nil {
			return "", err
		}
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	var defs, bare []string
	scanned := 0
	filepath.WalkDir(rootAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != rootAbs && ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if scanned >= limits.GrepResults*4 {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil || info.Size() > limits.GrepFileMax {
			return nil
		}
		if !insideWorkspace(rootAbs, p) || !looksTextFile(p) {
			return nil
		}
		data, err := readFileCapped(p, limits.GrepFileMax)
		if err != nil {
			return nil
		}
		scanned++
		rel, _ := filepath.Rel(rootAbs, p)
		rel = filepath.ToSlash(rel)
		for i, line := range strings.Split(string(data), "\n") {
			trim := strings.TrimSpace(line)
			switch {
			case defRe.MatchString(line):
				if len(defs) < a.MaxResults {
					if len(trim) > 200 {
						trim = trim[:200] + "…"
					}
					defs = append(defs, fmt.Sprintf("%s:%d  %s", rel, i+1, trim))
				}
			default:
				if bareRe.MatchString(line) && len(bare) < a.MaxResults {
					if len(trim) > 200 {
						trim = trim[:200] + "…"
					}
					bare = append(bare, fmt.Sprintf("%s:%d  %s", rel, i+1, trim))
				}
			}
		}
		return nil
	})
	var b strings.Builder
	if len(defs) > 0 {
		fmt.Fprintf(&b, "definitions of %q:\n", symbol)
		for _, d := range defs {
			fmt.Fprintf(&b, "%s\n", d)
		}
	} else if len(bare) > 0 {
		fmt.Fprintf(&b, "no definition keyword matched %q — bare occurrences:\n", symbol)
		for _, d := range bare {
			fmt.Fprintf(&b, "%s\n", d)
		}
	} else {
		fmt.Fprintf(&b, "no occurrences of %q found\n", symbol)
	}
	return clip(env, b.String(), reduce.KindGeneric), nil
}

func repoStatsTool() Tool {
	return Tool{
		Name:     "repo_stats",
		ReadOnly: true,
		Description: "Summarize the workspace: file counts by extension, total lines of code, largest files. " +
			"Good first call before an unfamiliar repository. Skips ignored directories and binaries.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Subdirectory to summarize (default workspace root)."}
  }
}`),
		Handler: handleRepoStats,
	}
}

type repoStatsArgs struct {
	Path string `json:"path"`
}

func handleRepoStats(ctx context.Context, raw json.RawMessage) (string, error) {
	var a repoStatsArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	root := env.Workspace
	var err error
	if strings.TrimSpace(a.Path) != "" {
		if root, err = env.resolve(a.Path, "dir"); err != nil {
			return "", err
		}
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	var totalFiles, textFiles int
	var totalBytes, totalLOC int64
	extCounts := map[string]int{}
	type big struct {
		rel  string
		size int64
	}
	var bigs []big
	scanned := 0
	filepath.WalkDir(rootAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != rootAbs && ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if scanned >= 20000 {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if !insideWorkspace(rootAbs, p) {
			return nil
		}
		scanned++
		totalFiles++
		totalBytes += info.Size()
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext == "" {
			ext = "(none)"
		}
		extCounts[ext]++
		rel, _ := filepath.Rel(rootAbs, p)
		rel = filepath.ToSlash(rel)
		if len(bigs) < 8 || info.Size() > bigs[len(bigs)-1].size {
			bigs = append(bigs, big{rel, info.Size()})
			sort.Slice(bigs, func(i, j int) bool { return bigs[i].size > bigs[j].size })
			if len(bigs) > 8 {
				bigs = bigs[:8]
			}
		}
		if info.Size() <= limits.GrepFileMax && looksTextFile(p) {
			data, err := readFileCapped(p, limits.GrepFileMax)
			if err == nil {
				textFiles++
				totalLOC += int64(strings.Count(string(data), "\n")) + 1
			}
		}
		return nil
	})
	type kv struct {
		k string
		v int
	}
	kvs := make([]kv, 0, len(extCounts))
	for k, v := range extCounts {
		kvs = append(kvs, kv{k, v})
	}
	sort.Slice(kvs, func(i, j int) bool {
		if kvs[i].v == kvs[j].v {
			return kvs[i].k < kvs[j].k
		}
		return kvs[i].v > kvs[j].v
	})
	if len(kvs) > 12 {
		kvs = kvs[:12]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "files: %d (%d text) · bytes: %d · lines: %d\n", totalFiles, textFiles, totalBytes, totalLOC)
	b.WriteString("by extension:")
	for _, kv := range kvs {
		fmt.Fprintf(&b, " %s %d", kv.k, kv.v)
	}
	b.WriteString("\nlargest files:\n")
	for _, big := range bigs {
		fmt.Fprintf(&b, "  %d  %s\n", big.size, big.rel)
	}
	return clip(env, b.String(), reduce.KindGeneric), nil
}
