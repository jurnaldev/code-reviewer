package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type OpenRouterConfig struct {
	APIKey  string
	Model   string
	BaseURL string // default https://openrouter.ai/api ; trailing /v1 tolerated
	HTTP    *http.Client
	Referer string // optional HTTP-Referer header (OpenRouter app ranking)
	Title   string // optional X-Title header
}

type OpenRouter struct {
	cfg OpenRouterConfig
}

func NewOpenRouter(c OpenRouterConfig) *OpenRouter {
	if c.HTTP == nil {
		c.HTTP = http.DefaultClient
	}
	if c.BaseURL == "" {
		c.BaseURL = "https://openrouter.ai/api"
	}
	return &OpenRouter{cfg: c}
}

func (o *OpenRouter) Name() string { return "openrouter" }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type openrouterMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openrouterReq struct {
	Model    string              `json:"model"`
	Messages []openrouterMessage `json:"messages"`
}

type openrouterResp struct {
	Choices []struct {
		Message openrouterMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (o *OpenRouter) Generate(ctx context.Context, system, user string) (string, TokenUsage, error) {
	body := openrouterReq{
		Model: o.cfg.Model,
		Messages: []openrouterMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return "", TokenUsage{}, err
	}
	base := strings.TrimSuffix(strings.TrimRight(o.cfg.BaseURL, "/"), "/v1")
	httpReq, err := http.NewRequestWithContext(ctx, "POST", base+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return "", TokenUsage{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	if o.cfg.Referer != "" {
		httpReq.Header.Set("HTTP-Referer", o.cfg.Referer)
	}
	if o.cfg.Title != "" {
		httpReq.Header.Set("X-Title", o.cfg.Title)
	}
	resp, err := o.cfg.HTTP.Do(httpReq)
	if err != nil {
		return "", TokenUsage{}, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", TokenUsage{}, fmt.Errorf("openrouter %d: %s", resp.StatusCode, string(rb))
	}
	var or openrouterResp
	if err := json.Unmarshal(rb, &or); err != nil {
		return "", TokenUsage{}, fmt.Errorf("decode response (body=%q): %w", truncate(string(rb), 300), err)
	}
	var text string
	if len(or.Choices) > 0 {
		text = or.Choices[0].Message.Content
	}
	return text, TokenUsage{InputTokens: or.Usage.PromptTokens, OutputTokens: or.Usage.CompletionTokens}, nil
}

func (o *OpenRouter) Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error) {
	user := fmt.Sprintf("File: %s\n\nDiff:\n%s", req.FilePath, req.DiffChunk)

	body := openrouterReq{
		Model: o.cfg.Model,
		Messages: []openrouterMessage{
			{Role: "system", Content: req.SystemPrompt},
			{Role: "user", Content: user},
		},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return ReviewResponse{}, err
	}

	base := strings.TrimSuffix(strings.TrimRight(o.cfg.BaseURL, "/"), "/v1")
	httpReq, err := http.NewRequestWithContext(ctx, "POST", base+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return ReviewResponse{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	if o.cfg.Referer != "" {
		httpReq.Header.Set("HTTP-Referer", o.cfg.Referer)
	}
	if o.cfg.Title != "" {
		httpReq.Header.Set("X-Title", o.cfg.Title)
	}

	resp, err := o.cfg.HTTP.Do(httpReq)
	if err != nil {
		return ReviewResponse{}, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return ReviewResponse{}, fmt.Errorf("openrouter %d: %s", resp.StatusCode, string(rb))
	}

	if len(bytes.TrimSpace(rb)) == 0 {
		return ReviewResponse{}, fmt.Errorf("openrouter: empty body (status %d, model=%s)", resp.StatusCode, o.cfg.Model)
	}
	var or openrouterResp
	if err := json.Unmarshal(rb, &or); err != nil {
		return ReviewResponse{}, fmt.Errorf("decode response (body=%q): %w", truncate(string(rb), 300), err)
	}
	if len(or.Choices) == 0 {
		return ReviewResponse{}, fmt.Errorf("openrouter: empty choices (body=%q)", truncate(string(rb), 300))
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
