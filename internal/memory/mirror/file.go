package mirror

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"

	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
)

// Entry is a single bullet in the mirror.
type Entry struct {
	Text     string
	MemoryID string // empty if not yet pushed to mem9
}

type Document struct {
	Project     string
	Conventions []Entry
	MRSummaries []Entry
	Feedback    []Entry
}

const (
	sectionConventions = "Conventions"
	sectionMRSummaries = "Recent MRs"
	sectionFeedback    = "Recent Feedback"
)

var stampRe = regexp.MustCompile(`<!--\s*mem9_id:\s*(\S+)\s*-->`)

func Parse(src string) (Document, error) {
	d := Document{}
	scanner := bufio.NewScanner(strings.NewReader(src))
	scanner.Buffer(make([]byte, 1<<16), 1<<20)
	current := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "# Memory:") {
			d.Project = strings.TrimSpace(strings.TrimPrefix(line, "# Memory:"))
			continue
		}
		if strings.HasPrefix(line, "## ") {
			current = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		body := strings.TrimPrefix(line, "- ")
		entry := Entry{}
		if m := stampRe.FindStringSubmatch(body); m != nil {
			entry.MemoryID = m[1]
			body = strings.TrimSpace(stampRe.ReplaceAllString(body, ""))
			body = strings.TrimSuffix(body, ".")
			body = strings.TrimSpace(body)
		}
		entry.Text = body
		switch current {
		case sectionConventions:
			d.Conventions = append(d.Conventions, entry)
		case sectionMRSummaries:
			d.MRSummaries = append(d.MRSummaries, entry)
		case sectionFeedback:
			d.Feedback = append(d.Feedback, entry)
		}
	}
	return d, scanner.Err()
}

func Render(d Document) string {
	var b strings.Builder
	if d.Project != "" {
		fmt.Fprintf(&b, "# Memory: %s\n\n", d.Project)
	}
	if len(d.Conventions) > 0 {
		b.WriteString("## " + sectionConventions + "\n")
		for _, e := range d.Conventions {
			writeEntry(&b, e)
		}
		b.WriteString("\n")
	}
	if len(d.MRSummaries) > 0 {
		b.WriteString("## " + sectionMRSummaries + "\n")
		for _, e := range d.MRSummaries {
			writeEntry(&b, e)
		}
		b.WriteString("\n")
	}
	if len(d.Feedback) > 0 {
		b.WriteString("## " + sectionFeedback + "\n")
		for _, e := range d.Feedback {
			writeEntry(&b, e)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func writeEntry(b *strings.Builder, e Entry) {
	if e.MemoryID != "" {
		fmt.Fprintf(b, "- %s. <!-- mem9_id: %s -->\n", strings.TrimSuffix(e.Text, "."), e.MemoryID)
	} else {
		fmt.Fprintf(b, "- %s\n", e.Text)
	}
}

func SlugForProject(p string) string {
	return strings.ReplaceAll(p, "/", "_")
}

func EntriesToMemories(es []Entry, k memory.Kind, project string) []memory.Memory {
	out := make([]memory.Memory, 0, len(es))
	for _, e := range es {
		out = append(out, memory.Memory{
			ID:      e.MemoryID,
			Kind:    k,
			Content: e.Text,
			Project: project,
		})
	}
	return out
}
