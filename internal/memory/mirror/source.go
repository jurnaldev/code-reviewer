package mirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
)

// Mem9Writer is the minimal slice of mem9.Client the mirror needs for sync.
type Mem9Writer interface {
	Create(ctx context.Context, content string, kind memory.Kind, project string) (string, error)
	Update(ctx context.Context, id, content string) error
}

type Source struct {
	dir  string
	mem9 Mem9Writer
}

func NewSource(dir string, mem9 Mem9Writer) *Source {
	return &Source{dir: ExpandHome(dir), mem9: mem9}
}

func ExpandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~/"))
		}
	}
	return p
}

func (s *Source) filePath(project string) string {
	return filepath.Join(s.dir, SlugForProject(project)+".md")
}

func (s *Source) read(project string) (Document, error) {
	body, err := os.ReadFile(s.filePath(project))
	if errors.Is(err, os.ErrNotExist) {
		return Document{Project: project}, nil
	}
	if err != nil {
		return Document{}, err
	}
	return Parse(string(body))
}

func (s *Source) write(d Document) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.filePath(d.Project), []byte(Render(d)), 0o644)
}

// Recall returns conventions + summaries from the mirror file (feedback excluded
// from recall — feedback is a write-only signal in the mirror).
func (s *Source) Recall(ctx context.Context, mr memory.MRRef) ([]memory.Memory, error) {
	d, err := s.read(mr.Project)
	if err != nil {
		return nil, err
	}
	if d.Project == "" {
		d.Project = mr.Project
	}
	out := EntriesToMemories(d.Conventions, memory.KindConvention, mr.Project)
	out = append(out, EntriesToMemories(d.MRSummaries, memory.KindMRSummary, mr.Project)...)
	return out, nil
}

// Sync reconciles the local file with the supplied remote view (mem9 ID → content)
// per kind. Pushes new/edited locals; appends remote-only entries.
func (s *Source) Sync(ctx context.Context, mr memory.MRRef, remote map[memory.Kind]map[string]string) error {
	d, err := s.read(mr.Project)
	if err != nil {
		return err
	}
	if d.Project == "" {
		d.Project = mr.Project
	}

	convPlan := Diff(d.Conventions, remote[memory.KindConvention])
	d.Conventions = applyPlan(ctx, d.Conventions, convPlan, s.mem9, memory.KindConvention, mr.Project)

	sumPlan := Diff(d.MRSummaries, remote[memory.KindMRSummary])
	d.MRSummaries = applyPlan(ctx, d.MRSummaries, sumPlan, s.mem9, memory.KindMRSummary, mr.Project)

	return s.write(d)
}

func applyPlan(ctx context.Context, current []Entry, plan Plan, mw Mem9Writer, k memory.Kind, project string) []Entry {
	for i := range current {
		if current[i].MemoryID != "" {
			continue
		}
		id, err := mw.Create(ctx, current[i].Text, k, project)
		if err == nil && id != "" {
			current[i].MemoryID = id
		}
	}
	for _, e := range plan.ToPut {
		_ = mw.Update(ctx, e.MemoryID, e.Text)
	}
	current = append(current, plan.ToAppend...)
	return current
}

// AppendConvention adds one already-stamped convention to the mirror file.
func (s *Source) AppendConvention(ctx context.Context, mr memory.MRRef, text, memID string) error {
	d, err := s.read(mr.Project)
	if err != nil {
		return err
	}
	if d.Project == "" {
		d.Project = mr.Project
	}
	d.Conventions = append(d.Conventions, Entry{Text: text, MemoryID: memID})
	return s.write(d)
}

// AppendMRSummary adds one already-stamped MR summary.
func (s *Source) AppendMRSummary(ctx context.Context, mr memory.MRRef, text, memID string) error {
	d, err := s.read(mr.Project)
	if err != nil {
		return err
	}
	if d.Project == "" {
		d.Project = mr.Project
	}
	d.MRSummaries = append(d.MRSummaries, Entry{Text: text, MemoryID: memID})
	return s.write(d)
}

// AppendFeedback appends a feedback line; no two-way sync on feedback.
func (s *Source) AppendFeedback(ctx context.Context, mr memory.MRRef, rating memory.FeedbackRating, ratedBy string) error {
	d, err := s.read(mr.Project)
	if err != nil {
		return err
	}
	if d.Project == "" {
		d.Project = mr.Project
	}
	line := fmt.Sprintf("!%d rated %s by %s on %s", mr.IID, rating, ratedBy, time.Now().UTC().Format("2006-01-02"))
	d.Feedback = append(d.Feedback, Entry{Text: line})
	return s.write(d)
}
