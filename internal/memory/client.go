package memory

import "context"

// Client is the orchestrator-facing facade. Implementations may compose multiple
// sources/sinks; on errors they soft-fail by returning empty results / nil error
// where the spec says memory must not block reviews.
type Client interface {
	// Recall composes a context block for a single MR review. Returns the
	// rendered FileContext markdown text and the structured memories it drew
	// from (used by extractor on write path).
	Recall(ctx context.Context, mr MRRef) (RecallResult, error)

	// Write extracts and persists conventions + summary derived from one MR.
	// Best-effort: error indicates total failure of all sinks; partial success
	// returns nil with logged warnings.
	Write(ctx context.Context, mr MRRef, findings []Finding, summaryHint string) error

	// WriteFeedback records per-MR maintainer thumbs.
	WriteFeedback(ctx context.Context, mr MRRef, rating FeedbackRating, ratedBy string) error
}

// RecallResult is what Recall returns.
type RecallResult struct {
	FileContext string   // ready-to-inject markdown block, possibly empty
	Memories    []Memory // structured underlying memories, for downstream use
	Truncated   bool
}
