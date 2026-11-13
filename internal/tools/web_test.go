package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebFetchReturnsReadableText(t *testing.T) {
	t.Setenv("CREATOR_ALLOW_PRIVATE_NET", "1")
	page := `<!doctype html>
<html><head><title>Docs &amp; Notes</title>
<style>body{color:red}</style>
<script>alert('boom')</script></head>
<body>
<h1>Getting started</h1>
<p>Hello &amp; welcome to the <b>documentation</b>.</p>
<ul><li>first step</li><li>second step</li></ul>
<!-- hidden comment -->
<script>tracker()</script>
</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	}))
	defer srv.Close()

	h := newHarness(t)
	out := h.call(t, "web_fetch", map[string]any{"url": srv.URL, "max_chars": 5000})

	for _, want := range []string{"title: Docs & Notes", "Getting started", "Hello & welcome to the documentation.", "first step", "second step"} {
		if !strings.Contains(out, want) {
			t.Fatalf("web_fetch missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"alert('boom')", "tracker()", "color:red", "hidden comment", "<p>", "<h1>"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("web_fetch leaked %q:\n%s", unwanted, out)
		}
	}
}

func TestWebFetchPlainText(t *testing.T) {
	t.Setenv("CREATOR_ALLOW_PRIVATE_NET", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("plain body\nsecond line"))
	}))
	defer srv.Close()

	h := newHarness(t)
	out := h.call(t, "web_fetch", map[string]any{"url": srv.URL})
	if !strings.Contains(out, "plain body\nsecond line") {
		t.Fatalf("plain text should pass through: %q", out)
	}
}

func TestWebFetchRejectsNonHTTP(t *testing.T) {
	h := newHarness(t)
	_, err := h.reg.Call(t.Context(), "web_fetch", rawJSON(`{"url":"file:///etc/passwd"}`))
	if err == nil || !strings.Contains(err.Error(), "http") {
		t.Fatalf("non-http URL should be refused: %v", err)
	}
}

func TestHTMLEntitiesAndWhitespace(t *testing.T) {
	got := htmlToText(`<p>a   b&#39;c &lt;x&gt; &#x41;</p><p>next</p>`)
	want := "a b'c <x> A\n\nnext"
	if got != want {
		t.Fatalf("htmlToText = %q, want %q", got, want)
	}
}

func rawJSON(s string) []byte { return []byte(s) }

func TestClampMaxBytes(t *testing.T) {
	if got := clampMaxBytes(0); got != 40000 {
		t.Fatalf("default: got %d, want 40000", got)
	}
	if got := clampMaxBytes(-1); got != 40000 {
		t.Fatalf("negative: got %d, want 40000", got)
	}
	if got := clampMaxBytes(10 << 20); got != webFetchMaxBytes {
		t.Fatalf("huge: got %d, want %d (webFetchMaxBytes)", got, webFetchMaxBytes)
	}
	if got := clampMaxBytes(webFetchMaxBytes + 1); got != webFetchMaxBytes {
		t.Fatalf("over by one: got %d, want %d", got, webFetchMaxBytes)
	}
	if got := clampMaxBytes(1 << 20); got != 1<<20 {
		t.Fatalf("one MiB: got %d, want %d", got, 1<<20)
	}
}

// A public URL that 302s into lookback/private/link-local space must not
// be followed — even with CREATOR_ALLOW_PRIVATE_NET=1, which is a direct-
// fetch opt-in, not an allowance for remote servers to launder internal
// addresses into the fetcher.
func TestWebFetchRefusesRedirectToPrivateNet(t *testing.T) {
	t.Setenv("CREATOR_ALLOW_PRIVATE_NET", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://10.255.255.1/latest/meta-data", http.StatusFound)
	}))
	defer srv.Close()

	h := newHarness(t)
	_, err := h.reg.Call(context.Background(), "web_fetch", json.RawMessage(fmt.Sprintf(`{"url":%q}`, srv.URL)))
	if err == nil {
		t.Fatal("web_fetch followed a redirect into private address space")
	}
}

// The pinned dialer validates addresses at dial time and must refuse
// private destinations without even dialing them.
func TestPinnedDialerRefusesPrivateIPs(t *testing.T) {
	tr := publicHTTPTransport()
	_, err := tr.DialContext(context.Background(), "tcp", "10.255.255.1:80")
	if err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("err = %v, want a policy refusal", err)
	}
}

func TestPinnedDialerAllowsPrivateOnlyWhenOptedIn(t *testing.T) {
	t.Setenv("CREATOR_ALLOW_PRIVATE_NET", "1")
	tr := publicHTTPTransport()
	_, err := tr.DialContext(context.Background(), "tcp", "127.0.0.1:1")
	if err == nil {
		t.Fatal("dialing an unused port should fail, not succeed")
	}
	if strings.Contains(err.Error(), "non-public") {
		t.Fatal("opted-in private dial must not be refused by policy")
	}
}

func TestNetFetchRefusesRedirectToLinkLocal(t *testing.T) {
	t.Setenv("CREATOR_ALLOW_PRIVATE_NET", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
	}))
	defer srv.Close()

	h := newHarness(t)
	_, err := h.reg.Call(context.Background(), "net_fetch", json.RawMessage(fmt.Sprintf(`{"url":%q}`, srv.URL)))
	if err == nil {
		t.Fatal("net_fetch followed a redirect into link-local address space")
	}
}
