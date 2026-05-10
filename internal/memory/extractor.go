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
