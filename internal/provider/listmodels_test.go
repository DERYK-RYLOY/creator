package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListModelsFallbackRegistry(t *testing.T) {
	t.Setenv("CREATOR_DATA_DIR", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, source, err := ListModels(ctx, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if source != "registry" && source != "registry fallback" {
		t.Fatalf("source = %q", source)
	}
	found := false
	for _, r := range rows {
		if r.ID == "gpt-5" {
			found = true
		}
	}
	if !found {
		t.Fatalf("registry fallback missing gpt-5: %+v", rows)
	}
}

func TestConnectedProvidersIncludesLocals(t *testing.T) {
	t.Setenv("CREATOR_DATA_DIR", t.TempDir())
	connected := ConnectedProviders()
	foundLocal := false
	for _, d := range connected {
		if d.Slug == "ollama" {
			foundLocal = true
		}
	}
	if !foundLocal {
		t.Fatal("local providers should be connectable without an API key")
	}
	for _, d := range Descriptors() {
		if strings.Contains(d.Name, "Anthropic") && MissingKey(d) {
			// nothing to assert; just make sure MissingKey can be called
			_ = d
		}
	}
}

func TestPollinationsRegistryReady(t *testing.T) {
	d, ok := Lookup("pollinations")
	if !ok {
		t.Fatal("free pollinations provider missing")
	}
	if !d.Local {
		t.Fatal("anonymous pollinations provider must not require an API key")
	}
	if d.BaseURL != "https://text.pollinations.ai/v1" {
		t.Fatalf("anonymous base url = %q", d.BaseURL)
	}
	if len(d.Models) == 0 {
		t.Fatal("pollinations needs a default model list")
	}
	fallback := registryModels(d)
	for _, m := range fallback {
		if !m.Free {
			t.Fatalf("fallback model %q should be marked free", m.ID)
		}
	}

	poll, ok := Lookup("pollinations-pollen")
	if !ok {
		t.Fatal("authenticated pollinations provider missing")
	}
	if poll.BaseURL != "https://gen.pollinations.ai/v1" {
		t.Fatalf("authenticated base url = %q", poll.BaseURL)
	}
	if len(poll.APIKeyEnv) == 0 || poll.APIKeyEnv[0] != "POLLINATIONS_API_KEY" {
		t.Fatalf("env vars = %v", poll.APIKeyEnv)
	}
}

func TestListModelsFromDescriptorFiltersAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"dead-model"},{"id":"working-model"}]}`)
	}))
	defer srv.Close()
	d := Descriptor{
		Slug:          "testnim",
		BaseURL:       srv.URL,
		Models:        []string{"dead-model", "working-model"},
		AllowModelIDs: []string{"working-model"},
		Free:          "free test tier",
	}
	got, _, err := listModelsFromDescriptor(context.Background(), d, d.BaseURL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "working-model" {
		t.Fatalf("allowlist failed: %+v", got)
	}
	if !got[0].Free {
		t.Fatal("allowlisted NIM model should be marked free for the NIM free tier")
	}
}

func TestVerifyFiltersCatalogToAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"id":"mistralai/mistral-large-4-1t-a52b"},{"id":"paid/model-x"}]}`)
		case "/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"stub","choices":[{"message":{"role":"assistant","content":"hi"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	d, ok := Lookup("nvidia")
	if !ok {
		t.Fatal("nvidia provider missing")
	}
	res, err := Verify(context.Background(), d, "nvapi-abc12345", srv.URL, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range res.Models {
		if m.ID == "paid/model-x" {
			t.Fatal("paid catalog entry leaked through the allowlist")
		}
	}
	if len(res.Models) != 1 || res.Models[0].ID != "mistralai/mistral-large-4-1t-a52b" {
		t.Fatalf("unexpected models after filtering: %+v", res.Models)
	}
	if !res.ChatOK {
		t.Fatal("chat probe should succeed against the stub")
	}
}
