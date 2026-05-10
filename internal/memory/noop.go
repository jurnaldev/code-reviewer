package memory

import "context"

// Noop is the no-op Client used when memory.enabled=false.
type Noop struct{}

func (Noop) Recall(ctx context.Context, mr MRRef) (RecallResult, error) {
	return RecallResult{}, nil
}

func (Noop) Write(ctx context.Context, mr MRRef, findings []Finding, summaryHint string) error {
	return nil
}

func (Noop) WriteFeedback(ctx context.Context, mr MRRef, rating FeedbackRating, ratedBy string) error {
	return nil
}
