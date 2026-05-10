package chunker

import (
	"strings"
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/diff"
	"github.com/stretchr/testify/require"
)

func mkHunk(lineCount int) diff.Hunk {
	h := diff.Hunk{NewStart: 1, NewLines: lineCount}
	for i := 0; i < lineCount; i++ {
		h.Lines = append(h.Lines, diff.DiffLine{Kind: '+', Content: strings.Repeat("x", 80), NewLineNo: i + 1})
	}
	return h
}

func TestChunk_FitsInOne(t *testing.T) {
	fd := diff.FileDiff{NewPath: "a.go", Hunks: []diff.Hunk{mkHunk(5)}}
	chunks := Chunk(fd, 10000)
	require.Len(t, chunks, 1)
	require.Equal(t, "a.go", chunks[0].FilePath)
	require.False(t, chunks[0].Truncated)
	require.NotEmpty(t, chunks[0].DiffText)
}

func TestChunk_SplitsByHunkGroup(t *testing.T) {
	// Each hunk ~ (80+2)*40 = 3280 chars ≈ 820 tokens at 4 chars/token
	fd := diff.FileDiff{NewPath: "a.go", Hunks: []diff.Hunk{
		mkHunk(40), mkHunk(40), mkHunk(40), mkHunk(40),
	}}
	chunks := Chunk(fd, 1000) // ~1000 tokens budget
	require.GreaterOrEqual(t, len(chunks), 2)
	for _, c := range chunks {
		require.LessOrEqual(t, EstimateTokens(c.DiffText), 1000+200) // small overhead allowed
	}
}

func TestChunk_OversizedHunkTruncated(t *testing.T) {
	fd := diff.FileDiff{NewPath: "big.go", Hunks: []diff.Hunk{mkHunk(2000)}}
	chunks := Chunk(fd, 500)
	require.Len(t, chunks, 1)
	require.True(t, chunks[0].Truncated)
}

func TestChunk_EmptyFile(t *testing.T) {
	fd := diff.FileDiff{NewPath: "empty.go"}
	chunks := Chunk(fd, 1000)
	require.Empty(t, chunks)
}

func TestEstimateTokens(t *testing.T) {
	require.Equal(t, 1, EstimateTokens(""))
	require.Equal(t, 1, EstimateTokens("x"))
	require.Equal(t, 2, EstimateTokens("xxxx"))
}
