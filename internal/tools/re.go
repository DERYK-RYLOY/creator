package tools

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/creatorcli/creator/internal/reduce"
)

// readFileCapped reads at most max+1 bytes from p and fails if the file is
// larger, so analysis tools cannot exhaust memory on multi-gigabyte blobs.
func readFileCapped(p string, max int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	lr := io.LimitReader(f, max+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes — narrow the analysis or use a smaller sample", p, max)
	}
	return data, nil
}

// hashTool computes cryptographic digests for triage, integrity checks and
// reverse-engineering artifact handling.
func hashTool() Tool {
	return Tool{
		Name:     "hash",
		ReadOnly: true,
		Description: "Compute cryptographic digests (sha256, sha512, sha1, md5) of a file. " +
			"Useful for triage, integrity checks and RE artifact handling.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path inside the workspace."},
    "algo": {"type": "string", "enum": ["sha256", "sha512", "sha1", "md5"], "description": "Digest algorithm."}
  },
  "required": ["path"]
}`),
		Handler: handleHash,
	}
}

type hashArgs struct {
	Path string `json:"path"`
	Algo string `json:"algo"`
}

func handleHash(ctx context.Context, raw json.RawMessage) (string, error) {
	var a hashArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	resolved, err := env.resolve(a.Path, "file")
	if err != nil {
		return "", err
	}
	algo := strings.ToLower(strings.TrimSpace(a.Algo))
	if algo == "" {
		algo = "sha256"
	}
	var h interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	}
	switch algo {
	case "sha256":
		h = sha256.New()
	case "sha512":
		h = sha512.New()
	case "sha1":
		h = sha1.New()
	case "md5":
		h = md5.New()
	default:
		return "", fmt.Errorf("unsupported hash algo %q", algo)
	}
	f, err := os.Open(resolved)
	if err != nil {
		return "", err
	}
	defer f.Close()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	out := fmt.Sprintf("%s  %s  (%d bytes)", algo, digest, n)
	return clip(env, out, reduce.KindGeneric), nil
}

// stringsTool extracts printable strings from a file, a standard first step in
// binary triage, firmware analysis and malware reverse engineering.
func stringsTool() Tool {
	return Tool{
		Name:     "strings",
		ReadOnly: true,
		Description: "Extract printable ASCII strings from a file (binary or text) using a minimum " +
			"run-length filter. Standard first step for reverse engineering and firmware triage.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path inside the workspace."},
    "min_len": {"type": "integer", "description": "Minimum string length to keep (default 8)."},
    "max_count": {"type": "integer", "description": "Maximum strings to return (default 200)."}
  },
  "required": ["path"]
}`),
		Handler: handleStrings,
	}
}

type stringsArgs struct {
	Path     string `json:"path"`
	MinLen   int    `json:"min_len"`
	MaxCount int    `json:"max_count"`
}

func handleStrings(ctx context.Context, raw json.RawMessage) (string, error) {
	var a stringsArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	env := currentEnv(ctx)
	resolved, err := env.resolve(a.Path, "file")
	if err != nil {
		return "", err
	}
	data, err := readFileCapped(resolved, limits.ReadBytes)
	if err != nil {
		return "", err
	}
	minLen := a.MinLen
	if minLen <= 0 {
		minLen = 8
	}
	maxCount := a.MaxCount
	if maxCount <= 0 {
		maxCount = 200
	}
	strs := extractStrings(string(data), minLen)
	if len(strs) > maxCount {
		strs = strs[:maxCount]
	}
	var b strings.Builder
	for _, s := range strs {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	return clip(env, b.String(), reduce.KindGeneric), nil
}

func extractStrings(data string, minLen int) []string {
	var out []string
	start := -1
	for i := 0; i < len(data); i++ {
		c := data[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.'
		if ok && start < 0 {
			start = i
		}
		if !ok {
			if start >= 0 && i-start >= minLen {
				out = append(out, data[start:i])
			}
			start = -1
		}
	}
	if start >= 0 && len(data)-start >= minLen {
		out = append(out, data[start:])
	}
	return out
}

// netFetchTool fetches a URL and returns bounded text, for live recon and
// endpoint probing from inside the agent's investigation loop.
func netFetchTool() Tool {
	return Tool{
		Name:     "net_fetch",
		ReadOnly: true,
		Description: "Fetch a URL over HTTP and return its body (text) up to a byte budget. " +
			"Use for live inspection and recon of endpoints during an investigation.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "url": {"type": "string", "description": "Absolute http(s) URL to fetch."},
    "max_bytes": {"type": "integer", "description": "Maximum bytes to return (default 40000)."}
  },
  "required": ["url"]
}`),
		Handler: handleNetFetch,
	}
}

type netFetchArgs struct {
	URL      string `json:"url"`
	MaxBytes int    `json:"max_bytes"`
}

// clampMaxBytes enforces the default and the hard cap for net_fetch's
// caller-supplied max_bytes. Anything above the cap is truncated, never
// allowed through to io.LimitReader with a multi-GB ceiling.
func clampMaxBytes(n int) int {
	if n <= 0 {
		return 40000
	}
	if n > webFetchMaxBytes {
		return webFetchMaxBytes
	}
	return n
}

func handleNetFetch(ctx context.Context, raw json.RawMessage) (string, error) {
	var a netFetchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	a.URL = strings.TrimSpace(a.URL)
	if !strings.HasPrefix(a.URL, "http://") && !strings.HasPrefix(a.URL, "https://") {
		return "", fmt.Errorf("url must be absolute and start with http:// or https://")
	}
	if err := checkPublicHTTPURL(ctx, a.URL); err != nil {
		return "", err
	}
	env := currentEnv(ctx)
	if env.Policy == nil {
		return "", fmt.Errorf("policy engine required for network fetches")
	}
	if _, err := env.Policy.Authorize(ctx, "curl -m 20 -sS "+shellQuoteArg(a.URL)); err != nil {
		return "", err
	}
	client := publicRedirectClient(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "creator/0.1 (+https://github.com/creatorcli/creator)")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch status %d", resp.StatusCode)
	}
	a.MaxBytes = clampMaxBytes(a.MaxBytes)
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(a.MaxBytes)))
	if err != nil {
		return "", err
	}
	return clip(env, string(body), reduce.KindGeneric), nil
}
