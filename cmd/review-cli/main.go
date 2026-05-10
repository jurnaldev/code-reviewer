package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/fahmi/gitlab-mr-review-bot/internal/config"
	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/httpretry"
	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
	"github.com/fahmi/gitlab-mr-review-bot/internal/review"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: review-cli --config FILE <mr-url>")
		os.Exit(2)
	}
	mrURL := flag.Arg(0)

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

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Review.JobTimeout)
	defer cancel()

	res, err := o.Run(ctx, mrURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "review failed:", err)
		os.Exit(1)
	}
	fmt.Printf("posted=%d skipped=%d findings=%d url=%s\n", res.Posted, res.Skipped, res.Findings, res.WebURL)
}
