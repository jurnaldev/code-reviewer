package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

type OpenAIConfig struct {
	APIKey  string
	Model   string
	BaseURL string // e.g. https://api.openai.com
	HTTP    *http.Client
}

type OpenAI struct {
	cfg        OpenAIConfig
	noJSONMode atomic.Bool
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
	ResponseFormat *openaiRF       `json:"response_format,omitempty"`
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
	msgs := []openaiMessage{
		{Role: "system", Content: req.SystemPrompt},
		{Role: "user", Content: user},
	}

	rb, status, err := o.post(ctx, msgs, !o.noJSONMode.Load())
	if err != nil {
		return ReviewResponse{}, err
	}
	if status == 400 && isJSONModeUnsupported(rb) && !o.noJSONMode.Load() {
		o.noJSONMode.Store(true)
		rb, status, err = o.post(ctx, msgs, false)
		if err != nil {
			return ReviewResponse{}, err
		}
	}
	if status/100 != 2 {
		return ReviewResponse{}, fmt.Errorf("openai %d: %s", status, string(rb))
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

func (o *OpenAI) post(ctx context.Context, msgs []openaiMessage, jsonMode bool) ([]byte, int, error) {
	body := openaiReq{Model: o.cfg.Model, Messages: msgs}
	if jsonMode {
		body.ResponseFormat = &openaiRF{Type: "json_object"}
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	base := strings.TrimSuffix(strings.TrimRight(o.cfg.BaseURL, "/"), "/v1")
	httpReq, err := http.NewRequestWithContext(ctx, "POST", base+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)

	resp, err := o.cfg.HTTP.Do(httpReq)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	return rb, resp.StatusCode, nil
}

func isJSONModeUnsupported(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "json mode is not supported") ||
		strings.Contains(s, "response_format") && strings.Contains(s, "not supported") ||
		strings.Contains(s, `"code":20024`)
}
