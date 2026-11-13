// Verify is the connect engine: it proves a pasted API key actually works
// before Creator stores it, and returns the live model list so the user picks
// from what the provider really serves (free models first).
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const verifyTimeout = 25 * time.Second

// ErrKeyPrefix marks a key that is well formed but does not match the
// provider's known prefix. It is a warning, never a failure: the live probe is
// the authority.
var ErrKeyPrefix = errors.New("key prefix mismatch")

// ModelInfo is one entry of a provider's live model list.
type ModelInfo struct {
	ID   string
	Free bool
	Note string // why it is free, e.g. "free tier, no card"
}

// Step is one verification stage, streamed to the UI while it runs.
type Step struct {
	Name   string // format | reach | models | chat
	OK     bool
	Detail string
}

// VerifyResult is the outcome of a successful connect probe.
type VerifyResult struct {
	Endpoint string
	Models   []ModelInfo
	Notes    []string
	Steps    []Step
	ChatOK   bool
}

// CheckKeyFormat validates a key before any network call. Whitespace, empty
// and absurdly short values are errors; a prefix mismatch is ErrKeyPrefix
// (warning).
func CheckKeyFormat(d Descriptor, key string) error {
	if d.Local {
		return nil
	}
	k := strings.TrimSpace(key)
	if k == "" {
		return errors.New("the key is empty — paste it exactly as copied from the provider")
	}
	if strings.ContainsAny(k, " \t\r\n") {
		return errors.New("the key contains whitespace - paste it exactly as copied, no spaces")
	}
	// Reject anything that isn't a printable ASCII token: invisible or
	// non-ASCII bytes copied along with the key would blow up inside the
	// HTTP layer with a meaningless error instead of pointing at the key.
	for _, r := range k {
		if r < 0x21 || r > 0x7e {
			return fmt.Errorf("the key contains invisible or non-ASCII characters (code point %d) - it was likely copied with a BOM or a stray character; re-paste it exactly", r)
		}
	}
	if len(k) < 8 {
		return fmt.Errorf("only %d characters — that is too short to be a %s key", len(k), d.Name)
	}
	for _, p := range d.KeyPrefixes {
		if p == "-" {
			return nil // provider has no stable prefix
		}
		if strings.HasPrefix(k, p) {
			return nil
		}
	}
	if len(d.KeyPrefixes) > 0 {
		return fmt.Errorf("%w: this does not start with %s, so it may be a key from a different provider — double check before continuing", ErrKeyPrefix, d.KeyHint())
	}
	return nil
}

// Verify proves connectivity end to end. baseURL overrides the descriptor
// (already containing any account id the user typed) and model, when set, is
// the first model the chat probe tries — the manual entry from the connect
// flow. progress is called once per step and may be nil.
func Verify(ctx context.Context, d Descriptor, key, baseURL, model string, progress func(Step)) (VerifyResult, error) {
	step := func(name string, ok bool, detail string) {
		if progress != nil {
			progress(Step{Name: name, OK: ok, Detail: detail})
		}
	}

	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = strings.TrimRight(d.BaseURL, "/")
	}
	if strings.Contains(base, "{account_id}") {
		return VerifyResult{}, errors.New("this provider needs your account id in the URL — finish the form and try again")
	}

	res := VerifyResult{Endpoint: base}
	trimmedKey := strings.TrimSpace(key)

	if !d.Local {
		if err := CheckKeyFormat(d, trimmedKey); err != nil {
			if errors.Is(err, ErrKeyPrefix) {
				res.Notes = append(res.Notes, err.Error())
				res.record("format", true, "unusual prefix — probing anyway")
				step("format", true, "unusual prefix — probing anyway")
			} else {
				return VerifyResult{}, err
			}
		} else {
			res.record("format", true, "looks like a "+d.Name+" key")
			step("format", true, "looks like a "+d.Name+" key")
		}
	} else {
		res.record("reach", true, d.Name+" — connecting anonymously (no key required)")
		step("reach", true, d.Name+" — connecting anonymously (no key required)")
	}

	models, listed, err := fetchModels(ctx, d, base, trimmedKey, step)
	if err != nil {
		return VerifyResult{}, err
	}
	models = filterModelIDs(models, d.AllowModelIDs)
	sortModels(models)
	res.Models = models
	if listed {
		res.record("models", true, fmt.Sprintf("%d models available", len(models)))
	}

	// A live completion is the final proof: the key is accepted and the model
	// actually answers.
	candidates := chatCandidates(d, models)
	if m := strings.TrimSpace(model); m != "" {
		// A model the user typed goes first: prove the exact pair they will use.
		shown := false
		for _, c := range candidates {
			if c == m {
				shown = true
				break
			}
		}
		if !shown {
			candidates = append([]string{m}, candidates...)
		}
	}
	var lastErr error
	for _, m := range candidates {
		ok, status, body, cerr := chatProbe(ctx, d, base, trimmedKey, m)
		if cerr != nil {
			lastErr = cerr
			continue
		}
		switch {
		case ok:
			res.ChatOK = true
			res.record("chat", true, m+" answered")
			step("chat", true, m+" answered")
			res.Notes = append(res.Notes, "chat confirmed with "+m)
			return res, nil
		case status == 429:
			if trimmedKey == "" {
				res.Notes = append(res.Notes, "anonymous rate limit hit — the endpoint is reachable but the anonymous budget is exhausted right now")
				res.record("chat", true, "rate limited (endpoint reachable)")
				step("chat", true, "rate limited (endpoint reachable)")
			} else {
				res.Notes = append(res.Notes, "valid key, currently rate limited by "+d.Name+" — that still means it works")
				res.record("chat", true, "rate limited (key is valid)")
				step("chat", true, "rate limited (key is valid)")
			}
			return res, nil
		default:
			lastErr = fmt.Errorf("%s: %s", httpLabel(status), snippet(body))
		}
	}
	if lastErr != nil {
		if !listed {
			return VerifyResult{}, fmt.Errorf("could not confirm %s: %v", d.Name, lastErr)
		}
		lower := strings.ToLower(lastErr.Error())
		isAuthFail := strings.Contains(lower, "401") || strings.Contains(lower, "key rejected") ||
			strings.Contains(lower, "authentication") || strings.Contains(lower, "unauthorized")
		if isAuthFail {
			res.Notes = append(res.Notes, "the key works for listing, but the chat probe rejected it — check that this key has chat/completions permission")
			// Surface the contradiction rather than assert "key is valid".
			res.Notes = append(res.Notes, "models listed, but the key was rejected by the chat probe — the key does not look fully valid")
		} else if trimmedKey == "" {
			res.Notes = append(res.Notes, "endpoint reachable but the probe failed ("+lastErr.Error()+") — if this server needs a key, pass one")
		} else {
			res.Notes = append(res.Notes, "models listed, but the chat probe failed ("+lastErr.Error()+") — could be a transient failure or a model problem, not necessarily a key problem")
		}
		res.record("chat", false, lastErr.Error())
		step("chat", false, lastErr.Error())
	}
	return res, nil
}

// ConnectedProviders returns local backends and non-local backends that have a
// credential either in the environment or in keys.json.
func ConnectedProviders() []Descriptor {
	var out []Descriptor
	for _, d := range registry {
		if d.Local || !MissingKey(d) {
			out = append(out, d)
		}
	}
	return out
}

// ListModels returns the models a connected provider serves, live when a catalog
// is available and as a registry fallback otherwise. The second return value is
// a short source label ("live catalog", "local catalog", "registry fallback",
// or "registry") suitable for display.
func ListModels(ctx context.Context, slug string) ([]ModelInfo, string, error) {
	d, ok := Lookup(slug)
	if !ok {
		return nil, "", fmt.Errorf("unknown provider %q", slug)
	}
	base := strings.TrimRight(d.BaseURL, "/")
	key := ""
	cred, ok, err := LoadCred(slug)
	if err != nil {
		return nil, "", err
	}
	if ok {
		if cred.BaseURL != "" {
			base = strings.TrimRight(cred.BaseURL, "/")
		}
		key = cred.Key
	}
	for _, env := range d.APIKeyEnv {
		if v := os.Getenv(env); v != "" {
			key = v
			break
		}
	}
	return listModelsFromDescriptor(ctx, d, base, key)
}

func listModelsFromDescriptor(ctx context.Context, d Descriptor, base, key string) ([]ModelInfo, string, error) {
	if d.Local {
		if base == "" {
			return registryModels(d), "registry", nil
		}
		models, listed, err := fetchModels(ctx, d, base, key, func(string, bool, string) {})
		if err == nil && listed {
			models = filterModelIDs(models, d.AllowModelIDs)
			sortModels(models)
			return models, "local catalog", nil
		}
		// Fall through with the error so callers see why the catalog is stale.
		fallback := registryModels(d)
		if err == nil {
			err = fmt.Errorf("%s could not list live models", d.Name)
		}
		return fallback, "registry fallback", err
	}
	if key == "" {
		return registryModels(d), "registry", nil
	}
	models, listed, err := fetchModels(ctx, d, base, key, func(string, bool, string) {})
	if err != nil || !listed {
		fallback := registryModels(d)
		if err == nil {
			err = fmt.Errorf("%s could not list live models", d.Name)
		}
		return fallback, "registry fallback", err
	}
	models = filterModelIDs(models, d.AllowModelIDs)
	sortModels(models)
	return models, "live catalog", nil
}

func filterModelIDs(models []ModelInfo, allow []string) []ModelInfo {
	if len(allow) == 0 {
		return models
	}
	seen := make(map[string]struct{}, len(allow))
	for _, id := range allow {
		seen[strings.ToLower(id)] = struct{}{}
	}
	out := make([]ModelInfo, 0, len(allow))
	for _, model := range models {
		if _, ok := seen[strings.ToLower(model.ID)]; ok {
			out = append(out, model)
		}
	}
	return out
}

func sortModels(models []ModelInfo) {
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Free != models[j].Free {
			return models[i].Free
		}
		return models[i].ID < models[j].ID
	})
}

func registryModels(d Descriptor) []ModelInfo {
	out := make([]ModelInfo, 0, len(d.Models))
	for _, id := range d.Models {
		free := false
		if d.Free != "" {
			if d.Slug == "openrouter" {
				free = strings.HasSuffix(strings.ToLower(id), ":free") || strings.EqualFold(id, "openrouter/free")
			} else {
				free = true
			}
		}
		out = append(out, ModelInfo{ID: id, Free: free, Note: d.Free})
	}
	sortModels(out)
	return out
}

// record keeps the step log in the result for UIs that do not stream progress.
func (r *VerifyResult) record(name string, ok bool, detail string) {
	r.Steps = append(r.Steps, Step{Name: name, OK: ok, Detail: detail})
}

// fetchModels calls the provider's model catalog and marks free entries. It
// reports whether listing worked; a missing catalog is not fatal (some
// OpenAI-compatible servers do not implement /models).
func fetchModels(ctx context.Context, d Descriptor, base, key string, step func(string, bool, string)) ([]ModelInfo, bool, error) {
	url := base + "/models"
	if d.ModelsURL != "" {
		url = d.ModelsURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	applyAuth(req, d, key)

	resp, err := doHTTP(ctx, req)
	if err != nil {
		step("reach", false, err.Error())
		return nil, false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	if len(body) > 8<<20 {
		step("models", false, "catalog body exceeds 8 MiB")
		return nil, false, fmt.Errorf("catalog response too large (>8 MiB)")
	}

	switch {
	case resp.StatusCode == 429:
		step("models", true, "rate limited — endpoint reachable")
		return nil, true, nil
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		step("reach", false, "key rejected")
		return nil, false, fmt.Errorf("%s rejected the key: %s — check the key (expected %s) or your account status",
			d.Name, snippet(string(body)), d.KeyHint())
	case resp.StatusCode == 404 || resp.StatusCode == 405:
		// No catalog at this endpoint. The chat probe decides from here.
		step("models", true, "no model catalog — falling back to a live chat probe")
		return nil, false, nil
	case resp.StatusCode >= 500:
		return nil, false, fmt.Errorf("%s is having server trouble (%d): %s", d.Name, resp.StatusCode, snippet(string(body)))
	case resp.StatusCode >= 400:
		return nil, false, fmt.Errorf("%s refused the model list (%d): %s", d.Name, resp.StatusCode, snippet(string(body)))
	}

	// Some providers serve a bare array of raw model objects instead of the
	// OpenAI {"data":[...]} wrapper (e.g. Pollinations' anonymous catalog).
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var arr []struct {
			Name  string `json:"name"`
			ID    string `json:"id"`
			Tier  string `json:"tier"`
			Note  string `json:"description"`
		}
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, false, fmt.Errorf("%s returned an unreadable model list: %v (body: %s)", d.Name, err, snippet(string(body)))
		}
		out := make([]ModelInfo, 0, len(arr))
		for _, m := range arr {
			id := strings.TrimSpace(m.ID)
			if id == "" {
				id = strings.TrimSpace(m.Name)
			}
			if id == "" {
				continue
			}
			tier := strings.ToLower(strings.TrimSpace(m.Tier))
			free := tier == "anonymous" || strings.Contains(tier, "free") || strings.HasSuffix(id, ":free")
			out = append(out, ModelInfo{ID: id, Free: free, Note: snippet(m.Note)})
		}
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Free != out[j].Free {
				return out[i].Free
			}
			return out[i].ID < out[j].ID
		})
		step("models", true, fmt.Sprintf("%d models listed", len(out)))
		return out, true, nil
	}
	var payload struct {
		Data []struct {
			ID       string            `json:"id"`
			Pricing  map[string]string `json:"pricing"`
			PaidOnly *bool             `json:"paid_only"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false, fmt.Errorf("%s returned an unreadable model list: %v (body: %s)", d.Name, err, snippet(string(body)))
	}
	out := make([]ModelInfo, 0, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID == "" {
			continue
		}
		price := m.Pricing["prompt"]
		if price == "" {
			price = m.Pricing["promptTextTokens"]
		}
		free, note := modelFree(d, m.ID, price, m.PaidOnly)
		out = append(out, ModelInfo{ID: m.ID, Free: free, Note: note})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Free != out[j].Free {
			return out[i].Free
		}
		return out[i].ID < out[j].ID
	})
	step("models", true, fmt.Sprintf("%d models listed", len(out)))
	return out, true, nil
}

// chatCandidates picks up to three models to probe with: the configured
// default (if actually served), then the first listed models.
func chatCandidates(d Descriptor, models []ModelInfo) []string {
	var out []string
	has := func(id string) bool {
		for _, m := range models {
			if m.ID == id {
				return true
			}
		}
		return false
	}
	if dm := d.DefaultModel(); dm != "" && (len(models) == 0 || has(dm)) {
		out = append(out, dm)
	}
	for _, m := range models {
		if len(out) >= 3 {
			break
		}
		if m.Free && !has0(out, m.ID) {
			out = append(out, m.ID)
		}
	}
	for _, m := range models {
		if len(out) >= 3 {
			break
		}
		if !has0(out, m.ID) {
			out = append(out, m.ID)
		}
	}
	if len(out) == 0 {
		out = append(out, d.DefaultModel())
	}
	return out
}

func has0(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// chatProbe sends a one-token completion and reports whether the provider
// accepted it.
func chatProbe(ctx context.Context, d Descriptor, base, key, model string) (ok bool, status int, body string, err error) {
	if model == "" {
		return false, 0, "", errors.New("no model to probe")
	}
	url, payload, err := chatRequest(d, base, model)
	if err != nil {
		return false, 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return false, 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	applyAuth(req, d, key)

	resp, err := doHTTPTimeout(ctx, req, 8*time.Second)
	if err != nil {
		return false, 0, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	truncated := int64(len(b)) > 1<<20
	msg := string(b)
	if truncated {
		msg = string(b) + "\n…(truncated at 1 MiB)"
	}
	return resp.StatusCode >= 200 && resp.StatusCode < 300, resp.StatusCode, msg, err
}

func chatRequest(d Descriptor, base, model string) (string, []byte, error) {
	if d.Protocol == ProtocolAnthropic {
		payload, err := json.Marshal(map[string]any{
			"model":      model,
			"max_tokens": 1,
			"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		})
		return base + "/messages", payload, err
	}
	body := map[string]any{
		"model":    model,
		"stream":   false,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}
	if isReasoningModel(model) {
		body["max_completion_tokens"] = 1
	} else {
		body["max_tokens"] = 1
	}
	payload, err := json.Marshal(body)
	return base + "/chat/completions", payload, err
}

func applyAuth(req *http.Request, d Descriptor, key string) {
	switch d.Protocol {
	case ProtocolAnthropic:
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	req.Header.Set("User-Agent", "creator/0.1")
}

func doHTTP(ctx context.Context, req *http.Request) (*http.Response, error) {
	return doHTTPTimeout(ctx, req, verifyTimeout)
}

func doHTTPTimeout(ctx context.Context, req *http.Request, timeout time.Duration) (*http.Response, error) {
	c := &http.Client{Timeout: timeout, CheckRedirect: checkRedirectRefusingCrossHost}
	resp, err := c.Do(req)
	if err != nil {
		host := req.URL.Host
		return nil, fmt.Errorf("cannot reach %s (%v) - check your connection or the base URL", host, err)
	}
	return resp, nil
}

func httpLabel(status int) string {
	switch status {
	case 401, 403:
		return "key rejected"
	case 404:
		return "endpoint not found"
	case 429:
		return "rate limited"
	default:
		return fmt.Sprintf("http %d", status)
	}
}

func snippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if s == "" {
		s = "no detail returned"
	}
	return s
}

func modelFree(d Descriptor, id, promptPrice string, paidOnly *bool) (bool, string) {
	if paidOnly != nil {
		if *paidOnly {
			return false, "paid model"
		}
		return true, "paid_only=false"
	}
	price := strings.TrimSpace(promptPrice)
	// A price is free if every non-whitespace numeric form of zero counts —
	// OpenRouter-style APIs put in "0.0", "0.00", "0.0000", or sometimes
	// "0.0000000000".
	priceFloat, priceErr := strconv.ParseFloat(price, 64)
	switch {
	case strings.HasSuffix(id, ":free"), strings.EqualFold(id, "openrouter/free"):
		return true, "free on OpenRouter"
	case price != "" && priceErr == nil && priceFloat == 0:
		return true, "zero price"
	case price != "":
		return false, ""
	case d.Free != "":
		return true, d.Free
	}
	return false, ""
}
