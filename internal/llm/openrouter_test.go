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

func TestOpenRouter_ReviewBuildsCorrectRequest(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/api/v1/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer or-test", r.Header.Get("Authorization"))

		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &captured))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"{\"findings\":[{\"severity\":\"minor\",\"category\":\"style\",\"file\":\"a.go\",\"line\":3,\"message\":\"r\"}]}"}}],
			"usage":{"prompt_tokens":11,"completion_tokens":2}
		}`))
	}))
	defer srv.Close()

	o := NewOpenRouter(OpenRouterConfig{APIKey: "or-test", Model: "openai/gpt-4o", BaseURL: srv.URL + "/api", HTTP: srv.Client()})

	resp, err := o.Review(context.Background(), ReviewRequest{
		SystemPrompt: "rubric",
		FilePath:     "a.go",
		DiffChunk:    "@@ -1 +1 @@\n+r",
	})
	require.NoError(t, err)
	require.Len(t, resp.Findings, 1)
	require.Equal(t, 11, resp.Usage.InputTokens)
	require.Equal(t, 2, resp.Usage.OutputTokens)

	require.Equal(t, "openai/gpt-4o", captured["model"])

	// Crucially: NO response_format. OpenRouter spans many models, json mode unreliable.
	_, hasRF := captured["response_format"]
	require.False(t, hasRF, "must not send response_format")

	msgs := captured["messages"].([]any)
	require.Len(t, msgs, 2)
	sys := msgs[0].(map[string]any)
	require.Equal(t, "system", sys["role"])
	require.Equal(t, "rubric", sys["content"])
	user := msgs[1].(map[string]any)
	require.Equal(t, "user", user["role"])
	require.Contains(t, user["content"], "a.go")
}

func TestOpenRouter_OptionalRefererAndTitle(t *testing.T) {
	var gotReferer, gotTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("HTTP-Referer")
		gotTitle = r.Header.Get("X-Title")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"findings\":[]}"}}],"usage":{}}`))
	}))
	defer srv.Close()

	o := NewOpenRouter(OpenRouterConfig{
		APIKey: "k", Model: "m", BaseURL: srv.URL, HTTP: srv.Client(),
		Referer: "https://example.com/bot",
		Title:   "MR Review Bot",
	})
	_, err := o.Review(context.Background(), ReviewRequest{SystemPrompt: "s", FilePath: "p", DiffChunk: "d"})
	require.NoError(t, err)
	require.Equal(t, "https://example.com/bot", gotReferer)
	require.Equal(t, "MR Review Bot", gotTitle)
}

func TestOpenRouter_OmitsHeadersWhenEmpty(t *testing.T) {
	var seenReferer, seenTitle bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, seenReferer = r.Header["Http-Referer"]
		_, seenTitle = r.Header["X-Title"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"findings\":[]}"}}],"usage":{}}`))
	}))
	defer srv.Close()

	o := NewOpenRouter(OpenRouterConfig{APIKey: "k", Model: "m", BaseURL: srv.URL, HTTP: srv.Client()})
	_, err := o.Review(context.Background(), ReviewRequest{SystemPrompt: "s", FilePath: "p", DiffChunk: "d"})
	require.NoError(t, err)
	require.False(t, seenReferer, "Referer header must be absent when not configured")
	require.False(t, seenTitle, "X-Title header must be absent when not configured")
}

func TestOpenRouter_ReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(402)
		_, _ = w.Write([]byte(`{"error":{"message":"insufficient credits"}}`))
	}))
	defer srv.Close()

	o := NewOpenRouter(OpenRouterConfig{APIKey: "k", Model: "m", BaseURL: srv.URL, HTTP: srv.Client()})
	_, err := o.Review(context.Background(), ReviewRequest{SystemPrompt: "s", FilePath: "p", DiffChunk: "d"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "402")
}

func TestOpenRouter_NameReturnsOpenRouter(t *testing.T) {
	o := NewOpenRouter(OpenRouterConfig{APIKey: "k", Model: "m"})
	require.Equal(t, "openrouter", o.Name())
}

func TestOpenRouter_DefaultBaseURL(t *testing.T) {
	o := NewOpenRouter(OpenRouterConfig{APIKey: "k", Model: "m"})
	require.Equal(t, "https://openrouter.ai/api", o.cfg.BaseURL)
}
