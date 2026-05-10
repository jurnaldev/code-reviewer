package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoad_DefaultsAndEnvInterp(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "glpat-abc")
	t.Setenv("ANTHROPIC_API_KEY", "sk-xyz")
	t.Setenv("DISCORD_TOKEN", "dt")
	t.Setenv("DISCORD_APP_ID", "da")

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
discord:
  token: env:DISCORD_TOKEN
  app_id: env:DISCORD_APP_ID
gitlab:
  base_url: https://gl.example.com
  token: env:GITLAB_TOKEN
llm:
  provider: anthropic
  model: claude-sonnet-4-6
  api_key: env:ANTHROPIC_API_KEY
review:
  max_file_tokens: 4000
`), 0644))

	cfg, err := Load(path)
	require.NoError(t, err)

	require.Equal(t, "https://gl.example.com", cfg.GitLab.BaseURL)
	require.Equal(t, "glpat-abc", cfg.GitLab.Token)
	require.Equal(t, "anthropic", cfg.LLM.Provider)
	require.Equal(t, "sk-xyz", cfg.LLM.APIKey)
	require.Equal(t, 4000, cfg.Review.MaxFileTokens)
	require.Equal(t, 200000, cfg.Review.MaxMRTokens)
	require.Equal(t, 4, cfg.Review.MaxConcurrentChunks)
	require.Equal(t, 10*time.Minute, cfg.Review.JobTimeout)
	require.Equal(t, 90*time.Second, cfg.Review.LLMCallTimeout)
	require.False(t, cfg.Review.DeepModeDefault)
}

func TestLoad_MissingEnvFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
discord: {token: x, app_id: y}
gitlab:
  base_url: https://x
  token: env:NOT_SET_VAR_XYZ
llm:
  provider: anthropic
  model: m
  api_key: k
`), 0644))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "NOT_SET_VAR_XYZ")
}

func TestLoad_RejectsUnknownProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
discord: {token: x, app_id: y}
gitlab: {base_url: x, token: t}
llm: {provider: bogus, model: m, api_key: k}
`), 0644))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider")
}

func TestLoad_DiscordRequired(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "g")
	t.Setenv("ANTHROPIC_API_KEY", "k")
	t.Setenv("DISCORD_TOKEN", "dt")
	t.Setenv("DISCORD_APP_ID", "did")

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
discord:
  token: env:DISCORD_TOKEN
  app_id: env:DISCORD_APP_ID
  guild_id: ""
  allowed_user_ids: ["alice"]
gitlab: {base_url: https://gl, token: env:GITLAB_TOKEN}
llm: {provider: anthropic, model: m, api_key: env:ANTHROPIC_API_KEY}
`), 0644))

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "dt", cfg.Discord.Token)
	require.Equal(t, "did", cfg.Discord.AppID)
	require.Equal(t, []string{"alice"}, cfg.Discord.AllowedUserIDs)
}

func TestLoad_DiscordTokenRequired(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "g")
	t.Setenv("ANTHROPIC_API_KEY", "k")

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
discord: {token: "", app_id: did}
gitlab: {base_url: https://gl, token: env:GITLAB_TOKEN}
llm: {provider: anthropic, model: m, api_key: env:ANTHROPIC_API_KEY}
`), 0644))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "discord.token")
}

func TestLoad_MemoryBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
discord:
  token: t
  app_id: a
gitlab:
  base_url: https://g
  token: gt
llm:
  provider: anthropic
  model: m
  api_key: k
memory:
  enabled: true
  recall_token_budget: 1500
  http_timeout: 5s
  mem9:
    enabled: true
    base_url: https://api.mem9.ai
    api_key: mk
    conventions_top_k: 30
    summaries_top_k: 4
  repo_rules:
    enabled: true
    path: .review/rules.md
  mirror:
    enabled: true
    dir: ~/.cache/gitlab-mr-bot/memory
`
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))
	c, err := Load(path)
	require.NoError(t, err)
	require.True(t, c.Memory.Enabled)
	require.Equal(t, 1500, c.Memory.RecallTokenBudget)
	require.Equal(t, 5*time.Second, c.Memory.HTTPTimeout)
	require.True(t, c.Memory.Mem9.Enabled)
	require.Equal(t, "mk", c.Memory.Mem9.APIKey)
	require.Equal(t, 30, c.Memory.Mem9.ConventionsTopK)
	require.Equal(t, 4, c.Memory.Mem9.SummariesTopK)
	require.Equal(t, ".review/rules.md", c.Memory.RepoRules.Path)
	require.Equal(t, "~/.cache/gitlab-mr-bot/memory", c.Memory.Mirror.Dir)
}

func TestLoad_MemoryBlock_DefaultsAndDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
discord:
  token: t
  app_id: a
gitlab:
  base_url: https://g
  token: gt
llm:
  provider: anthropic
  model: m
  api_key: k
`
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))
	c, err := Load(path)
	require.NoError(t, err)
	require.False(t, c.Memory.Enabled)
	require.Equal(t, 0, c.Memory.RecallTokenBudget)
	require.Equal(t, time.Duration(0), c.Memory.HTTPTimeout)
	require.False(t, c.Memory.Mem9.Enabled)
	require.Empty(t, c.Memory.Mem9.BaseURL)
	require.False(t, c.Memory.Mirror.Enabled)
	require.Empty(t, c.Memory.Mirror.Dir)
}

func TestLoad_MemoryBlock_RejectsMem9EnabledWithoutKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
discord: {token: t, app_id: a}
gitlab: {base_url: https://g, token: gt}
llm: {provider: anthropic, model: m, api_key: k}
memory:
  enabled: true
  mem9:
    enabled: true
    base_url: https://api.mem9.ai
`
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))
	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "memory.mem9.api_key")
}

func TestLoad_MemoryBlock_AppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
discord:
  token: t
  app_id: a
gitlab:
  base_url: https://g
  token: gt
llm:
  provider: anthropic
  model: m
  api_key: k
memory:
  enabled: true
  mem9:
    enabled: true
    api_key: k
  repo_rules:
    enabled: true
  mirror:
    enabled: true
`
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))
	c, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 2000, c.Memory.RecallTokenBudget)
	require.Equal(t, 10*time.Second, c.Memory.HTTPTimeout)
	require.Equal(t, "https://api.mem9.ai", c.Memory.Mem9.BaseURL)
	require.Equal(t, 20, c.Memory.Mem9.ConventionsTopK)
	require.Equal(t, 5, c.Memory.Mem9.SummariesTopK)
	require.Equal(t, ".review/rules.md", c.Memory.RepoRules.Path)
	require.Equal(t, "~/.cache/gitlab-mr-bot/memory", c.Memory.Mirror.Dir)
}
