package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/creatorcli/creator/internal/reduce"
)

// outlineTool extracts the structural outline of a file: functions, types,
// classes, methods. For Go it uses the real AST; for other languages it uses
// language-aware regex heuristics. This is the "read the map before reading
// the territory" tool.
func outlineTool() Tool {
	return Tool{
		Name:     "outline",
		ReadOnly: true,
		Description: "Extract the structural outline of a file: functions, types, classes, methods " +
			"with their line ranges. Use before reading a large file so you fetch only the symbols " +
			"you need. Go files use the real AST; other languages use syntax-aware heuristics.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path inside the workspace."}
  },
  "required": ["path"]
}`),
		Handler: handleOutline,
	}
}

type outlineArgs struct {
	Path string `json:"path"`
}

type symbol struct {
	Kind string
	Name string
	Line int
	End  int
}

func handleOutline(ctx context.Context, raw json.RawMessage) (string, error) {
	var a outlineArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	resolved, err := env.resolve(a.Path, "file")
	if err != nil {
		return "", err
	}
	data, err := readFileCapped(resolved, limits.CodeReadMax)
	if err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(resolved))
	var syms []symbol
	switch ext {
	case ".go":
		var outlineErr error
		syms, outlineErr = goOutline(string(data))
		if outlineErr != nil {
			return "", fmt.Errorf("cannot parse Go file %s: %v", env.rel(resolved), outlineErr)
		}
	default:
		syms = regexOutline(string(data), ext)
	}
	if len(syms) == 0 {
		return fmt.Sprintf("%s: no symbols found", env.rel(resolved)), nil
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s: %d symbols\n", env.rel(resolved), len(syms)))
	for _, s := range syms {
		b.WriteString(fmt.Sprintf("  %s  %s  (line %d", s.Kind, s.Name, s.Line))
		if s.End > s.Line {
			b.WriteString(fmt.Sprintf("-%d", s.End))
		}
		b.WriteString(")\n")
	}
	return clip(env, b.String(), reduce.KindGeneric), nil
}

func goOutline(src string) ([]symbol, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var syms []symbol
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.FuncDecl:
			kind := "func"
			name := v.Name.Name
			if v.Recv != nil {
				kind = "method"
				if len(v.Recv.List) > 0 {
					name = recvType(v.Recv.List[0].Type) + "." + name
				}
			}
			syms = append(syms, symbol{Kind: kind, Name: name, Line: fset.Position(v.Pos()).Line, End: fset.Position(v.End()).Line})
		case *ast.GenDecl:
			for _, spec := range v.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					syms = append(syms, symbol{Kind: "type", Name: s.Name.Name, Line: fset.Position(s.Pos()).Line, End: fset.Position(s.End()).Line})
				case *ast.ValueSpec:
					for _, n := range s.Names {
						kind := "var"
						if v.Tok.String() == "const" {
							kind = "const"
						}
						syms = append(syms, symbol{Kind: kind, Name: n.Name, Line: fset.Position(n.Pos()).Line, End: fset.Position(n.End()).Line})
					}
				}
			}
		}
		return true
	})
	sort.Slice(syms, func(i, j int) bool { return syms[i].Line < syms[j].Line })
	return syms, nil
}

func recvType(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.StarExpr:
		return recvType(v.X)
	case *ast.Ident:
		return v.Name
	case *ast.IndexExpr:
		return recvType(v.X)
	}
	return "?"
}

func regexOutline(src, ext string) []symbol {
	var patterns []struct {
		kind string
		re   *regexp.Regexp
	}
	switch ext {
	case ".py":
		patterns = []struct {
			kind string
			re   *regexp.Regexp
		}{
			{"func", regexp.MustCompile(`(?m)^def\s+(\w+)`)},
			{"class", regexp.MustCompile(`(?m)^class\s+(\w+)`)},
		}
	case ".js", ".ts", ".jsx", ".tsx":
		patterns = []struct {
			kind string
			re   *regexp.Regexp
		}{
			{"func", regexp.MustCompile(`(?m)function\s+(\w+)`)},
			{"const", regexp.MustCompile(`(?m)(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s*)?\(`)},
			{"class", regexp.MustCompile(`(?m)class\s+(\w+)`)},
		}
	case ".rs":
		patterns = []struct {
			kind string
			re   *regexp.Regexp
		}{
			{"fn", regexp.MustCompile(`(?m)(?:pub\s+)?fn\s+(\w+)`)},
			{"struct", regexp.MustCompile(`(?m)(?:pub\s+)?struct\s+(\w+)`)},
			{"enum", regexp.MustCompile(`(?m)(?:pub\s+)?enum\s+(\w+)`)},
			{"trait", regexp.MustCompile(`(?m)(?:pub\s+)?trait\s+(\w+)`)},
		}
	case ".java", ".kt":
		patterns = []struct {
			kind string
			re   *regexp.Regexp
		}{
			{"class", regexp.MustCompile(`(?m)(?:public|private|protected)?\s*class\s+(\w+)`)},
			{"func", regexp.MustCompile(`(?m)(?:public|private|protected)?\s*(?:static\s+)?\w+\s+(\w+)\s*\(`)},
		}
	default:
		patterns = []struct {
			kind string
			re   *regexp.Regexp
		}{
			{"func", regexp.MustCompile(`(?m)^(?:func|function|def|fn|sub)\s+(\w+)`)},
		}
	}
	lines := strings.Split(src, "\n")
	var syms []symbol
	for _, p := range patterns {
		for _, m := range p.re.FindAllStringSubmatchIndex(src, 0) {
			name := src[m[2]:m[3]]
			line := 1 + strings.Count(src[:m[0]], "\n")
			end := line
			// estimate end: next blank line or 20 lines
			for i := line; i < len(lines) && i < line+40; i++ {
				if strings.TrimSpace(lines[i]) == "" {
					end = i
					break
				}
				end = i + 1
			}
			syms = append(syms, symbol{Kind: p.kind, Name: name, Line: line, End: end})
		}
	}
	sort.Slice(syms, func(i, j int) bool { return syms[i].Line < syms[j].Line })
	return syms
}

// readSymbolTool reads a named symbol's full body by name. For Go it uses the
// AST; for other languages it locates the symbol and reads until the matching
// close brace / end of block.
func readSymbolTool() Tool {
	return Tool{
		Name:     "read_symbol",
		ReadOnly: true,
		Description: "Read a single named function, method, type or class by name, instead of " +
			"reading the whole file. Use outline first to discover symbol names. Keeps context small.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path inside the workspace."},
    "symbol": {"type": "string", "description": "Symbol name (function, method, type, class)."}
  },
  "required": ["path", "symbol"]
}`),
		Handler: handleReadSymbol,
	}
}

type readSymbolArgs struct {
	Path   string `json:"path"`
	Symbol string `json:"symbol"`
}

func handleReadSymbol(ctx context.Context, raw json.RawMessage) (string, error) {
	var a readSymbolArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	resolved, err := env.resolve(a.Path, "file")
	if err != nil {
		return "", err
	}
	data, err := readFileCapped(resolved, limits.CodeReadMax)
	if err != nil {
		return "", err
	}
	src := string(data)
	ext := strings.ToLower(filepath.Ext(resolved))
	var body string
	var startLine int
	if ext == ".go" {
		body, startLine = goReadSymbol(src, a.Symbol)
	} else {
		body, startLine = regexReadSymbol(src, a.Symbol)
	}
	if body == "" {
		return "", fmt.Errorf("symbol %q not found in %s", a.Symbol, a.Path)
	}
	out := fmt.Sprintf("%s:%d\n%s", env.rel(resolved), startLine, body)
	return clip(env, out, reduce.KindGeneric), nil
}

func goReadSymbol(src, name string) (string, int) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, 0)
	if err != nil {
		return "", 0
	}
	lines := strings.Split(src, "\n")
	for _, decl := range f.Decls {
		switch v := decl.(type) {
		case *ast.FuncDecl:
			symName := v.Name.Name
			if v.Recv != nil && len(v.Recv.List) > 0 {
				symName = recvType(v.Recv.List[0].Type) + "." + symName
			}
			if symName == name {
				start := fset.Position(v.Pos()).Line
				end := fset.Position(v.End()).Line
				return joinLines(lines, start, end), start
			}
		case *ast.GenDecl:
			for _, spec := range v.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.Name == name {
					start := fset.Position(ts.Pos()).Line
					end := fset.Position(ts.End()).Line
					// extend to the GenDecl's end if it's a single spec
					if v.Lparen == 0 {
						end = fset.Position(v.End()).Line
					}
					return joinLines(lines, start, end), start
				}
			}
		}
	}
	return "", 0
}

func joinLines(lines []string, from, to int) string {
	if from < 1 {
		from = 1
	}
	if to > len(lines) {
		to = len(lines)
	}
	return strings.Join(lines[from-1:to], "\n")
}

func regexReadSymbol(src, name string) (string, int) {
	lines := strings.Split(src, "\n")
	// find the line declaring the symbol
	re := regexp.MustCompile(`(?m)(?:func|function|def|fn|class|struct|type|interface)\s+` + regexp.QuoteMeta(name) + `\b`)
	loc := re.FindStringIndex(src)
	if loc == nil {
		return "", 0
	}
	startLine := 1 + strings.Count(src[:loc[0]], "\n")
	// read until matching close brace or blank-terminated block
	depth := 0
	started := false
	endLine := startLine
	for i := startLine - 1; i < len(lines); i++ {
		line := lines[i]
		for _, c := range line {
			if c == '{' {
				depth++
				started = true
			} else if c == '}' {
				depth--
				if started && depth == 0 {
					endLine = i + 1
					return strings.Join(lines[startLine-1:endLine], "\n"), startLine
				}
			}
		}
		endLine = i + 1
		if i > startLine+200 {
			break
		}
	}
	return strings.Join(lines[startLine-1:endLine], "\n"), startLine
}

// appendTool appends content to a file (creates if missing). Useful for
// incremental code generation without rewriting the whole file.
func appendTool() Tool {
	return Tool{
		Name:     "append",
		ReadOnly: false,
		Description: "Append content to the end of a file (creates it if missing). " +
			"Use for incremental code generation and log-style accumulation without rewriting the whole file.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path inside the workspace."},
    "content": {"type": "string", "description": "Content to append."}
  },
  "required": ["path", "content"]
}`),
		Handler: handleAppend,
	}
}

type appendArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func handleAppend(ctx context.Context, raw json.RawMessage) (string, error) {
	var a appendArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	resolved, err := env.resolve(a.Path, "")
	if err != nil {
		return "", err
	}
	perm := os.FileMode(0o600)
	existed := false
	if info, err := os.Stat(resolved); err == nil {
		perm = info.Mode().Perm()
		existed = true
		if info.IsDir() {
			return "", fmt.Errorf("%s is a directory", a.Path)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(resolved), 0o700); err != nil {
			return "", err
		}
	}
	note := checkpointForMutation(env, resolved, "append")
	// Identity-checked append: a symlink swapped in after resolve() fails the
	// check instead of receiving our bytes; new files use O_EXCL.
	flag := os.O_APPEND | os.O_CREATE | os.O_EXCL | os.O_WRONLY
	if existed {
		flag = os.O_APPEND | os.O_WRONLY
	}
	f, err := openChecked(env, resolved, flag, perm)
	if err != nil {
		return "", err
	}
	n, err := f.WriteString(a.Content)
	f.Close()
	_ = os.Chmod(resolved, perm)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("appended %d bytes to %s%s", n, a.Path, note), nil
}

// insertTool inserts content at a specific line number, shifting the rest down.
func insertTool() Tool {
	return Tool{
		Name:     "insert",
		ReadOnly: false,
		Description: "Insert content at a specific 1-based line number, shifting existing lines down. " +
			"Use for surgical additions (imports, new functions) without rewriting the whole file.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path inside the workspace."},
    "line": {"type": "integer", "description": "1-based line number to insert before."},
    "content": {"type": "string", "description": "Content to insert."}
  },
  "required": ["path", "line", "content"]
}`),
		Handler: handleInsert,
	}
}

type insertArgs struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

func handleInsert(ctx context.Context, raw json.RawMessage) (string, error) {
	var a insertArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	resolved, err := env.resolve(a.Path, "file")
	if err != nil {
		return "", err
	}
	data, err := readFileCapped(resolved, limits.CodeReadMax)
	if err != nil {
		return "", err
	}
	note := checkpointForMutation(env, resolved, "insert")
	lines := strings.Split(string(data), "\n")
	if a.Line < 1 {
		a.Line = 1
	}
	if a.Line > len(lines)+1 {
		a.Line = len(lines) + 1
	}
	insertLines := strings.Split(a.Content, "\n")
	out := make([]string, 0, len(lines)+len(insertLines))
	out = append(out, lines[:a.Line-1]...)
	out = append(out, insertLines...)
	out = append(out, lines[a.Line-1:]...)
	if err := writeFilePreserveMode(env, resolved, []byte(strings.Join(out, "\n"))); err != nil {
		return "", err
	}
	return fmt.Sprintf("inserted %d lines before line %d in %s%s", len(insertLines), a.Line, a.Path, note), nil
}

// patchTool applies a unified diff to a file. Essential for reviewing and
// applying precise changes without rewriting whole files.
func patchTool() Tool {
	return Tool{
		Name:     "patch",
		ReadOnly: false,
		Description: "Apply a unified diff (git-style) to a file. Essential for precise, reviewable " +
			"changes without rewriting whole files. Returns a summary of hunks applied.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path inside the workspace."},
    "diff": {"type": "string", "description": "Unified diff to apply."}
  },
  "required": ["path", "diff"]
}`),
		Handler: handlePatch,
	}
}

type patchArgs struct {
	Path string `json:"path"`
	Diff string `json:"diff"`
}

func handlePatch(ctx context.Context, raw json.RawMessage) (string, error) {
	var a patchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	resolved, err := env.resolve(a.Path, "file")
	if err != nil {
		return "", err
	}
	data, err := readFileCapped(resolved, limits.CodeReadMax)
	if err != nil {
		return "", err
	}
	note := checkpointForMutation(env, resolved, "patch")
	original := string(data)
	patched, hunks, err := applyUnifiedDiff(original, a.Diff)
	if err != nil {
		return "", err
	}
	if err := writeFilePreserveMode(env, resolved, []byte(patched)); err != nil {
		return "", err
	}
	return fmt.Sprintf("applied %d hunk(s) to %s%s", hunks, a.Path, note), nil
}

// applyUnifiedDiff applies a unified diff to original content and returns the
// patched content and the number of hunks applied.
// hunk represents one @@ ... @@ block.
type hunk struct {
	oldStart int // 0-based
	oldCount int
	newStart int
	newCount int
	lines    []string // prefixed with ' ', '-', '+', or '\\'
}

// parseHunks extracts all hunks from a unified diff. The body length is
// known in advance from the @@ header counts — oldCount lines drawn from the
// original file (context + removals) and newCount lines drawn from the new
// file (context + additions) — so the parser consumes exactly that many
// body lines and fails on any inconsistency instead of guessing. Removed
// lines whose content starts with "--" and added lines whose content starts
// with "++" (which a naive prefix-scanner would mistake for diff headers)
// are consumed correctly, and a truncated body is a hard error that never
// reaches the file.
func parseHunks(diff string) ([]hunk, error) {
	var hunks []hunk
	lines := strings.Split(diff, "\n")
	i := 0
	for i < len(lines) {
		if !strings.HasPrefix(lines[i], "@@") {
			i++
			continue
		}
		h := hunk{}
		var err error
		h.oldStart, h.oldCount, h.newStart, h.newCount, err = parseHunkHeader(lines[i])
		if err != nil {
			return nil, err
		}
		h.oldStart-- // to 0-based
		h.newStart--
		i++
		oldLeft, newLeft := h.oldCount, h.newCount
		for oldLeft > 0 || newLeft > 0 {
			if i >= len(lines) {
				return nil, fmt.Errorf("hunk at old line %d is truncated: diff header wants %d original and %d updated lines but the diff ended",
					h.oldStart+1, h.oldCount, h.newCount)
			}
			line := lines[i]
			if strings.HasPrefix(line, `\`) {
				// "\ No newline at end of file" annotates the previous line;
				// it does not consume a count.
				h.lines = append(h.lines, line)
				i++
				continue
			}
			if line == "" {
				// An empty line is a context line with empty content whose
				// leading space some diff producers strip.
				line = " "
			}
			switch line[0] {
			case ' ':
				oldLeft--
				newLeft--
				h.lines = append(h.lines, line)
				i++
			case '-':
				oldLeft--
				h.lines = append(h.lines, line)
				i++
			case '+':
				newLeft--
				h.lines = append(h.lines, line)
				i++
			default:
				return nil, fmt.Errorf("unexpected line %q in hunk body (wanted %d original and %d updated lines)",
					line, oldLeft, newLeft)
			}
		}
		hunks = append(hunks, h)
	}
	if len(hunks) == 0 {
		return nil, fmt.Errorf("no hunks found in diff")
	}
	return hunks, nil
}

// parseHunkHeader parses "@@ -a,b +c,d @@ ..." and returns 1-based starts and
// counts. Counts default to 1; zero is legal (pure insertion/deletion).
func parseHunkHeader(line string) (oldStart, oldCount, newStart, newCount int, err error) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return 0, 0, 0, 0, fmt.Errorf("malformed hunk header: %s", line)
	}
	if oldStart, oldCount, err = parseRangeSpec(fields[1], "-", "old"); err != nil {
		return 0, 0, 0, 0, err
	}
	if newStart, newCount, err = parseRangeSpec(fields[2], "+", "new"); err != nil {
		return 0, 0, 0, 0, err
	}
	return oldStart, oldCount, newStart, newCount, nil
}

// parseRangeSpec parses "-<start>[,<count>]" / "+<start>[,<count>]".
func parseRangeSpec(spec, prefix, name string) (start, count int, err error) {
	if !strings.HasPrefix(spec, prefix) {
		return 0, 0, fmt.Errorf("malformed %s spec %q (want %s<start>[,<count>])", name, spec, prefix)
	}
	body := spec[len(prefix):]
	startPart, countPart := body, "1"
	if comma := strings.Index(body, ","); comma >= 0 {
		startPart, countPart = body[:comma], body[comma+1:]
	}
	start, err = strconv.Atoi(startPart)
	if err != nil {
		return 0, 0, fmt.Errorf("malformed %s start in %q", name, spec)
	}
	count, err = strconv.Atoi(countPart)
	if err != nil || count < 0 {
		return 0, 0, fmt.Errorf("malformed %s count in %q", name, spec)
	}
	return start, count, nil
}

// applyUnifiedDiff applies a unified diff to original content and returns the
// patched content and the number of hunks applied. It verifies context lines
// match (with up to 3 lines of fuzz) and refuses to apply a hunk whose context
// does not line up, so a stale patch can never silently corrupt a file.
func applyUnifiedDiff(original, diff string) (string, int, error) {
	hunks, err := parseHunks(diff)
	if err != nil {
		return original, 0, err
	}
	lines := strings.Split(original, "\n")
	// track whether original ended with a newline
	trailingNL := strings.HasSuffix(original, "\n")
	if trailingNL && len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	out := make([]string, 0, len(lines)+64)
	pos := 0 // current position in original
	applied := 0
	for _, h := range hunks {
		// locate the hunk's old block in the original, allowing fuzz
		start := h.oldStart
		if start < 0 {
			start = 0
		}
		if start > len(lines) {
			start = len(lines)
		}
		// try exact, then fuzz up to 3 lines in either direction
		bestOffset := -1
		for fuzz := 0; fuzz <= 3; fuzz++ {
			for _, sign := range []int{0, -1, 1} {
				if fuzz == 0 && sign != 0 {
					continue
				}
				off := start + sign*fuzz
				if off < 0 || off > len(lines) {
					continue
				}
				if matchHunk(lines, off, h) {
					bestOffset = off
					break
				}
			}
			if bestOffset >= 0 {
				break
			}
		}
		if bestOffset < 0 {
			return original, applied, fmt.Errorf("hunk at old line %d does not match (context mismatch)", h.oldStart+1)
		}
		// copy unchanged lines up to the hunk
		for pos < bestOffset {
			out = append(out, lines[pos])
			pos++
		}
		// apply hunk
		for _, l := range h.lines {
			if l == "" {
				continue
			}
			switch l[0] {
			case ' ':
				// context: must match
				if pos >= len(lines) || lines[pos] != l[1:] {
					return original, applied, fmt.Errorf("context mismatch at line %d", pos+1)
				}
				out = append(out, lines[pos])
				pos++
			case '-':
				if pos >= len(lines) || lines[pos] != l[1:] {
					return original, applied, fmt.Errorf("removal mismatch at line %d: want %q got %q", pos+1, l[1:], safeIndex(lines, pos))
				}
				pos++
			case '+':
				out = append(out, l[1:])
			case '\\':
				// "\ No newline at end of file" - informational
			}
		}
		applied++
	}
	// copy remaining lines
	for pos < len(lines) {
		out = append(out, lines[pos])
		pos++
	}
	result := strings.Join(out, "\n")
	if trailingNL {
		result += "\n"
	}
	return result, applied, nil
}

func matchHunk(lines []string, offset int, h hunk) bool {
	pos := offset
	for _, l := range h.lines {
		if l == "" {
			continue
		}
		switch l[0] {
		case ' ', '-':
			if pos >= len(lines) || lines[pos] != l[1:] {
				return false
			}
			pos++
		case '+':
			// additions don't consume original lines
		case '\\':
		}
	}
	return true
}

func safeIndex(lines []string, i int) string {
	if i >= 0 && i < len(lines) {
		return lines[i]
	}
	return "<eof>"
}

// diffTool compares two files and returns a unified diff.
func diffTool() Tool {
	return Tool{
		Name:     "diff",
		ReadOnly: true,
		Description: "Compare two files and return a unified diff. Use for reviewing changes, " +
			"comparing implementations, or understanding what changed between two versions.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path_a": {"type": "string", "description": "First file path."},
    "path_b": {"type": "string", "description": "Second file path."}
  },
  "required": ["path_a", "path_b"]
}`),
		Handler: handleDiff,
	}
}

type diffArgs struct {
	PathA string `json:"path_a"`
	PathB string `json:"path_b"`
}

func handleDiff(ctx context.Context, raw json.RawMessage) (string, error) {
	var a diffArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	ra, err := env.resolve(a.PathA, "file")
	if err != nil {
		return "", err
	}
	rb, err := env.resolve(a.PathB, "file")
	if err != nil {
		return "", err
	}
	da, err := readFileCapped(ra, limits.CodeReadMax)
	if err != nil {
		return "", err
	}
	db, err := readFileCapped(rb, limits.CodeReadMax)
	if err != nil {
		return "", err
	}
	la := strings.Split(string(da), "\n")
	lb := strings.Split(string(db), "\n")
	// common-prefix/suffix diff (not a full LCS)
	diff := simpleDiff(la, lb, a.PathA, a.PathB)
	return clip(env, diff, reduce.KindGeneric), nil
}

func simpleDiff(a, b []string, nameA, nameB string) string {
	var out strings.Builder
	out.WriteString(fmt.Sprintf("--- %s\n+++ %s\n", nameA, nameB))
	// find common prefix
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	// find common suffix
	j := 0
	for j < len(a)-i && j < len(b)-i && a[len(a)-1-j] == b[len(b)-1-j] {
		j++
	}
	// print context + changes
	start := i - 3
	if start < 0 {
		start = 0
	}
	endA := len(a) - j
	endB := len(b) - j
	if endA < i {
		endA = i
	}
	if endB < i {
		endB = i
	}
	// context before
	for k := start; k < i; k++ {
		if k < len(a) {
			out.WriteString(fmt.Sprintf(" %s\n", a[k]))
		}
	}
	// removed lines
	for k := i; k < endA; k++ {
		if k < len(a) {
			out.WriteString(fmt.Sprintf("-%s\n", a[k]))
		}
	}
	// added lines
	for k := i; k < endB; k++ {
		if k < len(b) {
			out.WriteString(fmt.Sprintf("+%s\n", b[k]))
		}
	}
	// context after
	for k := endA; k < endA+3 && k < len(a); k++ {
		out.WriteString(fmt.Sprintf(" %s\n", a[k]))
	}
	return out.String()
}

// reviewTool reads a file and produces a structured review: line count, long
// lines, TODO/FIXME markers, and basic complexity hints. A lightweight
// code-review assistant.
func reviewTool() Tool {
	return Tool{
		Name:     "review",
		ReadOnly: true,
		Description: "Produce a structured review of a file: line count, long lines (>200 chars), " +
			"TODO/FIXME/HACK markers, and basic complexity hints. A lightweight code-review assistant.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path inside the workspace."}
  },
  "required": ["path"]
}`),
		Handler: handleReview,
	}
}

type reviewArgs struct {
	Path string `json:"path"`
}

func handleReview(ctx context.Context, raw json.RawMessage) (string, error) {
	var a reviewArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	resolved, err := env.resolve(a.Path, "file")
	if err != nil {
		return "", err
	}
	data, err := readFileCapped(resolved, limits.CodeReadMax)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	var longLines []int
	var markers []string
	markerRe := regexp.MustCompile(`(?i)(TODO|FIXME|HACK|XXX|BUG|NOTE)`)
	for i, line := range lines {
		if len(line) > 200 {
			longLines = append(longLines, i+1)
		}
		if m := markerRe.FindString(line); m != "" {
			markers = append(markers, fmt.Sprintf("  line %d: %s", i+1, strings.TrimSpace(line)))
		}
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("review: %s\n", env.rel(resolved)))
	b.WriteString(fmt.Sprintf("  lines: %d\n", len(lines)))
	b.WriteString(fmt.Sprintf("  bytes: %d\n", len(data)))
	b.WriteString(fmt.Sprintf("  long lines (>200 chars): %d\n", len(longLines)))
	if len(longLines) > 0 && len(longLines) <= 20 {
		b.WriteString(fmt.Sprintf("    %v\n", longLines))
	}
	b.WriteString(fmt.Sprintf("  markers (TODO/FIXME/HACK): %d\n", len(markers)))
	for _, m := range markers {
		b.WriteString(m + "\n")
	}
	// complexity hint: count of functions/methods
	funcCount := 0
	for _, line := range lines {
		tl := strings.TrimSpace(line)
		if strings.HasPrefix(tl, "func ") || strings.HasPrefix(tl, "def ") || strings.HasPrefix(tl, "function ") {
			funcCount++
		}
	}
	b.WriteString(fmt.Sprintf("  function/method declarations: %d\n", funcCount))
	if funcCount > 30 {
		b.WriteString("  hint: high function count; consider splitting into smaller files\n")
	}
	if len(longLines) > 5 {
		b.WriteString("  hint: multiple very long lines; consider refactoring for readability\n")
	}
	return clip(env, b.String(), reduce.KindGeneric), nil
}

// searchFilesTool searches for a pattern across multiple files and returns
// matches grouped by file, with context lines. Advanced grep with multi-file
// context.
func searchFilesTool() Tool {
	return Tool{
		Name:     "search_files",
		ReadOnly: true,
		Description: "Search a Go regexp across multiple files with context lines. Returns matches " +
			"grouped by file with N lines of context. More powerful than grep for code review.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Go regular expression."},
    "glob": {"type": "string", "description": "File glob (default **/*)."},
    "context": {"type": "integer", "description": "Lines of context around each match (default 2)."},
    "max_results": {"type": "integer", "description": "Max matches (default 50)."}
  },
  "required": ["pattern"]
}`),
		Handler: handleSearchFiles,
	}
}

type searchFilesArgs struct {
	Pattern    string `json:"pattern"`
	Glob       string `json:"glob"`
	Context    int    `json:"context"`
	MaxResults int    `json:"max_results"`
}

// looksTextFile reports whether the first bytes of p contain no NUL. Missing
// or unreadable files are skipped (false).
func looksTextFile(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return false
	}
	return !isBinary(buf[:n])
}

func handleSearchFiles(ctx context.Context, raw json.RawMessage) (string, error) {
	var a searchFilesArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	re, err := regexp.Compile(a.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regexp: %w", err)
	}
	if a.Glob == "" {
		a.Glob = "**/*"
	}
	if a.Context < 0 {
		a.Context = 0
	}
	if a.Context > 16 {
		// Hard cap — without one a 1e6 context would stream whole files into
		// the accumulator per match.
		a.Context = 16
	}
	if a.MaxResults <= 0 {
		a.MaxResults = 50
	}
	if a.MaxResults > limits.GrepResults {
		a.MaxResults = limits.GrepResults
	}
	// Expand the glob, supporting ** for recursive matching (filepath.Glob
	// does not). When ** is present, walk the workspace tree (skipping ignored
	// directories and oversized/binary files) and filter by the tail pattern.
	root, err := filepath.Abs(env.Workspace)
	if err != nil {
		return "", err
	}
	files := []string{}
	var skipped []string
	if strings.Contains(a.Glob, "**") {
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				skipped = append(skipped, p)
				return nil
			}
			if info.IsDir() {
				if ignoredDirs[info.Name()] && p != root {
					return filepath.SkipDir
				}
				return nil
			}
			if info.Size() > limits.GrepFileMax || !insideWorkspace(root, p) {
				return nil
			}
			if !looksTextFile(p) {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if matchDoubleStar(rel, a.Glob) {
				files = append(files, p)
			}
			return nil
		})
	} else {
		// Reject escapes: the joined pattern must stay under the resolved root.
		joined := filepath.Join(root, a.Glob)
		joinedAbs, err := filepath.Abs(joined)
		if err != nil {
			return "", fmt.Errorf("invalid glob: %w", err)
		}
		rootResolved, err := evalSymlinksPath(root)
		if err != nil {
			return "", err
		}
		absResolved, err := evalSymlinksPath(joinedAbs)
		if err == nil {
			joinedAbs = absResolved
		}
		rel, err := filepath.Rel(rootResolved, joinedAbs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("glob %q is outside the workspace", a.Glob)
		}
		matched, err := filepath.Glob(joinedAbs)
		if err != nil {
			return "", fmt.Errorf("invalid glob: %w", err)
		}
		for _, f := range matched {
			info, err := os.Stat(f)
			if err != nil || info.IsDir() || info.Size() > limits.GrepFileMax {
				continue
			}
			if !insideWorkspace(root, f) {
				continue
			}
			if !looksTextFile(f) {
				continue
			}
			relF, err := filepath.Rel(rootResolved, f)
			if err == nil && relF != ".." && !strings.HasPrefix(relF, ".."+string(filepath.Separator)) {
				files = append(files, f)
			}
		}
	}
	if len(files) > limits.GrepResults*4 {
		files = files[:limits.GrepResults*4]
	}
	var b strings.Builder
	total := 0
	for _, f := range files {
		if total >= a.MaxResults {
			break
		}
		data, err := readFileCapped(f, limits.CodeReadMax)
		if err != nil {
			skipped = append(skipped, f)
			continue
		}
		lines := strings.Split(string(data), "\n")
		var fileMatches []string
		for i, line := range lines {
			if total >= a.MaxResults {
				break
			}
			if re.MatchString(line) {
				total++
				// context
				start := i - a.Context
				if start < 0 {
					start = 0
				}
				end := i + a.Context + 1
				if end > len(lines) {
					end = len(lines)
				}
				for j := start; j < end; j++ {
					marker := " "
					if j == i {
						marker = ">"
					}
					fileMatches = append(fileMatches, fmt.Sprintf("  %s %4d: %s", marker, j+1, truncateLine(lines[j], 300)))
				}
				fileMatches = append(fileMatches, "")
			}
		}
		if len(fileMatches) > 0 {
			rel, _ := filepath.Rel(env.Workspace, f)
			b.WriteString(fmt.Sprintf("%s:\n", rel))
			for _, m := range fileMatches {
				b.WriteString(m + "\n")
			}
		}
	}
	skippedNote := ""
	relSkipped := []string(nil)
	if len(skipped) > 0 {
		relSkipped = make([]string, 0, len(skipped))
		for _, s := range skipped {
			if rel, err := filepath.Rel(env.Workspace, s); err == nil {
				relSkipped = append(relSkipped, filepath.ToSlash(rel))
			} else {
				relSkipped = append(relSkipped, s)
			}
		}
		skippedNote = fmt.Sprintf("\nnote: %d file(s) skipped (unreadable): %s\n", len(skipped), strings.Join(skippedCapped(relSkipped), ", "))
	}
	if total == 0 {
		if skippedNote != "" {
			// "No matches" with skipped files is a lie by omission.
			return fmt.Sprintf("no matches for %q (but %d file(s) were skipped: %s)", a.Pattern, len(skipped), strings.Join(skippedCapped(relSkipped), ", ")), nil
		}
		return fmt.Sprintf("no matches for %q", a.Pattern), nil
	}
	b.WriteString(fmt.Sprintf("\n%d match(es) total\n", total))
	return clip(env, b.String(), reduce.KindSearch) + skippedNote, nil
}

func truncateLine(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "... (" + fmt.Sprintf("%d", len(r)-max) + " more chars)"
}

// matchDoubleStar matches a path against a glob that may contain ** segments.
// ** matches any number of path segments (including zero).
func matchDoubleStar(path, pattern string) bool {
	path = filepath.ToSlash(path)
	pattern = filepath.ToSlash(pattern)
	// split both into segments
	pSegs := strings.Split(path, "/")
	gSegs := strings.Split(pattern, "/")
	return matchDS(pSegs, gSegs)
}

func matchDS(p, g []string) bool {
	// dynamic programming over segments
	// dp[i][j] = p[i:] matches g[j:]
	// use memoization via recursion with cache
	cache := map[string]bool{}
	var rec func(i, j int) bool
	rec = func(i, j int) bool {
		key := fmt.Sprintf("%d,%d", i, j)
		if v, ok := cache[key]; ok {
			return v
		}
		// both consumed
		if i == len(p) && j == len(g) {
			cache[key] = true
			return true
		}
		// pattern exhausted but path remains
		if j == len(g) {
			cache[key] = false
			return false
		}
		// ** matches zero or more segments
		if g[j] == "**" {
			// try consuming zero segments (skip **) or one segment (consume path)
			res := rec(i, j+1) || (i < len(p) && rec(i+1, j))
			cache[key] = res
			return res
		}
		// path exhausted but pattern remains (and not **)
		if i == len(p) {
			cache[key] = false
			return false
		}
		// single-segment match with * and ?
		ok, _ := filepath.Match(g[j], p[i])
		if !ok {
			cache[key] = false
			return false
		}
		res := rec(i+1, j+1)
		cache[key] = res
		return res
	}
	return rec(0, 0)
}
