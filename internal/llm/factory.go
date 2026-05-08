package llm

import (
	"fmt"
	"net/http"
)

// ProviderConfig is the subset of config.LLM the factory needs. Stripped here
// so the factory does not depend on internal/config (which would cycle).
type ProviderConfig struct {
	Provider string // "anthropic" | "openai" | "ollama"
	Model    string
	APIKey   string
	BaseURL  string
}

// NewProvider returns the Provider implementation for the configured provider.
func NewProvider(cfg ProviderConfig, hc *http.Client) (Provider, error) {
	switch cfg.Provider {
	case "anthropic":
		return NewAnthropic(AnthropicConfig{
			APIKey: cfg.APIKey, Model: cfg.Model, BaseURL: cfg.BaseURL, HTTP: hc,
		}), nil
	case "openai":
		return NewOpenAI(OpenAIConfig{
			APIKey: cfg.APIKey, Model: cfg.Model, BaseURL: cfg.BaseURL, HTTP: hc,
		}), nil
	case "ollama":
		return NewOllama(OllamaConfig{
			Model: cfg.Model, BaseURL: cfg.BaseURL, HTTP: hc,
		}), nil
	default:
		return nil, fmt.Errorf("unknown llm provider %q", cfg.Provider)
	}
}
