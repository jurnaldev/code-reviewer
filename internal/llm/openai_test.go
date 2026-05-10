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

func TestOpenAI_ReviewBuildsCorrectRequest(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))

		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &captured))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"{\"findings\":[{\"severity\":\"minor\",\"category\":\"style\",\"file\":\"a.go\",\"line\":2,\"message\":\"x\"}]}"}}],
			"usage":{"prompt_tokens":12,"completion_tokens":4}
		}`))
	}))
	defer srv.Close()

	o := NewOpenAI(OpenAIConfig{APIKey: "sk-test", Model: "gpt-4o", BaseURL: srv.URL, HTTP: srv.Client()})

	resp, err := o.Review(context.Background(), ReviewRequest{
		SystemPrompt: "rubric",
		FilePath:     "a.go",
		DiffChunk:    "@@ -1 +1 @@\n+x",
	})
	require.NoError(t, err)
	require.Len(t, resp.Findings, 1)
	require.Equal(t, 12, resp.Usage.InputTokens)
	require.Equal(t, 4, resp.Usage.OutputTokens)

	// Request body assertions
	require.Equal(t, "gpt-4o", captured["model"])
	rf := captured["response_format"].(map[string]any)
	require.Equal(t, "json_object", rf["type"])

	msgs := captured["messages"].([]any)
	require.Len(t, msgs, 2)
	sys := msgs[0].(map[string]any)
	require.Equal(t, "system", sys["role"])
	require.Equal(t, "rubric", sys["content"])
	user := msgs[1].(map[string]any)
	require.Equal(t, "user", user["role"])
	require.Contains(t, user["content"], "a.go")
}

func TestOpenAI_ReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	o := NewOpenAI(OpenAIConfig{APIKey: "k", Model: "m", BaseURL: srv.URL, HTTP: srv.Client()})
	_, err := o.Review(context.Background(), ReviewRequest{SystemPrompt: "s", FilePath: "p", DiffChunk: "d"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "401")
}

func TestOpenAI_NameReturnsOpenAI(t *testing.T) {
	o := NewOpenAI(OpenAIConfig{APIKey: "k", Model: "m"})
	require.Equal(t, "openai", o.Name())
}

func TestOpenAI_Generate_ReturnsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "hi"}},
			},
			"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 1},
		})
	}))
	defer srv.Close()
	o := NewOpenAI(OpenAIConfig{APIKey: "k", Model: "gpt-x", BaseURL: srv.URL, HTTP: srv.Client()})
	out, usage, err := o.Generate(context.Background(), "sys", "usr")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if out != "hi" {
		t.Fatalf("got %q", out)
	}
	if usage.InputTokens != 5 || usage.OutputTokens != 1 {
		t.Fatalf("usage %+v", usage)
	}
}
