package review

import (
	"context"
	"fmt"
	"sync"

	"github.com/fahmi/gitlab-mr-review-bot/internal/chunker"
	"github.com/fahmi/gitlab-mr-review-bot/internal/classifier"
	"github.com/fahmi/gitlab-mr-review-bot/internal/diff"
	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
)

type Config struct {
	GitLab        gitlab.Client
	Provider      llm.Provider
	SystemPrompt  string
	MaxFileTokens int
	MaxMRTokens   int
	MaxConcurrent int
	IgnoreGlobs   []string
}

type Orchestrator struct{ cfg Config }

func New(cfg Config) *Orchestrator {
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = llm.SystemPromptDefault
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 1
	}
	return &Orchestrator{cfg: cfg}
}

type RunResult struct {
	Findings int
	Posted   int // inline discussions posted
	Skipped  int
	WebURL   string
	Counts   map[string]int
}

func (o *Orchestrator) Run(ctx context.Context, mrURL string) (*RunResult, error) {
	ref, err := gitlab.ParseURL(mrURL)
	if err != nil {
		return nil, err
	}
	mr, changes, err := o.cfg.GitLab.GetMRWithChanges(ctx, ref.ProjectPath, ref.MRIID)
	if err != nil {
		return nil, fmt.Errorf("fetch MR: %w", err)
	}

	type job struct {
		path        string
		hunksByLine map[int]bool
		chunk       chunker.FileChunk
	}
	var jobs []job
	totalTokens := 0
	for _, ch := range changes {
		if ch.DeletedFile {
			continue
		}
		ignored, err := classifier.IsIgnored(ch.NewPath, o.cfg.IgnoreGlobs)
		if err != nil {
			return nil, err
		}
		if ignored || classifier.IsLockfile(ch.NewPath) {
			continue
		}
		// Wrap the bare diff hunks in a synthetic file header so parser succeeds.
		full := fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n%s",
			ch.OldPath, ch.NewPath, ch.OldPath, ch.NewPath, ch.Diff)
		pd, err := diff.Parse(full)
		if err != nil {
			continue
		}
		if len(pd.Files) == 0 {
			continue
		}
		fd := pd.Files[0]
		if classifier.IsBinary(fd) {
			continue
		}
		validLines := map[int]bool{}
		for _, h := range fd.Hunks {
			for _, ln := range h.Lines {
				if ln.Kind == '+' || ln.Kind == ' ' {
					validLines[ln.NewLineNo] = true
				}
			}
		}
		for _, c := range chunker.Chunk(fd, o.cfg.MaxFileTokens) {
			t := chunker.EstimateTokens(c.DiffText)
			if totalTokens+t > o.cfg.MaxMRTokens {
				break
			}
			totalTokens += t
			jobs = append(jobs, job{path: ch.NewPath, hunksByLine: validLines, chunk: c})
		}
	}

	// Run LLM calls with bounded concurrency.
	sem := make(chan struct{}, o.cfg.MaxConcurrent)
	var (
		mu       sync.Mutex
		findings []llm.Finding
		errsAny  error
	)
	var wg sync.WaitGroup
	for _, j := range jobs {
		j := j
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			resp, err := o.cfg.Provider.Review(ctx, llm.ReviewRequest{
				SystemPrompt: o.cfg.SystemPrompt,
				FilePath:     j.path,
				DiffChunk:    j.chunk.DiffText,
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errsAny = err // last error wins; partial success still possible
				return
			}
			for _, f := range resp.Findings {
				if f.File == "" {
					f.File = j.path
				}
				findings = append(findings, f)
			}
		}()
	}
	wg.Wait()

	// Build line-validity map per file from all jobs.
	lineMap := map[string]map[int]bool{}
	for _, j := range jobs {
		if _, ok := lineMap[j.path]; !ok {
			lineMap[j.path] = map[int]bool{}
		}
		for ln := range j.hunksByLine {
			lineMap[j.path][ln] = true
		}
	}

	agg := Aggregate(findings)

	// Summary note first.
	if err := o.cfg.GitLab.PostNote(ctx, ref.ProjectPath, ref.MRIID, agg.SummaryBody); err != nil {
		return nil, fmt.Errorf("post summary: %w", err)
	}

	posted := 0
	skipped := 0
	for _, f := range agg.Findings {
		valid := lineMap[f.File] != nil && lineMap[f.File][f.Line]
		if !valid {
			skipped++
			continue
		}
		body := f.Message
		if f.Suggestion != "" {
			body += "\n\n```suggestion\n" + f.Suggestion + "\n```"
		}
		pos := gitlab.Position{
			BaseSHA: mr.BaseSHA, StartSHA: mr.StartSHA, HeadSHA: mr.HeadSHA,
			NewPath: f.File, OldPath: f.File,
			NewLine: f.Line, PositionType: "text",
		}
		if err := o.cfg.GitLab.PostDiscussion(ctx, ref.ProjectPath, ref.MRIID, body, pos); err != nil {
			skipped++
			continue
		}
		posted++
	}
	_ = errsAny // surfacing partial errors is a future improvement
	return &RunResult{
		Findings: len(agg.Findings),
		Posted:   posted,
		Skipped:  skipped,
		WebURL:   mr.WebURL,
		Counts:   agg.Counts,
	}, nil
}
