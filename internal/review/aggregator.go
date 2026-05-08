package review

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
)

type AggregateResult struct {
	Findings    []llm.Finding
	Counts      map[string]int
	SummaryBody string
}

var sevRank = map[string]int{"critical": 0, "major": 1, "minor": 2, "nit": 3}

func Aggregate(fs []llm.Finding) AggregateResult {
	dedup := map[string]llm.Finding{}
	for _, f := range fs {
		k := fmt.Sprintf("%s|%d|%s", f.File, f.Line, f.Message)
		if _, ok := dedup[k]; !ok {
			dedup[k] = f
		}
	}
	out := make([]llm.Finding, 0, len(dedup))
	for _, f := range dedup {
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := sevRank[out[i].Severity], sevRank[out[j].Severity]
		if ri != rj {
			return ri < rj
		}
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	counts := map[string]int{}
	for _, f := range out {
		counts[f.Severity]++
	}
	return AggregateResult{
		Findings:    out,
		Counts:      counts,
		SummaryBody: buildSummary(out, counts),
	}
}

func buildSummary(fs []llm.Finding, counts map[string]int) string {
	var b strings.Builder
	b.WriteString("## AI Code Review\n\n")
	if len(fs) == 0 {
		b.WriteString("_no findings_\n")
		return b.String()
	}
	b.WriteString(fmt.Sprintf("**Findings:** critical=%d major=%d minor=%d nit=%d\n\n",
		counts["critical"], counts["major"], counts["minor"], counts["nit"]))
	for _, f := range fs {
		fmt.Fprintf(&b, "- **[%s/%s] %s:%d** — %s\n", f.Severity, f.Category, f.File, f.Line, f.Message)
	}
	b.WriteString("\n_Inline comments posted on diff lines where possible._\n")
	return b.String()
}
