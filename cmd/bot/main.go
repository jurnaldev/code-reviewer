package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/fahmi/gitlab-mr-review-bot/internal/config"
	dg "github.com/fahmi/gitlab-mr-review-bot/internal/discord"
	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/httpretry"
	"github.com/fahmi/gitlab-mr-review-bot/internal/jobs"
	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
	"github.com/fahmi/gitlab-mr-review-bot/internal/review"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	hc := &http.Client{
		Timeout:   60 * time.Second,
		Transport: &httpretry.Transport{Inner: http.DefaultTransport, Max: 3, Base: 500 * time.Millisecond},
	}
	gl := gitlab.NewRESTClient(cfg.GitLab.BaseURL, cfg.GitLab.Token, hc)

	prov, err := llm.NewProvider(llm.ProviderConfig{
		Provider: cfg.LLM.Provider,
		Model:    cfg.LLM.Model,
		APIKey:   cfg.LLM.APIKey,
		BaseURL:  cfg.LLM.BaseURL,
		Referer:  cfg.LLM.Referer,
		Title:    cfg.LLM.Title,
	}, hc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "llm provider:", err)
		os.Exit(1)
	}

	o := review.New(review.Config{
		GitLab:        gl,
		Provider:      prov,
		MaxFileTokens: cfg.Review.MaxFileTokens,
		MaxMRTokens:   cfg.Review.MaxMRTokens,
		MaxConcurrent: cfg.Review.MaxConcurrentChunks,
		IgnoreGlobs:   cfg.Review.IgnoreGlobs,
	})

	tracker := jobs.New()

	sess, err := discordgo.New("Bot " + cfg.Discord.Token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "discord new:", err)
		os.Exit(1)
	}

	bot := &dg.Bot{
		Session: sess,
		Runner:  o,
		Jobs:    tracker,
		Validator: dg.Validator{
			Tracker:        tracker,
			AllowedUserIDs: toSet(cfg.Discord.AllowedUserIDs),
			AllowedRoleIDs: toSet(cfg.Discord.AllowedRoleIDs),
		},
		TickEvery:  5 * time.Second,
		JobTimeout: cfg.Review.JobTimeout,
	}

	sess.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		bot.HandleInteraction(i.Interaction)
	})
	sess.Identify.Intents = discordgo.IntentsGuilds

	if err := sess.Open(); err != nil {
		fmt.Fprintln(os.Stderr, "discord open:", err)
		os.Exit(1)
	}
	defer func() { _ = sess.Close() }()

	if err := dg.RegisterCommands(sess, cfg.Discord.AppID, cfg.Discord.GuildID); err != nil {
		fmt.Fprintln(os.Stderr, "register commands:", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tracker.StartCleaner(ctx, 24*time.Hour, time.Hour)

	fmt.Println("bot online; awaiting /review interactions")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	fmt.Println("shutting down")
}

func toSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]bool, len(items))
	for _, s := range items {
		out[s] = true
	}
	return out
}
