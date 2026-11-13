package provider

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Protocol is the wire format a provider speaks.
type Protocol string

const (
	ProtocolOpenAI    Protocol = "openai"
	ProtocolAnthropic Protocol = "anthropic"
)

// Descriptor is a registry entry for a model backend.
type Descriptor struct {
	Slug      string
	Name      string
	Protocol  Protocol
	BaseURL   string
	APIKeyEnv []string
	// ModelsURL, when set, is the absolute URL of the model catalog. Some
	// providers serve their catalog somewhere other than baseURL + "/models"
	// (e.g. Pollinations serves it at the origin root); the chat probe still
	// goes through baseURL + "/chat/completions".
	ModelsURL string
	Models    []string
	Local     bool
	// StreamUsage marks backends that accept stream_options.include_usage on
	// streamed completions. Unknown or quirky compat endpoints keep it off,
	// where Creator estimates streamed usage instead of risking a 400.
	StreamUsage bool
	// SupportsThinking marks backends that take an explicit thinking budget
	// (Anthropic-style `thinking.budget_tokens`). On other backends a budget is
	// not sent: reasoning models on OpenAI-style APIs decide for themselves, and
	// senders that guess get 400s. Creator still records reasoning no matter the
	// backend or whether the budget is set.
	SupportsThinking bool
	// Docs is where a user creates a key for this backend. Shown by the
	// connect flow so nobody has to search for it.
	Docs string
	// AllowModelIDs opts a provider into a curated allowlist. The catalog for
	// this provider can contain dead, non-chat or paid endpoints, so Creator
	// returns only the entries below.
	AllowModelIDs []string
	// KeyPrefixes are the well known prefixes of this provider's keys. They
	// catch a pasted key from the wrong provider before any network call; the
	// live probe stays the authority.
	KeyPrefixes []string
	// Free describes the free tier in one short line; empty means paid only.
	Free string
}

// ThinkingCapable reports whether this backend accepts an explicit thinking
// budget from Request.ThinkingBudget.
func (c *Client) ThinkingCapable() bool { return c.desc.SupportsThinking }

// DefaultModel returns the first model in the descriptor's list.
func (d Descriptor) DefaultModel() string {
	if len(d.Models) == 0 {
		return ""
	}
	return d.Models[0]
}

// KeyHint is the prefix a user should see when pasting a key, e.g. "sk-ant-…".
// A single "-" entry is a registry sentinel for "no stable public prefix"; it
// is filtered here so users never see "-…" as an expected shape.
func (d Descriptor) KeyHint() string {
	if len(d.KeyPrefixes) == 0 {
		return ""
	}
	for _, p := range d.KeyPrefixes {
		if p == "-" {
			continue
		}
		return p + "…"
	}
	return ""
}

// NeedsAccountID reports whether the base URL still carries a placeholder that
// only the user can fill in (Cloudflare style).
func (d Descriptor) NeedsAccountID() bool {
	return strings.Contains(d.BaseURL, "{account_id}")
}

// registry lists every backend Creator ships with. The order is the order shown
// by `creator providers`.
var registry = []Descriptor{
	{
		Slug: "anthropic", Name: "Anthropic", Protocol: ProtocolAnthropic, SupportsThinking: true,
		BaseURL: "https://api.anthropic.com/v1", APIKeyEnv: []string{"ANTHROPIC_API_KEY"},
		Models:      []string{"claude-sonnet-4-5", "claude-opus-4-1", "claude-haiku-4-5"},
		Docs:        "https://console.anthropic.com/settings/keys",
		KeyPrefixes: []string{"sk-ant-"},
	},
	{
		Slug: "openai", Name: "OpenAI", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://api.openai.com/v1", APIKeyEnv: []string{"OPENAI_API_KEY"},
		Models:      []string{"gpt-5", "gpt-5-mini", "gpt-4.1", "gpt-4.1-mini", "o4-mini"},
		Docs:        "https://platform.openai.com/api-keys",
		KeyPrefixes: []string{"sk-"},
	},
	{
		Slug: "google", Name: "Google Gemini", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL:     "https://generativelanguage.googleapis.com/v1beta/openai",
		APIKeyEnv:   []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		Models:      []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite"},
		Docs:        "https://aistudio.google.com/app/apikey",
		KeyPrefixes: []string{"AIza"},
		Free:        "free tier from AI Studio, no card",
	},
	{
		Slug: "xai", Name: "xAI", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://api.x.ai/v1", APIKeyEnv: []string{"XAI_API_KEY"},
		Models:      []string{"grok-4", "grok-3", "grok-code-fast-1"},
		Docs:        "https://console.x.ai",
		KeyPrefixes: []string{"xai-"},
	},
	{
		Slug: "deepseek", Name: "DeepSeek", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://api.deepseek.com/v1", APIKeyEnv: []string{"DEEPSEEK_API_KEY"},
		Models:      []string{"deepseek-chat", "deepseek-reasoner"},
		Docs:        "https://platform.deepseek.com/api_keys",
		KeyPrefixes: []string{"sk-"},
	},
	{
		Slug: "mistral", Name: "Mistral", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://api.mistral.ai/v1", APIKeyEnv: []string{"MISTRAL_API_KEY"},
		Models:      []string{"mistral-large-latest", "codestral-latest", "mistral-small-latest"},
		Docs:        "https://console.mistral.ai/api-keys",
		KeyPrefixes: []string{"-"},
		Free:        "free mode on by default, no card",
	},
	{
		Slug: "groq", Name: "Groq", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://api.groq.com/openai/v1", APIKeyEnv: []string{"GROQ_API_KEY"},
		Models:      []string{"openai/gpt-oss-120b", "llama-3.3-70b-versatile", "qwen/qwen3.8-27b"},
		Docs:        "https://console.groq.com/keys",
		KeyPrefixes: []string{"gsk_"},
		Free:        "free tier, ~1,000 requests/day",
	},
	{
		Slug: "cerebras", Name: "Cerebras", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://api.cerebras.ai/v1", APIKeyEnv: []string{"CEREBRAS_API_KEY"},
		Models:      []string{"qwen-3-coder-480b", "llama-3.3-70b", "gpt-oss-120b"},
		Docs:        "https://cloud.cerebras.ai/",
		KeyPrefixes: []string{"csk-"},
		Free:        "free tier, no card",
	},
	{
		Slug: "openrouter", Name: "OpenRouter", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: []string{"OPENROUTER_API_KEY"},
		Models:      []string{"openrouter/free", "anthropic/claude-sonnet-4.5", "openai/gpt-5", "qwen/qwen3-coder", "z-ai/glm-4.6"},
		Docs:        "https://openrouter.ai/keys",
		KeyPrefixes: []string{"sk-or-"},
		Free:        "30+ free models marked :free",
	},
	{
		Slug: "pollinations", Name: "Pollinations Free", Protocol: ProtocolOpenAI,
		BaseURL:   "https://text.pollinations.ai/v1",
		ModelsURL: "https://text.pollinations.ai/models", // catalog lives at the origin root, not under /v1
		Local:     true,
		Models:    []string{"openai-fast"},
		Docs:      "https://pollinations.ai",
		Free:      "anonymous OpenAI-compatible access, 1 reply every ~45s per IP",
	},
	{
		Slug: "pollinations-pollen", Name: "Pollinations Pollen", Protocol: ProtocolOpenAI,
		BaseURL: "https://gen.pollinations.ai/v1", APIKeyEnv: []string{"POLLINATIONS_API_KEY"},
		Models: []string{
			"openai/gpt-5-nano",
			"openai/gpt-5.4-nano",
			"openai/gpt-oss-20b",
			"nvidia/nemotron-3.5-lightning",
			"deepseek/deepseek-v4.1-flash",
			"z-ai/glm-5.3-flash",
			"minimax/minimax-m3.1-flash",
			"openai/gpt-6-luna",
			"openai/gpt-5.6-luna",
		},
		Docs:        "https://enter.pollinations.ai/keys",
		KeyPrefixes: []string{"sk_"},
		Free:        "sk_ keys with many paid_only=false models",
	},
	{
		Slug: "together", Name: "Together AI", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://api.together.xyz/v1", APIKeyEnv: []string{"TOGETHER_API_KEY"},
		Models:      []string{"Qwen/Qwen3-Coder-480B-A35B-Instruct", "meta-llama/Llama-3.3-70B-Instruct-Turbo"},
		Docs:        "https://api.together.ai/settings/api-keys",
		KeyPrefixes: []string{"-"},
		Free:        "signup credits",
	},
	{
		Slug: "fireworks", Name: "Fireworks AI", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://api.fireworks.ai/inference/v1", APIKeyEnv: []string{"FIREWORKS_API_KEY"},
		Models:      []string{"accounts/fireworks/models/qwen3-coder-480b-a35b-instruct", "accounts/fireworks/models/deepseek-v3"},
		Docs:        "https://fireworks.ai/apikeys",
		KeyPrefixes: []string{"fw_"},
	},
	{
		Slug: "perplexity", Name: "Perplexity", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://api.perplexity.ai", APIKeyEnv: []string{"PERPLEXITY_API_KEY"},
		Models:      []string{"sonar-pro", "sonar"},
		Docs:        "https://www.perplexity.ai/settings/api",
		KeyPrefixes: []string{"pplx-"},
	},
	{
		Slug: "moonshot", Name: "Moonshot (Kimi)", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://api.moonshot.ai/v1", APIKeyEnv: []string{"MOONSHOT_API_KEY"},
		Models:      []string{"kimi-k2-0905-preview", "moonshot-v1-128k"},
		Docs:        "https://platform.moonshot.ai/console/api-keys",
		KeyPrefixes: []string{"sk-"},
	},
	{
		Slug: "zhipu", Name: "Z.ai (GLM)", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://api.z.ai/api/paas/v4", APIKeyEnv: []string{"ZAI_API_KEY", "ZHIPU_API_KEY"},
		Models:      []string{"glm-4.6", "glm-4.5-air", "glm-4.7-flash"},
		Docs:        "https://z.ai/manage-apikey",
		KeyPrefixes: []string{"-"},
		Free:        "GLM-4.7-Flash free on open.bigmodel.cn",
	},
	{
		Slug: "huggingface", Name: "Hugging Face", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://router.huggingface.co/v1", APIKeyEnv: []string{"HF_TOKEN", "HUGGINGFACE_TOKEN"},
		Models:      []string{"Qwen/Qwen3-Coder-480B-A35B-Instruct", "meta-llama/Llama-3.3-70B-Instruct"},
		Docs:        "https://huggingface.co/settings/tokens",
		KeyPrefixes: []string{"hf_"},
		Free:        "free inference through the router",
	},
	{
		Slug: "github", Name: "GitHub Models", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://models.github.ai/inference", APIKeyEnv: []string{"GITHUB_TOKEN", "GH_TOKEN"},
		Models:      []string{"openai/gpt-5-mini", "openai/gpt-4.1", "deepseek/DeepSeek-V3"},
		Docs:        "https://github.com/settings/personal-access-tokens",
		KeyPrefixes: []string{"github_pat_", "ghp_"},
		Free:        "free allowance with a GitHub token",
	},
	{
		Slug: "nvidia", Name: "NVIDIA NIM", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://integrate.api.nvidia.com/v1", APIKeyEnv: []string{"NVIDIA_API_KEY"},
		Models: []string{
			"mistralai/mistral-large-4-1t-a52b",
			"poolside/laguna-xs-2.1",
			"nvidia/nemotron-3.5-lightning-30b-a3b",
			"google/gemma-4-31b-it",
			"stepfun-ai/step-5-preview",
			"nvidia/nemotron-3.5-bolt-e4b",
			"nvidia/nemotron-3-nano-omni-30b-a3b-reasoning",
			"nvidia/nemotron-3.5-super-vl-120b-a12b",
		},
		AllowModelIDs: []string{
			"mistralai/mistral-large-4-1t-a52b",
			"poolside/laguna-xs-2.1",
			"nvidia/nemotron-3.5-lightning-30b-a3b",
			"google/gemma-4-31b-it",
			"stepfun-ai/step-5-preview",
			"nvidia/nemotron-3.5-bolt-e4b",
			"nvidia/nemotron-3-nano-omni-30b-a3b-reasoning",
			"nvidia/nemotron-3.5-super-vl-120b-a12b",
		},
		Docs:        "https://build.nvidia.com/",
		KeyPrefixes: []string{"nvapi-"},
		Free:        "free NIM developer tier; registry lists chat-tested working models only",
	},
	{
		Slug: "sambanova", Name: "SambaNova", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://cloud.sambanova.ai/v1", APIKeyEnv: []string{"SAMBANOVA_API_KEY"},
		Models:      []string{"Meta-Llama-3.3-70B-Instruct", "DeepSeek-R1"},
		Docs:        "https://cloud.sambanova.ai/apis",
		KeyPrefixes: []string{"svca_"},
		Free:        "free tier, no card",
	},
	{
		Slug: "siliconflow", Name: "SiliconFlow", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://api.siliconflow.cn/v1", APIKeyEnv: []string{"SILICONFLOW_API_KEY"},
		Models:      []string{"Qwen/Qwen3-32B", "tencent/Hunyuan-MT-7B"},
		Docs:        "https://cloud.siliconflow.cn/account/ak",
		KeyPrefixes: []string{"sk-"},
		Free:        "permanently free models",
	},
	{
		Slug: "qwen", Name: "Qwen (DashScope)", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL:     "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
		APIKeyEnv:   []string{"DASHSCOPE_API_KEY", "QWEN_API_KEY"},
		Models:      []string{"qwen-max", "qwen-plus", "qwen3-coder"},
		Docs:        "https://bailian.console.alibabacloud.com/?apiKey=1",
		KeyPrefixes: []string{"sk-"},
		Free:        "new-account credit",
	},
	{
		Slug: "cohere", Name: "Cohere", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "https://api.cohere.com/compatibility/v1", APIKeyEnv: []string{"COHERE_API_KEY"},
		Models:      []string{"command-r-plus", "command-r7b-12-2024", "c4ai-aya-expanse-32b"},
		Docs:        "https://dashboard.cohere.com/api-keys",
		KeyPrefixes: []string{"c-"},
		Free:        "trial key, 1,000 calls/month",
	},
	{
		Slug: "cloudflare", Name: "Cloudflare Workers AI", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL:     "https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1",
		APIKeyEnv:   []string{"CLOUDFLARE_API_TOKEN"},
		Models:      []string{"@cf/meta/llama-3.3-70b-instruct-fp8-fast", "@cf/openai/gpt-oss-120b"},
		Docs:        "https://dash.cloudflare.com/profile/api-tokens",
		KeyPrefixes: []string{},
		Free:        "10,000 Neurons/day free",
	},
	{
		Slug: "ollama", Name: "Ollama (local)", Protocol: ProtocolOpenAI,
		BaseURL: "http://localhost:11434/v1", Local: true,
		Models: []string{"qwen3-coder", "gpt-oss:20b", "deepseek-coder-v2", "llama3.3"},
		Docs:   "https://ollama.com/download",
	},
	{
		Slug: "lmstudio", Name: "LM Studio (local)", Protocol: ProtocolOpenAI,
		BaseURL: "http://localhost:1234/v1", Local: true,
		Models: []string{"local-model"},
		Docs:   "https://lmstudio.ai/",
	},
	{
		Slug: "vllm", Name: "vLLM / self-hosted (local)", Protocol: ProtocolOpenAI, StreamUsage: true,
		BaseURL: "http://localhost:8000/v1", Local: true,
		Models: []string{"local-model"},
		Docs:   "https://docs.vllm.ai/",
	},
	{
		Slug: "custom", Name: "Custom OpenAI-compatible endpoint", Protocol: ProtocolOpenAI,
		BaseURL: "", Local: true, Models: []string{"default"},
		Docs: "",
	},
}

// Descriptors returns the registry copy.
func Descriptors() []Descriptor {
	out := make([]Descriptor, len(registry))
	copy(out, registry)
	return out
}

// Lookup finds a descriptor by slug.
func Lookup(slug string) (Descriptor, bool) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	for _, d := range registry {
		if d.Slug == slug {
			return d, true
		}
	}
	return Descriptor{}, false
}

// Resolve builds a client for the given provider slug. baseURL, model and apiKey
// override the registry defaults; each may be empty. Credentials saved by the
// connect flow (keys.json) fill any gap left by flags and environment.
func Resolve(slug, model, baseURL, apiKey string) (*Client, error) {
	desc, ok := Lookup(slug)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (try one of: %s)", slug, strings.Join(slugs(), ", "))
	}
	cred, saved, err := LoadCred(slug)
	if err != nil {
		return nil, err
	}
	if baseURL == "" && saved {
		baseURL = cred.BaseURL
	}
	if baseURL != "" {
		desc.BaseURL = baseURL
	}
	if desc.BaseURL == "" {
		return nil, fmt.Errorf("provider %q needs a base URL: run `creator connect --provider %s` or pass --base-url", slug, slug)
	}
	if strings.Contains(desc.BaseURL, "{account_id}") {
		return nil, fmt.Errorf("provider %q still needs your account id in the URL: run `creator connect --provider %s`", slug, slug)
	}
	if model == "" {
		model = desc.DefaultModel()
	}
	key := apiKey
	if key == "" {
		for _, env := range desc.APIKeyEnv {
			if v := os.Getenv(env); v != "" {
				v = strings.TrimSpace(v)
				if v == "" {
					continue
				}
				key = v
				break
			}
		}
	}
	if key == "" && saved {
		key = cred.Key
	}
	if key == "" && !desc.Local {
		envs := strings.Join(desc.APIKeyEnv, " or ")
		return nil, fmt.Errorf("provider %q needs an API key: run `creator connect --provider %s`, or set %s, or pass --api-key", slug, slug, envs)
	}
	return &Client{desc: desc, model: model, base: strings.TrimRight(desc.BaseURL, "/"), key: key}, nil
}

// MissingKey reports whether a provider still needs credentials, used by
// `creator providers` and by auto-detection.
func MissingKey(d Descriptor) bool {
	if d.Local {
		return false
	}
	for _, env := range d.APIKeyEnv {
		if os.Getenv(env) != "" {
			return false
		}
	}
	// On a corrupt keys file, report the provider as missing a key: the
	// user is pointed at `creator connect`, which surfaces the corrupt-file
	// error instead of silently using no credential.
	// A base-URL-only credential does not satisfy Resolve() for non-local
	// providers, so it must not mark the key as present here either.
	if c, _, err := LoadCred(d.Slug); err == nil {
		if c.Key != "" {
			return false
		}
	}
	return true
}

// Detect returns the first configured provider, preferring the well known
// cloud backends in registry order.
func Detect() (string, bool) {
	for _, d := range registry {
		if !d.Local && !MissingKey(d) {
			return d.Slug, true
		}
	}
	return "", false
}

func slugs() []string {
	var out []string
	for _, d := range registry {
		out = append(out, d.Slug)
	}
	sort.Strings(out)
	return out
}

// ErrNoProvider is returned when nothing is configured.
var ErrNoProvider = errors.New("no provider configured: run `creator connect` to paste an API key, or use --provider ollama for a local model")
