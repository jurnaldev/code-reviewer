package llm

import "context"

type Provider interface {
	Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error)
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

const SystemPromptDefault = `You are a senior code reviewer. Review the supplied unified diff for:
- bugs and logic errors
- security vulnerabilities
- performance problems
- missing test coverage for new logic
- style violations only when egregious

Return ONLY a JSON object with this shape:
{"findings":[{"severity":"critical|major|minor|nit","category":"bug|security|perf|test|style","file":"path/from/diff","line":<int new-file line>,"message":"...","suggestion":"optional code"}]}

Use the new-file line number from the @@ -A,B +C,D @@ header. Omit a finding if you are not confident. Do not output prose outside the JSON.`
