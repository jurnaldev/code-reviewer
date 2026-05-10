package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fahmi/gitlab-mr-review-bot/internal/memory/mem9"
)

// Mem9API is the slice of mem9.Client the source uses. Lets tests stub it.
type Mem9API interface {
	Search(ctx context.Context, in mem9.SearchInput) ([]mem9.Hit, error)
	Create(ctx context.Context, in mem9.CreateInput) (string, error)
	Update(ctx context.Context, id string, in mem9.CreateInput) error
	Delete(ctx context.Context, id string) error
}

type Mem9Tuning struct {
	ConventionsTopK int
	SummariesTopK   int
}

type Mem9Source struct {
	api    Mem9API
	tuning Mem9Tuning
}

func NewMem9Source(api Mem9API, t Mem9Tuning) *Mem9Source {
	if t.ConventionsTopK == 0 {
		t.ConventionsTopK = 20
	}
	if t.SummariesTopK == 0 {
		t.SummariesTopK = 5
	}
	return &Mem9Source{api: api, tuning: t}
}

// Recall queries mem9 for conventions + recent MR summaries for a project.
func (s *Mem9Source) Recall(ctx context.Context, mr MRRef) ([]Memory, error) {
	q := buildQuery(mr)
	convs, err := s.api.Search(ctx, mem9.SearchInput{
		Query: q,
		Tags:  []string{"project:" + mr.Project, "type:" + string(KindConvention)},
		Mode:  "hybrid",
		Limit: s.tuning.ConventionsTopK,
	})
	if err != nil {
		return nil, err
	}
	sums, err := s.api.Search(ctx, mem9.SearchInput{
		Query: q,
		Tags:  []string{"project:" + mr.Project, "type:mr_summary"},
		Mode:  "hybrid",
		Limit: s.tuning.SummariesTopK,
	})
	if err != nil {
		return hitsToMemories(convs, KindConvention, mr.Project), err
	}
	out := hitsToMemories(convs, KindConvention, mr.Project)
	out = append(out, hitsToMemories(sums, KindMRSummary, mr.Project)...)
	return out, nil
}

// FetchFeedback returns recent down-rated feedback for the extractor.
func (s *Mem9Source) FetchFeedback(ctx context.Context, project string, limit int) ([]Memory, error) {
	hits, err := s.api.Search(ctx, mem9.SearchInput{
		Tags:  []string{"project:" + project, "type:feedback", "rating:down"},
		Mode:  "keyword",
		Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	return hitsToMemories(hits, KindFeedback, project), nil
}

// Create writes one memory to mem9 with appropriate tags.
func (s *Mem9Source) Create(ctx context.Context, content string, kind Kind, project string) (string, error) {
	tags := []string{"project:" + project, "type:" + kindTag(kind)}
	meta := map[string]string{"derived_at": time.Now().UTC().Format(time.RFC3339)}
	return s.api.Create(ctx, mem9.CreateInput{
		Content:  content,
		Tags:     tags,
		Metadata: meta,
	})
}

// Update edits an existing mem9 memory's content.
func (s *Mem9Source) Update(ctx context.Context, id, content string) error {
	return s.api.Update(ctx, id, mem9.CreateInput{Content: content})
}

// CreateFeedback records per-MR thumbs.
func (s *Mem9Source) CreateFeedback(ctx context.Context, mr MRRef, rating FeedbackRating, ratedBy string) (string, error) {
	tags := []string{"project:" + mr.Project, "type:feedback", "rating:" + string(rating)}
	meta := map[string]string{
		"mr_iid":   fmt.Sprintf("%d", mr.IID),
		"rated_by": ratedBy,
		"at":       time.Now().UTC().Format(time.RFC3339),
	}
	content := fmt.Sprintf("MR !%d review rated %s by %s", mr.IID, rating, ratedBy)
	return s.api.Create(ctx, mem9.CreateInput{
		Content:  content,
		Tags:     tags,
		Metadata: meta,
	})
}

func buildQuery(mr MRRef) string {
	parts := []string{mr.Title}
	parts = append(parts, mr.Files...)
	q := strings.Join(parts, " ")
	if len(q) > 200 {
		q = q[:200]
	}
	return q
}

func hitsToMemories(hits []mem9.Hit, k Kind, project string) []Memory {
	out := make([]Memory, 0, len(hits))
	for _, h := range hits {
		out = append(out, Memory{
			ID:      h.ID,
			Kind:    k,
			Content: h.Content,
			Project: project,
			Score:   h.Score,
		})
	}
	return out
}

func kindTag(k Kind) string {
	switch k {
	case KindMRSummary:
		return "mr_summary"
	default:
		return string(k)
	}
}
