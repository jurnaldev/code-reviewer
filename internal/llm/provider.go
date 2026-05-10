package llm

import (
	"context"
	_ "embed"
)

//go:embed system_prompt.md
var SystemPromptDefault string

type Provider interface {
	Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error)
	Generate(ctx context.Context, system, user string) (string, TokenUsage, error)
	Name() string
}

type ReviewRequest struct {
	SystemPrompt string
	FilePath     string
	DiffChunk    string
	FileContext  string // reserved for future use
}

type ReviewResponse struct {
	Findings []Finding
	Usage    TokenUsage
}

type Finding struct {
	Severity   string `json:"severity"`
	Category   string `json:"category"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

type TokenUsage struct {
	InputTokens      int
	OutputTokens     int
	CachedReadTokens int
}

