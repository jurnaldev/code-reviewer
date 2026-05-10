package memory

import (
	"context"
	"strings"
	"testing"
)

type stubReader struct {
	mems []Memory
	err  error
}

func (s *stubReader) Recall(ctx context.Context, mr MRRef) ([]Memory, error) {
	return s.mems, s.err
}

type stubMem9Adapter struct {
	created   []string
	feedback  []string
	feedbackErr error
}

func (s *stubMem9Adapter) Recall(ctx context.Context, mr MRRef) ([]Memory, error) { return nil, nil }
func (s *stubMem9Adapter) FetchFeedback(ctx context.Context, project string, limit int) ([]Memory, error) {
	return nil, nil
}
func (s *stubMem9Adapter) Create(ctx context.Context, content string, k Kind, project string) (string, error) {
	s.created = append(s.created, content)
	return "m_" + content[:1], nil
}
func (s *stubMem9Adapter) Update(ctx context.Context, id, content string) error { return nil }
func (s *stubMem9Adapter) CreateFeedback(ctx context.Context, mr MRRef, rating FeedbackRating, ratedBy string) (string, error) {
	s.feedback = append(s.feedback, string(rating)+":"+ratedBy)
	return "fb_1", s.feedbackErr
}

type stubMirrorSink struct {
	convs    []string
	summaries []string
	feedback []string
}

func (s *stubMirrorSink) AppendConvention(ctx context.Context, mr MRRef, text, id string) error {
	s.convs = append(s.convs, text)
	return nil
}
func (s *stubMirrorSink) AppendMRSummary(ctx context.Context, mr MRRef, text, id string) error {
	s.summaries = append(s.summaries, text)
	return nil
}
func (s *stubMirrorSink) AppendFeedback(ctx context.Context, mr MRRef, rating FeedbackRating, ratedBy string) error {
	s.feedback = append(s.feedback, string(rating)+":"+ratedBy)
	return nil
}

type stubExtractor struct {
	out ExtractionResult
	err error
}

func (s *stubExtractor) Extract(ctx context.Context, mr MRRef, findings []Finding, feedback []Memory) (ExtractionResult, error) {
	return s.out, s.err
}

func TestComposite_Recall_MergesAcrossSources(t *testing.T) {
	c := &Composite{
		Sources: []Source{
			&stubReader{mems: []Memory{{Kind: KindRule, Content: "always X"}}},
			&stubReader{mems: []Memory{{Kind: KindConvention, Content: "prefer Y"}}},
		},
		TokenBudget: 5000,
	}
	res, err := c.Recall(context.Background(), MRRef{Project: "g/r"})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if !strings.Contains(res.FileContext, "always X") || !strings.Contains(res.FileContext, "prefer Y") {
		t.Fatalf("missing content: %s", res.FileContext)
	}
}

func TestComposite_Recall_OneSourceFailureDoesNotBlock(t *testing.T) {
	c := &Composite{
		Sources: []Source{
			&stubReader{err: context.DeadlineExceeded},
			&stubReader{mems: []Memory{{Kind: KindConvention, Content: "ok"}}},
		},
		TokenBudget: 5000,
	}
	res, err := c.Recall(context.Background(), MRRef{Project: "g/r"})
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if !strings.Contains(res.FileContext, "ok") {
		t.Fatalf("survivor not present: %s", res.FileContext)
	}
}

func TestComposite_Write_FansOutToMem9AndMirror(t *testing.T) {
	mem9stub := &stubMem9Adapter{}
	mirror := &stubMirrorSink{}
	ex := &stubExtractor{out: ExtractionResult{Summary: "!7 sum", Conventions: []string{"rule one"}}}
	c := &Composite{
		Mem9:      mem9stub,
		Mirror:    mirror,
		Extractor: ex,
	}
	if err := c.Write(context.Background(), MRRef{IID: 7}, nil, ""); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(mem9stub.created) != 2 { // 1 conv + 1 summary
		t.Fatalf("mem9 created %v", mem9stub.created)
	}
	if len(mirror.convs) != 1 || len(mirror.summaries) != 1 {
		t.Fatalf("mirror %+v", mirror)
	}
}

func TestComposite_WriteFeedback_FansOut(t *testing.T) {
	mem9stub := &stubMem9Adapter{}
	mirror := &stubMirrorSink{}
	c := &Composite{Mem9: mem9stub, Mirror: mirror}
	if err := c.WriteFeedback(context.Background(), MRRef{IID: 9}, RatingDown, "alice"); err != nil {
		t.Fatalf("WriteFeedback: %v", err)
	}
	if len(mem9stub.feedback) != 1 || mem9stub.feedback[0] != "down:alice" {
		t.Fatalf("mem9 fb %v", mem9stub.feedback)
	}
	if len(mirror.feedback) != 1 {
		t.Fatalf("mirror fb %v", mirror.feedback)
	}
}
