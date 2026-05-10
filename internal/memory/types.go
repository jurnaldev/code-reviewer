package memory

import "time"

// Memory is one stored unit of context.
type Memory struct {
	ID        string            // mem9 ID; "" if not yet persisted
	Kind      Kind              // convention | mr_summary | feedback | rule
	Content   string            // human-readable text
	Project   string            // gitlab project path, e.g. "group/repo"
	Tags      map[string]string // mem9 tags (project, type, category, rating)
	Metadata  map[string]string // mem9 metadata (mr_iid, derived_at, etc.)
	Score     float64           // search relevance, 0 if not from search
	UpdatedAt time.Time
}

// Kind classifies the semantic purpose of a Memory.
type Kind string

const (
	KindConvention Kind = "convention"
	KindMRSummary  Kind = "mr_summary"
	KindFeedback   Kind = "feedback"
	KindRule       Kind = "rule" // from repo rules file; never persisted to mem9
)

// MRRef captures the minimum identification a memory write needs.
type MRRef struct {
	Project   string
	IID       int
	Title     string
	HeadSHA   string
	WebURL    string
	TargetRef string // target branch, used by reporules
	Files     []string
}

// Finding is duplicated narrowly from llm.Finding to avoid an import cycle.
// The extractor consumes this; orchestrator translates llm.Finding → memory.Finding.
type Finding struct {
	Severity string
	Category string
	File     string
	Line     int
	Message  string
}

// FeedbackRating is the per-MR thumbs.
type FeedbackRating string

const (
	RatingUp   FeedbackRating = "up"
	RatingDown FeedbackRating = "down"
)
