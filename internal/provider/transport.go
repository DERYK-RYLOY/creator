package provider

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxBodyBytes caps response bodies so a misbehaving endpoint cannot exhaust
// memory.
const maxBodyBytes = 64 << 20

// sameHostURL reports whether u refers to the same authority as orig — same
// hostname (case-insensitive) and same effective port (http/https default
// ports normalized). Scheme changes stay within the same authority only if
// the host is unchanged; different hosts are never the same authority.
func sameHostURL(u, orig *url.URL) bool {
	h, oh := strings.ToLower(u.Hostname()), strings.ToLower(orig.Hostname())
	if h != oh {
		return false
	}
	norm := func(scheme, port string) string {
		if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
			return ""
		}
		return port
	}
	return norm(u.Scheme, u.Port()) == norm(orig.Scheme, orig.Port())
}

// checkRedirectRefusingCrossHost refuses redirects to a different authority.
// Go's default policy strips Authorization cross-origin but never strips
// custom headers such as x-api-key, so a hostile or compromised endpoint
// answering 302 elsewhere would receive the user's API key. Refusing keeps
// credentials on the host the user configured. Same-origin redirects are
// followed.
func checkRedirectRefusingCrossHost(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if len(via) == 0 {
		return nil
	}
	if !sameHostURL(req.URL, via[0].URL) {
		return fmt.Errorf("refusing cross-host redirect from %s to %s (protects API credentials)",
			via[0].URL.Redacted(), req.URL.Redacted())
	}
	return nil
}

// A dropped connection is not an answer. Provider APIs, proxies and load
// balancers reset sockets routinely (idle keep-alive expiry, a rolling
// restart, an edge that gives up mid-response), so transient failures are
// retried with jittered exponential backoff instead of being surfaced as a
// failed turn. maxAttempts bounds the unary path; streamAttempts bounds whole
// stream re-opens that only happen while nothing has reached the caller.
const (
	maxAttempts    = 6
	streamAttempts = 3
)

var (
	retryBaseDelay = 500 * time.Millisecond
	retryMaxDelay  = 8 * time.Second
)

var httpClient = &http.Client{Timeout: 10 * time.Minute, CheckRedirect: checkRedirectRefusingCrossHost}

// streamHTTPClient serves the streaming path: no overall body-read cap
// (long reasoning generations must not be killed at a wall-clock deadline),
// only a bounded wait for response headers so a dead connection fails fast
// instead of hanging forever.
var streamHTTPClient = &http.Client{
	CheckRedirect: checkRedirectRefusingCrossHost,
	Transport: &http.Transport{
		ResponseHeaderTimeout: 120 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	},
}

// httpError carries a provider's status code and body for diagnostics.
type httpError struct {
	status int
	body   string
	url    string
}

// inBandStreamError marks an error event emitted inside a streaming SSE
// response (rather than an HTTP failure). The status is a synthetic
// HTTP-like code so isRetryable can classify overload conditions as
// retryable while leaving genuinely fatal events alone.
type inBandStreamError struct {
	status int
	msg    string
}

func (e *inBandStreamError) Error() string { return e.msg }

func (e *httpError) Error() string {
	body := strings.TrimSpace(e.body)
	if len(body) > 600 {
		body = body[:600] + "..."
	}
	return fmt.Sprintf("%s returned HTTP %d: %s", e.url, e.status, body)
}

// retryableStatus reports whether an HTTP status is worth another attempt.
func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests:
		return true
	}
	return status >= 500
}

// isTransientNet reports whether err is a connection-level failure rather than
// an answer from the provider. This is the class that surfaces to users as
// "connection reset by peer", "unexpected EOF" or "socket connection was
// closed unexpectedly"; none of them mean the model refused to respond.
func isTransientNet(err error) bool {
	if err == nil {
		return false
	}
	// A cancelled or expired caller context is not transient: retrying would
	// only delay the cancellation.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	// Deterministic failures — retrying these can never succeed: a
	// misconfigured endpoint, an unknown/mistyped hostname with no DNS
	// record, or a TLS certificate that fails verification. They all
	// surface as *net.OpError subclasses, so exclude them before the
	// blanket checks below.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		// A lookup that timed out or is flagged temporary may succeed on
		// retry; a definitive NXDOMAIN never will.
		return dnsErr.IsTimeout || dnsErr.IsTemporary
	}
	var certErr x509.CertificateInvalidError
	var authErr x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	if errors.As(err, &certErr) || errors.As(err, &authErr) || errors.As(err, &hostErr) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return true
	}
	// Some transports (proxies, vendored HTTP stacks) wrap the reset in an
	// opaque error, so the message is the last signal available.
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"connection reset",
		"connection was reset",
		"connection aborted",
		"forcibly closed",
		"broken pipe",
		"unexpected eof",
		"socket connection was closed",
		"server closed idle connection",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// isRetryable reports whether err is worth another attempt: a retryable HTTP
// status, or a connection-level failure such as ECONNRESET.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var he *httpError
	if errors.As(err, &he) {
		return retryableStatus(he.status)
	}
	var sse *inBandStreamError
	if errors.As(err, &sse) {
		return retryableStatus(sse.status)
	}
	return isTransientNet(err)
}

// retryDelay returns the wait before the next attempt: Retry-After when the
// provider sent one, otherwise exponential backoff with full jitter so a
// fleet of agents does not retry in lockstep.
func retryDelay(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if d, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
			// 2-minute ceiling still honors server hints longer than the
			// old 32s clamp while clamping absurd values.
			limit := 2 * time.Minute
			if d > limit {
				d = limit
			}
			return d
		}
	}
	d := retryBaseDelay << attempt
	if d <= 0 || d > retryMaxDelay {
		d = retryMaxDelay
	}
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// parseRetryAfter accepts both forms of the Retry-After header: delay-seconds
// and an HTTP date.
func parseRetryAfter(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	when, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	if d := time.Until(when); d > 0 {
		return d, true
	}
	return 0, true
}

// sleepCtx waits for d unless the caller cancels first. It reports false when
// the context ended, so retry loops return ctx.Err() instead of sleeping
// through a cancellation.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// dropIdleConnections discards pooled sockets after a reset so the next
// attempt dials a fresh connection instead of reusing a poisoned one.
func dropIdleConnections() {
	httpClient.CloseIdleConnections()
}

// postJSON sends body to url and retries transient failures with exponential
// backoff. It returns the decoded response body.
func postJSON(ctx context.Context, url string, headers map[string]string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var (
		lastErr error
		delay   time.Duration
	)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 && !sleepCtx(ctx, delay) {
			return nil, ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !isTransientNet(err) {
				return nil, err
			}
			lastErr = err
			dropIdleConnections()
			delay = retryDelay(attempt, nil)
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, int64(maxBodyBytes)+1))
		resp.Body.Close()
		if int64(len(data)) > int64(maxBodyBytes) {
			return nil, fmt.Errorf("response body exceeds %d bytes", maxBodyBytes)
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !isTransientNet(readErr) {
				return nil, readErr
			}
			lastErr = readErr
			dropIdleConnections()
			delay = retryDelay(attempt, nil)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = &httpError{status: resp.StatusCode, body: string(data), url: url}
			if retryableStatus(resp.StatusCode) {
				delay = retryDelay(attempt, resp)
				continue
			}
			return nil, lastErr
		}
		return data, nil
	}
	if lastErr == nil {
		lastErr = errors.New("request failed")
	}
	return nil, fmt.Errorf("%w (gave up after %d attempts)", lastErr, maxAttempts)
}
