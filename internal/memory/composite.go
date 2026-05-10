package memory

import (
	"context"
	"fmt"
	"log"
	"sync"
)

// Source is anything that produces memories during recall.
type Source interface {
	Recall(ctx context.Context, mr MRRef) ([]Memory, error)
}

// Mem9Adapter is the writer-side mem9 abstraction the composite needs.
type Mem9Adapter interface {
	Source
	FetchFeedback(ctx context.Context, project string, limit int) ([]Memory, error)
	Create(ctx context.Context, content string, k Kind, project string) (string, error)
	Update(ctx context.Context, id, content string) error
	CreateFeedback(ctx context.Context, mr MRRef, rating FeedbackRating, ratedBy string) (string, error)
}

// MirrorSink writes to local mirror file.
type MirrorSink interface {
	AppendConvention(ctx context.Context, mr MRRef, text, id string) error
	AppendMRSummary(ctx context.Context, mr MRRef, text, id string) error
	AppendFeedback(ctx context.Context, mr MRRef, rating FeedbackRating, ratedBy string) error
}

// ExtractorAPI is the extractor abstraction.
type ExtractorAPI interface {
	Extract(ctx context.Context, mr MRRef, findings []Finding, feedback []Memory) (ExtractionResult, error)
}

type Composite struct {
	Sources     []Source
	Mem9        Mem9Adapter
	Mirror      MirrorSink
	Extractor   ExtractorAPI
	TokenBudget int
}

func (c *Composite) Recall(ctx context.Context, mr MRRef) (RecallResult, error) {
	if len(c.Sources) == 0 {
		return RecallResult{}, nil
	}
	type out struct {
		mems []Memory
		err  error
	}
	results := make([]out, len(c.Sources))
	var wg sync.WaitGroup
	for i, src := range c.Sources {
		wg.Add(1)
		go func(i int, src Source) {
			defer wg.Done()
			m, err := src.Recall(ctx, mr)
			results[i] = out{m, err}
		}(i, src)
	}
	wg.Wait()

	var merged []Memory
	for i, r := range results {
		if r.err != nil {
			log.Printf("memory: source %d recall failed: %v", i, r.err)
			continue
		}
		merged = append(merged, r.mems...)
	}
	merged = dedupByID(merged)
	budget := c.TokenBudget
	if budget == 0 {
		budget = 2000
	}
	text, truncated := Format(merged, budget)
	return RecallResult{FileContext: text, Memories: merged, Truncated: truncated}, nil
}

func (c *Composite) Write(ctx context.Context, mr MRRef, findings []Finding, _ string) error {
	if c.Extractor == nil {
		return nil
	}
	var feedback []Memory
	if c.Mem9 != nil {
		feedback, _ = c.Mem9.FetchFeedback(ctx, mr.Project, 5)
	}
	res, err := c.Extractor.Extract(ctx, mr, findings, feedback)
	if err != nil {
		log.Printf("memory: extract failed: %v", err)
		return nil
	}

	for _, conv := range res.Conventions {
		var id string
		if c.Mem9 != nil {
			cid, cerr := c.Mem9.Create(ctx, conv, KindConvention, mr.Project)
			if cerr != nil {
				log.Printf("memory: mem9 create convention failed: %v", cerr)
			}
			id = cid
		}
		if c.Mirror != nil {
			if merr := c.Mirror.AppendConvention(ctx, mr, conv, id); merr != nil {
				log.Printf("memory: mirror append convention failed: %v", merr)
			}
		}
	}
	if res.Summary != "" {
		var id string
		if c.Mem9 != nil {
			cid, cerr := c.Mem9.Create(ctx, res.Summary, KindMRSummary, mr.Project)
			if cerr != nil {
				log.Printf("memory: mem9 create summary failed: %v", cerr)
			}
			id = cid
		}
		if c.Mirror != nil {
			if merr := c.Mirror.AppendMRSummary(ctx, mr, res.Summary, id); merr != nil {
				log.Printf("memory: mirror append summary failed: %v", merr)
			}
		}
	}
	return nil
}

func (c *Composite) WriteFeedback(ctx context.Context, mr MRRef, rating FeedbackRating, ratedBy string) error {
	var firstErr error
	if c.Mem9 != nil {
		if _, err := c.Mem9.CreateFeedback(ctx, mr, rating, ratedBy); err != nil {
			log.Printf("memory: mem9 feedback failed: %v", err)
			firstErr = err
		}
	}
	if c.Mirror != nil {
		if err := c.Mirror.AppendFeedback(ctx, mr, rating, ratedBy); err != nil {
			log.Printf("memory: mirror feedback failed: %v", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return fmt.Errorf("write feedback: %w", firstErr)
	}
	return nil
}

func dedupByID(mems []Memory) []Memory {
	seen := map[string]bool{}
	out := make([]Memory, 0, len(mems))
	for _, m := range mems {
		if m.ID != "" {
			if seen[m.ID] {
				continue
			}
			seen[m.ID] = true
		}
		out = append(out, m)
	}
	return out
}
