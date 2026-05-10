package memory

import (
	"context"
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/memory/mem9"
)

type stubMem9Search struct {
	hits   []mem9.Hit
	create func(in mem9.CreateInput) (string, error)
	update func(id string, in mem9.CreateInput) error
}

func (s *stubMem9Search) Search(ctx context.Context, in mem9.SearchInput) ([]mem9.Hit, error) {
	return s.hits, nil
}
func (s *stubMem9Search) Create(ctx context.Context, in mem9.CreateInput) (string, error) {
	if s.create != nil {
		return s.create(in)
	}
	return "m_new", nil
}
func (s *stubMem9Search) Update(ctx context.Context, id string, in mem9.CreateInput) error {
	if s.update != nil {
		return s.update(id, in)
	}
	return nil
}
func (s *stubMem9Search) Delete(ctx context.Context, id string) error { return nil }

func TestMem9Source_Recall_TagsConventionsAndSummaries(t *testing.T) {
	stub := &stubMem9Search{
		hits: []mem9.Hit{{ID: "m1", Content: "rule one", Score: 0.9}},
	}
	src := NewMem9Source(stub, Mem9Tuning{ConventionsTopK: 5, SummariesTopK: 3})
	mems, err := src.Recall(context.Background(), MRRef{Project: "g/r", Title: "fix bug", Files: []string{"a.go"}})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	// Two queries with tag filter; stub returns same hits both times.
	if len(mems) != 2 {
		t.Fatalf("expected 2 mems (1 conv + 1 sum), got %d", len(mems))
	}
}

func TestMem9Source_WriteConvention_PassesTags(t *testing.T) {
	var captured mem9.CreateInput
	stub := &stubMem9Search{
		create: func(in mem9.CreateInput) (string, error) {
			captured = in
			return "m_x", nil
		},
	}
	src := NewMem9Source(stub, Mem9Tuning{})
	id, err := src.Create(context.Background(), "rule x", KindConvention, "g/r")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != "m_x" {
		t.Fatalf("id %q", id)
	}
	if !contains(captured.Tags, "project:g/r") {
		t.Fatalf("missing project tag: %v", captured.Tags)
	}
	if !contains(captured.Tags, "type:convention") {
		t.Fatalf("missing type tag: %v", captured.Tags)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
