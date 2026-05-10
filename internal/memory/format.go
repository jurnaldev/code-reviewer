package memory

import "strings"

// estimateTokens is a 4-bytes-per-token approximation; matches what
// internal/chunker uses so budgets are consistent.
func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}

type formatSection struct {
	title   string
	entries []Memory
	bullet  bool
}

// Format renders memories into a single markdown block, dropping lowest-priority
// sections first when the token budget would be exceeded.
//
// Priority order (highest first): Rule, Convention, MRSummary.
// Returns (text, truncated).
func Format(mems []Memory, tokenBudget int) (string, bool) {
	if len(mems) == 0 {
		return "", false
	}
	rules := filterKind(mems, KindRule)
	convs := filterKind(mems, KindConvention)
	summaries := filterKind(mems, KindMRSummary)

	sections := []formatSection{
		{title: "Project Rules", entries: rules, bullet: false},
		{title: "Conventions", entries: convs, bullet: true},
		{title: "Recent MRs", entries: summaries, bullet: true},
	}

	// Greedy: try full → drop last → drop last → ...
	for keep := len(sections); keep > 0; keep-- {
		buf := renderSections(sections[:keep])
		if estimateTokens(buf) <= tokenBudget {
			return buf, keep < len(sections)
		}
	}
	// Even rules alone too big — truncate rules content.
	if len(rules) > 0 {
		max := tokenBudget * 4
		if max < 200 {
			max = 200
		}
		r := rules[0]
		if len(r.Content) > max {
			r.Content = r.Content[:max] + " _(truncated)_"
		}
		buf := renderSections([]formatSection{{title: "Project Rules", entries: []Memory{r}, bullet: false}})
		return buf, true
	}
	return "", true
}

func renderSections(sections []formatSection) string {
	var b strings.Builder
	b.WriteString("# Project Memory\n\n")
	for _, s := range sections {
		if len(s.entries) == 0 {
			continue
		}
		b.WriteString("## ")
		b.WriteString(s.title)
		b.WriteString("\n")
		for _, m := range s.entries {
			if s.bullet {
				b.WriteString("- ")
				b.WriteString(m.Content)
				b.WriteString("\n")
			} else {
				b.WriteString(m.Content)
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func filterKind(mems []Memory, k Kind) []Memory {
	var out []Memory
	for _, m := range mems {
		if m.Kind == k {
			out = append(out, m)
		}
	}
	return out
}
