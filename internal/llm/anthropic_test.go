package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropic_ReviewSendsCacheControl(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/v1/messages", r.URL.Path)
		require.Equal(t, "test-key", r.Header.Get("x-api-key"))
		require.Equal(t, "2023-06-01", r.Header.Get("anthropic-version"))

		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &captured))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content":[{"type":"text","text":"{\"findings\":[{\"severity\":\"minor\",\"category\":\"style\",\"file\":\"a.go\",\"line\":2,\"message\":\"x\"}]}"}],
			"usage":{"input_tokens":10,"output_tokens":3,"cache_read_input_tokens":7}
		}`))
	}))
	defer srv.Close()

	a := NewAnthropic(AnthropicConfig{APIKey: "test-key", Model: "claude-sonnet-4-6", BaseURL: srv.URL, HTTP: srv.Client()})

	resp, err := a.Review(context.Background(), ReviewRequest{
		SystemPrompt: "rubric",
		FilePath:     "a.go",
		DiffChunk:    "@@ -1 +1 @@\n+x",
	})
	require.NoError(t, err)
	require.Len(t, resp.Findings, 1)
	require.Equal(t, 7, resp.Usage.CachedReadTokens)

	// system block has cache_control
	sysBlocks := captured["system"].([]any)
	first := sysBlocks[0].(map[string]any)
	cc, ok := first["cache_control"].(map[string]any)
	require.True(t, ok, "cache_control missing")
	require.Equal(t, "ephemeral", cc["type"])
}

func TestAnthropic_ReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit","message":"slow down"}}`))
	}))
	defer srv.Close()

	a := NewAnthropic(AnthropicConfig{APIKey: "k", Model: "m", BaseURL: srv.URL, HTTP: srv.Client()})
	_, err := a.Review(context.Background(), ReviewRequest{SystemPrompt: "s", DiffChunk: "d", FilePath: "p"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "429")
}
