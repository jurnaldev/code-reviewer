package review

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/fahmi/gitlab-mr-review-bot/internal/chunker"
	"github.com/fahmi/gitlab-mr-review-bot/internal/classifier"
	"github.com/fahmi/gitlab-mr-review-bot/internal/diff"
	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
)

// postTimeout caps each GitLab post (summary/discussion). Detached from job
// ctx so partial results still emit when chunked review exhausts JobTimeout.
const postTimeout = 30 * time.Second

type Config struct {
	GitLab         gitlab.Client
	Provider       llm.Provider
	SystemPrompt   string
	MaxFileTokens  int
	MaxMRTokens    int
	MaxConcurrent  int
	LLMCallTimeout time.Duration
	IgnoreGlobs    []string
	Memory         memory.Client
}

type Orchestrator struct{ cfg Config }

func New(cfg Config) *Orchestrator {
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = llm.SystemPromptDefault
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 1
	}
	if cfg.LLMCallTimeout <= 0 {
		cfg.LLMCallTimeout = 90 * time.Second
	}
	if cfg.Memory == nil {
		cfg.Memory = memory.Noop{}
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

type ProgressFn func(stage, msg string)

func (o *Orchestrator) Run(ctx context.Context, mrURL string) (*RunResult, error) {
	return o.RunWithProgress(ctx, mrURL, nil)
}

func (o *Orchestrator) RunWithProgress(ctx context.Context, mrURL string, progress ProgressFn) (*RunResult, error) {
	emit := func(stage, msg string) {
		if progress != nil {
			progress(stage, msg)
		}
	}

	ref, err := gitlab.ParseURL(mrURL)
	if err != nil {
		return nil, err
	}

	emit("fetching", "fetching MR")
	mr, changes, err := o.cfg.GitLab.GetMRWithChanges(ctx, ref.ProjectPath, ref.MRIID)
	if err != nil {
		return nil, fmt.Errorf("fetch MR: %w", err)
	}

	files := make([]string, 0, len(changes))
	for _, ch := range changes {
		files = append(files, ch.NewPath)
	}
	mrRef := memory.MRRef{
		Project:   ref.ProjectPath,
		IID:       ref.MRIID,
		Title:     mr.Title,
		HeadSHA:   mr.HeadSHA,
		WebURL:    mr.WebURL,
		TargetRef: mr.TargetBranch,
		Files:     files,
	}

	emit("recalling", "loading memory")
	rec, recErr := o.cfg.Memory.Recall(ctx, mrRef)
	if recErr != nil {
		log.Printf("review: memory recall failed: %v", recErr)
	}
	fileContext := rec.FileContext

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

	emit("reviewing", fmt.Sprintf("reviewing %d chunks", len(jobs)))

	sem := make(chan struct{}, o.cfg.MaxConcurrent)
	var (
		mu       sync.Mutex
		findings []llm.Finding
		failed   int
		done     int
	)
	var wg sync.WaitGroup
	total := len(jobs)
dispatch:
	for _, j := range jobs {
		// Skip remaining work if the parent ctx is already done. Avoids burning
		// LLM spend on chunks whose results will be discarded after timeout.
		if ctx.Err() != nil {
			break dispatch
		}

		wg.Add(1)
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Done()
			break dispatch
		}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			callCtx, cancel := context.WithTimeout(ctx, o.cfg.LLMCallTimeout)
			defer cancel()
			resp, err := o.cfg.Provider.Review(callCtx, llm.ReviewRequest{
				SystemPrompt: o.cfg.SystemPrompt,
				FilePath:     j.path,
				DiffChunk:    j.chunk.DiffText,
				FileContext:  fileContext,
			})
			mu.Lock()
			defer mu.Unlock()
			done++
			emit("reviewing", fmt.Sprintf("%d/%d chunks reviewed", done, total))
			if err != nil {
				failed++
				log.Printf("review: chunk failed file=%s: %v", j.path, err)
				emit("reviewing", fmt.Sprintf("chunk failed file=%s: %v", j.path, err))
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
	if failed > 0 {
		banner := fmt.Sprintf("> _%d of %d chunks failed to review; results may be incomplete._\n\n", failed, total)
		agg.SummaryBody = banner + agg.SummaryBody
	}

	emit("posting", "posting summary")
	postCtx, postCancel := context.WithTimeout(context.WithoutCancel(ctx), postTimeout)
	err = o.cfg.GitLab.PostNote(postCtx, ref.ProjectPath, ref.MRIID, agg.SummaryBody)
	postCancel()
	if err != nil {
		return nil, fmt.Errorf("post summary: %w", err)
	}

	memFindings := make([]memory.Finding, 0, len(agg.Findings))
	for _, f := range agg.Findings {
		memFindings = append(memFindings, memory.Finding{
			Severity: f.Severity, Category: f.Category,
			File: f.File, Line: f.Line, Message: f.Message,
		})
	}
	go func() {
		wctx, wcancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer wcancel()
		if werr := o.cfg.Memory.Write(wctx, mrRef, memFindings, ""); werr != nil {
			log.Printf("review: memory write failed: %v", werr)
		}
	}()

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
		dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), postTimeout)
		err := o.cfg.GitLab.PostDiscussion(dctx, ref.ProjectPath, ref.MRIID, body, pos)
		dcancel()
		if err != nil {
			skipped++
			continue
		}
		posted++
	}

	emit("done", fmt.Sprintf("posted=%d skipped=%d findings=%d", posted, skipped, len(agg.Findings)))

	return &RunResult{
		Findings: len(agg.Findings),
		Posted:   posted,
		Skipped:  skipped,
		WebURL:   mr.WebURL,
		Counts:   agg.Counts,
	}, nil
}
