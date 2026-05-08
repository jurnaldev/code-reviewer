package llm

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewProvider_Anthropic(t *testing.T) {
	p, err := NewProvider(ProviderConfig{Provider: "anthropic", Model: "m", APIKey: "k"}, http.DefaultClient)
	require.NoError(t, err)
	require.Equal(t, "anthropic", p.Name())
}

func TestNewProvider_OpenAI(t *testing.T) {
	p, err := NewProvider(ProviderConfig{Provider: "openai", Model: "m", APIKey: "k"}, http.DefaultClient)
	require.NoError(t, err)
	require.Equal(t, "openai", p.Name())
}

func TestNewProvider_Ollama(t *testing.T) {
	p, err := NewProvider(ProviderConfig{Provider: "ollama", Model: "m"}, http.DefaultClient)
	require.NoError(t, err)
	require.Equal(t, "ollama", p.Name())
}

func TestNewProvider_OllamaCustomBase(t *testing.T) {
	p, err := NewProvider(ProviderConfig{Provider: "ollama", Model: "m", BaseURL: "http://example:11434"}, http.DefaultClient)
	require.NoError(t, err)
	require.Equal(t, "ollama", p.Name())
}

func TestNewProvider_OpenRouter(t *testing.T) {
	p, err := NewProvider(ProviderConfig{Provider: "openrouter", Model: "openai/gpt-4o", APIKey: "k"}, http.DefaultClient)
	require.NoError(t, err)
	require.Equal(t, "openrouter", p.Name())
}

func TestNewProvider_OpenRouterPropagatesRefererAndTitle(t *testing.T) {
	p, err := NewProvider(ProviderConfig{
		Provider: "openrouter", Model: "m", APIKey: "k",
		Referer: "https://app.example.com", Title: "My Bot",
	}, http.DefaultClient)
	require.NoError(t, err)
	or, ok := p.(*OpenRouter)
	require.True(t, ok)
	require.Equal(t, "https://app.example.com", or.cfg.Referer)
	require.Equal(t, "My Bot", or.cfg.Title)
}

func TestNewProvider_Unknown(t *testing.T) {
	_, err := NewProvider(ProviderConfig{Provider: "weird", Model: "m"}, http.DefaultClient)
	require.Error(t, err)
	require.Contains(t, err.Error(), "weird")
}
