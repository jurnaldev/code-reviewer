package discord

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/fahmi/gitlab-mr-review-bot/internal/jobs"
	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
	"github.com/fahmi/gitlab-mr-review-bot/internal/review"
	"github.com/stretchr/testify/require"
)

type fakeRunner struct {
	mu      sync.Mutex
	called  bool
	stages  []string
	wantErr error
	wantRes *review.RunResult
}

func (f *fakeRunner) RunWithProgress(ctx context.Context, mrURL string, p review.ProgressFn) (*review.RunResult, error) {
	f.mu.Lock()
	f.called = true
	f.mu.Unlock()
	if p != nil {
		p("fetching", "fetching MR")
		p("reviewing", "1/1 chunks reviewed")
		p("posting", "posting summary")
		p("done", "posted=1 skipped=0 findings=1")
	}
	return f.wantRes, f.wantErr
}

type fakeSession struct {
	mu                 sync.Mutex
	respCalls          int
	editCalls          int
	lastContent        string
	respondedEphemeral bool
}

func (s *fakeSession) InteractionRespond(i *discordgo.Interaction, r *discordgo.InteractionResponse, _ ...discordgo.RequestOption) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.respCalls++
	if r != nil && r.Data != nil {
		if r.Data.Content != "" {
			s.lastContent = r.Data.Content
		}
		if r.Data.Flags&discordgo.MessageFlagsEphemeral != 0 {
			s.respondedEphemeral = true
		}
	}
	return nil
}
func (s *fakeSession) InteractionResponseEdit(i *discordgo.Interaction, r *discordgo.WebhookEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.editCalls++
	if r.Content != nil {
		s.lastContent = *r.Content
	}
	return &discordgo.Message{}, nil
}
func (s *fakeSession) ApplicationCommandBulkOverwrite(string, string, []*discordgo.ApplicationCommand, ...discordgo.RequestOption) ([]*discordgo.ApplicationCommand, error) {
	return nil, nil
}

func mkInteraction(userID, mrURL string) *discordgo.Interaction {
	return &discordgo.Interaction{
		ID: "iid", Token: "tok", AppID: "app",
		Member: &discordgo.Member{User: &discordgo.User{ID: userID}, Roles: nil},
		Type:   discordgo.InteractionApplicationCommand,
		Data: discordgo.ApplicationCommandInteractionData{
			Name: "review",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "url", Type: discordgo.ApplicationCommandOptionString, Value: mrURL},
			},
		},
	}
}

func mkPingInteraction(userID string) *discordgo.Interaction {
	return &discordgo.Interaction{
		ID: "iid", Token: "tok", AppID: "app",
		Member: &discordgo.Member{User: &discordgo.User{ID: userID}, Roles: nil},
		Type:   discordgo.InteractionApplicationCommand,
		Data:   discordgo.ApplicationCommandInteractionData{Name: "ping"},
	}
}

func TestBot_HappyPath(t *testing.T) {
	sess := &fakeSession{}
	tr := jobs.New()
	runner := &fakeRunner{wantRes: &review.RunResult{Posted: 1, Skipped: 0, Findings: 1, WebURL: "https://gl/x"}}

	b := &Bot{
		Session: sess, Runner: runner, Jobs: tr,
		Validator:  Validator{Tracker: tr, AllowedHosts: map[string]bool{"gl.example.com": true}},
		TickEvery:  20 * time.Millisecond,
		JobTimeout: 2 * time.Second,
	}

	done := make(chan struct{})
	b.OnJobDone = func() { close(done) }

	b.HandleInteraction(mkInteraction("u1", "https://gl.example.com/team/proj/-/merge_requests/4"))

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not complete in time")
	}

	require.True(t, runner.called)
	require.GreaterOrEqual(t, sess.respCalls, 1, "must defer ack")
	require.GreaterOrEqual(t, sess.editCalls, 1, "must edit final message")
	require.Contains(t, sess.lastContent, "posted=1")
}

func TestBot_RejectsBadURL(t *testing.T) {
	sess := &fakeSession{}
	tr := jobs.New()
	b := &Bot{Session: sess, Runner: &fakeRunner{}, Jobs: tr, Validator: Validator{Tracker: tr, AllowedHosts: map[string]bool{"gl.example.com": true}}, TickEvery: time.Second, JobTimeout: time.Second}

	b.HandleInteraction(mkInteraction("u1", "garbage"))

	require.Equal(t, 1, sess.respCalls)
	require.Equal(t, 0, sess.editCalls)
}

func TestBot_RejectsDuplicate(t *testing.T) {
	sess := &fakeSession{}
	tr := jobs.New()
	tr.Create("u1", "https://gl.example.com/team/proj/-/merge_requests/4")

	b := &Bot{Session: sess, Runner: &fakeRunner{}, Jobs: tr, Validator: Validator{Tracker: tr, AllowedHosts: map[string]bool{"gl.example.com": true}}, TickEvery: time.Second, JobTimeout: time.Second}

	b.HandleInteraction(mkInteraction("u1", "https://gl.example.com/team/proj/-/merge_requests/4"))

	require.Equal(t, 1, sess.respCalls)
	require.Equal(t, 0, sess.editCalls)
}

func TestBot_PingReplies(t *testing.T) {
	sess := &fakeSession{}
	tr := jobs.New()
	runner := &fakeRunner{}
	b := &Bot{Session: sess, Runner: runner, Jobs: tr, Validator: Validator{Tracker: tr, AllowedHosts: map[string]bool{"gl.example.com": true}}, TickEvery: time.Second, JobTimeout: time.Second}

	b.HandleInteraction(mkPingInteraction("u1"))

	require.Equal(t, 1, sess.respCalls, "ping must reply once")
	require.Equal(t, 0, sess.editCalls, "ping must not edit")
	require.False(t, runner.called, "ping must not invoke runner")
	require.Contains(t, sess.lastContent, "pong")
}

func TestBot_RunnerError(t *testing.T) {
	sess := &fakeSession{}
	tr := jobs.New()
	runner := &fakeRunner{wantErr: errors.New("boom")}
	b := &Bot{Session: sess, Runner: runner, Jobs: tr, Validator: Validator{Tracker: tr, AllowedHosts: map[string]bool{"gl.example.com": true}}, TickEvery: 20 * time.Millisecond, JobTimeout: time.Second}

	done := make(chan struct{})
	b.OnJobDone = func() { close(done) }

	b.HandleInteraction(mkInteraction("u1", "https://gl.example.com/team/proj/-/merge_requests/4"))

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not complete")
	}
	require.Contains(t, sess.lastContent, "error")
}

func TestBot_HandleInteraction_FeedbackUp(t *testing.T) {
	mem := &stubBotMemory{}
	bot := &Bot{
		Session: &fakeSession{},
		Memory:  mem,
		Jobs:    jobs.New(),
	}
	job := bot.Jobs.Create("u1", "https://gitlab.example/group/repo/-/merge_requests/7")
	bot.Jobs.Update(job.ID, func(j *jobs.Job) { j.Status = jobs.StatusDone })

	i := &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
		Data: discordgo.MessageComponentInteractionData{
			CustomID: "review_feedback:up:" + job.ID,
		},
		Member: &discordgo.Member{User: &discordgo.User{ID: "u1"}},
	}
	bot.HandleInteraction(i)
	if mem.lastRating != memory.RatingUp {
		t.Fatalf("rating got %s", mem.lastRating)
	}
	if mem.lastMR.IID != 7 {
		t.Fatalf("iid got %d", mem.lastMR.IID)
	}
}

func TestBot_HandleInteraction_FeedbackJobMissing(t *testing.T) {
	mem := &stubBotMemory{}
	sess := &fakeSession{}
	bot := &Bot{Session: sess, Memory: mem, Jobs: jobs.New()}
	i := &discordgo.Interaction{
		Type: discordgo.InteractionMessageComponent,
		Data: discordgo.MessageComponentInteractionData{
			CustomID: "review_feedback:up:nonexistent",
		},
		Member: &discordgo.Member{User: &discordgo.User{ID: "u1"}},
	}
	bot.HandleInteraction(i)
	if mem.called {
		t.Fatalf("memory should not be called for missing job")
	}
	if !sess.respondedEphemeral {
		t.Fatalf("expected ephemeral reply")
	}
}

type stubBotMemory struct {
	called      bool
	lastRating  memory.FeedbackRating
	lastMR      memory.MRRef
	lastRatedBy string
}

func (s *stubBotMemory) Recall(ctx context.Context, mr memory.MRRef) (memory.RecallResult, error) {
	return memory.RecallResult{}, nil
}
func (s *stubBotMemory) Write(ctx context.Context, mr memory.MRRef, findings []memory.Finding, _ string) error {
	return nil
}
func (s *stubBotMemory) WriteFeedback(ctx context.Context, mr memory.MRRef, rating memory.FeedbackRating, ratedBy string) error {
	s.called = true
	s.lastMR = mr
	s.lastRating = rating
	s.lastRatedBy = ratedBy
	return nil
}
