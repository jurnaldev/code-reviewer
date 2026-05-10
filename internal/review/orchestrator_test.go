package review

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
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
func (f *fakeProvider) Generate(ctx context.Context, system, user string) (string, llm.TokenUsage, error) {
	return "", llm.TokenUsage{}, nil
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
func (f *fakeGL) GetFileRaw(ctx context.Context, projectPath, filePath, ref string) (string, error) {
	return "", gitlab.ErrFileNotFound
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

func TestOrchestrator_RunWithProgress_EmitsStages(t *testing.T) {
	gl := &fakeGL{}
	prov := &fakeProvider{findings: []llm.Finding{
		{Severity: "minor", Category: "style", File: "a.go", Line: 1, Message: "m"},
	}}
	o := New(Config{
		GitLab:        gl,
		Provider:      prov,
		MaxFileTokens: 4000,
		MaxMRTokens:   200000,
		MaxConcurrent: 1,
	})

	var stages []string
	progress := func(stage, msg string) {
		stages = append(stages, stage)
	}
	_, err := o.RunWithProgress(context.Background(), "https://gl/grp/proj/-/merge_requests/9", progress)
	require.NoError(t, err)
	require.Contains(t, stages, "fetching")
	require.Contains(t, stages, "reviewing")
	require.Contains(t, stages, "posting")
	require.Equal(t, "done", stages[len(stages)-1])
}

type failingProvider struct{}

func (failingProvider) Name() string { return "failing" }
func (failingProvider) Review(ctx context.Context, req llm.ReviewRequest) (llm.ReviewResponse, error) {
	return llm.ReviewResponse{}, errors.New("malformed")
}
func (failingProvider) Generate(ctx context.Context, system, user string) (string, llm.TokenUsage, error) {
	return "", llm.TokenUsage{}, errors.New("malformed")
}

func TestOrchestrator_Run_ReportsChunkFailures(t *testing.T) {
	gl := &fakeGL{}
	o := New(Config{
		GitLab:        gl,
		Provider:      failingProvider{},
		MaxFileTokens: 4000,
		MaxMRTokens:   200000,
		MaxConcurrent: 1,
	})
	_, err := o.Run(context.Background(), "https://gl/grp/proj/-/merge_requests/9")
	require.NoError(t, err)
	require.Len(t, gl.notes, 1)
	require.Contains(t, gl.notes[0], "failed to review")
}

// --- memory integration tests ---

type recordingProvider struct {
	lastReq llm.ReviewRequest
}

func (p *recordingProvider) Review(ctx context.Context, req llm.ReviewRequest) (llm.ReviewResponse, error) {
	p.lastReq = req
	return llm.ReviewResponse{Findings: []llm.Finding{}}, nil
}
func (p *recordingProvider) Generate(ctx context.Context, system, user string) (string, llm.TokenUsage, error) {
	return `{"summary":"","conventions":[]}`, llm.TokenUsage{}, nil
}
func (p *recordingProvider) Name() string { return "recording" }

type fakeGitLab struct{}

func (fakeGitLab) GetMRWithChanges(ctx context.Context, project string, iid int) (*gitlab.MR, []gitlab.FileChange, error) {
	return &gitlab.MR{
			IID: iid, Title: "test mr", WebURL: "https://example",
			BaseSHA: "b", StartSHA: "s", HeadSHA: "h", TargetBranch: "main",
		}, []gitlab.FileChange{
			{NewPath: "a.go", OldPath: "a.go", Diff: "@@ -1,1 +1,1 @@\n-old\n+new\n"},
		}, nil
}
func (fakeGitLab) PostNote(ctx context.Context, project string, iid int, body string) error { return nil }
func (fakeGitLab) PostDiscussion(ctx context.Context, project string, iid int, body string, pos gitlab.Position) error {
	return nil
}
func (fakeGitLab) GetFileRaw(ctx context.Context, project, path, ref string) (string, error) {
	return "", gitlab.ErrFileNotFound
}

type stubMemory struct {
	recallCalled     bool
	writeCalled      bool
	recallReturn     memory.RecallResult
	recallErr        error
	receivedFindings []memory.Finding
}

func (s *stubMemory) Recall(ctx context.Context, mr memory.MRRef) (memory.RecallResult, error) {
	s.recallCalled = true
	return s.recallReturn, s.recallErr
}
func (s *stubMemory) Write(ctx context.Context, mr memory.MRRef, findings []memory.Finding, _ string) error {
	s.writeCalled = true
	s.receivedFindings = findings
	return nil
}
func (s *stubMemory) WriteFeedback(ctx context.Context, mr memory.MRRef, rating memory.FeedbackRating, ratedBy string) error {
	return nil
}

func TestOrchestrator_InjectsMemoryFileContext(t *testing.T) {
	prov := &recordingProvider{}
	mem := &stubMemory{recallReturn: memory.RecallResult{FileContext: "## Project Rules\nUse Postgres."}}
	o := New(Config{
		GitLab:        fakeGitLab{},
		Provider:      prov,
		Memory:        mem,
		MaxFileTokens: 4000,
		MaxMRTokens:   200000,
	})
	_, err := o.Run(context.Background(), "https://gitlab.example/group/repo/-/merge_requests/7")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !mem.recallCalled {
		t.Fatalf("Recall not called")
	}
	if prov.lastReq.FileContext != "## Project Rules\nUse Postgres." {
		t.Fatalf("FileContext got %q", prov.lastReq.FileContext)
	}
	time.Sleep(100 * time.Millisecond)
	if !mem.writeCalled {
		t.Fatalf("Write not called")
	}
}

func TestOrchestrator_MemorySoftFailDoesNotBlock(t *testing.T) {
	prov := &recordingProvider{}
	mem := &stubMemory{recallErr: errors.New("mem9 down")}
	o := New(Config{GitLab: fakeGitLab{}, Provider: prov, Memory: mem, MaxFileTokens: 4000, MaxMRTokens: 200000})
	_, err := o.Run(context.Background(), "https://gitlab.example/group/repo/-/merge_requests/7")
	if err != nil {
		t.Fatalf("expected nil err on memory failure, got %v", err)
	}
}
