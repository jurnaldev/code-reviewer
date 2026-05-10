package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
	"github.com/fahmi/gitlab-mr-review-bot/internal/memory"
	"github.com/fahmi/gitlab-mr-review-bot/internal/memory/mem9"
	"github.com/stretchr/testify/require"
)

// fakeAnthropic returns a canned findings JSON.
func fakeAnthropicHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content":[{"type":"text","text":"{\"findings\":[{\"severity\":\"major\",\"category\":\"bug\",\"file\":\"a.go\",\"line\":2,\"message\":\"boom\"}]}"}],
			"usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0}
		}`))
	})
}

func fakeGitLabHandler(t *testing.T, postedNotes *int, postedDiscussions *int) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/grp%2Fproj/merge_requests/9", func(w http.ResponseWriter, r *http.Request) {
		// ServeMux already exact-matches this path (longer subpaths /changes, /notes,
		// /discussions have their own handlers). r.URL.Path is the decoded form, so
		// compare against EscapedPath() if a path check is needed; here we only
		// guard the method.
		if r.Method != "GET" {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"iid": 9, "title": "x", "web_url": "u",
			"diff_refs": map[string]string{"base_sha": "B", "start_sha": "S", "head_sha": "H"},
		})
	})
	mux.HandleFunc("/api/v4/projects/grp%2Fproj/merge_requests/9/changes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"changes": []map[string]any{
				{"old_path": "a.go", "new_path": "a.go", "diff": "@@ -1 +1,2 @@\n-old\n+new1\n+new2\n"},
			},
		})
	})
	mux.HandleFunc("/api/v4/projects/grp%2Fproj/merge_requests/9/notes", func(w http.ResponseWriter, r *http.Request) {
		*postedNotes++
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":1}`))
	})
	mux.HandleFunc("/api/v4/projects/grp%2Fproj/merge_requests/9/discussions", func(w http.ResponseWriter, r *http.Request) {
		*postedDiscussions++
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"d1"}`))
	})
	return mux
}

func TestE2E_HappyPath(t *testing.T) {
	var notes, discussions int
	gl := httptest.NewServer(fakeGitLabHandler(t, &notes, &discussions))
	defer gl.Close()
	llmSrv := httptest.NewServer(fakeAnthropicHandler())
	defer llmSrv.Close()

	prov := llm.NewAnthropic(llm.AnthropicConfig{
		APIKey: "k", Model: "m", BaseURL: llmSrv.URL, HTTP: llmSrv.Client(),
	})
	client := gitlab.NewRESTClient(gl.URL, "tok", gl.Client())
	o := New(Config{
		GitLab: client, Provider: prov,
		MaxFileTokens: 4000, MaxMRTokens: 200000, MaxConcurrent: 2,
	})

	res, err := o.Run(context.Background(), gl.URL+"/grp/proj/-/merge_requests/9")
	require.NoError(t, err)
	require.Equal(t, 1, notes)
	require.Equal(t, 1, discussions)
	require.Equal(t, 1, res.Posted)
}

func TestE2E_MemoryRecallInjectedIntoProviderRequest(t *testing.T) {
	mem9Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"memories": []map[string]any{
				{"id": "m1", "content": "Always validate JWTs.", "score": 0.9},
			},
		})
	}))
	defer mem9Srv.Close()

	gl := &fakeGitLab{}
	prov := &recordingProvider{}

	mem9c := mem9.New(mem9.Config{BaseURL: mem9Srv.URL, APIKey: "k", HTTP: mem9Srv.Client()})
	mem9src := memory.NewMem9Source(mem9c, memory.Mem9Tuning{ConventionsTopK: 5, SummariesTopK: 5})
	composite := &memory.Composite{
		Sources:     []memory.Source{mem9src},
		Mem9:        mem9src,
		Extractor:   memory.NewExtractor(prov),
		TokenBudget: 5000,
	}
	o := New(Config{
		GitLab:        gl,
		Provider:      prov,
		Memory:        composite,
		MaxFileTokens: 4000,
		MaxMRTokens:   200000,
	})
	_, err := o.Run(context.Background(), "https://gitlab.example/g/r/-/merge_requests/1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(prov.lastReq.FileContext, "Always validate JWTs") {
		t.Fatalf("FileContext missing recalled rule: %q", prov.lastReq.FileContext)
	}
}

func TestE2E_MemoryDownDoesNotBlockReview(t *testing.T) {
	mem9Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer mem9Srv.Close()

	gl := &fakeGitLab{}
	prov := &recordingProvider{}
	mem9c := mem9.New(mem9.Config{BaseURL: mem9Srv.URL, APIKey: "k", HTTP: mem9Srv.Client()})
	mem9src := memory.NewMem9Source(mem9c, memory.Mem9Tuning{})
	composite := &memory.Composite{
		Sources:   []memory.Source{mem9src},
		Mem9:      mem9src,
		Extractor: memory.NewExtractor(prov),
	}
	o := New(Config{
		GitLab:        gl,
		Provider:      prov,
		Memory:        composite,
		MaxFileTokens: 4000,
		MaxMRTokens:   200000,
	})
	_, err := o.Run(context.Background(), "https://gitlab.example/g/r/-/merge_requests/1")
	if err != nil {
		t.Fatalf("expected nil err on memory failure: %v", err)
	}
}
