package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type OpenRouterConfig struct {
	APIKey  string
	Model   string
	BaseURL string // default https://openrouter.ai/api
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

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.cfg.BaseURL+"/v1/chat/completions", bytes.NewReader(buf))
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

	var or openrouterResp
	if err := json.Unmarshal(rb, &or); err != nil {
		return ReviewResponse{}, fmt.Errorf("decode response: %w", err)
	}
	if len(or.Choices) == 0 {
		return ReviewResponse{}, fmt.Errorf("openrouter: empty choices")
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
