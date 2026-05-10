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
	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
	"github.com/fahmi/gitlab-mr-review-bot/internal/memory/mem9"
	"github.com/fahmi/gitlab-mr-review-bot/internal/memory/mirror"
	"github.com/fahmi/gitlab-mr-review-bot/internal/memory/reporules"
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

	memClient := buildMemory(cfg.Memory, gl, prov)

	o := review.New(review.Config{
		GitLab:         gl,
		Provider:       prov,
		MaxFileTokens:  cfg.Review.MaxFileTokens,
		MaxMRTokens:    cfg.Review.MaxMRTokens,
		MaxConcurrent:  cfg.Review.MaxConcurrentChunks,
		LLMCallTimeout: cfg.Review.LLMCallTimeout,
		IgnoreGlobs:    cfg.Review.IgnoreGlobs,
		Memory:         memClient,
	})

	tracker := jobs.New()

	sess, err := discordgo.New("Bot " + cfg.Discord.Token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "discord new:", err)
		os.Exit(1)
	}

	host, err := dg.HostFromBaseURL(cfg.GitLab.BaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gitlab base url:", err)
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
			AllowedHosts:   map[string]bool{host: true},
		},
		TickEvery:  5 * time.Second,
		JobTimeout: cfg.Review.JobTimeout,
		Memory:     memClient,
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

func buildMemory(cfg config.Memory, gl gitlab.Client, prov llm.Provider) memory.Client {
	if !cfg.Enabled {
		return memory.Noop{}
	}
	composite := &memory.Composite{
		TokenBudget: cfg.RecallTokenBudget,
	}

	var sources []memory.Source

	if cfg.Mem9.Enabled {
		mem9c := mem9.New(mem9.Config{
			BaseURL: cfg.Mem9.BaseURL,
			APIKey:  cfg.Mem9.APIKey,
			Timeout: cfg.HTTPTimeout,
		})
		mem9src := memory.NewMem9Source(mem9c, memory.Mem9Tuning{
			ConventionsTopK: cfg.Mem9.ConventionsTopK,
			SummariesTopK:   cfg.Mem9.SummariesTopK,
		})
		composite.Mem9 = mem9src
		sources = append(sources, mem9src)
	}
	if cfg.RepoRules.Enabled {
		sources = append(sources, reporules.New(gl, cfg.RepoRules.Path))
	}
	if cfg.Mirror.Enabled {
		var mwriter mirror.Mem9Writer
		if composite.Mem9 != nil {
			mwriter = &mirrorMem9Adapter{m: composite.Mem9}
		}
		mr := mirror.NewSource(cfg.Mirror.Dir, mwriter)
		composite.Mirror = mr
		sources = append(sources, mr)
	}
	composite.Sources = sources
	composite.Extractor = memory.NewExtractor(prov)
	return composite
}

type mirrorMem9Adapter struct{ m memory.Mem9Adapter }

func (a *mirrorMem9Adapter) Create(ctx context.Context, content string, k memory.Kind, project string) (string, error) {
	return a.m.Create(ctx, content, k, project)
}

func (a *mirrorMem9Adapter) Update(ctx context.Context, id, content string) error {
	return a.m.Update(ctx, id, content)
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
