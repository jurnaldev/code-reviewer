# mem9 Memory Layer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a persistent memory layer to the GitLab MR reviewer bot, backed by mem9 + repo-side rules markdown + a local two-way-synced mirror, so reviews improve over time per project.

**Architecture:** New `internal/memory` package exposes a `Client` interface. A `composite.Client` fans recall out across three sources (mem9, repo rules file, local mirror), merges + token-caps the result, and feeds it into `ReviewRequest.FileContext`. After each review, an extractor LLM pass distills durable conventions and writes them back to mem9 + the local mirror. Discord buttons on the bot's final reply capture per-MR maintainer thumbs.

**Tech Stack:** Go 1.22+, existing libs (`bwmarrin/discordgo`, `gopkg.in/yaml.v3`, `httptest`), mem9 REST API (`/v1alpha2/mem9s/memories`), GitLab raw-file API.

**Spec:** `docs/superpowers/specs/2026-05-10-mem9-memory-design.md` (read first).

---

## File Structure

### Files to create

```
internal/memory/
  types.go                  — Memory, Convention, MRSummary, Feedback structs
  client.go                 — Client interface
  noop.go                   — disabled-mode impl
  composite.go              — Recall fans out + merges; Write fans out
  format.go                 — render []Memory → FileContext markdown
  extractor.go              — second-LLM pass
  extractor_prompt.md       — extractor system prompt
  errors.go                 — sentinel errors
  mem9/
    client.go               — REST client
    types.go                — wire types
  reporules/
    source.go               — read .review/rules.md from MR target branch
  mirror/
    file.go                 — parse + render stamped .md
    sync.go                 — 3-way merge logic
  noop_test.go
  composite_test.go
  format_test.go
  extractor_test.go
  mem9/client_test.go
  reporules/source_test.go
  mirror/file_test.go
  mirror/sync_test.go
```

### Files to modify

```
internal/config/config.go            — add Memory block + validation
internal/llm/provider.go             — add Generate(ctx, sys, user) method to Provider
internal/llm/anthropic.go            — implement Generate; place FileContext in cached prefix
internal/llm/openai.go               — implement Generate; place FileContext in user prefix
internal/llm/ollama.go               — implement Generate
internal/llm/openrouter.go           — implement Generate
internal/gitlab/client.go            — add GetFileRaw(ctx, project, path, ref) method
internal/review/orchestrator.go      — call memory.Recall before chunk loop, memory.Write after PostNote
internal/discord/bot.go              — add feedback buttons, handle InteractionMessageComponent
internal/discord/register.go         — (no change unless we add a /feedback command — we don't)
cmd/bot/main.go                      — construct memory client, start jobs cleaner
config.example.yaml                  — document memory block
```

---

## Task 1: Add `memory` config block

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write failing test for memory config parsing**

Append to `internal/config/config_test.go`:

```go
func TestLoad_MemoryBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
discord:
  token: t
  app_id: a
gitlab:
  base_url: https://g
  token: gt
llm:
  provider: anthropic
  model: m
  api_key: k
memory:
  enabled: true
  recall_token_budget: 1500
  http_timeout: 5s
  mem9:
    enabled: true
    base_url: https://api.mem9.ai
    api_key: mk
    conventions_top_k: 30
    summaries_top_k: 4
  repo_rules:
    enabled: true
    path: .review/rules.md
  mirror:
    enabled: true
    dir: ~/.cache/gitlab-mr-bot/memory
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.Memory.Enabled {
		t.Fatalf("expected memory.enabled=true")
	}
	if c.Memory.RecallTokenBudget != 1500 {
		t.Fatalf("budget got %d", c.Memory.RecallTokenBudget)
	}
	if c.Memory.HTTPTimeout != 5*time.Second {
		t.Fatalf("timeout got %v", c.Memory.HTTPTimeout)
	}
	if !c.Memory.Mem9.Enabled || c.Memory.Mem9.APIKey != "mk" {
		t.Fatalf("mem9 sub-block not parsed: %+v", c.Memory.Mem9)
	}
	if c.Memory.Mem9.ConventionsTopK != 30 || c.Memory.Mem9.SummariesTopK != 4 {
		t.Fatalf("topk got %d/%d", c.Memory.Mem9.ConventionsTopK, c.Memory.Mem9.SummariesTopK)
	}
	if c.Memory.RepoRules.Path != ".review/rules.md" {
		t.Fatalf("repo_rules.path got %q", c.Memory.RepoRules.Path)
	}
	if c.Memory.Mirror.Dir != "~/.cache/gitlab-mr-bot/memory" {
		t.Fatalf("mirror.dir got %q", c.Memory.Mirror.Dir)
	}
}

func TestLoad_MemoryBlock_DefaultsAndDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
discord:
  token: t
  app_id: a
gitlab:
  base_url: https://g
  token: gt
llm:
  provider: anthropic
  model: m
  api_key: k
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Memory.Enabled {
		t.Fatalf("memory should default disabled when block absent")
	}
}

func TestLoad_MemoryBlock_RejectsMem9EnabledWithoutKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
discord: {token: t, app_id: a}
gitlab: {base_url: https://g, token: gt}
llm: {provider: anthropic, model: m, api_key: k}
memory:
  enabled: true
  mem9:
    enabled: true
    base_url: https://api.mem9.ai
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "memory.mem9.api_key") {
		t.Fatalf("expected api_key validation error, got %v", err)
	}
}
```

Add `import` for `path/filepath`, `strings`, `time` if missing.

- [ ] **Step 2: Run test, verify failure**

Run: `go test ./internal/config/ -run TestLoad_MemoryBlock -v`
Expected: FAIL — `Memory` field not found on Config.

- [ ] **Step 3: Add Memory types + parsing**

Edit `internal/config/config.go`. Add after `type Review struct`:

```go
type Memory struct {
	Enabled           bool          `yaml:"enabled"`
	RecallTokenBudget int           `yaml:"recall_token_budget"`
	HTTPTimeout       time.Duration `yaml:"http_timeout"`
	Mem9              Mem9          `yaml:"mem9"`
	RepoRules         RepoRules     `yaml:"repo_rules"`
	Mirror            Mirror        `yaml:"mirror"`
}

type Mem9 struct {
	Enabled         bool   `yaml:"enabled"`
	BaseURL         string `yaml:"base_url"`
	APIKey          string `yaml:"api_key"`
	ConventionsTopK int    `yaml:"conventions_top_k"`
	SummariesTopK   int    `yaml:"summaries_top_k"`
}

type RepoRules struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

type Mirror struct {
	Enabled bool   `yaml:"enabled"`
	Dir     string `yaml:"dir"`
}
```

Add field to `Config`:

```go
type Config struct {
	Discord Discord `yaml:"discord"`
	GitLab  GitLab  `yaml:"gitlab"`
	LLM     LLM     `yaml:"llm"`
	Review  Review  `yaml:"review"`
	Memory  Memory  `yaml:"memory"`
}
```

Extend `interpEnvFields`:

```go
func interpEnvFields(c *Config) error {
	for _, p := range []*string{&c.GitLab.Token, &c.LLM.APIKey, &c.Discord.Token, &c.Discord.AppID, &c.Memory.Mem9.APIKey} {
		v, err := interp(*p)
		if err != nil {
			return err
		}
		*p = v
	}
	return nil
}
```

Extend `applyDefaults`:

```go
func applyDefaults(c *Config) {
	// ... existing review defaults ...
	if c.Memory.Enabled {
		if c.Memory.RecallTokenBudget == 0 {
			c.Memory.RecallTokenBudget = 2000
		}
		if c.Memory.HTTPTimeout == 0 {
			c.Memory.HTTPTimeout = 10 * time.Second
		}
		if c.Memory.Mem9.Enabled {
			if c.Memory.Mem9.BaseURL == "" {
				c.Memory.Mem9.BaseURL = "https://api.mem9.ai"
			}
			if c.Memory.Mem9.ConventionsTopK == 0 {
				c.Memory.Mem9.ConventionsTopK = 20
			}
			if c.Memory.Mem9.SummariesTopK == 0 {
				c.Memory.Mem9.SummariesTopK = 5
			}
		}
		if c.Memory.RepoRules.Enabled && c.Memory.RepoRules.Path == "" {
			c.Memory.RepoRules.Path = ".review/rules.md"
		}
		if c.Memory.Mirror.Enabled && c.Memory.Mirror.Dir == "" {
			c.Memory.Mirror.Dir = "~/.cache/gitlab-mr-bot/memory"
		}
	}
}
```

Extend `validate`:

```go
func validate(c *Config) error {
	// ... existing checks ...
	if c.Memory.Enabled && c.Memory.Mem9.Enabled && c.Memory.Mem9.APIKey == "" {
		return fmt.Errorf("memory.mem9.api_key required when memory.mem9.enabled is true")
	}
	return nil
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/config/ -v`
Expected: PASS for all three new tests + existing.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add memory config block with mem9, repo_rules, mirror sub-blocks"
```

---

## Task 2: Define memory types + Client interface

**Files:**
- Create: `internal/memory/types.go`
- Create: `internal/memory/client.go`
- Create: `internal/memory/errors.go`

- [ ] **Step 1: Create types.go**

```go
package memory

import "time"

// Memory is one stored unit of context.
type Memory struct {
	ID        string            // mem9 ID; "" if not yet persisted
	Kind      Kind              // convention | mr_summary | feedback | rule
	Content   string            // human-readable text
	Project   string            // gitlab project path, e.g. "group/repo"
	Tags      map[string]string // mem9 tags (project, type, category, rating)
	Metadata  map[string]string // mem9 metadata (mr_iid, derived_at, etc.)
	Score     float64           // search relevance, 0 if not from search
	UpdatedAt time.Time
}

type Kind string

const (
	KindConvention Kind = "convention"
	KindMRSummary  Kind = "mr_summary"
	KindFeedback   Kind = "feedback"
	KindRule       Kind = "rule" // from repo rules file; never persisted to mem9
)

// MRRef captures the minimum identification a memory write needs.
type MRRef struct {
	Project   string
	IID       int
	Title     string
	HeadSHA   string
	WebURL    string
	TargetRef string // target branch, used by reporules
	Files     []string
}

// Finding is duplicated narrowly from llm.Finding to avoid an import cycle.
// The extractor consumes this; orchestrator translates llm.Finding → memory.Finding.
type Finding struct {
	Severity string
	Category string
	File     string
	Line     int
	Message  string
}

// FeedbackRating is the per-MR thumbs.
type FeedbackRating string

const (
	RatingUp   FeedbackRating = "up"
	RatingDown FeedbackRating = "down"
)
```

- [ ] **Step 2: Create errors.go**

```go
package memory

import "errors"

var (
	ErrNotFound = errors.New("memory: not found")
	ErrDisabled = errors.New("memory: disabled")
)
```

- [ ] **Step 3: Create client.go**

```go
package memory

import "context"

// Client is the orchestrator-facing facade. Implementations may compose multiple
// sources/sinks; on errors they soft-fail by returning empty results / nil error
// where the spec says memory must not block reviews.
type Client interface {
	// Recall composes a context block for a single MR review. Returns the
	// rendered FileContext markdown text and the structured memories it drew
	// from (used by extractor on write path).
	Recall(ctx context.Context, mr MRRef) (RecallResult, error)

	// Write extracts and persists conventions + summary derived from one MR.
	// Best-effort: error indicates total failure of all sinks; partial success
	// returns nil with logged warnings.
	Write(ctx context.Context, mr MRRef, findings []Finding, summaryHint string) error

	// WriteFeedback records per-MR maintainer thumbs.
	WriteFeedback(ctx context.Context, mr MRRef, rating FeedbackRating, ratedBy string) error
}

// RecallResult is what Recall returns.
type RecallResult struct {
	FileContext string   // ready-to-inject markdown block, possibly empty
	Memories    []Memory // structured underlying memories, for downstream use
	Truncated   bool
}
```

- [ ] **Step 4: Run build, verify compiles**

Run: `go build ./internal/memory/...`
Expected: PASS, no output.

- [ ] **Step 5: Commit**

```bash
git add internal/memory/types.go internal/memory/client.go internal/memory/errors.go
git commit -m "feat(memory): add types and Client interface"
```

---

## Task 3: Add noop Client

**Files:**
- Create: `internal/memory/noop.go`
- Create: `internal/memory/noop_test.go`

- [ ] **Step 1: Write failing test**

`internal/memory/noop_test.go`:

```go
package memory

import (
	"context"
	"testing"
)

func TestNoop_RecallReturnsEmpty(t *testing.T) {
	c := Noop{}
	res, err := c.Recall(context.Background(), MRRef{Project: "g/r"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.FileContext != "" {
		t.Fatalf("expected empty FileContext, got %q", res.FileContext)
	}
	if len(res.Memories) != 0 {
		t.Fatalf("expected zero memories, got %d", len(res.Memories))
	}
}

func TestNoop_WriteAndFeedbackNoError(t *testing.T) {
	c := Noop{}
	if err := c.Write(context.Background(), MRRef{}, nil, ""); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := c.WriteFeedback(context.Background(), MRRef{}, RatingUp, "u1"); err != nil {
		t.Fatalf("feedback: %v", err)
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/memory/ -run TestNoop -v`
Expected: FAIL — Noop type undefined.

- [ ] **Step 3: Implement Noop**

`internal/memory/noop.go`:

```go
package memory

import "context"

// Noop is the no-op Client used when memory.enabled=false.
type Noop struct{}

func (Noop) Recall(ctx context.Context, mr MRRef) (RecallResult, error) {
	return RecallResult{}, nil
}

func (Noop) Write(ctx context.Context, mr MRRef, findings []Finding, summaryHint string) error {
	return nil
}

func (Noop) WriteFeedback(ctx context.Context, mr MRRef, rating FeedbackRating, ratedBy string) error {
	return nil
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/memory/ -run TestNoop -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/memory/noop.go internal/memory/noop_test.go
git commit -m "feat(memory): add Noop client for disabled mode"
```

---

## Task 4: Add `Provider.Generate` method (interface + Anthropic impl)

**Files:**
- Modify: `internal/llm/provider.go`
- Modify: `internal/llm/anthropic.go`
- Modify: `internal/llm/anthropic_test.go`

The extractor calls a plain-text completion. Existing `Provider.Review` returns `[]Finding`. We add `Generate` for free-form output.

- [ ] **Step 1: Write failing test for Anthropic.Generate**

Append to `internal/llm/anthropic_test.go`:

```go
func TestAnthropic_Generate_ReturnsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		// Verify system prompt is present in System block
		systems, _ := got["system"].([]any)
		if len(systems) == 0 {
			t.Fatalf("no system block")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "hello"},
			},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 2},
		})
	}))
	defer srv.Close()

	a := NewAnthropic(AnthropicConfig{
		APIKey: "k", Model: "claude-x", BaseURL: srv.URL, HTTP: srv.Client(),
	})
	out, usage, err := a.Generate(context.Background(), "system text", "user text")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if out != "hello" {
		t.Fatalf("got %q, want hello", out)
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 2 {
		t.Fatalf("usage %+v", usage)
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/llm/ -run TestAnthropic_Generate -v`
Expected: FAIL — Generate undefined.

- [ ] **Step 3: Add Generate to Provider interface**

Edit `internal/llm/provider.go`:

```go
type Provider interface {
	Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error)
	Generate(ctx context.Context, system, user string) (string, TokenUsage, error)
	Name() string
}
```

- [ ] **Step 4: Implement Anthropic.Generate**

Append to `internal/llm/anthropic.go`:

```go
func (a *Anthropic) Generate(ctx context.Context, system, user string) (string, TokenUsage, error) {
	body := anthropicReq{
		Model:     a.cfg.Model,
		MaxTokens: 2048,
		System: []anthropicBlock{{
			Type:         "text",
			Text:         system,
			CacheControl: map[string]string{"type": "ephemeral"},
		}},
		Messages: []anthropicMsg{{
			Role:    "user",
			Content: []anthropicBlock{{Type: "text", Text: user}},
		}},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return "", TokenUsage{}, err
	}
	base := strings.TrimSuffix(strings.TrimRight(a.cfg.BaseURL, "/"), "/v1")
	httpReq, err := http.NewRequestWithContext(ctx, "POST", base+"/v1/messages", bytes.NewReader(buf))
	if err != nil {
		return "", TokenUsage{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", a.cfg.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.cfg.HTTP.Do(httpReq)
	if err != nil {
		return "", TokenUsage{}, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", TokenUsage{}, fmt.Errorf("anthropic %d: %s", resp.StatusCode, string(rb))
	}
	var ar anthropicResp
	if err := json.Unmarshal(rb, &ar); err != nil {
		return "", TokenUsage{}, fmt.Errorf("decode response: %w", err)
	}
	var text string
	for _, c := range ar.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}
	return text, TokenUsage{
		InputTokens:      ar.Usage.InputTokens,
		OutputTokens:     ar.Usage.OutputTokens,
		CachedReadTokens: ar.Usage.CacheReadInputTokens,
	}, nil
}
```

- [ ] **Step 5: Run, verify pass + ensure no other provider breaks build**

Run: `go test ./internal/llm/ -run TestAnthropic_Generate -v`
Expected: PASS.

Run: `go build ./...`
Expected: FAIL — openai/ollama/openrouter don't implement Generate. Move to Task 5 to fix.

- [ ] **Step 6: Commit (partial)**

```bash
git add internal/llm/provider.go internal/llm/anthropic.go internal/llm/anthropic_test.go
git commit -m "feat(llm): add Generate to Provider interface and Anthropic impl"
```

---

## Task 5: Implement `Generate` on OpenAI, Ollama, OpenRouter

**Files:**
- Modify: `internal/llm/openai.go`, `internal/llm/openai_test.go`
- Modify: `internal/llm/ollama.go`, `internal/llm/ollama_test.go`
- Modify: `internal/llm/openrouter.go`, `internal/llm/openrouter_test.go`

- [ ] **Step 1: Write failing tests for each provider Generate**

Append to `internal/llm/openai_test.go`:

```go
func TestOpenAI_Generate_ReturnsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "hi"}},
			},
			"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 1},
		})
	}))
	defer srv.Close()
	o := NewOpenAI(OpenAIConfig{APIKey: "k", Model: "gpt-x", BaseURL: srv.URL, HTTP: srv.Client()})
	out, usage, err := o.Generate(context.Background(), "sys", "usr")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if out != "hi" {
		t.Fatalf("got %q", out)
	}
	if usage.InputTokens != 5 || usage.OutputTokens != 1 {
		t.Fatalf("usage %+v", usage)
	}
}
```

Append to `internal/llm/ollama_test.go`:

```go
func TestOllama_Generate_ReturnsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"role": "assistant", "content": "hi"},
			"prompt_eval_count": 4, "eval_count": 1,
		})
	}))
	defer srv.Close()
	o := NewOllama(OllamaConfig{Model: "m", BaseURL: srv.URL, HTTP: srv.Client()})
	out, usage, err := o.Generate(context.Background(), "sys", "usr")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if out != "hi" {
		t.Fatalf("got %q", out)
	}
	if usage.InputTokens != 4 || usage.OutputTokens != 1 {
		t.Fatalf("usage %+v", usage)
	}
}
```

Append to `internal/llm/openrouter_test.go`:

```go
func TestOpenRouter_Generate_ReturnsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "hi"}},
			},
			"usage": map[string]any{"prompt_tokens": 6, "completion_tokens": 2},
		})
	}))
	defer srv.Close()
	o := NewOpenRouter(OpenRouterConfig{APIKey: "k", Model: "x/y", BaseURL: srv.URL, HTTP: srv.Client()})
	out, usage, err := o.Generate(context.Background(), "sys", "usr")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if out != "hi" {
		t.Fatalf("got %q", out)
	}
	if usage.InputTokens != 6 || usage.OutputTokens != 2 {
		t.Fatalf("usage %+v", usage)
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/llm/ -run "TestOpenAI_Generate|TestOllama_Generate|TestOpenRouter_Generate" -v`
Expected: FAIL — Generate undefined on each.

- [ ] **Step 3: Implement Generate on OpenAI**

Append to `internal/llm/openai.go` (assumes existing helper for HTTP call structure; mirror existing Review impl):

```go
func (o *OpenAI) Generate(ctx context.Context, system, user string) (string, TokenUsage, error) {
	body := map[string]any{
		"model": o.cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"max_tokens": 2048,
	}
	buf, _ := json.Marshal(body)
	base := strings.TrimRight(o.cfg.BaseURL, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return "", TokenUsage{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+o.cfg.APIKey)
	resp, err := o.cfg.HTTP.Do(req)
	if err != nil {
		return "", TokenUsage{}, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", TokenUsage{}, fmt.Errorf("openai %d: %s", resp.StatusCode, string(rb))
	}
	var or struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rb, &or); err != nil {
		return "", TokenUsage{}, err
	}
	var text string
	if len(or.Choices) > 0 {
		text = or.Choices[0].Message.Content
	}
	return text, TokenUsage{InputTokens: or.Usage.PromptTokens, OutputTokens: or.Usage.CompletionTokens}, nil
}
```

- [ ] **Step 4: Implement Generate on Ollama**

Append to `internal/llm/ollama.go`:

```go
func (o *Ollama) Generate(ctx context.Context, system, user string) (string, TokenUsage, error) {
	body := map[string]any{
		"model":  o.cfg.Model,
		"stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	buf, _ := json.Marshal(body)
	base := strings.TrimRight(o.cfg.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/api/chat", bytes.NewReader(buf))
	if err != nil {
		return "", TokenUsage{}, err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := o.cfg.HTTP.Do(req)
	if err != nil {
		return "", TokenUsage{}, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", TokenUsage{}, fmt.Errorf("ollama %d: %s", resp.StatusCode, string(rb))
	}
	var or struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		PromptEval int `json:"prompt_eval_count"`
		Eval       int `json:"eval_count"`
	}
	if err := json.Unmarshal(rb, &or); err != nil {
		return "", TokenUsage{}, err
	}
	return or.Message.Content, TokenUsage{InputTokens: or.PromptEval, OutputTokens: or.Eval}, nil
}
```

- [ ] **Step 5: Implement Generate on OpenRouter**

Append to `internal/llm/openrouter.go` (OpenRouter follows OpenAI Chat Completions shape; no JSON-mode):

```go
func (o *OpenRouter) Generate(ctx context.Context, system, user string) (string, TokenUsage, error) {
	body := map[string]any{
		"model": o.cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"max_tokens": 2048,
	}
	buf, _ := json.Marshal(body)
	base := strings.TrimRight(o.cfg.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return "", TokenUsage{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+o.cfg.APIKey)
	if o.cfg.Referer != "" {
		req.Header.Set("HTTP-Referer", o.cfg.Referer)
	}
	if o.cfg.Title != "" {
		req.Header.Set("X-Title", o.cfg.Title)
	}
	resp, err := o.cfg.HTTP.Do(req)
	if err != nil {
		return "", TokenUsage{}, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", TokenUsage{}, fmt.Errorf("openrouter %d: %s", resp.StatusCode, string(rb))
	}
	var or struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rb, &or); err != nil {
		return "", TokenUsage{}, err
	}
	var text string
	if len(or.Choices) > 0 {
		text = or.Choices[0].Message.Content
	}
	return text, TokenUsage{InputTokens: or.Usage.PromptTokens, OutputTokens: or.Usage.CompletionTokens}, nil
}
```

- [ ] **Step 6: Run all llm tests, verify pass + build**

Run: `go test ./internal/llm/ -v`
Expected: PASS for all.

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/llm/openai.go internal/llm/openai_test.go internal/llm/ollama.go internal/llm/ollama_test.go internal/llm/openrouter.go internal/llm/openrouter_test.go
git commit -m "feat(llm): implement Generate on OpenAI, Ollama, OpenRouter"
```

---

## Task 6: mem9 REST client — POST/GET memories

**Files:**
- Create: `internal/memory/mem9/client.go`
- Create: `internal/memory/mem9/types.go`
- Create: `internal/memory/mem9/client_test.go`

- [ ] **Step 1: Write failing test for Create + Search**

`internal/memory/mem9/client_test.go`:

```go
package mem9

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClient_Create_SendsAPIKeyAndJSON(t *testing.T) {
	var gotKey string
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "m_abc",
		})
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "key", HTTP: srv.Client(), Timeout: time.Second})
	id, err := c.Create(context.Background(), CreateInput{
		Content:  "Prefer X over Y",
		Tags:     []string{"project:g/r", "type:convention"},
		Metadata: map[string]string{"derived_from_mr_iid": "123"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != "m_abc" {
		t.Fatalf("id %q", id)
	}
	if gotKey != "key" {
		t.Fatalf("api key %q", gotKey)
	}
	if !strings.HasSuffix(gotPath, "/v1alpha2/mem9s/memories") {
		t.Fatalf("path %q", gotPath)
	}
	if gotBody["content"] != "Prefer X over Y" {
		t.Fatalf("content %v", gotBody["content"])
	}
}

func TestClient_Search_FiltersByTagsAndQuery(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"memories": []map[string]any{
				{"id": "m1", "content": "rule one", "score": 0.9, "tags": []string{"project:g/r", "type:convention"}},
				{"id": "m2", "content": "rule two", "score": 0.7, "tags": []string{"project:g/r", "type:convention"}},
			},
		})
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, APIKey: "k", HTTP: srv.Client()})
	out, err := c.Search(context.Background(), SearchInput{
		Query: "auth",
		Tags:  []string{"project:g/r", "type:convention"},
		Mode:  "hybrid",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d hits", len(out))
	}
	if out[0].ID != "m1" || out[0].Content != "rule one" {
		t.Fatalf("hit0 %+v", out[0])
	}
	if !strings.Contains(gotURL, "q=auth") {
		t.Fatalf("missing q in %q", gotURL)
	}
	if !strings.Contains(gotURL, "limit=5") {
		t.Fatalf("missing limit in %q", gotURL)
	}
}

func TestClient_Update_PUT(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, APIKey: "k", HTTP: srv.Client()})
	if err := c.Update(context.Background(), "m1", CreateInput{Content: "new"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if gotMethod != "PUT" {
		t.Fatalf("method %q", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/v1alpha2/mem9s/memories/m1") {
		t.Fatalf("path %q", gotPath)
	}
}

func TestClient_Search_Returns_5xxAsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, APIKey: "k", HTTP: srv.Client()})
	_, err := c.Search(context.Background(), SearchInput{})
	if err == nil {
		t.Fatalf("expected err on 500")
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/memory/mem9/ -v`
Expected: FAIL — package doesn't compile.

- [ ] **Step 3: Implement types + client**

`internal/memory/mem9/types.go`:

```go
package mem9

type Hit struct {
	ID       string
	Content  string
	Score    float64
	Tags     []string
	Metadata map[string]string
}

type CreateInput struct {
	Content  string
	Tags     []string
	Metadata map[string]string
}

type SearchInput struct {
	Query  string
	Tags   []string
	Mode   string // "hybrid" | "semantic" | "keyword"
	Limit  int
	Offset int
}
```

`internal/memory/mem9/client.go`:

```go
package mem9

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BaseURL string
	APIKey  string
	AgentID string
	HTTP    *http.Client
	Timeout time.Duration
}

type Client struct {
	cfg Config
}

func New(c Config) *Client {
	if c.HTTP == nil {
		c.HTTP = http.DefaultClient
	}
	if c.Timeout == 0 {
		c.Timeout = 10 * time.Second
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return &Client{cfg: c}
}

func (c *Client) Create(ctx context.Context, in CreateInput) (string, error) {
	body := map[string]any{
		"content":  in.Content,
		"tags":     in.Tags,
		"metadata": in.Metadata,
	}
	buf, _ := json.Marshal(body)
	req, err := c.req(ctx, "POST", "/v1alpha2/mem9s/memories", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	rb, err := c.do(req)
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return "", fmt.Errorf("decode create: %w", err)
	}
	return out.ID, nil
}

func (c *Client) Update(ctx context.Context, id string, in CreateInput) error {
	body := map[string]any{
		"content":  in.Content,
		"tags":     in.Tags,
		"metadata": in.Metadata,
	}
	buf, _ := json.Marshal(body)
	req, err := c.req(ctx, "PUT", "/v1alpha2/mem9s/memories/"+url.PathEscape(id), bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	_, err = c.do(req)
	return err
}

func (c *Client) Delete(ctx context.Context, id string) error {
	req, err := c.req(ctx, "DELETE", "/v1alpha2/mem9s/memories/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	_, err = c.do(req)
	return err
}

func (c *Client) Search(ctx context.Context, in SearchInput) ([]Hit, error) {
	q := url.Values{}
	if in.Query != "" {
		q.Set("q", in.Query)
	}
	if len(in.Tags) > 0 {
		q.Set("tags", strings.Join(in.Tags, ","))
	}
	if in.Mode != "" {
		q.Set("mode", in.Mode)
	}
	if in.Limit > 0 {
		q.Set("limit", strconv.Itoa(in.Limit))
	}
	if in.Offset > 0 {
		q.Set("offset", strconv.Itoa(in.Offset))
	}
	path := "/v1alpha2/mem9s/memories"
	if e := q.Encode(); e != "" {
		path += "?" + e
	}
	req, err := c.req(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	rb, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var out struct {
		Memories []Hit `json:"memories"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return nil, fmt.Errorf("decode search: %w", err)
	}
	return out.Memories, nil
}

func (c *Client) req(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	r, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	r.Header.Set("X-API-Key", c.cfg.APIKey)
	if c.cfg.AgentID != "" {
		r.Header.Set("X-Mnemo-Agent-Id", c.cfg.AgentID)
	}
	return r, nil
}

func (c *Client) do(r *http.Request) ([]byte, error) {
	resp, err := c.cfg.HTTP.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("mem9 %s %s: %d %s", r.Method, r.URL.Path, resp.StatusCode, string(rb))
	}
	return rb, nil
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/memory/mem9/ -v`
Expected: PASS for all four tests.

- [ ] **Step 5: Commit**

```bash
git add internal/memory/mem9/
git commit -m "feat(memory/mem9): add REST client for create, search, update, delete"
```

---

## Task 7: Add `gitlab.Client.GetFileRaw`

**Files:**
- Modify: `internal/gitlab/client.go`
- Modify: `internal/gitlab/client_test.go`

- [ ] **Step 1: Write failing test**

Append to `internal/gitlab/client_test.go`:

```go
func TestRESTClient_GetFileRaw_Success(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Write([]byte("rules content"))
	}))
	defer srv.Close()
	c := NewRESTClient(srv.URL, "tok", srv.Client())
	body, err := c.GetFileRaw(context.Background(), "group/repo", ".review/rules.md", "main")
	if err != nil {
		t.Fatalf("GetFileRaw: %v", err)
	}
	if body != "rules content" {
		t.Fatalf("body %q", body)
	}
	if !strings.Contains(gotPath, "/api/v4/projects/group%2Frepo/repository/files/.review%2Frules.md/raw") {
		t.Fatalf("path %q", gotPath)
	}
	if !strings.Contains(gotPath, "ref=main") {
		t.Fatalf("missing ref in %q", gotPath)
	}
}

func TestRESTClient_GetFileRaw_NotFoundReturnsErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	c := NewRESTClient(srv.URL, "tok", srv.Client())
	_, err := c.GetFileRaw(context.Background(), "g/r", "missing.md", "main")
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("got %v, want ErrFileNotFound", err)
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/gitlab/ -run TestRESTClient_GetFileRaw -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Add ErrFileNotFound + GetFileRaw**

Edit `internal/gitlab/client.go`. Add to existing imports if needed: `errors`.

```go
var ErrFileNotFound = errors.New("gitlab: file not found")
```

Add method to interface:

```go
type Client interface {
	GetMRWithChanges(ctx context.Context, projectPath string, mrIID int) (*MR, []FileChange, error)
	PostNote(ctx context.Context, projectPath string, mrIID int, body string) error
	PostDiscussion(ctx context.Context, projectPath string, mrIID int, body string, pos Position) error
	GetFileRaw(ctx context.Context, projectPath, filePath, ref string) (string, error)
}
```

Implement:

```go
func (c *RESTClient) GetFileRaw(ctx context.Context, projectPath, filePath, ref string) (string, error) {
	u := c.projURL(projectPath) + "/repository/files/" + url.PathEscape(filePath) + "/raw?ref=" + url.QueryEscape(ref)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 404 {
		return "", ErrFileNotFound
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("gitlab GET %s: %d %s", u, resp.StatusCode, string(rb))
	}
	return string(rb), nil
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/gitlab/ -run TestRESTClient_GetFileRaw -v`
Expected: PASS.

Run: `go build ./...`
Expected: any test-only stub of `gitlab.Client` interface in other packages may now fail. Search and update:

```bash
rg -n "gitlab.Client$|implements gitlab.Client" --type go
```

If a stub exists in `internal/discord/bot_test.go` or `internal/review/orchestrator_test.go`, add a no-op `GetFileRaw`. Apply now if needed.

- [ ] **Step 5: Commit**

```bash
git add internal/gitlab/client.go internal/gitlab/client_test.go
git commit -m "feat(gitlab): add GetFileRaw for fetching repo files at ref"
```

---

## Task 8: Repo rules Source

**Files:**
- Create: `internal/memory/reporules/source.go`
- Create: `internal/memory/reporules/source_test.go`

- [ ] **Step 1: Write failing test**

`internal/memory/reporules/source_test.go`:

```go
package reporules

import (
	"context"
	"errors"
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
)

type stubGitLab struct {
	body string
	err  error
}

func (s stubGitLab) GetFileRaw(ctx context.Context, project, path, ref string) (string, error) {
	return s.body, s.err
}

func TestSource_Recall_ReturnsRule(t *testing.T) {
	src := New(stubGitLab{body: "Always use Postgres for IDs."}, ".review/rules.md")
	mems, err := src.Recall(context.Background(), memory.MRRef{Project: "g/r", TargetRef: "main"})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(mems) != 1 {
		t.Fatalf("got %d", len(mems))
	}
	if mems[0].Kind != memory.KindRule {
		t.Fatalf("kind %v", mems[0].Kind)
	}
	if mems[0].Content != "Always use Postgres for IDs." {
		t.Fatalf("content %q", mems[0].Content)
	}
}

func TestSource_Recall_404Silent(t *testing.T) {
	src := New(stubGitLab{err: gitlab.ErrFileNotFound}, ".review/rules.md")
	mems, err := src.Recall(context.Background(), memory.MRRef{Project: "g/r", TargetRef: "main"})
	if err != nil {
		t.Fatalf("expected nil err on 404, got %v", err)
	}
	if len(mems) != 0 {
		t.Fatalf("expected zero memories")
	}
}

func TestSource_Recall_OtherErrLogged(t *testing.T) {
	src := New(stubGitLab{err: errors.New("boom")}, ".review/rules.md")
	mems, err := src.Recall(context.Background(), memory.MRRef{Project: "g/r", TargetRef: "main"})
	if err == nil {
		t.Fatalf("expected err propagation for non-404")
	}
	if len(mems) != 0 {
		t.Fatalf("expected zero memories on err")
	}
}

func TestSource_Recall_TruncatesOver4KB(t *testing.T) {
	big := make([]byte, 5000)
	for i := range big {
		big[i] = 'a'
	}
	src := New(stubGitLab{body: string(big)}, ".review/rules.md")
	mems, _ := src.Recall(context.Background(), memory.MRRef{Project: "g/r"})
	if len(mems) != 1 {
		t.Fatalf("got %d", len(mems))
	}
	if len(mems[0].Content) > 4096+len(" _(truncated)_") {
		t.Fatalf("not truncated, len=%d", len(mems[0].Content))
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/memory/reporules/ -v`
Expected: FAIL — package doesn't compile.

- [ ] **Step 3: Implement Source**

`internal/memory/reporules/source.go`:

```go
package reporules

import (
	"context"
	"errors"

	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
)

const maxFileBytes = 4096

// FileGetter is the minimal slice of gitlab.Client used here.
type FileGetter interface {
	GetFileRaw(ctx context.Context, projectPath, filePath, ref string) (string, error)
}

type Source struct {
	gl   FileGetter
	path string
}

func New(gl FileGetter, path string) *Source {
	return &Source{gl: gl, path: path}
}

func (s *Source) Recall(ctx context.Context, mr memory.MRRef) ([]memory.Memory, error) {
	ref := mr.TargetRef
	if ref == "" {
		ref = "HEAD"
	}
	body, err := s.gl.GetFileRaw(ctx, mr.Project, s.path, ref)
	if errors.Is(err, gitlab.ErrFileNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(body) > maxFileBytes {
		body = body[:maxFileBytes] + " _(truncated)_"
	}
	return []memory.Memory{{
		Kind:    memory.KindRule,
		Content: body,
		Project: mr.Project,
	}}, nil
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/memory/reporules/ -v`
Expected: PASS for all four.

- [ ] **Step 5: Commit**

```bash
git add internal/memory/reporules/
git commit -m "feat(memory/reporules): add Source that fetches .review/rules.md from MR target branch"
```

---

## Task 9: Mirror — stamped markdown parse + render

**Files:**
- Create: `internal/memory/mirror/file.go`
- Create: `internal/memory/mirror/file_test.go`

- [ ] **Step 1: Write failing tests for Parse + Render**

`internal/memory/mirror/file_test.go`:

```go
package mirror

import (
	"strings"
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
)

const sample = `# Memory: group/repo

## Conventions
- Prefer errors.Is over == for sentinel errors. <!-- mem9_id: m_abc -->
- Wrap external HTTP behind retry helper. <!-- mem9_id: m_def -->
- Pending push entry, no stamp.

## Recent MRs
- !123 "fix nil deref" — summary text. <!-- mem9_id: m_ghi -->

## Recent Feedback
- !123 rated 👎 by @alice on 2026-05-10
`

func TestParse_ExtractsConventionsWithStamps(t *testing.T) {
	doc, err := Parse(sample)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Conventions) != 3 {
		t.Fatalf("conventions %d", len(doc.Conventions))
	}
	if doc.Conventions[0].MemoryID != "m_abc" {
		t.Fatalf("first id %q", doc.Conventions[0].MemoryID)
	}
	if !strings.HasPrefix(doc.Conventions[0].Text, "Prefer errors.Is") {
		t.Fatalf("first text %q", doc.Conventions[0].Text)
	}
	if doc.Conventions[2].MemoryID != "" {
		t.Fatalf("third should be unstamped, got %q", doc.Conventions[2].MemoryID)
	}
	if len(doc.MRSummaries) != 1 {
		t.Fatalf("summaries %d", len(doc.MRSummaries))
	}
	if len(doc.Feedback) != 1 {
		t.Fatalf("feedback %d", len(doc.Feedback))
	}
}

func TestRender_RoundTrip(t *testing.T) {
	doc := Document{
		Project: "group/repo",
		Conventions: []Entry{
			{Text: "rule one", MemoryID: "m1"},
			{Text: "rule two"},
		},
		MRSummaries: []Entry{
			{Text: `!7 "title" — summary`, MemoryID: "m_s7"},
		},
		Feedback: []Entry{
			{Text: "!9 rated 👎 by @bob on 2026-05-10"},
		},
	}
	rendered := Render(doc)
	parsed, err := Parse(rendered)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(parsed.Conventions) != 2 || parsed.Conventions[0].MemoryID != "m1" {
		t.Fatalf("conventions roundtrip mismatch: %+v", parsed.Conventions)
	}
	if len(parsed.MRSummaries) != 1 || parsed.MRSummaries[0].MemoryID != "m_s7" {
		t.Fatalf("summaries roundtrip mismatch: %+v", parsed.MRSummaries)
	}
	if len(parsed.Feedback) != 1 {
		t.Fatalf("feedback roundtrip mismatch: %+v", parsed.Feedback)
	}
}

func TestParse_EmptyDocReturnsZero(t *testing.T) {
	doc, err := Parse("")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Conventions) != 0 || len(doc.MRSummaries) != 0 {
		t.Fatalf("expected zero, got %+v", doc)
	}
}

func TestSlugForProject(t *testing.T) {
	if got := SlugForProject("group/repo"); got != "group_repo" {
		t.Fatalf("got %q", got)
	}
	if got := SlugForProject("nested/group/repo-x"); got != "nested_group_repo-x" {
		t.Fatalf("got %q", got)
	}
}

func TestEntryToMemoryAndBack(t *testing.T) {
	mems := EntriesToMemories([]Entry{{Text: "x", MemoryID: "m1"}}, memory.KindConvention, "g/r")
	if len(mems) != 1 || mems[0].ID != "m1" || mems[0].Kind != memory.KindConvention {
		t.Fatalf("got %+v", mems)
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/memory/mirror/ -v`
Expected: FAIL — package doesn't compile.

- [ ] **Step 3: Implement file.go**

```go
package mirror

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"

	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
)

// Entry is a single bullet in the mirror.
type Entry struct {
	Text     string
	MemoryID string // empty if not yet pushed to mem9
}

type Document struct {
	Project     string
	Conventions []Entry
	MRSummaries []Entry
	Feedback    []Entry
}

const (
	sectionConventions = "Conventions"
	sectionMRSummaries = "Recent MRs"
	sectionFeedback    = "Recent Feedback"
)

var stampRe = regexp.MustCompile(`<!--\s*mem9_id:\s*(\S+)\s*-->`)

func Parse(src string) (Document, error) {
	d := Document{}
	scanner := bufio.NewScanner(strings.NewReader(src))
	scanner.Buffer(make([]byte, 1<<16), 1<<20)
	current := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "# Memory:") {
			d.Project = strings.TrimSpace(strings.TrimPrefix(line, "# Memory:"))
			continue
		}
		if strings.HasPrefix(line, "## ") {
			current = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		body := strings.TrimPrefix(line, "- ")
		entry := Entry{}
		if m := stampRe.FindStringSubmatch(body); m != nil {
			entry.MemoryID = m[1]
			body = strings.TrimSpace(stampRe.ReplaceAllString(body, ""))
			body = strings.TrimSuffix(body, ".")
			body = strings.TrimSpace(body)
		}
		entry.Text = body
		switch current {
		case sectionConventions:
			d.Conventions = append(d.Conventions, entry)
		case sectionMRSummaries:
			d.MRSummaries = append(d.MRSummaries, entry)
		case sectionFeedback:
			d.Feedback = append(d.Feedback, entry)
		}
	}
	return d, scanner.Err()
}

func Render(d Document) string {
	var b strings.Builder
	if d.Project != "" {
		fmt.Fprintf(&b, "# Memory: %s\n\n", d.Project)
	}
	if len(d.Conventions) > 0 {
		b.WriteString("## " + sectionConventions + "\n")
		for _, e := range d.Conventions {
			writeEntry(&b, e)
		}
		b.WriteString("\n")
	}
	if len(d.MRSummaries) > 0 {
		b.WriteString("## " + sectionMRSummaries + "\n")
		for _, e := range d.MRSummaries {
			writeEntry(&b, e)
		}
		b.WriteString("\n")
	}
	if len(d.Feedback) > 0 {
		b.WriteString("## " + sectionFeedback + "\n")
		for _, e := range d.Feedback {
			writeEntry(&b, e)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func writeEntry(b *strings.Builder, e Entry) {
	if e.MemoryID != "" {
		fmt.Fprintf(b, "- %s. <!-- mem9_id: %s -->\n", strings.TrimSuffix(e.Text, "."), e.MemoryID)
	} else {
		fmt.Fprintf(b, "- %s\n", e.Text)
	}
}

func SlugForProject(p string) string {
	return strings.ReplaceAll(p, "/", "_")
}

func EntriesToMemories(es []Entry, k memory.Kind, project string) []memory.Memory {
	out := make([]memory.Memory, 0, len(es))
	for _, e := range es {
		out = append(out, memory.Memory{
			ID:      e.MemoryID,
			Kind:    k,
			Content: e.Text,
			Project: project,
		})
	}
	return out
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/memory/mirror/ -v`
Expected: PASS for all six tests.

- [ ] **Step 5: Commit**

```bash
git add internal/memory/mirror/file.go internal/memory/mirror/file_test.go
git commit -m "feat(memory/mirror): add stamped markdown parse + render"
```

---

## Task 10: Mirror — 3-way sync logic

**Files:**
- Create: `internal/memory/mirror/sync.go`
- Create: `internal/memory/mirror/sync_test.go`

- [ ] **Step 1: Write failing tests for Diff**

`internal/memory/mirror/sync_test.go`:

```go
package mirror

import (
	"reflect"
	"sort"
	"testing"
)

func TestDiff_NewLocalEntryNeedsPush(t *testing.T) {
	local := []Entry{
		{Text: "rule a", MemoryID: "m_a"},
		{Text: "rule b new"},
	}
	remote := map[string]string{"m_a": "rule a"}
	plan := Diff(local, remote)
	if len(plan.ToPost) != 1 || plan.ToPost[0].Text != "rule b new" {
		t.Fatalf("ToPost %+v", plan.ToPost)
	}
	if len(plan.ToPut) != 0 {
		t.Fatalf("ToPut %+v", plan.ToPut)
	}
}

func TestDiff_LocalEditedNeedsPut(t *testing.T) {
	local := []Entry{
		{Text: "rule a edited", MemoryID: "m_a"},
	}
	remote := map[string]string{"m_a": "rule a"}
	plan := Diff(local, remote)
	if len(plan.ToPut) != 1 || plan.ToPut[0].MemoryID != "m_a" {
		t.Fatalf("ToPut %+v", plan.ToPut)
	}
}

func TestDiff_RemoteOnlyAppendedToLocal(t *testing.T) {
	local := []Entry{}
	remote := map[string]string{"m_x": "remote rule"}
	plan := Diff(local, remote)
	if len(plan.ToAppend) != 1 || plan.ToAppend[0].MemoryID != "m_x" {
		t.Fatalf("ToAppend %+v", plan.ToAppend)
	}
}

func TestDiff_StableOrdering(t *testing.T) {
	local := []Entry{}
	remote := map[string]string{"m_b": "b", "m_a": "a", "m_c": "c"}
	plan := Diff(local, remote)
	got := []string{plan.ToAppend[0].MemoryID, plan.ToAppend[1].MemoryID, plan.ToAppend[2].MemoryID}
	want := []string{"m_a", "m_b", "m_c"}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestDiff_LocalDeletedNoOpOnMem9() {
	// Entries only in remote when local has no matching ID are appended.
	// Spec: "leave local (someone deleted upstream)" — meaning if a stamped
	// local entry has no remote match, do NOT delete from local. Diff should
	// produce no Delete action for that case.
}

func TestDiff_StampedLocalMissingFromRemote_NoDelete(t *testing.T) {
	local := []Entry{
		{Text: "removed upstream", MemoryID: "m_gone"},
	}
	remote := map[string]string{}
	plan := Diff(local, remote)
	if len(plan.ToPost) != 0 || len(plan.ToPut) != 0 || len(plan.ToAppend) != 0 {
		t.Fatalf("expected no ops, got %+v", plan)
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/memory/mirror/ -run TestDiff -v`
Expected: FAIL — Diff/Plan undefined.

- [ ] **Step 3: Implement sync.go**

```go
package mirror

import "sort"

// Plan describes the actions needed to reconcile local mirror with mem9.
type Plan struct {
	ToPost   []Entry // local entries without ID, novel content
	ToPut    []Entry // local entries with ID whose content differs from remote
	ToAppend []Entry // remote entries not present locally — append to local file
}

// Diff computes a sync plan. `local` is mirror entries; `remote` maps mem9_id → content.
func Diff(local []Entry, remote map[string]string) Plan {
	var plan Plan
	localIDs := map[string]bool{}

	for _, e := range local {
		if e.MemoryID == "" {
			plan.ToPost = append(plan.ToPost, e)
			continue
		}
		localIDs[e.MemoryID] = true
		if rc, ok := remote[e.MemoryID]; ok {
			if rc != e.Text {
				plan.ToPut = append(plan.ToPut, e)
			}
		}
		// stamped local + missing remote → no-op (preserve local)
	}

	// Stable order for ToAppend
	keys := make([]string, 0, len(remote))
	for id := range remote {
		if !localIDs[id] {
			keys = append(keys, id)
		}
	}
	sort.Strings(keys)
	for _, id := range keys {
		plan.ToAppend = append(plan.ToAppend, Entry{MemoryID: id, Text: remote[id]})
	}
	return plan
}
```

Delete the placeholder TestDiff_LocalDeletedNoOpOnMem9 (no `*testing.T`); the real test is `TestDiff_StampedLocalMissingFromRemote_NoDelete`.

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/memory/mirror/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/memory/mirror/sync.go internal/memory/mirror/sync_test.go
git commit -m "feat(memory/mirror): add 3-way sync Diff between local entries and mem9 state"
```

---

## Task 11: Mirror Source impl (file IO + sync orchestration)

**Files:**
- Create: `internal/memory/mirror/source.go`
- Create: `internal/memory/mirror/source_test.go`

- [ ] **Step 1: Write failing tests**

`internal/memory/mirror/source_test.go`:

```go
package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
)

type stubMem9 struct {
	created []string
	updated []string
	posts   []string
}

func (s *stubMem9) Create(ctx context.Context, content string, kind memory.Kind, project string) (string, error) {
	s.created = append(s.created, content)
	s.posts = append(s.posts, content)
	return "m_new_" + content[:1], nil
}

func (s *stubMem9) Update(ctx context.Context, id, content string) error {
	s.updated = append(s.updated, id+":"+content)
	return nil
}

func TestSource_Recall_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "g_r.md")
	os.WriteFile(f, []byte(sample), 0o600)
	src := NewSource(dir, &stubMem9{})
	mems, err := src.Recall(context.Background(), memory.MRRef{Project: "g/r"})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	// Conventions (3) + MRSummaries (1) — feedback excluded from recall
	if len(mems) != 4 {
		t.Fatalf("got %d", len(mems))
	}
}

func TestSource_Sync_PostsUnstamped(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "g_r.md")
	os.WriteFile(f, []byte(`# Memory: g/r

## Conventions
- Pending convention.
`), 0o600)
	stub := &stubMem9{}
	src := NewSource(dir, stub)
	if err := src.Sync(context.Background(), memory.MRRef{Project: "g/r"}, map[memory.Kind]map[string]string{
		memory.KindConvention: {},
	}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(stub.created) != 1 {
		t.Fatalf("expected 1 create, got %d", len(stub.created))
	}
	// File should now contain stamp
	got, _ := os.ReadFile(f)
	if !strings.Contains(string(got), "<!-- mem9_id: m_new_P -->") {
		t.Fatalf("file missing stamp: %s", got)
	}
}

func TestSource_AppendConvention_CreatesDirIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "memory")
	src := NewSource(dir, &stubMem9{})
	if err := src.AppendConvention(context.Background(), memory.MRRef{Project: "g/r"}, "rule x", "m_x"); err != nil {
		t.Fatalf("AppendConvention: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "g_r.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(body), "rule x") || !strings.Contains(string(body), "m_x") {
		t.Fatalf("body: %s", body)
	}
}

func TestSource_AppendFeedback_AppendsLine(t *testing.T) {
	dir := t.TempDir()
	src := NewSource(dir, &stubMem9{})
	if err := src.AppendFeedback(context.Background(), memory.MRRef{Project: "g/r", IID: 7}, memory.RatingDown, "alice"); err != nil {
		t.Fatalf("AppendFeedback: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "g_r.md"))
	if !strings.Contains(string(body), "!7 rated down by alice") {
		t.Fatalf("body: %s", body)
	}
}

func TestSource_Recall_MissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	src := NewSource(dir, &stubMem9{})
	mems, err := src.Recall(context.Background(), memory.MRRef{Project: "no/file"})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(mems) != 0 {
		t.Fatalf("expected empty on missing file")
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/memory/mirror/ -run TestSource -v`
Expected: FAIL — Source undefined.

- [ ] **Step 3: Implement source.go**

```go
package mirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
)

// Mem9Writer is the minimal slice of mem9.Client the mirror needs for sync.
type Mem9Writer interface {
	Create(ctx context.Context, content string, kind memory.Kind, project string) (string, error)
	Update(ctx context.Context, id, content string) error
}

type Source struct {
	dir  string
	mem9 Mem9Writer
}

func NewSource(dir string, mem9 Mem9Writer) *Source {
	return &Source{dir: ExpandHome(dir), mem9: mem9}
}

func ExpandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~/"))
		}
	}
	return p
}

func (s *Source) filePath(project string) string {
	return filepath.Join(s.dir, SlugForProject(project)+".md")
}

func (s *Source) read(project string) (Document, error) {
	body, err := os.ReadFile(s.filePath(project))
	if errors.Is(err, os.ErrNotExist) {
		return Document{Project: project}, nil
	}
	if err != nil {
		return Document{}, err
	}
	return Parse(string(body))
}

func (s *Source) write(d Document) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.filePath(d.Project), []byte(Render(d)), 0o644)
}

// Recall returns conventions + summaries from the mirror file (feedback excluded
// from recall — feedback is a write-only signal in the mirror).
func (s *Source) Recall(ctx context.Context, mr memory.MRRef) ([]memory.Memory, error) {
	d, err := s.read(mr.Project)
	if err != nil {
		return nil, err
	}
	if d.Project == "" {
		d.Project = mr.Project
	}
	out := EntriesToMemories(d.Conventions, memory.KindConvention, mr.Project)
	out = append(out, EntriesToMemories(d.MRSummaries, memory.KindMRSummary, mr.Project)...)
	return out, nil
}

// Sync reconciles the local file with the supplied remote view (mem9 ID → content)
// per kind. Pushes new/edited locals; appends remote-only entries.
func (s *Source) Sync(ctx context.Context, mr memory.MRRef, remote map[memory.Kind]map[string]string) error {
	d, err := s.read(mr.Project)
	if err != nil {
		return err
	}
	if d.Project == "" {
		d.Project = mr.Project
	}

	// Conventions
	convPlan := Diff(d.Conventions, remote[memory.KindConvention])
	d.Conventions = applyPlan(ctx, d.Conventions, convPlan, s.mem9, memory.KindConvention, mr.Project)

	// MR summaries
	sumPlan := Diff(d.MRSummaries, remote[memory.KindMRSummary])
	d.MRSummaries = applyPlan(ctx, d.MRSummaries, sumPlan, s.mem9, memory.KindMRSummary, mr.Project)

	return s.write(d)
}

func applyPlan(ctx context.Context, current []Entry, plan Plan, mw Mem9Writer, k memory.Kind, project string) []Entry {
	// POST unstamped locals → fill in IDs in current slice.
	for i := range current {
		if current[i].MemoryID != "" {
			continue
		}
		id, err := mw.Create(ctx, current[i].Text, k, project)
		if err == nil && id != "" {
			current[i].MemoryID = id
		}
	}
	// PUT edited locals.
	for _, e := range plan.ToPut {
		_ = mw.Update(ctx, e.MemoryID, e.Text)
	}
	// Append remote-only.
	current = append(current, plan.ToAppend...)
	return current
}

// AppendConvention adds one already-stamped convention to the mirror file.
// Used by the post-review write path after mem9 create.
func (s *Source) AppendConvention(ctx context.Context, mr memory.MRRef, text, memID string) error {
	d, err := s.read(mr.Project)
	if err != nil {
		return err
	}
	if d.Project == "" {
		d.Project = mr.Project
	}
	d.Conventions = append(d.Conventions, Entry{Text: text, MemoryID: memID})
	return s.write(d)
}

// AppendMRSummary adds one already-stamped MR summary.
func (s *Source) AppendMRSummary(ctx context.Context, mr memory.MRRef, text, memID string) error {
	d, err := s.read(mr.Project)
	if err != nil {
		return err
	}
	if d.Project == "" {
		d.Project = mr.Project
	}
	d.MRSummaries = append(d.MRSummaries, Entry{Text: text, MemoryID: memID})
	return s.write(d)
}

// AppendFeedback appends a feedback line; no two-way sync on feedback.
func (s *Source) AppendFeedback(ctx context.Context, mr memory.MRRef, rating memory.FeedbackRating, ratedBy string) error {
	d, err := s.read(mr.Project)
	if err != nil {
		return err
	}
	if d.Project == "" {
		d.Project = mr.Project
	}
	line := fmt.Sprintf("!%d rated %s by %s on %s", mr.IID, rating, ratedBy, time.Now().UTC().Format("2006-01-02"))
	d.Feedback = append(d.Feedback, Entry{Text: line})
	return s.write(d)
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/memory/mirror/ -v`
Expected: PASS for all tests including new Source tests.

- [ ] **Step 5: Commit**

```bash
git add internal/memory/mirror/source.go internal/memory/mirror/source_test.go
git commit -m "feat(memory/mirror): add Source with read/sync/append operations"
```

---

## Task 12: Format — render []Memory → FileContext block with priority cap

**Files:**
- Create: `internal/memory/format.go`
- Create: `internal/memory/format_test.go`

- [ ] **Step 1: Write failing tests**

`internal/memory/format_test.go`:

```go
package memory

import (
	"strings"
	"testing"
)

func TestFormat_RendersAllSections(t *testing.T) {
	mems := []Memory{
		{Kind: KindRule, Content: "Always use Postgres."},
		{Kind: KindConvention, Content: "Prefer errors.Is."},
		{Kind: KindMRSummary, Content: `!7 "title" — summary`},
	}
	out, truncated := Format(mems, 5000)
	if truncated {
		t.Fatalf("should not truncate")
	}
	if !strings.Contains(out, "## Project Rules") {
		t.Fatalf("missing rules section: %s", out)
	}
	if !strings.Contains(out, "## Conventions") {
		t.Fatalf("missing conventions: %s", out)
	}
	if !strings.Contains(out, "## Recent MRs") {
		t.Fatalf("missing summaries: %s", out)
	}
}

func TestFormat_PriorityDropsLowestFirst(t *testing.T) {
	rules := strings.Repeat("a", 1000)
	convs := strings.Repeat("b", 1000)
	summaries := strings.Repeat("c", 1000)
	mems := []Memory{
		{Kind: KindRule, Content: rules},
		{Kind: KindConvention, Content: convs},
		{Kind: KindMRSummary, Content: summaries},
	}
	// Budget too small for all; rules must survive, summaries must drop first.
	out, truncated := Format(mems, 600) // ~150 tokens, only fits rules
	if !truncated {
		t.Fatalf("expected truncated=true")
	}
	if !strings.Contains(out, rules) {
		t.Fatalf("rules dropped — must always survive when fits")
	}
	if strings.Contains(out, summaries) {
		t.Fatalf("summaries should have been dropped first")
	}
}

func TestFormat_EmptyInputReturnsEmpty(t *testing.T) {
	out, _ := Format(nil, 1000)
	if out != "" {
		t.Fatalf("expected empty, got %q", out)
	}
}

func TestEstimateTokens_Approx4Bytes(t *testing.T) {
	if got := estimateTokens("aaaa"); got != 1 {
		t.Fatalf("got %d", got)
	}
	if got := estimateTokens(strings.Repeat("a", 400)); got != 100 {
		t.Fatalf("got %d", got)
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/memory/ -run "TestFormat|TestEstimate" -v`
Expected: FAIL.

- [ ] **Step 3: Implement format.go**

```go
package memory

import (
	"strings"
)

// estimateTokens is a 4-bytes-per-token approximation; matches what
// internal/chunker uses so budgets are consistent.
func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// Format renders memories into a single markdown block, dropping lowest-priority
// sections first when the token budget would be exceeded.
//
// Priority order (highest first): Rule, Convention, MRSummary.
// Returns (text, truncated).
func Format(mems []Memory, tokenBudget int) (string, bool) {
	if len(mems) == 0 {
		return "", false
	}
	rules := filterKind(mems, KindRule)
	convs := filterKind(mems, KindConvention)
	summaries := filterKind(mems, KindMRSummary)

	type section struct {
		title   string
		entries []Memory
		bullet  bool
	}
	sections := []section{
		{title: "Project Rules", entries: rules, bullet: false},
		{title: "Conventions", entries: convs, bullet: true},
		{title: "Recent MRs", entries: summaries, bullet: true},
	}

	// Greedy: try full → drop last → drop last → ...
	for keep := len(sections); keep > 0; keep-- {
		buf := renderSections(sections[:keep])
		if estimateTokens(buf) <= tokenBudget {
			return buf, keep < len(sections)
		}
	}
	// Even rules alone too big — truncate rules content.
	if len(rules) > 0 {
		// hard truncate first rule text to fit budget, drop the rest
		max := tokenBudget * 4
		if max < 200 {
			max = 200
		}
		r := rules[0]
		if len(r.Content) > max {
			r.Content = r.Content[:max] + " _(truncated)_"
		}
		buf := renderSections([]section{{title: "Project Rules", entries: []Memory{r}, bullet: false}})
		return buf, true
	}
	return "", true
}

func renderSections(sections []struct {
	title   string
	entries []Memory
	bullet  bool
}) string {
	var b strings.Builder
	b.WriteString("# Project Memory\n\n")
	for _, s := range sections {
		if len(s.entries) == 0 {
			continue
		}
		b.WriteString("## ")
		b.WriteString(s.title)
		b.WriteString("\n")
		for _, m := range s.entries {
			if s.bullet {
				b.WriteString("- ")
				b.WriteString(m.Content)
				b.WriteString("\n")
			} else {
				b.WriteString(m.Content)
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func filterKind(mems []Memory, k Kind) []Memory {
	var out []Memory
	for _, m := range mems {
		if m.Kind == k {
			out = append(out, m)
		}
	}
	return out
}
```

Note: `renderSections` parameter type uses an unnamed struct — Go disallows that across function boundaries when the struct has methods, but for simple inline struct args it's allowed only via the same composite literal type. Convert to a named type at top of file:

```go
type formatSection struct {
	title   string
	entries []Memory
	bullet  bool
}
```

Refactor `Format` and `renderSections` to use `formatSection`. Update test if needed (it doesn't reference the type).

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/memory/ -run "TestFormat|TestEstimate" -v`
Expected: PASS for all.

- [ ] **Step 5: Commit**

```bash
git add internal/memory/format.go internal/memory/format_test.go
git commit -m "feat(memory): add format.go for priority-aware FileContext rendering"
```

---

## Task 13: mem9 Source adapter (wraps mem9.Client to memory.Source contract)

**Files:**
- Create: `internal/memory/mem9source.go`
- Create: `internal/memory/mem9source_test.go`

The composite client treats every input uniformly. This adapter exposes mem9 as one Source.

- [ ] **Step 1: Write failing test**

`internal/memory/mem9source_test.go`:

```go
package memory

import (
	"context"
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/memory/mem9"
)

type stubMem9Search struct {
	hits   []mem9.Hit
	create func(in mem9.CreateInput) (string, error)
	update func(id string, in mem9.CreateInput) error
}

func (s *stubMem9Search) Search(ctx context.Context, in mem9.SearchInput) ([]mem9.Hit, error) {
	return s.hits, nil
}
func (s *stubMem9Search) Create(ctx context.Context, in mem9.CreateInput) (string, error) {
	if s.create != nil {
		return s.create(in)
	}
	return "m_new", nil
}
func (s *stubMem9Search) Update(ctx context.Context, id string, in mem9.CreateInput) error {
	if s.update != nil {
		return s.update(id, in)
	}
	return nil
}
func (s *stubMem9Search) Delete(ctx context.Context, id string) error { return nil }

func TestMem9Source_Recall_TagsConventionsAndSummaries(t *testing.T) {
	stub := &stubMem9Search{
		hits: []mem9.Hit{{ID: "m1", Content: "rule one", Score: 0.9}},
	}
	src := NewMem9Source(stub, Mem9Tuning{ConventionsTopK: 5, SummariesTopK: 3})
	mems, err := src.Recall(context.Background(), MRRef{Project: "g/r", Title: "fix bug", Files: []string{"a.go"}})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	// Two queries with tag filter; stub returns same hits both times.
	if len(mems) != 2 {
		t.Fatalf("expected 2 mems (1 conv + 1 sum), got %d", len(mems))
	}
}

func TestMem9Source_WriteConvention_PassesTags(t *testing.T) {
	var captured mem9.CreateInput
	stub := &stubMem9Search{
		create: func(in mem9.CreateInput) (string, error) {
			captured = in
			return "m_x", nil
		},
	}
	src := NewMem9Source(stub, Mem9Tuning{})
	id, err := src.Create(context.Background(), "rule x", KindConvention, "g/r")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != "m_x" {
		t.Fatalf("id %q", id)
	}
	tagsJoined := ""
	for _, tg := range captured.Tags {
		tagsJoined += tg + ","
	}
	if !contains(captured.Tags, "project:g/r") {
		t.Fatalf("missing project tag: %v", captured.Tags)
	}
	if !contains(captured.Tags, "type:convention") {
		t.Fatalf("missing type tag: %v", captured.Tags)
	}
	_ = tagsJoined
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/memory/ -run TestMem9Source -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement mem9source.go**

```go
package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fahmi/gitlab-mr-review-bot/internal/memory/mem9"
)

// Mem9API is the slice of mem9.Client the source uses. Lets tests stub it.
type Mem9API interface {
	Search(ctx context.Context, in mem9.SearchInput) ([]mem9.Hit, error)
	Create(ctx context.Context, in mem9.CreateInput) (string, error)
	Update(ctx context.Context, id string, in mem9.CreateInput) error
	Delete(ctx context.Context, id string) error
}

type Mem9Tuning struct {
	ConventionsTopK int
	SummariesTopK   int
}

type Mem9Source struct {
	api    Mem9API
	tuning Mem9Tuning
}

func NewMem9Source(api Mem9API, t Mem9Tuning) *Mem9Source {
	if t.ConventionsTopK == 0 {
		t.ConventionsTopK = 20
	}
	if t.SummariesTopK == 0 {
		t.SummariesTopK = 5
	}
	return &Mem9Source{api: api, tuning: t}
}

// Recall queries mem9 for conventions + recent MR summaries for a project.
func (s *Mem9Source) Recall(ctx context.Context, mr MRRef) ([]memory.Memory, error) {
	q := buildQuery(mr)
	convs, err := s.api.Search(ctx, mem9.SearchInput{
		Query: q,
		Tags:  []string{"project:" + mr.Project, "type:" + string(KindConvention)},
		Mode:  "hybrid",
		Limit: s.tuning.ConventionsTopK,
	})
	if err != nil {
		return nil, err
	}
	sums, err := s.api.Search(ctx, mem9.SearchInput{
		Query: q,
		Tags:  []string{"project:" + mr.Project, "type:mr_summary"},
		Mode:  "hybrid",
		Limit: s.tuning.SummariesTopK,
	})
	if err != nil {
		return hitsToMemories(convs, KindConvention, mr.Project), err
	}
	out := hitsToMemories(convs, KindConvention, mr.Project)
	out = append(out, hitsToMemories(sums, KindMRSummary, mr.Project)...)
	return out, nil
}

// FetchFeedback returns recent down-rated feedback for the extractor.
func (s *Mem9Source) FetchFeedback(ctx context.Context, project string, limit int) ([]Memory, error) {
	hits, err := s.api.Search(ctx, mem9.SearchInput{
		Tags:  []string{"project:" + project, "type:feedback", "rating:down"},
		Mode:  "keyword",
		Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	return hitsToMemories(hits, KindFeedback, project), nil
}

// Create writes one memory to mem9 with appropriate tags.
func (s *Mem9Source) Create(ctx context.Context, content string, kind Kind, project string) (string, error) {
	tags := []string{"project:" + project, "type:" + kindTag(kind)}
	meta := map[string]string{"derived_at": time.Now().UTC().Format(time.RFC3339)}
	return s.api.Create(ctx, mem9.CreateInput{
		Content:  content,
		Tags:     tags,
		Metadata: meta,
	})
}

// Update edits an existing mem9 memory's content.
func (s *Mem9Source) Update(ctx context.Context, id, content string) error {
	return s.api.Update(ctx, id, mem9.CreateInput{Content: content})
}

// CreateFeedback records per-MR thumbs.
func (s *Mem9Source) CreateFeedback(ctx context.Context, mr MRRef, rating FeedbackRating, ratedBy string) (string, error) {
	tags := []string{"project:" + mr.Project, "type:feedback", "rating:" + string(rating)}
	meta := map[string]string{
		"mr_iid":   fmt.Sprintf("%d", mr.IID),
		"rated_by": ratedBy,
		"at":       time.Now().UTC().Format(time.RFC3339),
	}
	content := fmt.Sprintf("MR !%d review rated %s by %s", mr.IID, rating, ratedBy)
	return s.api.Create(ctx, mem9.CreateInput{
		Content:  content,
		Tags:     tags,
		Metadata: meta,
	})
}

func buildQuery(mr MRRef) string {
	parts := []string{mr.Title}
	parts = append(parts, mr.Files...)
	q := strings.Join(parts, " ")
	if len(q) > 200 {
		q = q[:200]
	}
	return q
}

func hitsToMemories(hits []mem9.Hit, k Kind, project string) []Memory {
	out := make([]Memory, 0, len(hits))
	for _, h := range hits {
		out = append(out, Memory{
			ID:      h.ID,
			Kind:    k,
			Content: h.Content,
			Project: project,
			Score:   h.Score,
		})
	}
	return out
}

func kindTag(k Kind) string {
	switch k {
	case KindMRSummary:
		return "mr_summary"
	default:
		return string(k)
	}
}
```

Replace `memory.Memory` self-references — already in package `memory`, so just use `Memory`. Fix in code if redundant.

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/memory/ -run TestMem9Source -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/memory/mem9source.go internal/memory/mem9source_test.go
git commit -m "feat(memory): add Mem9Source adapter"
```

---

## Task 14: Extractor

**Files:**
- Create: `internal/memory/extractor_prompt.md`
- Create: `internal/memory/extractor.go`
- Create: `internal/memory/extractor_test.go`

- [ ] **Step 1: Create extractor_prompt.md**

```markdown
You distill durable, project-relevant conventions from a code-review session for future reuse.

You receive: an MR title, description, file list, the aggregated review findings, and recent down-rated maintainer feedback for this project.

Output strict JSON only — no prose, no markdown:

{
  "summary": "<one or two sentence MR summary>",
  "conventions": [
    "<one rule per string, generally applicable, project-relevant>",
    ...
  ]
}

Rules for conventions:
- One sentence each.
- Skip MR-specific facts (file names, function names tied to this MR only).
- Skip findings that align with categories of past down-rated feedback.
- Empty array allowed.

Rules for summary:
- Reference MR by !iid (e.g. "!42 fixes nil deref in auth middleware...").
- 1-2 sentences max.
- Empty string allowed if MR is too small to summarize.
```

- [ ] **Step 2: Write failing test for Extract**

`internal/memory/extractor_test.go`:

```go
package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
)

type stubGenerator struct {
	got string
	out string
	err error
}

func (s *stubGenerator) Review(ctx context.Context, req llm.ReviewRequest) (llm.ReviewResponse, error) {
	return llm.ReviewResponse{}, nil
}
func (s *stubGenerator) Generate(ctx context.Context, system, user string) (string, llm.TokenUsage, error) {
	s.got = user
	return s.out, llm.TokenUsage{}, s.err
}
func (s *stubGenerator) Name() string { return "stub" }

func TestExtractor_ParsesJSON(t *testing.T) {
	gen := &stubGenerator{out: `{"summary":"!7 fixes auth","conventions":["Always use Postgres.","Wrap retries in helper."]}`}
	ex := NewExtractor(gen)
	out, err := ex.Extract(context.Background(), MRRef{IID: 7, Title: "fix auth"}, []Finding{{Severity: "major", Category: "bug", Message: "race"}}, nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if out.Summary != "!7 fixes auth" {
		t.Fatalf("summary %q", out.Summary)
	}
	if len(out.Conventions) != 2 {
		t.Fatalf("conventions %d", len(out.Conventions))
	}
}

func TestExtractor_TolerantOfFencedJSON(t *testing.T) {
	gen := &stubGenerator{out: "```json\n{\"summary\":\"x\",\"conventions\":[]}\n```"}
	ex := NewExtractor(gen)
	out, err := ex.Extract(context.Background(), MRRef{}, nil, nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if out.Summary != "x" {
		t.Fatalf("got %q", out.Summary)
	}
}

func TestExtractor_BadJSON_ReturnsErr(t *testing.T) {
	gen := &stubGenerator{out: "not json"}
	ex := NewExtractor(gen)
	_, err := ex.Extract(context.Background(), MRRef{}, nil, nil)
	if err == nil {
		t.Fatalf("expected err on bad JSON")
	}
}

func TestExtractor_SendsFindingsAndFeedback(t *testing.T) {
	gen := &stubGenerator{out: `{"summary":"","conventions":[]}`}
	ex := NewExtractor(gen)
	_, _ = ex.Extract(context.Background(),
		MRRef{IID: 5, Title: "t", Files: []string{"a.go"}},
		[]Finding{{Severity: "minor", Category: "style", Message: "naming"}},
		[]Memory{{Kind: KindFeedback, Content: "MR !4 rated down"}},
	)
	if !strings.Contains(gen.got, "naming") {
		t.Fatalf("missing finding: %s", gen.got)
	}
	if !strings.Contains(gen.got, "rated down") {
		t.Fatalf("missing past feedback: %s", gen.got)
	}
	if !strings.Contains(gen.got, "a.go") {
		t.Fatalf("missing file: %s", gen.got)
	}
}
```

- [ ] **Step 3: Run, verify failure**

Run: `go test ./internal/memory/ -run TestExtractor -v`
Expected: FAIL — Extractor undefined.

- [ ] **Step 4: Implement extractor.go**

```go
package memory

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
)

//go:embed extractor_prompt.md
var extractorSystemPrompt string

type Extractor struct {
	provider llm.Provider
}

func NewExtractor(p llm.Provider) *Extractor {
	return &Extractor{provider: p}
}

type ExtractionResult struct {
	Summary     string
	Conventions []string
}

var fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)\\s*```")

func (e *Extractor) Extract(ctx context.Context, mr MRRef, findings []Finding, feedback []Memory) (ExtractionResult, error) {
	user := buildExtractorInput(mr, findings, feedback)
	out, _, err := e.provider.Generate(ctx, extractorSystemPrompt, user)
	if err != nil {
		return ExtractionResult{}, fmt.Errorf("generate: %w", err)
	}
	out = strings.TrimSpace(out)
	if m := fenceRe.FindStringSubmatch(out); m != nil {
		out = strings.TrimSpace(m[1])
	}
	var raw struct {
		Summary     string   `json:"summary"`
		Conventions []string `json:"conventions"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return ExtractionResult{}, fmt.Errorf("parse extractor json: %w (raw=%q)", err, out)
	}
	return ExtractionResult{Summary: raw.Summary, Conventions: raw.Conventions}, nil
}

func buildExtractorInput(mr MRRef, findings []Finding, feedback []Memory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "MR: !%d %q\n", mr.IID, mr.Title)
	if len(mr.Files) > 0 {
		fmt.Fprintf(&b, "Files: %s\n", strings.Join(mr.Files, ", "))
	}
	if len(findings) > 0 {
		b.WriteString("\nFindings:\n")
		for _, f := range findings {
			fmt.Fprintf(&b, "- [%s/%s] %s:%d %s\n", f.Severity, f.Category, f.File, f.Line, f.Message)
		}
	}
	if len(feedback) > 0 {
		b.WriteString("\nPast down-rated feedback:\n")
		for _, fb := range feedback {
			fmt.Fprintf(&b, "- %s\n", fb.Content)
		}
	}
	return b.String()
}
```

- [ ] **Step 5: Run, verify pass**

Run: `go test ./internal/memory/ -run TestExtractor -v`
Expected: PASS for all four.

- [ ] **Step 6: Commit**

```bash
git add internal/memory/extractor.go internal/memory/extractor_prompt.md internal/memory/extractor_test.go
git commit -m "feat(memory): add Extractor for post-review LLM convention extraction"
```

---

## Task 15: Composite client (recall fan-out + merge + write fan-out)

**Files:**
- Create: `internal/memory/composite.go`
- Create: `internal/memory/composite_test.go`

- [ ] **Step 1: Write failing tests**

`internal/memory/composite_test.go`:

```go
package memory

import (
	"context"
	"strings"
	"testing"
)

type stubReader struct {
	mems []Memory
	err  error
}

func (s *stubReader) Recall(ctx context.Context, mr MRRef) ([]Memory, error) {
	return s.mems, s.err
}

type stubMem9Adapter struct {
	created   []string
	feedback  []string
	feedbackErr error
}

func (s *stubMem9Adapter) Recall(ctx context.Context, mr MRRef) ([]Memory, error) { return nil, nil }
func (s *stubMem9Adapter) FetchFeedback(ctx context.Context, project string, limit int) ([]Memory, error) {
	return nil, nil
}
func (s *stubMem9Adapter) Create(ctx context.Context, content string, k Kind, project string) (string, error) {
	s.created = append(s.created, content)
	return "m_" + content[:1], nil
}
func (s *stubMem9Adapter) Update(ctx context.Context, id, content string) error { return nil }
func (s *stubMem9Adapter) CreateFeedback(ctx context.Context, mr MRRef, rating FeedbackRating, ratedBy string) (string, error) {
	s.feedback = append(s.feedback, string(rating)+":"+ratedBy)
	return "fb_1", s.feedbackErr
}

type stubMirrorSink struct {
	convs    []string
	summaries []string
	feedback []string
}

func (s *stubMirrorSink) AppendConvention(ctx context.Context, mr MRRef, text, id string) error {
	s.convs = append(s.convs, text)
	return nil
}
func (s *stubMirrorSink) AppendMRSummary(ctx context.Context, mr MRRef, text, id string) error {
	s.summaries = append(s.summaries, text)
	return nil
}
func (s *stubMirrorSink) AppendFeedback(ctx context.Context, mr MRRef, rating FeedbackRating, ratedBy string) error {
	s.feedback = append(s.feedback, string(rating)+":"+ratedBy)
	return nil
}

type stubExtractor struct {
	out ExtractionResult
	err error
}

func (s *stubExtractor) Extract(ctx context.Context, mr MRRef, findings []Finding, feedback []Memory) (ExtractionResult, error) {
	return s.out, s.err
}

func TestComposite_Recall_MergesAcrossSources(t *testing.T) {
	c := &Composite{
		Sources: []Source{
			&stubReader{mems: []Memory{{Kind: KindRule, Content: "always X"}}},
			&stubReader{mems: []Memory{{Kind: KindConvention, Content: "prefer Y"}}},
		},
		TokenBudget: 5000,
	}
	res, err := c.Recall(context.Background(), MRRef{Project: "g/r"})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if !strings.Contains(res.FileContext, "always X") || !strings.Contains(res.FileContext, "prefer Y") {
		t.Fatalf("missing content: %s", res.FileContext)
	}
}

func TestComposite_Recall_OneSourceFailureDoesNotBlock(t *testing.T) {
	c := &Composite{
		Sources: []Source{
			&stubReader{err: context.DeadlineExceeded},
			&stubReader{mems: []Memory{{Kind: KindConvention, Content: "ok"}}},
		},
		TokenBudget: 5000,
	}
	res, err := c.Recall(context.Background(), MRRef{Project: "g/r"})
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if !strings.Contains(res.FileContext, "ok") {
		t.Fatalf("survivor not present: %s", res.FileContext)
	}
}

func TestComposite_Write_FansOutToMem9AndMirror(t *testing.T) {
	mem9stub := &stubMem9Adapter{}
	mirror := &stubMirrorSink{}
	ex := &stubExtractor{out: ExtractionResult{Summary: "!7 sum", Conventions: []string{"rule one"}}}
	c := &Composite{
		Mem9:      mem9stub,
		Mirror:    mirror,
		Extractor: ex,
	}
	if err := c.Write(context.Background(), MRRef{IID: 7}, nil, ""); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(mem9stub.created) != 2 { // 1 conv + 1 summary
		t.Fatalf("mem9 created %v", mem9stub.created)
	}
	if len(mirror.convs) != 1 || len(mirror.summaries) != 1 {
		t.Fatalf("mirror %+v", mirror)
	}
}

func TestComposite_WriteFeedback_FansOut(t *testing.T) {
	mem9stub := &stubMem9Adapter{}
	mirror := &stubMirrorSink{}
	c := &Composite{Mem9: mem9stub, Mirror: mirror}
	if err := c.WriteFeedback(context.Background(), MRRef{IID: 9}, RatingDown, "alice"); err != nil {
		t.Fatalf("WriteFeedback: %v", err)
	}
	if len(mem9stub.feedback) != 1 || mem9stub.feedback[0] != "down:alice" {
		t.Fatalf("mem9 fb %v", mem9stub.feedback)
	}
	if len(mirror.feedback) != 1 {
		t.Fatalf("mirror fb %v", mirror.feedback)
	}
}
```

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/memory/ -run TestComposite -v`
Expected: FAIL — Composite undefined.

- [ ] **Step 3: Implement composite.go**

```go
package memory

import (
	"context"
	"fmt"
	"log"
	"sync"
)

// Source is anything that produces memories during recall.
type Source interface {
	Recall(ctx context.Context, mr MRRef) ([]Memory, error)
}

// Mem9Adapter is the writer-side mem9 abstraction the composite needs.
type Mem9Adapter interface {
	Source
	FetchFeedback(ctx context.Context, project string, limit int) ([]Memory, error)
	Create(ctx context.Context, content string, k Kind, project string) (string, error)
	Update(ctx context.Context, id, content string) error
	CreateFeedback(ctx context.Context, mr MRRef, rating FeedbackRating, ratedBy string) (string, error)
}

// MirrorSink writes to local mirror file.
type MirrorSink interface {
	AppendConvention(ctx context.Context, mr MRRef, text, id string) error
	AppendMRSummary(ctx context.Context, mr MRRef, text, id string) error
	AppendFeedback(ctx context.Context, mr MRRef, rating FeedbackRating, ratedBy string) error
}

// ExtractorAPI is the extractor abstraction.
type ExtractorAPI interface {
	Extract(ctx context.Context, mr MRRef, findings []Finding, feedback []Memory) (ExtractionResult, error)
}

type Composite struct {
	Sources     []Source
	Mem9        Mem9Adapter
	Mirror      MirrorSink
	Extractor   ExtractorAPI
	TokenBudget int
}

func (c *Composite) Recall(ctx context.Context, mr MRRef) (RecallResult, error) {
	if len(c.Sources) == 0 {
		return RecallResult{}, nil
	}
	type out struct {
		mems []Memory
		err  error
	}
	results := make([]out, len(c.Sources))
	var wg sync.WaitGroup
	for i, src := range c.Sources {
		wg.Add(1)
		go func(i int, src Source) {
			defer wg.Done()
			m, err := src.Recall(ctx, mr)
			results[i] = out{m, err}
		}(i, src)
	}
	wg.Wait()

	var merged []Memory
	for i, r := range results {
		if r.err != nil {
			log.Printf("memory: source %d recall failed: %v", i, r.err)
			continue
		}
		merged = append(merged, r.mems...)
	}
	merged = dedupByID(merged)
	budget := c.TokenBudget
	if budget == 0 {
		budget = 2000
	}
	text, truncated := Format(merged, budget)
	return RecallResult{FileContext: text, Memories: merged, Truncated: truncated}, nil
}

func (c *Composite) Write(ctx context.Context, mr MRRef, findings []Finding, _ string) error {
	if c.Extractor == nil {
		return nil
	}
	var feedback []Memory
	if c.Mem9 != nil {
		feedback, _ = c.Mem9.FetchFeedback(ctx, mr.Project, 5)
	}
	res, err := c.Extractor.Extract(ctx, mr, findings, feedback)
	if err != nil {
		log.Printf("memory: extract failed: %v", err)
		return nil
	}

	for _, conv := range res.Conventions {
		var id string
		if c.Mem9 != nil {
			cid, cerr := c.Mem9.Create(ctx, conv, KindConvention, mr.Project)
			if cerr != nil {
				log.Printf("memory: mem9 create convention failed: %v", cerr)
			}
			id = cid
		}
		if c.Mirror != nil {
			if merr := c.Mirror.AppendConvention(ctx, mr, conv, id); merr != nil {
				log.Printf("memory: mirror append convention failed: %v", merr)
			}
		}
	}
	if res.Summary != "" {
		var id string
		if c.Mem9 != nil {
			cid, cerr := c.Mem9.Create(ctx, res.Summary, KindMRSummary, mr.Project)
			if cerr != nil {
				log.Printf("memory: mem9 create summary failed: %v", cerr)
			}
			id = cid
		}
		if c.Mirror != nil {
			if merr := c.Mirror.AppendMRSummary(ctx, mr, res.Summary, id); merr != nil {
				log.Printf("memory: mirror append summary failed: %v", merr)
			}
		}
	}
	return nil
}

func (c *Composite) WriteFeedback(ctx context.Context, mr MRRef, rating FeedbackRating, ratedBy string) error {
	var firstErr error
	if c.Mem9 != nil {
		if _, err := c.Mem9.CreateFeedback(ctx, mr, rating, ratedBy); err != nil {
			log.Printf("memory: mem9 feedback failed: %v", err)
			firstErr = err
		}
	}
	if c.Mirror != nil {
		if err := c.Mirror.AppendFeedback(ctx, mr, rating, ratedBy); err != nil {
			log.Printf("memory: mirror feedback failed: %v", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return fmt.Errorf("write feedback: %w", firstErr)
	}
	return nil
}

func dedupByID(mems []Memory) []Memory {
	seen := map[string]bool{}
	out := make([]Memory, 0, len(mems))
	for _, m := range mems {
		if m.ID != "" {
			if seen[m.ID] {
				continue
			}
			seen[m.ID] = true
		}
		out = append(out, m)
	}
	return out
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/memory/ -v`
Expected: PASS for entire memory package.

- [ ] **Step 5: Commit**

```bash
git add internal/memory/composite.go internal/memory/composite_test.go
git commit -m "feat(memory): add Composite client fanning recall + write across sources"
```

---

## Task 16: Wire memory.Recall into orchestrator

**Files:**
- Modify: `internal/review/orchestrator.go`
- Modify: `internal/review/orchestrator_test.go`

- [ ] **Step 1: Write failing test**

Append to `internal/review/orchestrator_test.go`:

```go
type stubMemory struct {
	recallCalled    bool
	writeCalled     bool
	recallReturn    memory.RecallResult
	recallErr       error
	receivedFindings []memory.Finding
}

func (s *stubMemory) Recall(ctx context.Context, mr memory.MRRef) (memory.RecallResult, error) {
	s.recallCalled = true
	return s.recallReturn, s.recallErr
}
func (s *stubMemory) Write(ctx context.Context, mr memory.MRRef, findings []memory.Finding, _ string) error {
	s.writeCalled = true
	s.receivedFindings = findings
	return nil
}
func (s *stubMemory) WriteFeedback(ctx context.Context, mr memory.MRRef, rating memory.FeedbackRating, ratedBy string) error {
	return nil
}

func TestOrchestrator_InjectsMemoryFileContext(t *testing.T) {
	prov := &recordingProvider{}
	mem := &stubMemory{recallReturn: memory.RecallResult{FileContext: "## Project Rules\nUse Postgres."}}
	o := New(Config{
		GitLab:   &fakeGitLab{},
		Provider: prov,
		Memory:   mem,
	})
	_, err := o.Run(context.Background(), "https://gitlab.example/group/repo/-/merge_requests/7")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !mem.recallCalled {
		t.Fatalf("Recall not called")
	}
	if prov.lastReq.FileContext != "## Project Rules\nUse Postgres." {
		t.Fatalf("FileContext got %q", prov.lastReq.FileContext)
	}
	if !mem.writeCalled {
		t.Fatalf("Write not called")
	}
}

func TestOrchestrator_MemorySoftFailDoesNotBlock(t *testing.T) {
	prov := &recordingProvider{}
	mem := &stubMemory{recallErr: errors.New("mem9 down")}
	o := New(Config{GitLab: &fakeGitLab{}, Provider: prov, Memory: mem})
	_, err := o.Run(context.Background(), "https://gitlab.example/group/repo/-/merge_requests/7")
	if err != nil {
		t.Fatalf("expected nil err on memory failure, got %v", err)
	}
}

```

If `recordingProvider` / `fakeGitLab` don't already exist in `orchestrator_test.go`, add them at the top of that file:

```go
type recordingProvider struct {
	lastReq llm.ReviewRequest
}

func (p *recordingProvider) Review(ctx context.Context, req llm.ReviewRequest) (llm.ReviewResponse, error) {
	p.lastReq = req
	return llm.ReviewResponse{Findings: []llm.Finding{}}, nil
}
func (p *recordingProvider) Generate(ctx context.Context, system, user string) (string, llm.TokenUsage, error) {
	return `{"summary":"","conventions":[]}`, llm.TokenUsage{}, nil
}
func (p *recordingProvider) Name() string { return "recording" }

type fakeGitLab struct{}

func (fakeGitLab) GetMRWithChanges(ctx context.Context, project string, iid int) (*gitlab.MR, []gitlab.FileChange, error) {
	return &gitlab.MR{
			IID: iid, Title: "test mr", WebURL: "https://example",
			BaseSHA: "b", StartSHA: "s", HeadSHA: "h", TargetBranch: "main",
		}, []gitlab.FileChange{
			{NewPath: "a.go", OldPath: "a.go", Diff: "@@ -1,1 +1,1 @@\n-old\n+new\n"},
		}, nil
}
func (fakeGitLab) PostNote(ctx context.Context, project string, iid int, body string) error {
	return nil
}
func (fakeGitLab) PostDiscussion(ctx context.Context, project string, iid int, body string, pos gitlab.Position) error {
	return nil
}
func (fakeGitLab) GetFileRaw(ctx context.Context, project, path, ref string) (string, error) {
	return "", gitlab.ErrFileNotFound
}
```

Add imports: `"context"`, `"github.com/fahmi/gitlab-mr-review-bot/internal/llm"`, `"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"`, `"github.com/fahmi/gitlab-mr-review-bot/internal/memory"`, `"errors"`.

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/review/ -run TestOrchestrator_InjectsMemory -v`
Expected: FAIL — Config.Memory unknown.

- [ ] **Step 3: Modify orchestrator.go**

Add to `Config`:

```go
type Config struct {
	GitLab         gitlab.Client
	Provider       llm.Provider
	SystemPrompt   string
	MaxFileTokens  int
	MaxMRTokens    int
	MaxConcurrent  int
	LLMCallTimeout time.Duration
	IgnoreGlobs    []string
	Memory         memory.Client
}
```

In `New`, default `Memory` to `memory.Noop{}` if nil.

```go
func New(cfg Config) *Orchestrator {
	// ... existing defaults ...
	if cfg.Memory == nil {
		cfg.Memory = memory.Noop{}
	}
	return &Orchestrator{cfg: cfg}
}
```

In `RunWithProgress`, after `mr, changes, err := o.cfg.GitLab.GetMRWithChanges(...)`, build MR ref + call Recall:

```go
files := make([]string, 0, len(changes))
for _, ch := range changes {
	files = append(files, ch.NewPath)
}
mrRef := memory.MRRef{
	Project:   ref.ProjectPath,
	IID:       ref.MRIID,
	Title:     mr.Title,
	HeadSHA:   mr.HeadSHA,
	WebURL:    mr.WebURL,
	TargetRef: mr.TargetBranch, // see note below
	Files:     files,
}

emit("recalling", "loading memory")
rec, _ := o.cfg.Memory.Recall(ctx, mrRef)
fileContext := rec.FileContext
```

Note: `mr.TargetBranch` requires adding the field to `gitlab.MR` struct + parsing it from `mrEnvelope`. If not present, leave `TargetRef: ""` and the source falls back to `HEAD`. Add it now in `internal/gitlab/types.go`/`client.go` if straightforward:

```go
// gitlab/types.go
type MR struct {
	IID          int
	Title        string
	WebURL       string
	BaseSHA      string
	StartSHA     string
	HeadSHA      string
	TargetBranch string
}

// gitlab/client.go: extend mrEnvelope
type mrEnvelope struct {
	IID          int    `json:"iid"`
	Title        string `json:"title"`
	WebURL       string `json:"web_url"`
	TargetBranch string `json:"target_branch"`
	DiffRefs     struct{ ... } `json:"diff_refs"`
}
// And in GetMRWithChanges return path:
return &MR{ ..., TargetBranch: env.TargetBranch }, changes, nil
```

In the chunk dispatch loop, plumb `fileContext` into ReviewRequest:

```go
resp, err := o.cfg.Provider.Review(callCtx, llm.ReviewRequest{
	SystemPrompt: o.cfg.SystemPrompt,
	FilePath:     j.path,
	DiffChunk:    j.chunk.DiffText,
	FileContext:  fileContext,
})
```

After `o.cfg.GitLab.PostNote(...)` succeeds, call Write:

```go
memFindings := make([]memory.Finding, 0, len(agg.Findings))
for _, f := range agg.Findings {
	memFindings = append(memFindings, memory.Finding{
		Severity: f.Severity, Category: f.Category,
		File: f.File, Line: f.Line, Message: f.Message,
	})
}
go func() {
	wctx, wcancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer wcancel()
	_ = o.cfg.Memory.Write(wctx, mrRef, memFindings, "")
}()
```

(Async write so the user-facing review return isn't delayed by extractor LLM call.)

- [ ] **Step 4: Use FileContext in providers**

Edit `internal/llm/anthropic.go` `Review` to splice `req.FileContext` in front of the user message but inside the cached prefix region. Simplest: append to System block as a second cached text block:

```go
system := []anthropicBlock{{
	Type:         "text",
	Text:         req.SystemPrompt,
	CacheControl: map[string]string{"type": "ephemeral"},
}}
if req.FileContext != "" {
	system = append(system, anthropicBlock{
		Type:         "text",
		Text:         "\n\n" + req.FileContext,
		CacheControl: map[string]string{"type": "ephemeral"},
	})
}
body := anthropicReq{
	Model:     a.cfg.Model,
	MaxTokens: 4096,
	System:    system,
	Messages: []anthropicMsg{{
		Role:    "user",
		Content: []anthropicBlock{{Type: "text", Text: user}},
	}},
}
```

For OpenAI / OpenRouter / Ollama: prepend FileContext into the user message before the diff block:

```go
user := ""
if req.FileContext != "" {
	user = req.FileContext + "\n\n"
}
user += fmt.Sprintf("File: %s\n\nDiff:\n%s", req.FilePath, req.DiffChunk)
```

- [ ] **Step 5: Run, verify pass**

Run: `go test ./internal/review/ ./internal/llm/ -v`
Expected: PASS.

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/review/orchestrator.go internal/review/orchestrator_test.go internal/llm/anthropic.go internal/llm/openai.go internal/llm/ollama.go internal/llm/openrouter.go internal/gitlab/client.go internal/gitlab/types.go
git commit -m "feat(review): wire memory recall + async write into orchestrator"
```

---

## Task 17: Discord feedback buttons

**Files:**
- Modify: `internal/discord/bot.go`
- Modify: `internal/discord/bot_test.go`
- Modify: `internal/discord/session.go` (add InteractionResponseEditWithComponents helper if needed)

- [ ] **Step 1: Add memory client to Bot struct**

Edit `internal/discord/bot.go`:

```go
type Bot struct {
	Session    SessionAPI
	Runner     Runner
	Jobs       *jobs.Tracker
	Validator  Validator
	TickEvery  time.Duration
	JobTimeout time.Duration
	Memory     memory.Client // new

	OnJobDone func()
}
```

Add import: `"github.com/fahmi/gitlab-mr-review-bot/internal/memory"` and `"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"` (for ParseURL).

- [ ] **Step 2: Write failing test for button interaction**

Append to `internal/discord/bot_test.go`:

```go
func TestBot_HandleInteraction_FeedbackUp(t *testing.T) {
	mem := &stubBotMemory{}
	bot := &Bot{
		Session: &fakeSession{},
		Memory:  mem,
		Jobs:    jobs.New(),
	}
	// Pre-seed a finished job for jobID lookup.
	job := bot.Jobs.Create("u1", "https://gitlab.example/group/repo/-/merge_requests/7")
	bot.Jobs.Update(job.ID, func(j *jobs.Job) { j.Status = jobs.StatusDone })

	i := &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
		Data: discordgo.MessageComponentInteractionData{
			CustomID: "review_feedback:up:" + job.ID,
		},
		Member: &discordgo.Member{User: &discordgo.User{ID: "u1"}},
	}
	bot.HandleInteraction(i)
	if mem.lastRating != memory.RatingUp {
		t.Fatalf("rating got %s", mem.lastRating)
	}
	if mem.lastMR.IID != 7 {
		t.Fatalf("iid got %d", mem.lastMR.IID)
	}
}

func TestBot_HandleInteraction_FeedbackJobMissing(t *testing.T) {
	mem := &stubBotMemory{}
	sess := &fakeSession{}
	bot := &Bot{Session: sess, Memory: mem, Jobs: jobs.New()}
	i := &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
		Data: discordgo.MessageComponentInteractionData{
			CustomID: "review_feedback:up:nonexistent",
		},
		Member: &discordgo.Member{User: &discordgo.User{ID: "u1"}},
	}
	bot.HandleInteraction(i)
	if mem.called {
		t.Fatalf("memory should not be called for missing job")
	}
	if !sess.respondedEphemeral {
		t.Fatalf("expected ephemeral reply")
	}
}

type stubBotMemory struct {
	called      bool
	lastRating  memory.FeedbackRating
	lastMR      memory.MRRef
	lastRatedBy string
}

func (s *stubBotMemory) Recall(ctx context.Context, mr memory.MRRef) (memory.RecallResult, error) {
	return memory.RecallResult{}, nil
}
func (s *stubBotMemory) Write(ctx context.Context, mr memory.MRRef, findings []memory.Finding, _ string) error {
	return nil
}
func (s *stubBotMemory) WriteFeedback(ctx context.Context, mr memory.MRRef, rating memory.FeedbackRating, ratedBy string) error {
	s.called = true
	s.lastMR = mr
	s.lastRating = rating
	s.lastRatedBy = ratedBy
	return nil
}
```

If `fakeSession` doesn't already track `respondedEphemeral`, extend it.

- [ ] **Step 3: Run, verify failure**

Run: `go test ./internal/discord/ -run TestBot_HandleInteraction_Feedback -v`
Expected: FAIL.

- [ ] **Step 4: Implement handler**

In `HandleInteraction`, before the existing command-name switch, branch on interaction Type:

```go
func (b *Bot) HandleInteraction(i *discordgo.Interaction) {
	if i.Type == discordgo.InteractionMessageComponent {
		b.handleComponent(i)
		return
	}
	switch commandName(i) {
	// ... existing code ...
	}
}

func (b *Bot) handleComponent(i *discordgo.Interaction) {
	data, ok := i.Data.(discordgo.MessageComponentInteractionData)
	if !ok {
		return
	}
	parts := strings.SplitN(data.CustomID, ":", 3)
	if len(parts) != 3 || parts[0] != "review_feedback" {
		return
	}
	var rating memory.FeedbackRating
	switch parts[1] {
	case "up":
		rating = memory.RatingUp
	case "down":
		rating = memory.RatingDown
	default:
		return
	}
	jobID := parts[2]
	job, ok := b.Jobs.Get(jobID)
	if !ok {
		b.replyEphemeral(i, "this review has expired — feedback not recorded")
		return
	}
	ref, err := gitlab.ParseURL(job.MRURL)
	if err != nil {
		b.replyEphemeral(i, "invalid MR URL on job")
		return
	}
	userID, _ := principalFrom(i)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.Memory.WriteFeedback(ctx, memory.MRRef{
		Project: ref.ProjectPath,
		IID:     ref.MRIID,
		WebURL:  job.WebURL,
	}, rating, userID); err != nil {
		b.replyEphemeral(i, "feedback failed: "+err.Error())
		return
	}
	b.replyEphemeral(i, "noted, thanks")
}
```

Add import `"strings"`.

- [ ] **Step 5: Add buttons to final review reply**

Modify `editFinal` in `bot.go` to optionally accept components, and update `runJob` final call:

```go
func (b *Bot) editFinalWithComponents(i *discordgo.Interaction, content string, components []discordgo.MessageComponent) {
	c := content
	_, _ = b.Session.InteractionResponseEdit(i, &discordgo.WebhookEdit{Content: &c, Components: &components})
}
```

In `runJob` success branch:

```go
final := fmt.Sprintf(":white_check_mark: review done — posted=%d skipped=%d findings=%d\n%s",
	res.Posted, res.Skipped, res.Findings, res.WebURL)
buttons := []discordgo.MessageComponent{
	discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{
			Label:    "👍 helpful",
			Style:    discordgo.SuccessButton,
			CustomID: "review_feedback:up:" + jobID,
		},
		discordgo.Button{
			Label:    "👎 not helpful",
			Style:    discordgo.DangerButton,
			CustomID: "review_feedback:down:" + jobID,
		},
	}},
}
b.editFinalWithComponents(i, final, buttons)
```

- [ ] **Step 6: Run, verify pass**

Run: `go test ./internal/discord/ -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/discord/bot.go internal/discord/bot_test.go internal/discord/session.go
git commit -m "feat(discord): add per-MR feedback buttons + InteractionMessageComponent handler"
```

---

## Task 18: Wire memory client in main + jobs cleaner

**Files:**
- Modify: `cmd/bot/main.go`
- Modify: `config.example.yaml`

- [ ] **Step 1: Read current main.go**

Run: `cat cmd/bot/main.go` to confirm structure (already read once during exploration). Locate the orchestrator construction and bot construction.

- [ ] **Step 2: Build memory client**

Add helper in `cmd/bot/main.go`:

```go
func buildMemory(cfg config.Memory, gl gitlab.Client, prov llm.Provider) memory.Client {
	if !cfg.Enabled {
		return memory.Noop{}
	}
	composite := &memory.Composite{
		TokenBudget: cfg.RecallTokenBudget,
	}

	var sources []memory.Source

	if cfg.Mem9.Enabled {
		mem9c := mem9.New(mem9.Config{
			BaseURL: cfg.Mem9.BaseURL,
			APIKey:  cfg.Mem9.APIKey,
			Timeout: cfg.HTTPTimeout,
		})
		mem9src := memory.NewMem9Source(mem9c, memory.Mem9Tuning{
			ConventionsTopK: cfg.Mem9.ConventionsTopK,
			SummariesTopK:   cfg.Mem9.SummariesTopK,
		})
		composite.Mem9 = mem9src
		sources = append(sources, mem9src)
	}
	if cfg.RepoRules.Enabled {
		sources = append(sources, reporules.New(gl, cfg.RepoRules.Path))
	}
	if cfg.Mirror.Enabled {
		var mwriter mirror.Mem9Writer
		if composite.Mem9 != nil {
			mwriter = &mirrorMem9Adapter{m: composite.Mem9}
		}
		mr := mirror.NewSource(cfg.Mirror.Dir, mwriter)
		composite.Mirror = mr
		sources = append(sources, mr)
	}
	composite.Sources = sources
	composite.Extractor = memory.NewExtractor(prov)
	return composite
}

// mirrorMem9Adapter bridges mem9source.Create(content,kind,project) to the
// mirror.Mem9Writer interface (same signature, slightly different package types).
type mirrorMem9Adapter struct{ m memory.Mem9Adapter }

func (a *mirrorMem9Adapter) Create(ctx context.Context, content string, k memory.Kind, project string) (string, error) {
	return a.m.Create(ctx, content, k, project)
}
func (a *mirrorMem9Adapter) Update(ctx context.Context, id, content string) error {
	return a.m.Update(ctx, id, content)
}
```

Add imports:

```go
"context"
"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
"github.com/fahmi/gitlab-mr-review-bot/internal/memory/mem9"
"github.com/fahmi/gitlab-mr-review-bot/internal/memory/mirror"
"github.com/fahmi/gitlab-mr-review-bot/internal/memory/reporules"
```

- [ ] **Step 3: Pass memory into orchestrator + bot**

Wherever `review.New(...)` is called, pass `Memory: memClient`. Where `discord.Bot{...}` is constructed, set `Memory: memClient`.

```go
memClient := buildMemory(cfg.Memory, glClient, llmProvider)
orch := review.New(review.Config{
	GitLab:         glClient,
	Provider:       llmProvider,
	SystemPrompt:   "",
	MaxFileTokens:  cfg.Review.MaxFileTokens,
	MaxMRTokens:    cfg.Review.MaxMRTokens,
	MaxConcurrent:  cfg.Review.MaxConcurrentChunks,
	LLMCallTimeout: cfg.Review.LLMCallTimeout,
	IgnoreGlobs:    cfg.Review.IgnoreGlobs,
	Memory:         memClient,
})
bot := &discord.Bot{
	Session:    sess,
	Runner:     orch,
	Jobs:       jobsTracker,
	Validator:  validator,
	TickEvery:  3 * time.Second, // keep whatever the existing main.go used; do not change
	JobTimeout: cfg.Review.JobTimeout,
	Memory:     memClient,
}
```

- [ ] **Step 4: Start jobs cleaner with 24h TTL**

After constructing `jobsTracker`, before the bot run-loop:

```go
appCtx, appCancel := context.WithCancel(context.Background())
defer appCancel()
go jobsTracker.StartCleaner(appCtx, 24*time.Hour, time.Hour)
```

If `cmd/bot/main.go` already creates a top-level context, reuse that name instead of `appCtx` and skip the new cancel.

- [ ] **Step 5: Update config.example.yaml**

Append:

```yaml
memory:
  enabled: false                          # set true to opt in
  recall_token_budget: 2000
  http_timeout: 10s
  mem9:
    enabled: true
    base_url: https://api.mem9.ai         # or http://localhost:8080
    api_key: env:MEM9_API_KEY
    conventions_top_k: 20
    summaries_top_k: 5
  repo_rules:
    enabled: true
    path: .review/rules.md
  mirror:
    enabled: true
    dir: ~/.cache/gitlab-mr-bot/memory
```

- [ ] **Step 6: Verify build + run unit tests**

Run: `go build ./...`
Expected: PASS.

Run: `go test ./...`
Expected: PASS for all existing + new tests.

- [ ] **Step 7: Commit**

```bash
git add cmd/bot/main.go config.example.yaml
git commit -m "feat(bot): wire memory client into orchestrator + bot, start 24h jobs cleaner"
```

---

## Task 19: Integration test — orchestrator with stub mem9 server

**Files:**
- Modify: `internal/review/e2e_test.go`

- [ ] **Step 1: Write integration test**

Append to `internal/review/e2e_test.go` (extend existing test or add new):

```go
func TestE2E_MemoryRecallInjectedIntoProviderRequest(t *testing.T) {
	// stub mem9 server
	mem9Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"memories": []map[string]any{
				{"id": "m1", "content": "Always validate JWTs.", "score": 0.9},
			},
		})
	}))
	defer mem9Srv.Close()

	// stub GitLab + provider as in existing tests
	gl := &fakeGitLab{ /* one MR, one chunk */ }
	prov := &recordingProvider{}

	mem9c := mem9.New(mem9.Config{BaseURL: mem9Srv.URL, APIKey: "k", HTTP: mem9Srv.Client()})
	mem9src := memory.NewMem9Source(mem9c, memory.Mem9Tuning{ConventionsTopK: 5, SummariesTopK: 5})
	composite := &memory.Composite{
		Sources:     []memory.Source{mem9src},
		Mem9:        mem9src,
		Extractor:   memory.NewExtractor(prov),
		TokenBudget: 5000,
	}
	o := New(Config{GitLab: gl, Provider: prov, Memory: composite})
	_, err := o.Run(context.Background(), "https://gitlab.example/g/r/-/merge_requests/1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(prov.lastReq.FileContext, "Always validate JWTs") {
		t.Fatalf("FileContext missing recalled rule: %q", prov.lastReq.FileContext)
	}
}

func TestE2E_MemoryDownDoesNotBlockReview(t *testing.T) {
	mem9Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer mem9Srv.Close()
	gl := &fakeGitLab{}
	prov := &recordingProvider{}
	mem9c := mem9.New(mem9.Config{BaseURL: mem9Srv.URL, APIKey: "k", HTTP: mem9Srv.Client()})
	mem9src := memory.NewMem9Source(mem9c, memory.Mem9Tuning{})
	composite := &memory.Composite{Sources: []memory.Source{mem9src}, Mem9: mem9src, Extractor: memory.NewExtractor(prov)}
	o := New(Config{GitLab: gl, Provider: prov, Memory: composite})
	_, err := o.Run(context.Background(), "https://gitlab.example/g/r/-/merge_requests/1")
	if err != nil {
		t.Fatalf("expected nil err on memory failure: %v", err)
	}
}
```

- [ ] **Step 2: Run, verify pass**

Run: `go test ./internal/review/ -run TestE2E -v`
Expected: PASS for new tests + existing.

- [ ] **Step 3: Commit**

```bash
git add internal/review/e2e_test.go
git commit -m "test(review): e2e for memory injection and soft-fail behavior"
```

---

## Task 20: Final smoke + README update

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Run full suite**

Run: `go test ./... -count=1`
Expected: PASS across all packages.

Run: `go vet ./...`
Expected: no findings.

- [ ] **Step 2: Update README**

Append a "Memory" section:

```markdown
## Memory (optional)

Set `memory.enabled: true` in `config.yaml` to give the bot persistent project memory across reviews. Three sources combine into one context block:

- **`.review/rules.md` in the MR target branch** — curated team rules; highest priority.
- **mem9** — durable LLM-extracted conventions and recent MR summaries (`X-API-Key` auth).
- **Local mirror** — `~/.cache/gitlab-mr-bot/memory/<project>.md`, two-way synced with mem9. Edit by hand to add or correct entries; sync on next review.

After every review, an extractor LLM pass distills durable conventions and a one-line MR summary, writes them to mem9 and the mirror.

The bot's final Discord reply has 👍 / 👎 buttons. Clicks record per-MR maintainer feedback; future reviews use down-rated signals to suppress repeat suggestion patterns.

All memory operations are best-effort — failures never block a review.

Set `MEM9_API_KEY` in env. See the `memory:` block in `config.example.yaml` for tuning.
```

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: add memory section to README"
```

- [ ] **Step 4: Final tag**

Optional: `git log --oneline -25` to review the commit chain. The series should look like a clean phased rollout: config → memory primitives → mem9 client → reporules → mirror → format → mem9 source → extractor → composite → orchestrator wiring → discord buttons → main wiring → integration tests → docs.

---

## Verification checklist

After all tasks complete:

- [ ] `go test ./... -count=1` green
- [ ] `go vet ./...` clean
- [ ] `go build ./...` clean
- [ ] `config.yaml` with `memory.enabled: false` runs identically to pre-change
- [ ] `config.yaml` with `memory.enabled: true` and a stub mem9 server: review completes, FileContext seen in provider request (verify with verbose log)
- [ ] Discord 👍 click writes a feedback record (verify with stub mem9 capturing POST)
- [ ] Mirror file appears at `~/.cache/gitlab-mr-bot/memory/<project>.md` after first review
- [ ] mem9 outage (point at non-routable URL) does not break review
