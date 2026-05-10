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
