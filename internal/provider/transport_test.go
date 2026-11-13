package provider

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fastRetries keeps the backoff schedule tiny so retry tests stay quick.
func fastRetries(t *testing.T) {
	t.Helper()
	base, max := retryBaseDelay, retryMaxDelay
	retryBaseDelay = time.Millisecond
	retryMaxDelay = 5 * time.Millisecond
	t.Cleanup(func() {
		retryBaseDelay, retryMaxDelay = base, max
	})
}

func TestIsTransientNet(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"reset by peer", errors.New("read tcp 10.0.0.1:443: read: connection reset by peer"), true},
		{"undici reset", errors.New("ECONNRESET: The socket connection was closed unexpectedly"), true},
		{"windows reset", errors.New("An existing connection was forcibly closed by the remote host"), true},
		{"broken pipe", errors.New("write tcp: broken pipe"), true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"bare eof", io.EOF, true},
		{"op error", &net.OpError{Op: "read", Err: errors.New("boom")}, true},
		{"dns nxdomain", &net.DNSError{IsNotFound: true, Name: "api.example.com"}, false},
		{"dns refused", &net.DNSError{IsNotFound: true, Name: "api.example.com"}, false},
		{"dns timeout", &net.DNSError{IsTimeout: true, Name: "api.example.com"}, true},
		{"dns temporary", &net.DNSError{IsTemporary: true, Name: "api.example.com"}, true},
		{"tls unknown authority", x509.UnknownAuthorityError{Cert: &x509.Certificate{}}, false},
		{"tls hostname mismatch", x509.HostnameError{Certificate: &x509.Certificate{}, Host: "api.example.com"}, false},
		{"tls invalid cert", x509.CertificateInvalidError{Cert: &x509.Certificate{}}, false},
		{"cancelled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"api error", errors.New("invalid api key"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := isTransientNet(tc.err); got != tc.want {
			t.Errorf("%s: isTransientNet = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsRetryableStatus(t *testing.T) {
	if !isRetryable(&httpError{status: 503, url: "u"}) {
		t.Error("503 should be retryable")
	}
	if !isRetryable(&httpError{status: 429, url: "u"}) {
		t.Error("429 should be retryable")
	}
	if isRetryable(&httpError{status: 400, url: "u"}) {
		t.Error("400 must not be retried")
	}
	if isRetryable(&httpError{status: 401, url: "u"}) {
		t.Error("401 must not be retried")
	}
	if !isRetryable(&inBandStreamError{status: 529, msg: "x"}) {
		t.Error("529 in-band must be retryable")
	}
	if isRetryable(&inBandStreamError{status: 400, msg: "x"}) {
		t.Error("400 in-band must not be retryable")
	}
}

func TestRetryDelayHonorsRetryAfter(t *testing.T) {
	resp := &http.Response{Header: http.Header{"Retry-After": {"2"}}}
	if d := retryDelay(0, resp); d != 2*time.Second {
		t.Fatalf("Retry-After ignored: %v", d)
	}
	// without the header the delay stays bounded by the cap
	d := retryDelay(10, nil)
	if d < 0 || d > retryMaxDelay {
		t.Fatalf("backoff out of range: %v", d)
	}
}

func TestPostJSONRetriesDroppedConnection(t *testing.T) {
	fastRetries(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// Accept the request, then drop the socket without a response:
			// this is what a reset or an expired keep-alive looks like.
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					conn.Close()
					return
				}
			}
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	data, err := postJSON(context.Background(), srv.URL, nil, map[string]string{"a": "b"})
	if err != nil {
		t.Fatalf("postJSON should survive one dropped connection: %v", err)
	}
	if !strings.Contains(string(data), `"ok":true`) {
		t.Fatalf("unexpected body: %s", data)
	}
	if calls != 2 {
		t.Fatalf("expected 2 attempts, got %d", calls)
	}
}

func TestPostJSONStopsOnClientError(t *testing.T) {
	fastRetries(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(400)
		io.WriteString(w, `{"error":"bad request"}`)
	}))
	defer srv.Close()

	if _, err := postJSON(context.Background(), srv.URL, nil, map[string]string{}); err == nil {
		t.Fatal("expected an error for HTTP 400")
	}
	if calls != 1 {
		t.Fatalf("400 must not be retried, got %d attempts", calls)
	}
}

func TestPostJSONHonorsCancelDuringBackoff(t *testing.T) {
	base, max := retryBaseDelay, retryMaxDelay
	retryBaseDelay = 200 * time.Millisecond
	retryMaxDelay = time.Second
	t.Cleanup(func() { retryBaseDelay, retryMaxDelay = base, max })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := postJSON(ctx, srv.URL, nil, map[string]string{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancellation should interrupt backoff, waited %v", elapsed)
	}
}

func mustGET(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestStreamHTTPClientHasNoOverallTimeout(t *testing.T) {
	if streamHTTPClient.Timeout != 0 {
		t.Fatalf("streaming client must not cap the body read; got %s", streamHTTPClient.Timeout)
	}
	tr, ok := streamHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatal("streaming client must carry a *http.Transport")
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Fatal("streaming client must bound the wait for response headers")
	}
	if httpClient.Timeout <= 0 {
		t.Fatal("unary client must keep its overall timeout")
	}
}

// A hostile/compromised endpoint answering 302 elsewhere must not receive
// the user's x-api-key: Go's default redirect policy never strips custom
// headers, only Authorization/Cookie/Www-Authenticate cross-origin.
func TestCrossHostRedirectIsRefused(t *testing.T) {
	keySeen := make(chan string, 1)
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keySeen <- r.Header.Get("x-api-key")
		w.WriteHeader(http.StatusOK)
	}))
	defer dst.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dst.URL+"/", http.StatusFound)
	}))
	defer src.Close()
	req := mustGET(t, src.URL+"/x")
	req.Header.Set("x-api-key", "secret-marker")
	if _, err := httpClient.Do(req); err == nil {
		t.Fatal("expected cross-host redirect to be refused")
	}
	select {
	case got := <-keySeen:
		t.Fatalf("x-api-key header leaked to redirect target: %q", got)
	default:
	}
}

func TestSameHostRedirectIsFollowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			io.WriteString(w, "ok")
			return
		}
		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	defer srv.Close()
	resp, err := httpClient.Do(mustGET(t, srv.URL+"/start"))
	if err != nil {
		t.Fatalf("same-host redirect should succeed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q", body)
	}
}
