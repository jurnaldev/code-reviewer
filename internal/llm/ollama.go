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
