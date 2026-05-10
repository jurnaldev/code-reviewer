package discord

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/jobs"
	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
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
	Memory     memory.Client

	// OnJobDone is called when a job finishes (success or error). Used by tests
	// to synchronize; production code can leave it nil.
	OnJobDone func()
}

// HandleInteraction dispatches slash commands and message component interactions.
// /review is deferred and edited as the job progresses (Discord 3-second ack rule).
// /ping replies immediately with an ephemeral "pong" so callers can confirm the bot is online.
func (b *Bot) HandleInteraction(i *discordgo.Interaction) {
	if i.Type == discordgo.InteractionMessageComponent {
		b.handleComponent(i)
		return
	}
	switch commandName(i) {
	case pingCommandName:
		b.handlePing(i)
		return
	case reviewCommandName:
		// fall through to review handling below
	default:
		// Unknown command: do nothing rather than ack so Discord shows the
		// generic "interaction failed" — keeps misconfigured deployments visible.
		return
	}

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
	buttons := []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{
				Label:    "👍 helpful",
				Style:    discordgo.SuccessButton,
				CustomID: "review_feedback:up:" + jobID,
			},
			discordgo.Button{
				Label:    "👎 not helpful",
				Style:    discordgo.DangerButton,
				CustomID: "review_feedback:down:" + jobID,
			},
		}},
	}
	b.editFinalWithComponents(i, final, buttons)
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

func (b *Bot) editFinalWithComponents(i *discordgo.Interaction, content string, components []discordgo.MessageComponent) {
	c := content
	_, _ = b.Session.InteractionResponseEdit(i, &discordgo.WebhookEdit{Content: &c, Components: &components})
}

func (b *Bot) handleComponent(i *discordgo.Interaction) {
	if b.Memory == nil {
		return
	}
	data, ok := i.Data.(discordgo.MessageComponentInteractionData)
	if !ok {
		return
	}
	parts := strings.SplitN(data.CustomID, ":", 3)
	if len(parts) != 3 || parts[0] != "review_feedback" {
		return
	}
	var rating memory.FeedbackRating
	switch parts[1] {
	case "up":
		rating = memory.RatingUp
	case "down":
		rating = memory.RatingDown
	default:
		return
	}
	jobID := parts[2]
	job, ok := b.Jobs.Get(jobID)
	if !ok {
		b.replyEphemeral(i, "this review has expired — feedback not recorded")
		return
	}
	ref, err := gitlab.ParseURL(job.MRURL)
	if err != nil {
		b.replyEphemeral(i, "invalid MR URL on job")
		return
	}
	userID, _ := principalFrom(i)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.Memory.WriteFeedback(ctx, memory.MRRef{
		Project: ref.ProjectPath,
		IID:     ref.MRIID,
		WebURL:  job.WebURL,
	}, rating, userID); err != nil {
		b.replyEphemeral(i, "feedback failed: "+err.Error())
		return
	}
	b.replyEphemeral(i, "noted, thanks")
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

func (b *Bot) handlePing(i *discordgo.Interaction) {
	b.replyEphemeral(i, ":white_check_mark: pong — bot online")
}

func commandName(i *discordgo.Interaction) string {
	data, ok := i.Data.(discordgo.ApplicationCommandInteractionData)
	if !ok {
		return ""
	}
	return data.Name
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
