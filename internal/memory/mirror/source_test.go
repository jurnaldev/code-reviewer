package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
)

type stubMem9 struct {
	created []string
	updated []string
	posts   []string
}

func (s *stubMem9) Create(ctx context.Context, content string, kind memory.Kind, project string) (string, error) {
	s.created = append(s.created, content)
	s.posts = append(s.posts, content)
	return "m_new_" + content[:1], nil
}

func (s *stubMem9) Update(ctx context.Context, id, content string) error {
	s.updated = append(s.updated, id+":"+content)
	return nil
}

func TestSource_Recall_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "g_r.md")
	os.WriteFile(f, []byte(sample), 0o600)
	src := NewSource(dir, &stubMem9{})
	mems, err := src.Recall(context.Background(), memory.MRRef{Project: "g/r"})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	// Conventions (3) + MRSummaries (1) — feedback excluded from recall
	if len(mems) != 4 {
		t.Fatalf("got %d", len(mems))
	}
}

func TestSource_Sync_PostsUnstamped(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "g_r.md")
	os.WriteFile(f, []byte(`# Memory: g/r

## Conventions
- Pending convention.
`), 0o600)
	stub := &stubMem9{}
	src := NewSource(dir, stub)
	if err := src.Sync(context.Background(), memory.MRRef{Project: "g/r"}, map[memory.Kind]map[string]string{
		memory.KindConvention: {},
	}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(stub.created) != 1 {
		t.Fatalf("expected 1 create, got %d", len(stub.created))
	}
	// File should now contain stamp
	got, _ := os.ReadFile(f)
	if !strings.Contains(string(got), "<!-- mem9_id: m_new_P -->") {
		t.Fatalf("file missing stamp: %s", got)
	}
}

func TestSource_AppendConvention_CreatesDirIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "memory")
	src := NewSource(dir, &stubMem9{})
	if err := src.AppendConvention(context.Background(), memory.MRRef{Project: "g/r"}, "rule x", "m_x"); err != nil {
		t.Fatalf("AppendConvention: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "g_r.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(body), "rule x") || !strings.Contains(string(body), "m_x") {
		t.Fatalf("body: %s", body)
	}
}

func TestSource_AppendFeedback_AppendsLine(t *testing.T) {
	dir := t.TempDir()
	src := NewSource(dir, &stubMem9{})
	if err := src.AppendFeedback(context.Background(), memory.MRRef{Project: "g/r", IID: 7}, memory.RatingDown, "alice"); err != nil {
		t.Fatalf("AppendFeedback: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "g_r.md"))
	if !strings.Contains(string(body), "!7 rated down by alice") {
		t.Fatalf("body: %s", body)
	}
}

func TestSource_Recall_MissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	src := NewSource(dir, &stubMem9{})
	mems, err := src.Recall(context.Background(), memory.MRRef{Project: "no/file"})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(mems) != 0 {
		t.Fatalf("expected empty on missing file")
	}
}
