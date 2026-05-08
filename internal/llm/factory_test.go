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

func TestNewProvider_Unknown(t *testing.T) {
	_, err := NewProvider(ProviderConfig{Provider: "weird", Model: "m"}, http.DefaultClient)
	require.Error(t, err)
	require.Contains(t, err.Error(), "weird")
}
