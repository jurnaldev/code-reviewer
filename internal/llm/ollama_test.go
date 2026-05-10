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

func TestOllama_ReviewBuildsCorrectRequest(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/api/chat", r.URL.Path)

		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &captured))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"llama3.1",
			"message":{"role":"assistant","content":"{\"findings\":[{\"severity\":\"nit\",\"category\":\"style\",\"file\":\"x.go\",\"line\":1,\"message\":\"y\"}]}"},
			"prompt_eval_count":7,
			"eval_count":3,
			"done":true
		}`))
	}))
	defer srv.Close()

	o := NewOllama(OllamaConfig{Model: "llama3.1", BaseURL: srv.URL, HTTP: srv.Client()})

	resp, err := o.Review(context.Background(), ReviewRequest{
		SystemPrompt: "rubric",
		FilePath:     "x.go",
		DiffChunk:    "@@ -1 +1 @@\n+y",
	})
	require.NoError(t, err)
	require.Len(t, resp.Findings, 1)
	require.Equal(t, 7, resp.Usage.InputTokens)
	require.Equal(t, 3, resp.Usage.OutputTokens)

	require.Equal(t, "llama3.1", captured["model"])
	require.Equal(t, "json", captured["format"])
	require.Equal(t, false, captured["stream"])

	msgs := captured["messages"].([]any)
	require.Len(t, msgs, 2)
	sys := msgs[0].(map[string]any)
	require.Equal(t, "system", sys["role"])
	user := msgs[1].(map[string]any)
	require.Contains(t, user["content"], "x.go")
}

func TestOllama_ReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"model not found"}`))
	}))
	defer srv.Close()

	o := NewOllama(OllamaConfig{Model: "missing", BaseURL: srv.URL, HTTP: srv.Client()})
	_, err := o.Review(context.Background(), ReviewRequest{SystemPrompt: "s", FilePath: "p", DiffChunk: "d"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}

func TestOllama_NameReturnsOllama(t *testing.T) {
	o := NewOllama(OllamaConfig{Model: "m"})
	require.Equal(t, "ollama", o.Name())
}

func TestOllama_DefaultBaseURL(t *testing.T) {
	o := NewOllama(OllamaConfig{Model: "m"})
	require.Equal(t, "http://localhost:11434", o.cfg.BaseURL)
}

func TestOllama_Generate_ReturnsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message":           map[string]any{"role": "assistant", "content": "hi"},
			"prompt_eval_count": 4, "eval_count": 1,
		})
	}))
	defer srv.Close()
	o := NewOllama(OllamaConfig{Model: "m", BaseURL: srv.URL, HTTP: srv.Client()})
	out, usage, err := o.Generate(context.Background(), "sys", "usr")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if out != "hi" {
		t.Fatalf("got %q", out)
	}
	if usage.InputTokens != 4 || usage.OutputTokens != 1 {
		t.Fatalf("usage %+v", usage)
	}
}
