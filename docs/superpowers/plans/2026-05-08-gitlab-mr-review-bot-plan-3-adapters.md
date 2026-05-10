# GitLab MR Review Bot — Plan 3: Additional LLM Adapters

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add OpenAI (Chat Completions) and Ollama adapters that satisfy the existing `llm.Provider` interface, plus a factory so `cmd/bot` and `cmd/review-cli` can pick the configured provider without duplicated switch logic. No orchestrator changes.

**Architecture:** Two new files in `internal/llm` — `openai.go` and `ollama.go` — each with the same shape as the Anthropic adapter (Config struct, `New*` constructor, `Review` method). A new `factory.go` builds whichever adapter the config selects. Both `main.go` files lose their provider switch and call the factory.

**Tech Stack:** Existing module + standard library only. OpenAI: Chat Completions endpoint with `response_format: json_object`. Ollama: `/api/chat` with `format: "json"`, no auth.

---

## File Structure

```
internal/llm/
  openai.go            # OpenAI adapter (Provider impl)
  openai_test.go
  ollama.go            # Ollama adapter (Provider impl)
  ollama_test.go
  factory.go           # NewProvider(cfg, http) → Provider
  factory_test.go
cmd/
  bot/main.go          # MODIFY: replace provider switch with factory call
  review-cli/main.go   # MODIFY: replace provider switch with factory call
```

---

## Task 1: OpenAI adapter

**Files:**
- Create: `internal/llm/openai.go`
- Create: `internal/llm/openai_test.go`

- [ ] **Step 1: Write the failing test**

```go
package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAI_ReviewBuildsCorrectRequest(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))

		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &captured))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"{\"findings\":[{\"severity\":\"minor\",\"category\":\"style\",\"file\":\"a.go\",\"line\":2,\"message\":\"x\"}]}"}}],
			"usage":{"prompt_tokens":12,"completion_tokens":4}
		}`))
	}))
	defer srv.Close()

	o := NewOpenAI(OpenAIConfig{APIKey: "sk-test", Model: "gpt-4o", BaseURL: srv.URL, HTTP: srv.Client()})

	resp, err := o.Review(context.Background(), ReviewRequest{
		SystemPrompt: "rubric",
		FilePath:     "a.go",
		DiffChunk:    "@@ -1 +1 @@\n+x",
	})
	require.NoError(t, err)
	require.Len(t, resp.Findings, 1)
	require.Equal(t, 12, resp.Usage.InputTokens)
	require.Equal(t, 4, resp.Usage.OutputTokens)

	// Request body assertions
	require.Equal(t, "gpt-4o", captured["model"])
	rf := captured["response_format"].(map[string]any)
	require.Equal(t, "json_object", rf["type"])

	msgs := captured["messages"].([]any)
	require.Len(t, msgs, 2)
	sys := msgs[0].(map[string]any)
	require.Equal(t, "system", sys["role"])
	require.Equal(t, "rubric", sys["content"])
	user := msgs[1].(map[string]any)
	require.Equal(t, "user", user["role"])
	require.Contains(t, user["content"], "a.go")
}

func TestOpenAI_ReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	o := NewOpenAI(OpenAIConfig{APIKey: "k", Model: "m", BaseURL: srv.URL, HTTP: srv.Client()})
	_, err := o.Review(context.Background(), ReviewRequest{SystemPrompt: "s", FilePath: "p", DiffChunk: "d"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "401")
}

func TestOpenAI_NameReturnsOpenAI(t *testing.T) {
	o := NewOpenAI(OpenAIConfig{APIKey: "k", Model: "m"})
	require.Equal(t, "openai", o.Name())
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./internal/llm/... -run OpenAI`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement `internal/llm/openai.go`**

```go
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type OpenAIConfig struct {
	APIKey  string
	Model   string
	BaseURL string // e.g. https://api.openai.com
	HTTP    *http.Client
}

type OpenAI struct {
	cfg OpenAIConfig
}

func NewOpenAI(c OpenAIConfig) *OpenAI {
	if c.HTTP == nil {
		c.HTTP = http.DefaultClient
	}
	if c.BaseURL == "" {
		c.BaseURL = "https://api.openai.com"
	}
	return &OpenAI{cfg: c}
}

func (o *OpenAI) Name() string { return "openai" }

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiReq struct {
	Model          string          `json:"model"`
	Messages       []openaiMessage `json:"messages"`
	ResponseFormat openaiRF        `json:"response_format"`
}

type openaiRF struct {
	Type string `json:"type"`
}

type openaiResp struct {
	Choices []struct {
		Message openaiMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (o *OpenAI) Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error) {
	user := fmt.Sprintf("File: %s\n\nDiff:\n%s", req.FilePath, req.DiffChunk)

	body := openaiReq{
		Model: o.cfg.Model,
		Messages: []openaiMessage{
			{Role: "system", Content: req.SystemPrompt},
			{Role: "user", Content: user},
		},
		ResponseFormat: openaiRF{Type: "json_object"},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return ReviewResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.cfg.BaseURL+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return ReviewResponse{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)

	resp, err := o.cfg.HTTP.Do(httpReq)
	if err != nil {
		return ReviewResponse{}, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return ReviewResponse{}, fmt.Errorf("openai %d: %s", resp.StatusCode, string(rb))
	}

	var or openaiResp
	if err := json.Unmarshal(rb, &or); err != nil {
		return ReviewResponse{}, fmt.Errorf("decode response: %w", err)
	}
	if len(or.Choices) == 0 {
		return ReviewResponse{}, fmt.Errorf("openai: empty choices")
	}
	findings, err := ParseFindings(or.Choices[0].Message.Content)
	if err != nil {
		return ReviewResponse{}, err
	}
	return ReviewResponse{
		Findings: findings,
		Usage: TokenUsage{
			InputTokens:  or.Usage.PromptTokens,
			OutputTokens: or.Usage.CompletionTokens,
		},
	}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/llm/... -run OpenAI -v`
Expected: 3 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/openai.go internal/llm/openai_test.go
git commit -m "feat(llm): openai chat completions adapter"
```

---

## Task 2: Ollama adapter

**Files:**
- Create: `internal/llm/ollama.go`
- Create: `internal/llm/ollama_test.go`

- [ ] **Step 1: Write the failing test**

```go
package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOllama_ReviewBuildsCorrectRequest(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/api/chat", r.URL.Path)

		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &captured))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"llama3.1",
			"message":{"role":"assistant","content":"{\"findings\":[{\"severity\":\"nit\",\"category\":\"style\",\"file\":\"x.go\",\"line\":1,\"message\":\"y\"}]}"},
			"prompt_eval_count":7,
			"eval_count":3,
			"done":true
		}`))
	}))
	defer srv.Close()

	o := NewOllama(OllamaConfig{Model: "llama3.1", BaseURL: srv.URL, HTTP: srv.Client()})

	resp, err := o.Review(context.Background(), ReviewRequest{
		SystemPrompt: "rubric",
		FilePath:     "x.go",
		DiffChunk:    "@@ -1 +1 @@\n+y",
	})
	require.NoError(t, err)
	require.Len(t, resp.Findings, 1)
	require.Equal(t, 7, resp.Usage.InputTokens)
	require.Equal(t, 3, resp.Usage.OutputTokens)

	require.Equal(t, "llama3.1", captured["model"])
	require.Equal(t, "json", captured["format"])
	require.Equal(t, false, captured["stream"])

	msgs := captured["messages"].([]any)
	require.Len(t, msgs, 2)
	sys := msgs[0].(map[string]any)
	require.Equal(t, "system", sys["role"])
	user := msgs[1].(map[string]any)
	require.Contains(t, user["content"], "x.go")
}

func TestOllama_ReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"model not found"}`))
	}))
	defer srv.Close()

	o := NewOllama(OllamaConfig{Model: "missing", BaseURL: srv.URL, HTTP: srv.Client()})
	_, err := o.Review(context.Background(), ReviewRequest{SystemPrompt: "s", FilePath: "p", DiffChunk: "d"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}

func TestOllama_NameReturnsOllama(t *testing.T) {
	o := NewOllama(OllamaConfig{Model: "m"})
	require.Equal(t, "ollama", o.Name())
}

func TestOllama_DefaultBaseURL(t *testing.T) {
	o := NewOllama(OllamaConfig{Model: "m"})
	require.Equal(t, "http://localhost:11434", o.cfg.BaseURL)
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./internal/llm/... -run Ollama`
Expected: FAIL.

- [ ] **Step 3: Implement `internal/llm/ollama.go`**

```go
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type OllamaConfig struct {
	Model   string
	BaseURL string // default http://localhost:11434
	HTTP    *http.Client
}

type Ollama struct {
	cfg OllamaConfig
}

func NewOllama(c OllamaConfig) *Ollama {
	if c.HTTP == nil {
		c.HTTP = http.DefaultClient
	}
	if c.BaseURL == "" {
		c.BaseURL = "http://localhost:11434"
	}
	return &Ollama{cfg: c}
}

func (o *Ollama) Name() string { return "ollama" }

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaReq struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Format   string          `json:"format"`
}

type ollamaResp struct {
	Model           string        `json:"model"`
	Message         ollamaMessage `json:"message"`
	PromptEvalCount int           `json:"prompt_eval_count"`
	EvalCount       int           `json:"eval_count"`
	Done            bool          `json:"done"`
}

func (o *Ollama) Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error) {
	user := fmt.Sprintf("File: %s\n\nDiff:\n%s", req.FilePath, req.DiffChunk)

	body := ollamaReq{
		Model: o.cfg.Model,
		Messages: []ollamaMessage{
			{Role: "system", Content: req.SystemPrompt},
			{Role: "user", Content: user},
		},
		Stream: false,
		Format: "json",
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return ReviewResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.cfg.BaseURL+"/api/chat", bytes.NewReader(buf))
	if err != nil {
		return ReviewResponse{}, err
	}
	httpReq.Header.Set("content-type", "application/json")

	resp, err := o.cfg.HTTP.Do(httpReq)
	if err != nil {
		return ReviewResponse{}, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return ReviewResponse{}, fmt.Errorf("ollama %d: %s", resp.StatusCode, string(rb))
	}

	var or ollamaResp
	if err := json.Unmarshal(rb, &or); err != nil {
		return ReviewResponse{}, fmt.Errorf("decode response: %w", err)
	}
	findings, err := ParseFindings(or.Message.Content)
	if err != nil {
		return ReviewResponse{}, err
	}
	return ReviewResponse{
		Findings: findings,
		Usage: TokenUsage{
			InputTokens:  or.PromptEvalCount,
			OutputTokens: or.EvalCount,
		},
	}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/llm/... -run Ollama -v`
Expected: 4 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/ollama.go internal/llm/ollama_test.go
git commit -m "feat(llm): ollama /api/chat adapter"
```

---

## Task 3: Provider factory

**Files:**
- Create: `internal/llm/factory.go`
- Create: `internal/llm/factory_test.go`

- [ ] **Step 1: Write the failing test**

```go
package llm

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewProvider_Anthropic(t *testing.T) {
	p, err := NewProvider(ProviderConfig{Provider: "anthropic", Model: "m", APIKey: "k"}, http.DefaultClient)
	require.NoError(t, err)
	require.Equal(t, "anthropic", p.Name())
}

func TestNewProvider_OpenAI(t *testing.T) {
	p, err := NewProvider(ProviderConfig{Provider: "openai", Model: "m", APIKey: "k"}, http.DefaultClient)
	require.NoError(t, err)
	require.Equal(t, "openai", p.Name())
}

func TestNewProvider_Ollama(t *testing.T) {
	p, err := NewProvider(ProviderConfig{Provider: "ollama", Model: "m"}, http.DefaultClient)
	require.NoError(t, err)
	require.Equal(t, "ollama", p.Name())
}

func TestNewProvider_OllamaCustomBase(t *testing.T) {
	p, err := NewProvider(ProviderConfig{Provider: "ollama", Model: "m", BaseURL: "http://example:11434"}, http.DefaultClient)
	require.NoError(t, err)
	require.Equal(t, "ollama", p.Name())
}

func TestNewProvider_Unknown(t *testing.T) {
	_, err := NewProvider(ProviderConfig{Provider: "weird", Model: "m"}, http.DefaultClient)
	require.Error(t, err)
	require.Contains(t, err.Error(), "weird")
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./internal/llm/... -run NewProvider`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement `internal/llm/factory.go`**

```go
package llm

import (
	"fmt"
	"net/http"
)

// ProviderConfig is the subset of config.LLM the factory needs. Stripped here
// so the factory does not depend on internal/config (which would cycle).
type ProviderConfig struct {
	Provider string // "anthropic" | "openai" | "ollama"
	Model    string
	APIKey   string
	BaseURL  string
}

// NewProvider returns the Provider implementation for the configured provider.
func NewProvider(cfg ProviderConfig, hc *http.Client) (Provider, error) {
	switch cfg.Provider {
	case "anthropic":
		return NewAnthropic(AnthropicConfig{
			APIKey: cfg.APIKey, Model: cfg.Model, BaseURL: cfg.BaseURL, HTTP: hc,
		}), nil
	case "openai":
		return NewOpenAI(OpenAIConfig{
			APIKey: cfg.APIKey, Model: cfg.Model, BaseURL: cfg.BaseURL, HTTP: hc,
		}), nil
	case "ollama":
		return NewOllama(OllamaConfig{
			Model: cfg.Model, BaseURL: cfg.BaseURL, HTTP: hc,
		}), nil
	default:
		return nil, fmt.Errorf("unknown llm provider %q", cfg.Provider)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/llm/... -run NewProvider -v`
Expected: 5 PASS.

- [ ] **Step 5: Run full llm package**

Run: `go test ./internal/llm/... -v`
Expected: ALL PASS (existing Anthropic + parser + new OpenAI + Ollama + factory).

- [ ] **Step 6: Commit**

```bash
git add internal/llm/factory.go internal/llm/factory_test.go
git commit -m "feat(llm): provider factory dispatch"
```

---

## Task 4: Wire factory into both entrypoints

**Files:**
- Modify: `cmd/bot/main.go`
- Modify: `cmd/review-cli/main.go`

- [ ] **Step 1: Modify `cmd/bot/main.go`**

Replace the existing provider switch block:

```go
	var prov llm.Provider
	switch cfg.LLM.Provider {
	case "anthropic":
		prov = llm.NewAnthropic(llm.AnthropicConfig{
			APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model, BaseURL: cfg.LLM.BaseURL, HTTP: hc,
		})
	default:
		fmt.Fprintln(os.Stderr, "provider not yet supported:", cfg.LLM.Provider)
		os.Exit(1)
	}
```

with:

```go
	prov, err := llm.NewProvider(llm.ProviderConfig{
		Provider: cfg.LLM.Provider,
		Model:    cfg.LLM.Model,
		APIKey:   cfg.LLM.APIKey,
		BaseURL:  cfg.LLM.BaseURL,
	}, hc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "llm provider:", err)
		os.Exit(1)
	}
```

- [ ] **Step 2: Modify `cmd/review-cli/main.go`**

Replace the existing provider switch block (same shape as in `cmd/bot/main.go`) with the same `llm.NewProvider` call.

- [ ] **Step 3: Build both binaries**

Run: `go build ./cmd/bot ./cmd/review-cli`
Expected: clean build for both.

- [ ] **Step 4: Run full repo tests**

Run: `go test ./... -count=1`
Expected: ALL PASS.

- [ ] **Step 5: Update `README.md`**

In the "Running the Discord Bot" section, add a sentence near config setup:

```markdown
The bot supports three providers via `llm.provider` in `config.yaml`:
- `anthropic` (default): set `ANTHROPIC_API_KEY`. Uses prompt caching.
- `openai`: set `OPENAI_API_KEY`. Uses Chat Completions with JSON-object response format.
- `ollama`: no API key needed; defaults to `http://localhost:11434`. Override with `llm.base_url`.
```

- [ ] **Step 6: Commit**

```bash
git add cmd/bot/main.go cmd/review-cli/main.go README.md
git commit -m "feat(cli,bot): switch to llm.NewProvider factory"
```

---

## Self-Review Checklist (run after Task 4)

1. **Spec coverage:**
   - OpenAI Chat Completions adapter — Task 1 ✓
   - Ollama `/api/chat` adapter — Task 2 ✓
   - Provider factory dispatch — Task 3 ✓
   - Both entrypoints use factory — Task 4 ✓
   - Deep mode (two-pass) — **deferred** (no orchestrator change in this plan; can be a Plan 4 if needed)

2. **Placeholders:** none.

3. **Type consistency:**
   - `OpenAIConfig{APIKey, Model, BaseURL, HTTP}` matches factory call ✓
   - `OllamaConfig{Model, BaseURL, HTTP}` matches factory call (no APIKey field) ✓
   - `AnthropicConfig{APIKey, Model, BaseURL, HTTP}` already exists from Plan 1 ✓
   - `Provider.Name()` returns lowercase string consistent with `cfg.LLM.Provider` enum ✓
   - `ParseFindings` reused by all three adapters ✓

---

## Followup

If `--deep` two-pass mode is wanted later, sketch:
1. Add `Deep bool` to `review.Config`
2. New `deepAggregate(ctx, prov, findings) (AggregateResult, error)`: send all findings to provider with aggregator prompt, parse the deduped/summarized response
3. Orchestrator branches on `Deep`: when true, use deep aggregator; otherwise call existing `Aggregate(findings)`
4. Discord bot reads `--deep` slash command option (boolean), passes to orchestrator via a per-call `RunOptions` struct (signature change to `RunWithProgress`)
