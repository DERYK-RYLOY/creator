package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/creatorcli/creator/internal/reduce"
)

// webFetchTool fetches a page and returns readable text rather than markup.
//
// net_fetch is the raw-byte tool: it exists for recon and endpoints. This is
// the reading tool: HTML is reduced to prose so a documentation page or issue
// does not spend the whole observation budget on <script> tags and class names.
func webFetchTool() Tool {
	return Tool{
		Name:     "web_fetch",
		ReadOnly: true,
		Description: "Fetch an http(s) URL and return its readable text: HTML is converted to prose (scripts, styles, " +
			"navigation markup and tags removed), entities decoded, whitespace collapsed. Use it to read documentation, " +
			"issues or specs. For raw bodies use net_fetch.",
		Schema: schema(`{
  "type": "object",
  "properties": {
    "url": {"type": "string", "description": "Absolute http(s) URL."},
    "max_chars": {"type": "integer", "description": "Maximum characters of readable text to return (default 20000, max 200000)."}
  },
  "required": ["url"]
}`),
		Handler: handleWebFetch,
	}
}

type webFetchArgs struct {
	URL      string `json:"url"`
	MaxChars int    `json:"max_chars"`
}

const (
	webFetchMaxBytes = 4 << 20
	webFetchDefault  = 20000
	webFetchCeiling  = 200000
)

// shellQuoteArg single-quotes s so it parses as exactly one shell word.
// Used when an untrusted argument is handed to a command-string policy check,
// so the classifier sees the same word the real command would get - never
// flags smuggled inside the value.
func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func handleWebFetch(ctx context.Context, raw json.RawMessage) (string, error) {
	var a webFetchArgs
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
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.5")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBytes))
	if err != nil {
		return "", err
	}

	text := string(body)
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "html") || looksLikeHTML(text) {
		text = htmlToText(text)
	} else {
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}
	// A fetched page must never carry terminal control sequences into the
	// rendered output (clear-screen, OSC 8 hyperlinks, fake prompts...).
	text = stripControlSeq(text)
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("the page produced no readable text (content-type %q)", ct)
	}

	max := a.MaxChars
	if max <= 0 {
		max = webFetchDefault
	}
	if max > webFetchCeiling {
		max = webFetchCeiling
	}
	if len(text) > max {
		cut := max
		for cut > 0 && !isRuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + fmt.Sprintf("\nÃƒÂ¢Ã¢â€šÂ¬Ã‚Â¦ (truncated at %d characters)", cut)
	}
	header := fmt.Sprintf("%s · %s · %d bytes", stripControlSeq(a.URL), statusLabel(ct), len(body))
	return header + "\n" + clip(env, text, reduce.KindGeneric), nil
}

func statusLabel(contentType string) string {
	if contentType == "" {
		return "text"
	}
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return strings.TrimSpace(contentType)
}

func looksLikeHTML(s string) bool {
	head := s
	if len(head) > 512 {
		head = head[:512]
	}
	head = strings.ToLower(head)
	return strings.Contains(head, "<!doctype html") || strings.Contains(head, "<html")
}

// isRuneStart reports whether b begins a UTF-8 sequence.
func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// checkPublicHTTPURL refuses URLs whose scheme is not http(s) or whose host
// resolves to loopback/private/link-local/unspecified/CGN space. The first is
// required by the wire protocol; the second keeps the read-only fetch tools
// from becoming an internal-network oracle.
func checkPublicHTTPURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url must use http:// or https://")
	}
	if v := os.Getenv("CREATOR_ALLOW_PRIVATE_NET"); v == "1" || strings.EqualFold(v, "true") {
		return nil // explicit opt-in for tests / trusted internal networks
	}
	return validatePublicHost(ctx, u)
}

// validatePublicHost checks that u's host resolves exclusively to public
// addresses. It does not look at CREATOR_ALLOW_PRIVATE_NET.
func validatePublicHost(ctx context.Context, u *url.URL) error {
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("url must have a host")
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("cannot resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("cannot resolve %q", host)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("refusing %q: resolves to non-public address %s", host, ip)
		}
	}
	return nil
}

// checkRedirectTarget validates one redirect hop. Unlike the initial-request
// check it ignores CREATOR_ALLOW_PRIVATE_NET: an explicitly trusted internal
// fetch may not be re-routed by the remote server into internal space ÃƒÂ¢Ã¢â€šÂ¬Ã¢â‚¬Â the
// opt-in belongs to the user, not to a 302.
func checkRedirectTarget(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid redirect url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("refusing redirect to non-http(s) url %q", u.Scheme)
	}
	return validatePublicHost(ctx, u)
}

// publicHTTPTransport dials through a custom DialContext that re-validates
// the resolved addresses AT DIAL TIME and pins the connection to one of
// them. The check and the connection therefore see the same resolution,
// closing the TOCTOU window a validate-then-dial DNS rebinding needs.
func publicHTTPTransport() *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			allowPrivate := false
			if v := os.Getenv("CREATOR_ALLOW_PRIVATE_NET"); v == "1" || strings.EqualFold(v, "true") {
				allowPrivate = true
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("cannot resolve %q: %w", host, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("cannot resolve %q", host)
			}
			if !allowPrivate {
				for _, ip := range ips {
					if isBlockedIP(ip) {
						return nil, fmt.Errorf("refusing %q: resolves to non-public address %s", host, ip)
					}
				}
			}
			// Pin to the first validated address: no second resolution.
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
	}
}

// publicRedirectClient returns a client whose CheckRedirect re-validates
// every hop against checkRedirectTarget, so a public URL answering 302 to
// 127.0.0.1/10.x/169.254.169.254 cannot launder the SSRF guard. The dial
// path additionally pins resolution to the validated addresses.
func publicRedirectClient(ctx context.Context) *http.Client {
	return &http.Client{
		Timeout:   25 * time.Second,
		Transport: publicHTTPTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			if err := checkRedirectTarget(ctx, req.URL.String()); err != nil {
				return fmt.Errorf("refusing redirect to %q: %w", req.URL.Host, err)
			}
			return nil
		},
	}
}

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsPrivate() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1]&0xc0 == 64 {
		return true // carrier-grade NAT, RFC 6598
	}
	return false
}

// htmlToText strips markup and returns readable prose. It is intentionally a
// small tolerant scanner, not a parser: real pages are malformed, and a model
// needs the sentences, not a DOM.
func htmlToText(src string) string {
	var out strings.Builder
	title := htmlTitle(src)

	blockTags := map[string]bool{
		"p": true, "div": true, "br": true, "li": true, "tr": true, "td": true, "th": true,
		"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
		"section": true, "article": true, "header": true, "footer": true, "main": true,
		"aside": true, "nav": true, "pre": true, "blockquote": true, "table": true,
		"ul": true, "ol": true, "dl": true, "dt": true, "dd": true, "hr": true,
		"figure": true, "figcaption": true, "form": true, "label": true, "option": true,
	}
	skipTags := map[string]bool{
		"script": true, "style": true, "head": true, "noscript": true,
		"template": true, "svg": true, "canvas": true, "iframe": true,
	}

	i := 0
	skip := ""
	for i < len(src) {
		if src[i] != '<' {
			j := strings.IndexByte(src[i:], '<')
			chunk := src[i:]
			if j >= 0 {
				chunk = src[i : i+j]
				i += j
			} else {
				i = len(src)
			}
			if skip == "" {
				out.WriteString(decodeEntities(chunk))
			}
			continue
		}
		if strings.HasPrefix(src[i:], "<!--") {
			end := strings.Index(src[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		end := strings.IndexByte(src[i:], '>')
		if end < 0 {
			break
		}
		tagBody := src[i+1 : i+end]
		i += end + 1
		name, closing := parseTag(tagBody)
		name = strings.ToLower(name)
		if name == "" {
			continue
		}
		if skip != "" {
			if closing && name == skip {
				skip = ""
			}
			continue
		}
		if !closing && skipTags[name] {
			skip = name
			continue
		}
		if blockTags[name] {
			out.WriteByte('\n')
			if name == "p" || name == "br" || name == "hr" || (len(name) == 2 && name[0] == 'h') {
				out.WriteByte('\n')
			}
		}
	}
	text := collapseWhitespace(out.String())
	if title != "" {
		return "title: " + title + "\n\n" + text
	}
	return text
}

// parseTag splits a tag body into its name and whether it closes an element.
// A trailing slash (self-closing syntax) is stripped along with the slash.
func parseTag(body string) (name string, closing bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", false
	}
	if strings.HasPrefix(body, "/") {
		closing = true
		body = strings.TrimSpace(body[1:])
	}
	body = strings.TrimSpace(strings.TrimSuffix(body, "/"))
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case ' ', '\t', '\n', '\r':
			return body[:i], closing
		}
	}
	return body, closing
}

func htmlTitle(src string) string {
	low := strings.ToLower(src)
	if len(low) != len(src) {
		// Case folding changed the byte length, so offsets would not line up.
		return ""
	}
	start := strings.Index(low, "<title")
	if start < 0 {
		return ""
	}
	open := strings.IndexByte(low[start:], '>')
	if open < 0 {
		return ""
	}
	start += open + 1
	end := strings.Index(low[start:], "</title>")
	if end < 0 {
		return ""
	}
	return collapseWhitespace(decodeEntities(src[start : start+end]))
}

// collapseWhitespace squeezes spaces and blank lines while keeping paragraph
// breaks, which is what makes fetched prose readable in a terminal.
func collapseWhitespace(s string) string {
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			blank++
			if blank > 1 || len(out) == 0 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// decodeEntities resolves the named and numeric entities that actually show up
// in documentation pages; unknown entities are left intact.
func decodeEntities(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '&' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i:], ';')
		if end < 0 || end > 12 {
			b.WriteByte(s[i])
			i++
			continue
		}
		entity := s[i+1 : i+end]
		if decoded, ok := decodeEntity(entity); ok {
			b.WriteString(decoded)
		} else {
			b.WriteString(s[i : i+end+1])
		}
		i += end + 1
	}
	return b.String()
}

func decodeEntity(entity string) (string, bool) {
	if entity == "" {
		return "", false
	}
	if entity[0] == '#' {
		var code int64
		var err error
		if len(entity) > 1 && (entity[1] == 'x' || entity[1] == 'X') {
			code, err = strconv.ParseInt(entity[2:], 16, 32)
		} else {
			code, err = strconv.ParseInt(entity[1:], 10, 32)
		}
		if err != nil || code <= 0 || code > 0x10FFFF {
			return "", false
		}
		return string(rune(code)), true
	}
	v, ok := namedEntities[entity]
	return v, ok
}

// namedEntities covers the entities that actually appear in documentation;
// anything else is left as written rather than mangled.
var namedEntities = map[string]string{
	"amp": "&", "lt": "<", "gt": ">", "quot": `"`, "apos": "'",
	"nbsp": " ", "ensp": " ", "emsp": " ", "thinsp": " ",
	"mdash": "ÃƒÂ¢Ã¢â€šÂ¬Ã¢â‚¬Â", "ndash": "ÃƒÂ¢Ã¢â€šÂ¬Ã¢â‚¬Å“", "hellip": "ÃƒÂ¢Ã¢â€šÂ¬Ã‚Â¦", "middot": "Ãƒâ€šÃ‚Â·",
	"lsquo": "ÃƒÂ¢Ã¢â€šÂ¬Ã‹Å“", "rsquo": "ÃƒÂ¢Ã¢â€šÂ¬Ã¢â€žÂ¢", "ldquo": "ÃƒÂ¢Ã¢â€šÂ¬Ã…â€œ", "rdquo": "ÃƒÂ¢Ã¢â€šÂ¬Ã‚Â",
	"laquo": "Ãƒâ€šÃ‚Â«", "raquo": "Ãƒâ€šÃ‚Â»", "copy": "Ãƒâ€šÃ‚Â©", "reg": "Ãƒâ€šÃ‚Â®", "trade": "ÃƒÂ¢Ã¢â‚¬Å¾Ã‚Â¢",
	"times": "ÃƒÆ’Ã¢â‚¬â€", "divide": "ÃƒÆ’Ã‚Â·", "deg": "Ãƒâ€šÃ‚Â°", "plusmn": "Ãƒâ€šÃ‚Â±",
	"euro": "ÃƒÂ¢Ã¢â‚¬Å¡Ã‚Â¬", "pound": "Ãƒâ€šÃ‚Â£", "yen": "Ãƒâ€šÃ‚Â¥", "cent": "Ãƒâ€šÃ‚Â¢",
	"sect": "Ãƒâ€šÃ‚Â§", "para": "Ãƒâ€šÃ‚Â¶", "bull": "ÃƒÂ¢Ã¢â€šÂ¬Ã‚Â¢", "dagger": "ÃƒÂ¢Ã¢â€šÂ¬Ã‚Â ",
	"larr": "ÃƒÂ¢Ã¢â‚¬Â Ã‚Â", "rarr": "ÃƒÂ¢Ã¢â‚¬Â Ã¢â‚¬â„¢", "uarr": "ÃƒÂ¢Ã¢â‚¬Â Ã¢â‚¬Ëœ", "darr": "ÃƒÂ¢Ã¢â‚¬Â Ã¢â‚¬Å“",
}
