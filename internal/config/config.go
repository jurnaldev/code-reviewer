package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Discord Discord `yaml:"discord"`
	GitLab  GitLab  `yaml:"gitlab"`
	LLM     LLM     `yaml:"llm"`
	Review  Review  `yaml:"review"`
	Memory  Memory  `yaml:"memory"`
}

type GitLab struct {
	BaseURL string `yaml:"base_url"`
	Token   string `yaml:"token"`
}

type Discord struct {
	Token          string   `yaml:"token"`
	AppID          string   `yaml:"app_id"`
	GuildID        string   `yaml:"guild_id"`
	AllowedUserIDs []string `yaml:"allowed_user_ids"`
	AllowedRoleIDs []string `yaml:"allowed_role_ids"`
}

type LLM struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	APIKey   string `yaml:"api_key"`
	BaseURL  string `yaml:"base_url"`
	Referer  string `yaml:"referer"` // openrouter HTTP-Referer
	Title    string `yaml:"title"`   // openrouter X-Title
}

type Review struct {
	MaxFileTokens       int           `yaml:"max_file_tokens"`
	MaxMRTokens         int           `yaml:"max_mr_tokens"`
	MaxConcurrentChunks int           `yaml:"max_concurrent_chunks"`
	JobTimeout          time.Duration `yaml:"job_timeout"`
	LLMCallTimeout      time.Duration `yaml:"llm_call_timeout"`
	IgnoreGlobs         []string      `yaml:"ignore_globs"`
	DeepModeDefault     bool          `yaml:"deep_mode_default"`
}

type Memory struct {
	Enabled           bool          `yaml:"enabled"`
	RecallTokenBudget int           `yaml:"recall_token_budget"`
	HTTPTimeout       time.Duration `yaml:"http_timeout"`
	Mem9              Mem9          `yaml:"mem9"`
	RepoRules         RepoRules     `yaml:"repo_rules"`
	Mirror            Mirror        `yaml:"mirror"`
}

type Mem9 struct {
	Enabled         bool   `yaml:"enabled"`
	BaseURL         string `yaml:"base_url"`
	APIKey          string `yaml:"api_key"`
	ConventionsTopK int    `yaml:"conventions_top_k"`
	SummariesTopK   int    `yaml:"summaries_top_k"`
}

type RepoRules struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

type Mirror struct {
	Enabled bool   `yaml:"enabled"`
	Dir     string `yaml:"dir"`
}

var allowedProviders = map[string]bool{"anthropic": true, "openai": true, "ollama": true, "openrouter": true}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	if err := interpEnvFields(&c); err != nil {
		return nil, err
	}
	applyDefaults(&c)
	if err := validate(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

func interpEnvFields(c *Config) error {
	for _, p := range []*string{&c.GitLab.Token, &c.LLM.APIKey, &c.Discord.Token, &c.Discord.AppID, &c.Memory.Mem9.APIKey} {
		v, err := interp(*p)
		if err != nil {
			return err
		}
		*p = v
	}
	return nil
}

func interp(s string) (string, error) {
	const pfx = "env:"
	if !strings.HasPrefix(s, pfx) {
		return s, nil
	}
	name := strings.TrimPrefix(s, pfx)
	v, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("env var %s not set", name)
	}
	return v, nil
}

func applyDefaults(c *Config) {
	if c.Review.MaxFileTokens == 0 {
		c.Review.MaxFileTokens = 8000
	}
	if c.Review.MaxMRTokens == 0 {
		c.Review.MaxMRTokens = 200000
	}
	if c.Review.MaxConcurrentChunks == 0 {
		c.Review.MaxConcurrentChunks = 4
	}
	if c.Review.JobTimeout == 0 {
		c.Review.JobTimeout = 10 * time.Minute
	}
	if c.Review.LLMCallTimeout == 0 {
		c.Review.LLMCallTimeout = 90 * time.Second
	}
	if c.Memory.Enabled {
		if c.Memory.RecallTokenBudget == 0 {
			c.Memory.RecallTokenBudget = 2000
		}
		if c.Memory.HTTPTimeout == 0 {
			c.Memory.HTTPTimeout = 10 * time.Second
		}
		if c.Memory.Mem9.Enabled {
			if c.Memory.Mem9.BaseURL == "" {
				c.Memory.Mem9.BaseURL = "https://api.mem9.ai"
			}
			if c.Memory.Mem9.ConventionsTopK == 0 {
				c.Memory.Mem9.ConventionsTopK = 20
			}
			if c.Memory.Mem9.SummariesTopK == 0 {
				c.Memory.Mem9.SummariesTopK = 5
			}
		}
		if c.Memory.RepoRules.Enabled && c.Memory.RepoRules.Path == "" {
			c.Memory.RepoRules.Path = ".review/rules.md"
		}
		if c.Memory.Mirror.Enabled && c.Memory.Mirror.Dir == "" {
			c.Memory.Mirror.Dir = "~/.cache/gitlab-mr-bot/memory"
		}
	}
}

func validate(c *Config) error {
	if c.Discord.Token == "" {
		return fmt.Errorf("discord.token required")
	}
	if c.Discord.AppID == "" {
		return fmt.Errorf("discord.app_id required")
	}
	if c.GitLab.BaseURL == "" {
		return fmt.Errorf("gitlab.base_url required")
	}
	if c.GitLab.Token == "" {
		return fmt.Errorf("gitlab.token required")
	}
	if !allowedProviders[c.LLM.Provider] {
		return fmt.Errorf("llm.provider %q not in {anthropic, openai, ollama, openrouter}", c.LLM.Provider)
	}
	if c.LLM.Model == "" {
		return fmt.Errorf("llm.model required")
	}
	if c.LLM.APIKey == "" && c.LLM.Provider != "ollama" {
		return fmt.Errorf("llm.api_key required for provider %q", c.LLM.Provider)
	}
	if c.Memory.Enabled && c.Memory.Mem9.Enabled && c.Memory.Mem9.APIKey == "" {
		return fmt.Errorf("memory.mem9.api_key required when memory.mem9.enabled is true")
	}
	return nil
}
