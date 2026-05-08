package discord

import (
	"context"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/fahmi/gitlab-mr-review-bot/internal/jobs"
	"github.com/fahmi/gitlab-mr-review-bot/internal/review"
)

// Runner is satisfied by *review.Orchestrator.
type Runner interface {
	RunWithProgress(ctx context.Context, mrURL string, p review.ProgressFn) (*review.RunResult, error)
}

type Bot struct {
	Session    SessionAPI
	Runner     Runner
	Jobs       *jobs.Tracker
	Validator  Validator
	TickEvery  time.Duration
	JobTimeout time.Duration

	// OnJobDone is called when a job finishes (success or error). Used by tests
	// to synchronize; production code can leave it nil.
	OnJobDone func()
}

// HandleInteraction dispatches the /review slash command. Replies are deferred
// (Discord 3-second ack rule) and the message is edited as the job progresses.
func (b *Bot) HandleInteraction(i *discordgo.Interaction) {
	mrURL, ok := extractURLOption(i)
	if !ok {
		b.replyEphemeral(i, "usage: /review url:<merge-request-url>")
		return
	}
	userID, roles := principalFrom(i)
	vr, err := b.Validator.Validate(Request{UserID: userID, RoleIDs: roles, MRURL: mrURL})
	if err != nil {
		b.replyEphemeral(i, "rejected: "+err.Error())
		return
	}

	job := b.Jobs.Create(userID, mrURL)
	_ = vr // used downstream when GitLab call needs project/iid; orchestrator parses URL itself

	// Defer the interaction so we have up to 15 minutes to edit it.
	if err := b.Session.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		b.Jobs.Update(job.ID, func(j *jobs.Job) {
			j.Status = jobs.StatusError
			j.ErrMessage = err.Error()
			j.EndedAt = time.Now()
		})
		return
	}

	go b.runJob(i, job.ID, mrURL)
}

func (b *Bot) runJob(i *discordgo.Interaction, jobID, mrURL string) {
	defer func() {
		if b.OnJobDone != nil {
			b.OnJobDone()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), b.JobTimeout)
	defer cancel()

	tickerCtx, tickerCancel := context.WithCancel(ctx)
	defer tickerCancel()
	go b.statusTicker(tickerCtx, i, jobID)

	progress := func(stage, msg string) {
		b.Jobs.Update(jobID, func(j *jobs.Job) {
			j.Status = jobs.Status(stage)
			j.Progress = msg
		})
	}

	res, err := b.Runner.RunWithProgress(ctx, mrURL, progress)
	tickerCancel()

	if err != nil {
		b.Jobs.Update(jobID, func(j *jobs.Job) {
			j.Status = jobs.StatusError
			j.ErrMessage = err.Error()
			j.EndedAt = time.Now()
		})
		b.editFinal(i, fmt.Sprintf(":x: review failed — error: %s", err.Error()))
		return
	}

	b.Jobs.Update(jobID, func(j *jobs.Job) {
		j.Status = jobs.StatusDone
		j.Findings = res.Findings
		j.Posted = res.Posted
		j.WebURL = res.WebURL
		j.EndedAt = time.Now()
	})

	final := fmt.Sprintf(":white_check_mark: review done — posted=%d skipped=%d findings=%d\n%s",
		res.Posted, res.Skipped, res.Findings, res.WebURL)
	b.editFinal(i, final)
}

func (b *Bot) statusTicker(ctx context.Context, i *discordgo.Interaction, jobID string) {
	t := time.NewTicker(b.TickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j, ok := b.Jobs.Get(jobID)
			if !ok {
				return
			}
			b.editFinal(i, fmt.Sprintf("status: %s — %s", j.Status, j.Progress))
		}
	}
}

func (b *Bot) editFinal(i *discordgo.Interaction, content string) {
	c := content
	_, _ = b.Session.InteractionResponseEdit(i, &discordgo.WebhookEdit{Content: &c})
}

func (b *Bot) replyEphemeral(i *discordgo.Interaction, content string) {
	_ = b.Session.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func extractURLOption(i *discordgo.Interaction) (string, bool) {
	data, ok := i.Data.(discordgo.ApplicationCommandInteractionData)
	if !ok {
		return "", false
	}
	for _, opt := range data.Options {
		if opt.Name == "url" {
			s, ok := opt.Value.(string)
			if !ok || s == "" {
				return "", false
			}
			return s, true
		}
	}
	return "", false
}

func principalFrom(i *discordgo.Interaction) (string, []string) {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.ID, i.Member.Roles
	}
	if i.User != nil {
		return i.User.ID, nil
	}
	return "", nil
}
