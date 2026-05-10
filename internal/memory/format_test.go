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
