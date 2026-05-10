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

type AnthropicConfig struct {
	APIKey  string
	Model   string
	BaseURL string // e.g. https://api.anthropic.com
	HTTP    *http.Client
}

type Anthropic struct {
	cfg AnthropicConfig
}

func NewAnthropic(c AnthropicConfig) *Anthropic {
	if c.HTTP == nil {
		c.HTTP = http.DefaultClient
	}
	if c.BaseURL == "" {
		c.BaseURL = "https://api.anthropic.com"
	}
	return &Anthropic{cfg: c}
}

func (a *Anthropic) Name() string { return "anthropic" }

type anthropicReq struct {
	Model     string           `json:"model"`
	MaxTokens int              `json:"max_tokens"`
	System    []anthropicBlock `json:"system"`
	Messages  []anthropicMsg   `json:"messages"`
}

type anthropicBlock struct {
	Type         string            `json:"type"`
	Text         string            `json:"text"`
	CacheControl map[string]string `json:"cache_control,omitempty"`
}

type anthropicMsg struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicResp struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens          int `json:"input_tokens"`
		OutputTokens         int `json:"output_tokens"`
		CacheReadInputTokens int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

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

func (a *Anthropic) Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error) {
	user := fmt.Sprintf("File: %s\n\nDiff:\n%s", req.FilePath, req.DiffChunk)

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
	buf, err := json.Marshal(body)
	if err != nil {
		return ReviewResponse{}, err
	}

	base := strings.TrimSuffix(strings.TrimRight(a.cfg.BaseURL, "/"), "/v1")
	httpReq, err := http.NewRequestWithContext(ctx, "POST", base+"/v1/messages", bytes.NewReader(buf))
	if err != nil {
		return ReviewResponse{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", a.cfg.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.cfg.HTTP.Do(httpReq)
	if err != nil {
		return ReviewResponse{}, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return ReviewResponse{}, fmt.Errorf("anthropic %d: %s", resp.StatusCode, string(rb))
	}

	var ar anthropicResp
	if err := json.Unmarshal(rb, &ar); err != nil {
		return ReviewResponse{}, fmt.Errorf("decode response: %w", err)
	}
	var text string
	for _, c := range ar.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}
	findings, err := ParseFindings(text)
	if err != nil {
		return ReviewResponse{}, err
	}
	return ReviewResponse{
		Findings: findings,
		Usage: TokenUsage{
			InputTokens:      ar.Usage.InputTokens,
			OutputTokens:     ar.Usage.OutputTokens,
			CachedReadTokens: ar.Usage.CacheReadInputTokens,
		},
	}, nil
}
