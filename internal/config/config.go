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

var allowedProviders = map[string]bool{"anthropic": true, "openai": true, "ollama": true}

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
	for _, p := range []*string{&c.GitLab.Token, &c.LLM.APIKey, &c.Discord.Token, &c.Discord.AppID} {
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
		return fmt.Errorf("llm.provider %q not in {anthropic, openai, ollama}", c.LLM.Provider)
	}
	if c.LLM.Model == "" {
		return fmt.Errorf("llm.model required")
	}
	if c.LLM.APIKey == "" && c.LLM.Provider != "ollama" {
		return fmt.Errorf("llm.api_key required for provider %q", c.LLM.Provider)
	}
	return nil
}
