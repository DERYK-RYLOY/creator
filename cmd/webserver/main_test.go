package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIndexServesLandingPage(t *testing.T) {
	srv := httptest.NewServer(newHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.Contains(string(body), "never floods") {
		t.Fatal("landing page markup missing headline")
	}
	if h := resp.Header.Get("X-Content-Type-Options"); h != "nosniff" {
		t.Fatalf("missing nosniff header, got %q", h)
	}
}

func TestAssetsServePNG(t *testing.T) {
	srv := httptest.NewServer(newHandler())
	defer srv.Close()
	for _, name := range []string{"creator-scifi.png", "creator-gear.png", "favicon-32.png"} {
		resp, err := http.Get(srv.URL + "/assets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		ct := resp.Header.Get("Content-Type")
		size, _ := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d", name, resp.StatusCode)
		}
		if ct != "image/png" {
			t.Fatalf("%s: content-type = %q", name, ct)
		}
		if size < 1024 {
			t.Fatalf("%s: suspicious size %d", name, size)
		}
	}
}

func TestTraversalAndUnknownPathsAre404(t *testing.T) {
	srv := httptest.NewServer(newHandler())
	defer srv.Close()
	for _, p := range []string{"/assets/../index.html", "/assets/", "/does-not-exist", "/assets/secret.txt"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", p, resp.StatusCode)
		}
	}
}

func TestMethodNotAllowed(t *testing.T) {
	srv := httptest.NewServer(newHandler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /: status = %d, want 405", resp.StatusCode)
	}
}
