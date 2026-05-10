package memory

import (
	"context"
	"testing"
)

func TestNoop_RecallReturnsEmpty(t *testing.T) {
	c := Noop{}
	res, err := c.Recall(context.Background(), MRRef{Project: "g/r"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.FileContext != "" {
		t.Fatalf("expected empty FileContext, got %q", res.FileContext)
	}
	if len(res.Memories) != 0 {
		t.Fatalf("expected zero memories, got %d", len(res.Memories))
	}
}

func TestNoop_WriteAndFeedbackNoError(t *testing.T) {
	c := Noop{}
	if err := c.Write(context.Background(), MRRef{}, nil, ""); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := c.WriteFeedback(context.Background(), MRRef{}, RatingUp, "u1"); err != nil {
		t.Fatalf("feedback: %v", err)
	}
}
