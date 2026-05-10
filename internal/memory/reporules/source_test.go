package reporules

import (
	"context"
	"errors"
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
)

type stubGitLab struct {
	body string
	err  error
}

func (s stubGitLab) GetFileRaw(ctx context.Context, project, path, ref string) (string, error) {
	return s.body, s.err
}

func TestSource_Recall_ReturnsRule(t *testing.T) {
	src := New(stubGitLab{body: "Always use Postgres for IDs."}, ".review/rules.md")
	mems, err := src.Recall(context.Background(), memory.MRRef{Project: "g/r", TargetRef: "main"})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(mems) != 1 {
		t.Fatalf("got %d", len(mems))
	}
	if mems[0].Kind != memory.KindRule {
		t.Fatalf("kind %v", mems[0].Kind)
	}
	if mems[0].Content != "Always use Postgres for IDs." {
		t.Fatalf("content %q", mems[0].Content)
	}
}

func TestSource_Recall_404Silent(t *testing.T) {
	src := New(stubGitLab{err: gitlab.ErrFileNotFound}, ".review/rules.md")
	mems, err := src.Recall(context.Background(), memory.MRRef{Project: "g/r", TargetRef: "main"})
	if err != nil {
		t.Fatalf("expected nil err on 404, got %v", err)
	}
	if len(mems) != 0 {
		t.Fatalf("expected zero memories")
	}
}

func TestSource_Recall_OtherErrLogged(t *testing.T) {
	src := New(stubGitLab{err: errors.New("boom")}, ".review/rules.md")
	mems, err := src.Recall(context.Background(), memory.MRRef{Project: "g/r", TargetRef: "main"})
	if err == nil {
		t.Fatalf("expected err propagation for non-404")
	}
	if len(mems) != 0 {
		t.Fatalf("expected zero memories on err")
	}
}

func TestSource_Recall_TruncatesOver4KB(t *testing.T) {
	big := make([]byte, 5000)
	for i := range big {
		big[i] = 'a'
	}
	src := New(stubGitLab{body: string(big)}, ".review/rules.md")
	mems, _ := src.Recall(context.Background(), memory.MRRef{Project: "g/r"})
	if len(mems) != 1 {
		t.Fatalf("got %d", len(mems))
	}
	if len(mems[0].Content) > 4096+len(" _(truncated)_") {
		t.Fatalf("not truncated, len=%d", len(mems[0].Content))
	}
}
