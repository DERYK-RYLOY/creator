package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/creatorcli/creator/internal/reduce"
)

func schema(s string) json.RawMessage { return json.RawMessage(s) }

func readTool() Tool {
	return Tool{
		Name:     "read",
		ReadOnly: true,
		Description: "Read a UTF-8 text file. Content is returned with line numbers. " +
			"Very large files come back as a digest plus a handle; page the rest with out().",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path, relative to the workspace or absolute inside it."},
    "start_line": {"type": "integer", "description": "First line to return (1-based, inclusive)."},
    "end_line": {"type": "integer", "description": "Last line to return (1-based, inclusive)."}
  },
  "required": ["path"]
}`),
		Handler: handleRead,
	}
}

type readArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

func handleRead(ctx context.Context, raw json.RawMessage) (string, error) {
	var a readArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	path, err := env.resolve(a.Path, "file")
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > limits.ReadBytes {
		return "", fmt.Errorf("%s is %d bytes; read a slice with start_line/end_line, or use grep", a.Path, info.Size())
	}
	data, err := readFileCapped(path, limits.CodeReadMax)
	if err != nil {
		return "", err
	}
	if isBinary(data) {
		return "", fmt.Errorf("%s looks binary; use shell with a hex dump if you really need it", a.Path)
	}
	lines := splitLines(string(data))
	if len(lines) == 0 {
		return fmt.Sprintf("%s is empty", env.rel(path)), nil
	}
	from, to := 1, len(lines)
	if a.StartLine > 0 {
		from = a.StartLine
	}
	if a.EndLine > 0 && a.EndLine < to {
		to = a.EndLine
	}
	if from > len(lines) {
		return "", fmt.Errorf("start_line %d is past the end (%d lines)", from, len(lines))
	}
	if to > len(lines) {
		to = len(lines)
	}
	if to < from {
		return "", fmt.Errorf("end_line %d is before start_line %d", to, from)
	}
	body := strings.Join(numbered(lines[from-1:to], from), "\n")
	header := fmt.Sprintf("%s (%d lines, %d bytes)", env.rel(path), len(lines), info.Size())
	if from > 1 || to < len(lines) {
		header += fmt.Sprintf(" showing %d-%d", from, to)
	}
	return header + "\n" + clip(env, body, reduce.KindGeneric), nil
}

// clip enforces the observation budget on a tool result.
func clip(env *Env, text string, kind reduce.Kind) string {
	if reduce.EstimateTokens(text) <= env.Budget {
		return text
	}
	digest, _ := env.digest(text, kind)
	return digest
}

func writeTool() Tool {
	return Tool{
		Name: "write",
		Description: "Create or overwrite a file with exact content. Parent directories are created. " +
			"The previous version is checkpointed so `creator revert <id>` can restore it.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path inside the workspace."},
    "content": {"type": "string", "description": "Full file content."}
  },
  "required": ["path", "content"]
}`),
		Handler: handleWrite,
	}
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func handleWrite(ctx context.Context, raw json.RawMessage) (string, error) {
	var a writeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	path, err := env.resolve(a.Path, "")
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return "", fmt.Errorf("%s is a directory", a.Path)
	}
	note := checkpointForMutation(env, path, "write")
	lines := len(splitLines(a.Content))
	if a.Content != "" && !strings.HasSuffix(a.Content, "\n") {
		a.Content += "\n"
		lines = len(splitLines(a.Content))
	}
	if err := writeFilePreserveMode(env, path, []byte(a.Content)); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %s (%d lines, %d bytes)%s", env.rel(path), lines, len(a.Content), note), nil
}

func editTool() Tool {
	return Tool{
		Name: "edit",
		Description: "Replace an exact string in a file. Fails when old_string is missing or appears more than once, " +
			"which keeps edits unambiguous. A unique match whose whitespace differs slightly is still accepted and " +
			"reported as whitespace-normalized. The file is checkpointed before modification.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path inside the workspace."},
    "old_string": {"type": "string", "description": "Exact text to replace, including indentation."},
    "new_string": {"type": "string", "description": "Replacement text."},
    "replace_all": {"type": "boolean", "description": "Replace every occurrence instead of requiring a unique match."}
  },
  "required": ["path", "old_string", "new_string"]
}`),
		Handler: handleEdit,
	}
}

type editArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func handleEdit(ctx context.Context, raw json.RawMessage) (string, error) {
	var a editArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.OldString == "" {
		return "", errors.New("old_string must not be empty")
	}
	if a.OldString == a.NewString {
		return "", errors.New("old_string and new_string are identical")
	}
	env := currentEnv(ctx)
	path, err := env.resolve(a.Path, "file")
	if err != nil {
		return "", err
	}
	data, err := readFileCapped(path, limits.CodeReadMax)
	if err != nil {
		return "", err
	}
	original := string(data)

	count := strings.Count(original, a.OldString)
	if count > 1 && !a.ReplaceAll {
		return "", fmt.Errorf("old_string appears %d times in %s; add surrounding context or set replace_all", count, a.Path)
	}
	if count >= 1 {
		var updated string
		if a.ReplaceAll {
			updated = strings.ReplaceAll(original, a.OldString, a.NewString)
		} else {
			updated = strings.Replace(original, a.OldString, a.NewString, 1)
		}
		return persistEdit(env, path, a, updated, "", count)
	}

	// Not found verbatim. Fall back to whitespace-normalized matching: models
	// frequently get indentation or trailing whitespace slightly wrong around
	// a unique block, and one failed edit costs a full round trip.
	span, hit, note := matchWhitespace(original, a.OldString)
	if hit {
		updated, uerr := rewriteWithIndent(original, span, a.NewString)
		if uerr == nil {
			// The whitespace-normalized fallback matches ONE span, so a
			// requested replace_all is not honored here: report exactly one
			// occurrence instead of implying every one was replaced.
			phaseCount := 0
			if a.ReplaceAll {
				phaseCount = 1
				note = note + "; single whitespace-normalized span — verbatim replace-all did not match"
			}
			return persistEdit(env, path, a, updated, " (matched "+note+")", phaseCount)
		}
	}
	hint := nearestContext(original, a.OldString)
	if hint != "" {
		return "", fmt.Errorf("old_string not found in %s; %s", a.Path, hint)
	}
	return "", fmt.Errorf("old_string not found in %s", a.Path)
}

// persistEdit checkpoints the previous content, writes the update and renders
// the intake note (whitespace-normalized match info) plus the checkpoint id.
func persistEdit(env *Env, path string, a editArgs, updated, note string, replaced int) (string, error) {
	cp, cpErr := env.Store.SaveCheckpoint(env.Session, path, env.rel(path), "edit")
	if err := writeFilePreserveMode(env, path, []byte(updated)); err != nil {
		return "", err
	}
	if cpErr == nil {
		note += " (previous version: " + cp.ID + ")"
	}
	if a.ReplaceAll {
		if replaced > 0 {
			noun := "occurrences"
			if replaced == 1 {
				noun = "occurrence"
			}
			return fmt.Sprintf("replaced %d %s in %s%s", replaced, noun, env.rel(path), note), nil
		}
		return fmt.Sprintf("replaced all occurrences in %s%s", env.rel(path), note), nil
	}
	return fmt.Sprintf("edited %s%s", env.rel(path), note), nil
}

// editSpan locates a textual region of a document by byte offsets.
type editSpan struct {
	start int
	end   int
}

// lineOffsets computes the starting byte offset of every line plus a final
// entry pointing at end-of-content.
func lineOffsets(content string) []int {
	lines := strings.Split(content, "\n")
	offsets := make([]int, len(lines)+1)
	pos := 0
	for i, l := range lines {
		offsets[i] = pos
		pos += len(l) + 1 // +1 for the newline
	}
	offsets[len(lines)] = len(content)
	return offsets
}

// matchWhitespace finds a block in content whose whitespace-trimmed lines equal
// the trimmed lines of old. Blank lines inside the pattern must match
// blank-or-whitespace-only lines in the document — one per pattern blank
// line — so the tolerance cannot swallow code, keys or data that happens to
// sit in the document. Span covers from the first pattern line to the last.
func matchWhitespace(content, old string) (editSpan, bool, string) {
	pattern := splitNormalized(strings.TrimSuffix(old, "\n"))
	if len(pattern) == 0 {
		return editSpan{}, false, ""
	}
	docLines := strings.Split(content, "\n")
	offsets := lineOffsets(content)

	valid := func(i int) bool {
		j := i
		for pi := 0; pi < len(pattern); pi++ {
			if j >= len(docLines) {
				return false
			}
			want := pattern[pi]
			if want == "" {
				// A blank pattern line is satisfied only by a blank
				// (or whitespace-only) document line. Accepting arbitrary
				// content here let the fallback eat lines it never matched.
				if strings.TrimSpace(docLines[j]) != "" {
					return false
				}
				j++
				continue
			}
			if want != strings.TrimSpace(docLines[j]) {
				return false
			}
			j++
		}
		return true
	}

	// limit: how many document lines a match window may consume beyond the
	// pattern length (the room blank-line tolerance buys).
	var hits []editSpan
	for i := 0; i < len(docLines); i++ {
		if !valid(i) {
			continue
		}
		// Find the document line index where this window ends, mirroring the
		// skip logic of valid().
		j := i
		for pi := 0; pi < len(pattern); pi++ {
			if pattern[pi] == "" {
				j++
				continue
			}
			j++
		}
		span := editSpan{start: offsets[i], end: offsets[min(j, len(docLines))]}
		if span.end == span.start {
			span.end = len(content)
		}
		hits = append(hits, span)
	}
	switch len(hits) {
	case 1:
		return hits[0], true, "with whitespace normalization"
	case 0:
		return editSpan{}, false, ""
	default:
		return editSpan{}, false, ""
	}
}

// splitNormalized trims each line and drops blanks that begin or end the
// pattern; interior blanks are inferred during matching instead.
func splitNormalized(text string) []string {
	raw := strings.Split(text, "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		out = append(out, strings.TrimSpace(l))
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// rewriteWithIndent replaces a span, preserving the file's own indentation:
// the replacement's first line inherits the anchor line's indent, and interior
// lines keep their relative extra indentation (any leading whitespace deeper
// than the replacement's own first-line indent is preserved on top of the
// anchor indent).
func rewriteWithIndent(content string, span editSpan, replacement string) (string, error) {
	if span.start < 0 || span.end > len(content) || span.start > span.end {
		return "", errors.New("invalid match span")
	}
	anchor := content[:span.start]
	nl := strings.LastIndexByte(anchor, '\n')
	anchorLine := anchor[nl+1:]
	indentPart := ""
	if strings.TrimSpace(anchorLine) == "" && anchorLine != "" {
		// The match begins on a whitespace-only line: keep the file's own
		// indentation for that line.
		indentPart = anchorLine
	} else if strings.HasSuffix(anchor, "\n") {
		// Anchor ends at a newline: the span starts at a line boundary, so
		// inherit the indent of the FIRST line being replaced. (The old code
		// always wrote the first replacement line flush to col 0 — a dead
		// branch that corrupts indented targets.)
		first := ""
		if span.end > span.start {
			first = content[span.start:span.end]
			if i := strings.IndexByte(first, '\n'); i >= 0 {
				first = first[:i]
			}
		}
		s := strings.TrimLeft(first, " \t")
		indentPart = first[:len(first)-len(s)]
	} else {
		// Mid-line match: inherit indent of the current line.
		s := strings.TrimLeft(anchorLine, " \t")
		indentPart = anchorLine[:len(anchorLine)-len(s)]
	}

	rLines := strings.Split(replacement, "\n")
	if len(rLines) > 1 && strings.TrimSpace(rLines[0]) == "" {
		rLines = rLines[1:]
	}
	if len(rLines) > 0 && strings.TrimSpace(rLines[len(rLines)-1]) == "" {
		rLines = rLines[:len(rLines)-1]
	}

	var sb strings.Builder
	sb.WriteString(anchor)
	for i, l := range rLines {
		if i > 0 {
			sb.WriteString("\n")
		}
		switch {
		case strings.TrimSpace(l) == "":
			// blank lines carry no indentation
		case i == 0:
			sb.WriteString(indentPart + strings.TrimSpace(l))
		default:
			stripped := strings.TrimLeft(l, " \t")
			lead := l[:len(l)-len(stripped)]
			if len(lead) > len(indentPart) {
				sb.WriteString(indentPart + lead[len(indentPart):] + stripped)
			} else {
				sb.WriteString(indentPart + stripped)
			}
		}
	}
	sb.WriteString(content[span.end:])
	result := sb.String()
	// The span swallowed the file's final newline (it runs to EOF); the
	// replacement did not end with one, so restore it.
	if span.end == len(content) && strings.HasSuffix(content, "\n") && !strings.HasSuffix(replacement, "\n") {
		result += "\n"
	}
	return result, nil
}

// nearestContext points the model at the closest lines of the file to the
// content it asked for, so one failed match never costs a blind re-read.
func nearestContext(content, old string) string {
	oldLines := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	probe := strings.TrimSpace(oldLines[0])
	if probe == "" && len(oldLines) > 1 {
		probe = strings.TrimSpace(oldLines[1])
	}
	if probe == "" {
		return ""
	}
	token := probe
	for _, f := range strings.Fields(probe) {
		token = strings.Trim(f, "\"'`()[]{}:,")
		if len(token) > 3 {
			break
		}
	}
	if len(token) < 4 {
		token = strings.FieldsFunc(probe, func(r rune) bool { return r == ' ' || r == '\t' })[0]
	}
	docLines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	var found []string
	for i, l := range docLines {
		if strings.Contains(l, token) {
			found = append(found, fmt.Sprintf("line %d: %s", i+1, truncate(strings.TrimSpace(l), 90)))
			if len(found) == 3 {
				break
			}
		}
	}
	if len(found) == 0 {
		return ""
	}
	return fmt.Sprintf("closest matching lines: %s; re-read the file before editing again", strings.Join(found, " | "))
}

func listTool() Tool {
	return Tool{
		Name:        "list",
		ReadOnly:    true,
		Description: "List a directory's immediate entries, with directories marked by a trailing slash. Useful before choosing a file to read.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Directory path, defaulting to the workspace root."}
  }
}`),
		Handler: handleList,
	}
}

type listArgs struct {
	Path string `json:"path"`
}

func handleList(ctx context.Context, raw json.RawMessage) (string, error) {
	var a listArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	if a.Path == "" {
		a.Path = "."
	}
	path, err := env.resolve(a.Path, "dir")
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d entries)\n", env.rel(path), len(entries))
	for _, e := range entries {
		if e.IsDir() {
			fmt.Fprintf(&b, "  %s/\n", e.Name())
			continue
		}
		size := int64(0)
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		fmt.Fprintf(&b, "  %-48s %8d B\n", e.Name(), size)
	}
	return clip(env, strings.TrimRight(b.String(), "\n"), reduce.KindListing), nil
}

func globTool() Tool {
	return Tool{
		Name:     "glob",
		ReadOnly: true,
		Description: "Find files by glob pattern. `**` matches across directories; a pattern without / matches only " +
			"direct children. Skips .git, node_modules, vendor and build output. Prefer this over shell find: " +
			"results are sorted and token-budgeted.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Glob such as **/*.go or internal/**/*_test.go; *.go matches workspace root only."},
    "path": {"type": "string", "description": "Directory to search from, default workspace root."},
    "max_results": {"type": "integer", "description": "Hard cap on returned paths (default 200)."}
  },
  "required": ["pattern"]
}`),
		Handler: handleGlob,
	}
}

type globArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	MaxResults int    `json:"max_results"`
}

func handleGlob(ctx context.Context, raw json.RawMessage) (string, error) {
	var a globArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Pattern == "" {
		return "", errors.New("pattern is required")
	}
	env := currentEnv(ctx)
	root := env.Workspace
	if a.Path != "" {
		var err error
		if root, err = env.resolve(a.Path, "dir"); err != nil {
			return "", err
		}
	}
	matcher, err := compileGlob(a.Pattern)
	if err != nil {
		return "", err
	}
	max := a.MaxResults
	if max <= 0 {
		max = 200
	}
	if max > limits.GlobResults {
		max = limits.GlobResults
	}
	var matches []string
	truncated := false
	// Collect, sort, then cap — capturing walk order up to the cap and
	// sorting afterwards would return an arbitrary subset of the matches.
	const scanCeiling = 50000 // hard stop against pathological trees
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if ignoredDirs[d.Name()] && p != root {
				return filepath.SkipDir
			}
			return nil
		}
		relPath, err := filepath.Rel(env.Workspace, p)
		if err != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)
		if !matcher(relPath, d.Name()) {
			return nil
		}
		if len(matches) >= scanCeiling {
			truncated = true
			return filepath.SkipAll
		}
		matches = append(matches, relPath)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(matches)
	if len(matches) > max {
		truncated = true
		matches = matches[:max]
	}
	body := strings.Join(matches, "\n")
	if len(matches) == 0 {
		return fmt.Sprintf("no files matched %q", a.Pattern), nil
	}
	header := fmt.Sprintf("%d files matched %q", len(matches), a.Pattern)
	if truncated {
		header += fmt.Sprintf(" (capped at %d; first %d alphabetically)", max, len(matches))
	}
	return header + "\n" + clip(env, body, reduce.KindListing), nil
}

func grepTool() Tool {
	return Tool{
		Name:     "grep",
		ReadOnly: true,
		Description: "Search file contents with a Go regular expression. Returns path:line:text grouped by file, token-budgeted. " +
			"Skips binaries, dependency directories and files over 2MB.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Go/RE2 regular expression."},
    "path": {"type": "string", "description": "File or directory to search, default workspace root."},
    "glob": {"type": "string", "description": "Restrict to paths matching this glob, e.g. **/*.go."},
    "ignore_case": {"type": "boolean"},
    "max_results": {"type": "integer", "description": "Hard cap on match lines (default 400)."}
  },
  "required": ["pattern"]
}`),
		Handler: handleGrep,
	}
}

type grepArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	IgnoreCase bool   `json:"ignore_case"`
	MaxResults int    `json:"max_results"`
}

func handleGrep(ctx context.Context, raw json.RawMessage) (string, error) {
	var a grepArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Pattern == "" {
		return "", errors.New("pattern is required")
	}
	expr := a.Pattern
	if a.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return "", fmt.Errorf("invalid regexp: %w", err)
	}
	env := currentEnv(ctx)
	target := env.Workspace
	if a.Path != "" {
		if target, err = env.resolve(a.Path, ""); err != nil {
			return "", err
		}
	}
	var pathMatcher func(string, string) bool
	if a.Glob != "" {
		if pathMatcher, err = compileGlob(a.Glob); err != nil {
			return "", err
		}
	}
	max := a.MaxResults
	if max <= 0 {
		max = 200
	}
	if max > limits.GrepResults {
		max = limits.GrepResults
	}

	var hits []string
	var skipped []string
	truncated := false
	searchFile := func(path string) error {
		// Walk does not descend symlinked directories, but files reached by
		// the walk may themselves be symlinks pointing outside the
		// workspace — those are skipped entirely.
		if !insideWorkspace(env.Workspace, path) {
			return nil
		}
		if info, err := os.Stat(path); err != nil || info.Size() > limits.GrepFileMax {
			return nil
		}
		relPath, err := filepath.Rel(env.Workspace, path)
		if err != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)
		if pathMatcher != nil && !pathMatcher(relPath, filepath.Base(path)) {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		lineNo := 0
		first := true
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if first {
				first = false
				if isBinary([]byte(line)) {
					return nil
				}
			}
			if len(hits) >= max {
				truncated = true
				return io.EOF
			}
			if !re.MatchString(line) {
				continue
			}
			text := strings.TrimRight(line, "\r")
			if len(text) > 300 {
				text = string([]rune(text)[:300]) + "…"
			}
			hits = append(hits, fmt.Sprintf("%s:%d:%s", relPath, lineNo, text))
		}
		if serr := scanner.Err(); serr != nil {
			// A >1 MiB line halts Scan with ErrTooLong; say so instead of
			// silently returning nothing for the file.
			skipped = append(skipped, relPath)
		}
		return nil
	}

	info, statErr := os.Stat(target)
	if statErr != nil {
		return "", statErr
	}
	if info.IsDir() {
		err = filepath.WalkDir(target, func(p string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() {
				if ignoredDirs[d.Name()] && p != target {
					return filepath.SkipDir
				}
				return nil
			}
			if err := searchFile(p); err != nil {
				if errors.Is(err, io.EOF) {
					return filepath.SkipAll
				}
				return nil
			}
			return nil
		})
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
	} else if err := searchFile(target); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}

	skippedNote := ""
	if len(skipped) > 0 {
		names := skipped
		const capNames = 4
		extra := 0
		if len(names) > capNames {
			extra = len(names) - capNames
			names = names[:capNames]
		}
		skippedNote = fmt.Sprintf("\nnote: %d file(s) skipped (line over 1 MiB or read error): %s", len(skipped), strings.Join(names, ", "))
		if extra > 0 {
			skippedNote += fmt.Sprintf(" and %d more", extra)
		}
	}
	if len(hits) == 0 {
		if skippedNote != "" {
			// Files were skipped — "no matches" would be a lie by omission.
			return fmt.Sprintf("no matches for %q (but %d file(s) were skipped: %s)", a.Pattern, len(skipped), strings.Join(skippedCapped(skipped), ", ")), nil
		}
		return fmt.Sprintf("no matches for %q", a.Pattern), nil
	}
	header := fmt.Sprintf("%d matches for %q", len(hits), a.Pattern)
	if truncated {
		header += fmt.Sprintf(" (capped at %d; narrow the search)", max)
	}
	return header + "\n" + clip(env, strings.Join(hits, "\n"), reduce.KindSearch) + skippedNote, nil
}

func skippedCapped(skipped []string) []string {
	const cap = 4
	if len(skipped) <= cap {
		return skipped
	}
	return append(skipped[:cap], fmt.Sprintf("and %d more", len(skipped)-cap))
}

func isBinary(data []byte) bool {
	limit := len(data)
	if limit > 512 {
		limit = 512
	}
	for _, b := range data[:limit] {
		if b == 0 {
			return true
		}
	}
	return false
}

// compileGlob builds a matcher supporting ** across path separators. A pattern
// without a slash matches a file name only at the workspace root (i.e. its
// relative path contains no separator), which is standard glob semantics.
func compileGlob(pattern string) (func(rel, base string) bool, error) {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	if pattern == "" {
		return nil, errors.New("empty glob")
	}
	re, err := regexp.Compile(globToRegexp(pattern))
	if err != nil {
		return nil, fmt.Errorf("invalid glob %q: %w", pattern, err)
	}
	hasSlash := strings.Contains(pattern, "/")
	return func(rel, base string) bool {
		if hasSlash {
			return re.MatchString(rel)
		}
		return !strings.Contains(rel, "/") && re.MatchString(base)
	}, nil
}

func globToRegexp(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '{':
			close := strings.IndexByte(pattern[i:], '}')
			if close < 0 {
				b.WriteString("\\{")
				continue
			}
			inner := pattern[i+1 : i+close]
			var alts []string
			for _, alt := range strings.Split(inner, ",") {
				converted := globToRegexp(alt)
				converted = strings.TrimPrefix(converted, "^")
				converted = strings.TrimSuffix(converted, "$")
				alts = append(alts, converted)
			}
			b.WriteString("(?:" + strings.Join(alts, "|") + ")")
			i += close
		case '.', '+', '(', ')', '|', '}', '[', ']', '^', '$', '\\':
			b.WriteString("\\")
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString("$")
	return b.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
