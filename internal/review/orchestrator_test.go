package review

import (
	"context"
	"sync"
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
	"github.com/stretchr/testify/require"
)

type fakeProvider struct {
	mu       sync.Mutex
	calls    int
	findings []llm.Finding
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Review(ctx context.Context, req llm.ReviewRequest) (llm.ReviewResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return llm.ReviewResponse{Findings: f.findings}, nil
}

type fakeGL struct {
	mu          sync.Mutex
	notes       []string
	discussions []struct {
		Body string
		Pos  gitlab.Position
	}
}

func (f *fakeGL) GetMRWithChanges(ctx context.Context, project string, iid int) (*gitlab.MR, []gitlab.FileChange, error) {
	mr := &gitlab.MR{IID: iid, BaseSHA: "B", StartSHA: "S", HeadSHA: "H", WebURL: "u"}
	changes := []gitlab.FileChange{
		{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1 +1,2 @@\n-old\n+new1\n+new2\n"},
	}
	return mr, changes, nil
}
func (f *fakeGL) PostNote(ctx context.Context, project string, iid int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, body)
	return nil
}
func (f *fakeGL) PostDiscussion(ctx context.Context, project string, iid int, body string, pos gitlab.Position) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discussions = append(f.discussions, struct {
		Body string
		Pos  gitlab.Position
	}{body, pos})
	return nil
}

func TestOrchestrator_Run_PostsSummaryAndInline(t *testing.T) {
	gl := &fakeGL{}
	prov := &fakeProvider{findings: []llm.Finding{
		{Severity: "major", Category: "bug", File: "a.go", Line: 1, Message: "boom", Suggestion: "fix"},
	}}

	o := New(Config{
		GitLab:        gl,
		Provider:      prov,
		MaxFileTokens: 4000,
		MaxMRTokens:   200000,
		MaxConcurrent: 2,
		IgnoreGlobs:   []string{},
	})
	res, err := o.Run(context.Background(), "https://gl.example.com/grp/proj/-/merge_requests/9")
	require.NoError(t, err)
	require.Equal(t, 1, res.Posted)
	require.Len(t, gl.notes, 1)
	require.Contains(t, gl.notes[0], "AI Code Review")
	require.Len(t, gl.discussions, 1)
	require.Equal(t, 1, gl.discussions[0].Pos.NewLine)
	require.Equal(t, "a.go", gl.discussions[0].Pos.NewPath)
}

func TestOrchestrator_Run_RespectsIgnoreGlobs(t *testing.T) {
	gl := &fakeGL{}
	prov := &fakeProvider{findings: []llm.Finding{{Severity: "minor", Category: "style", File: "x", Line: 1, Message: "m"}}}
	o := New(Config{
		GitLab:        gl,
		Provider:      prov,
		MaxFileTokens: 4000,
		MaxMRTokens:   200000,
		MaxConcurrent: 2,
		IgnoreGlobs:   []string{"**/*.go"},
	})
	res, err := o.Run(context.Background(), "https://gl/grp/proj/-/merge_requests/1")
	require.NoError(t, err)
	require.Equal(t, 0, prov.calls)
	require.Equal(t, 0, res.Posted)
	require.Len(t, gl.notes, 1)
	require.Contains(t, gl.notes[0], "no findings")
}

func TestOrchestrator_Run_FallsBackWhenLineMissing(t *testing.T) {
	gl := &fakeGL{}
	prov := &fakeProvider{findings: []llm.Finding{
		{Severity: "minor", Category: "style", File: "a.go", Line: 999, Message: "off-diff"},
	}}
	o := New(Config{
		GitLab:        gl,
		Provider:      prov,
		MaxFileTokens: 4000,
		MaxMRTokens:   200000,
		MaxConcurrent: 1,
	})
	res, err := o.Run(context.Background(), "https://gl/x/y/-/merge_requests/2")
	require.NoError(t, err)
	require.Equal(t, 0, len(gl.discussions)) // fallback
	require.Equal(t, 0, res.Posted)
	require.Contains(t, gl.notes[0], "off-diff")
}

func TestOrchestrator_Run_BadURL(t *testing.T) {
	o := New(Config{GitLab: &fakeGL{}, Provider: &fakeProvider{}})
	_, err := o.Run(context.Background(), "not a url")
	require.Error(t, err)
}
