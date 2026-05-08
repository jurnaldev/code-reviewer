package review

import (
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
	"github.com/stretchr/testify/require"
)

func TestAggregate_DedupesByFileLineMessage(t *testing.T) {
	in := []llm.Finding{
		{Severity: "minor", Category: "style", File: "a.go", Line: 10, Message: "naming"},
		{Severity: "minor", Category: "style", File: "a.go", Line: 10, Message: "naming"}, // dup
		{Severity: "major", Category: "bug", File: "a.go", Line: 11, Message: "nil"},
	}
	r := Aggregate(in)
	require.Len(t, r.Findings, 2)
	// major sorted before minor
	require.Equal(t, "major", r.Findings[0].Severity)
	require.Equal(t, 1, r.Counts["major"])
	require.Equal(t, 1, r.Counts["minor"])
}

func TestAggregate_BuildsSummary(t *testing.T) {
	in := []llm.Finding{
		{Severity: "critical", Category: "security", File: "x.go", Line: 1, Message: "sqli"},
	}
	r := Aggregate(in)
	require.Contains(t, r.SummaryBody, "AI Code Review")
	require.Contains(t, r.SummaryBody, "critical")
	require.Contains(t, r.SummaryBody, "x.go:1")
	require.Contains(t, r.SummaryBody, "sqli")
}

func TestAggregate_EmptySummary(t *testing.T) {
	r := Aggregate(nil)
	require.Empty(t, r.Findings)
	require.Contains(t, r.SummaryBody, "no findings")
}
