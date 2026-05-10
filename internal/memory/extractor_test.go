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
