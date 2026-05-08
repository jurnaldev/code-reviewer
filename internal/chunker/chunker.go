package chunker

import (
	"fmt"
	"strings"

	"github.com/fahmi/gitlab-mr-review-bot/internal/diff"
)

type FileChunk struct {
	FilePath  string
	DiffText  string
	Truncated bool
}

// EstimateTokens uses a 4-chars-per-token heuristic. Always >= 1.
func EstimateTokens(s string) int {
	return len(s)/4 + 1
}

func Chunk(fd diff.FileDiff, maxTokens int) []FileChunk {
	if len(fd.Hunks) == 0 {
		return nil
	}
	var out []FileChunk
	var buf strings.Builder
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		out = append(out, FileChunk{FilePath: fd.NewPath, DiffText: buf.String()})
		buf.Reset()
	}

	header := fmt.Sprintf("--- %s\n+++ %s\n", fd.OldPath, fd.NewPath)
	buf.WriteString(header)

	for _, h := range fd.Hunks {
		hs := renderHunk(h)
		if EstimateTokens(hs) > maxTokens {
			// oversized single hunk: truncate, single chunk
			truncated := truncateString(hs, maxTokens*4)
			buf.WriteString(truncated)
			flushedAsTruncated := FileChunk{FilePath: fd.NewPath, DiffText: buf.String(), Truncated: true}
			buf.Reset()
			return []FileChunk{flushedAsTruncated}
		}
		// Would adding this hunk exceed budget?
		if EstimateTokens(buf.String()+hs) > maxTokens {
			flush()
			buf.WriteString(header)
		}
		buf.WriteString(hs)
	}
	flush()
	return out
}

func renderHunk(h diff.Hunk) string {
	var b strings.Builder
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
	for _, l := range h.Lines {
		b.WriteRune(l.Kind)
		b.WriteString(l.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... [truncated]\n"
}
