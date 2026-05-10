package mirror

import (
	"strings"
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
)

const sample = `# Memory: group/repo

## Conventions
- Prefer errors.Is over == for sentinel errors. <!-- mem9_id: m_abc -->
- Wrap external HTTP behind retry helper. <!-- mem9_id: m_def -->
- Pending push entry, no stamp.

## Recent MRs
- !123 "fix nil deref" — summary text. <!-- mem9_id: m_ghi -->

## Recent Feedback
- !123 rated 👎 by @alice on 2026-05-10
`

func TestParse_ExtractsConventionsWithStamps(t *testing.T) {
	doc, err := Parse(sample)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Conventions) != 3 {
		t.Fatalf("conventions %d", len(doc.Conventions))
	}
	if doc.Conventions[0].MemoryID != "m_abc" {
		t.Fatalf("first id %q", doc.Conventions[0].MemoryID)
	}
	if !strings.HasPrefix(doc.Conventions[0].Text, "Prefer errors.Is") {
		t.Fatalf("first text %q", doc.Conventions[0].Text)
	}
	if doc.Conventions[2].MemoryID != "" {
		t.Fatalf("third should be unstamped, got %q", doc.Conventions[2].MemoryID)
	}
	if len(doc.MRSummaries) != 1 {
		t.Fatalf("summaries %d", len(doc.MRSummaries))
	}
	if len(doc.Feedback) != 1 {
		t.Fatalf("feedback %d", len(doc.Feedback))
	}
}

func TestRender_RoundTrip(t *testing.T) {
	doc := Document{
		Project: "group/repo",
		Conventions: []Entry{
			{Text: "rule one", MemoryID: "m1"},
			{Text: "rule two"},
		},
		MRSummaries: []Entry{
			{Text: `!7 "title" — summary`, MemoryID: "m_s7"},
		},
		Feedback: []Entry{
			{Text: "!9 rated 👎 by @bob on 2026-05-10"},
		},
	}
	rendered := Render(doc)
	parsed, err := Parse(rendered)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(parsed.Conventions) != 2 || parsed.Conventions[0].MemoryID != "m1" {
		t.Fatalf("conventions roundtrip mismatch: %+v", parsed.Conventions)
	}
	if len(parsed.MRSummaries) != 1 || parsed.MRSummaries[0].MemoryID != "m_s7" {
		t.Fatalf("summaries roundtrip mismatch: %+v", parsed.MRSummaries)
	}
	if len(parsed.Feedback) != 1 {
		t.Fatalf("feedback roundtrip mismatch: %+v", parsed.Feedback)
	}
}

func TestParse_EmptyDocReturnsZero(t *testing.T) {
	doc, err := Parse("")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Conventions) != 0 || len(doc.MRSummaries) != 0 {
		t.Fatalf("expected zero, got %+v", doc)
	}
}

func TestSlugForProject(t *testing.T) {
	if got := SlugForProject("group/repo"); got != "group_repo" {
		t.Fatalf("got %q", got)
	}
	if got := SlugForProject("nested/group/repo-x"); got != "nested_group_repo-x" {
		t.Fatalf("got %q", got)
	}
}

func TestEntryToMemoryAndBack(t *testing.T) {
	mems := EntriesToMemories([]Entry{{Text: "x", MemoryID: "m1"}}, memory.KindConvention, "g/r")
	if len(mems) != 1 || mems[0].ID != "m1" || mems[0].Kind != memory.KindConvention {
		t.Fatalf("got %+v", mems)
	}
}
