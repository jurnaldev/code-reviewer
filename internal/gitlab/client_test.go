package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRESTClient_GetMRChanges(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/team%2Fproject/merge_requests/42", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method)
		require.Equal(t, "tok", r.Header.Get("PRIVATE-TOKEN"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"iid": 42, "title": "x", "web_url": "u",
			"diff_refs": map[string]string{
				"base_sha": "B", "start_sha": "S", "head_sha": "H",
			},
		})
	})
	mux.HandleFunc("/api/v4/projects/team%2Fproject/merge_requests/42/changes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"changes": []map[string]any{
				{"old_path": "a.go", "new_path": "a.go", "diff": "@@ -1 +1 @@\n+x"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewRESTClient(srv.URL, "tok", srv.Client())
	mr, changes, err := c.GetMRWithChanges(context.Background(), "team/project", 42)
	require.NoError(t, err)
	require.Equal(t, "B", mr.BaseSHA)
	require.Equal(t, "H", mr.HeadSHA)
	require.Len(t, changes, 1)
	require.Equal(t, "a.go", changes[0].NewPath)
}

func TestRESTClient_PostNote(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/api/v4/projects/team%2Fp/merge_requests/3/notes", r.URL.EscapedPath())
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	c := NewRESTClient(srv.URL, "tok", srv.Client())
	require.NoError(t, c.PostNote(context.Background(), "team/p", 3, "hello"))
	require.Equal(t, "hello", captured["body"])
}

func TestRESTClient_PostDiscussion(t *testing.T) {
	var captured url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.NoError(t, r.ParseForm())
		captured = r.PostForm
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"d1"}`))
	}))
	defer srv.Close()

	c := NewRESTClient(srv.URL, "tok", srv.Client())
	pos := Position{BaseSHA: "B", StartSHA: "S", HeadSHA: "H", NewPath: "a.go", OldPath: "a.go", NewLine: 7, PositionType: "text"}
	require.NoError(t, c.PostDiscussion(context.Background(), "team/p", 3, "found a thing", pos))
	require.Equal(t, "found a thing", captured.Get("body"))
	require.Equal(t, "B", captured.Get("position[base_sha]"))
	require.Equal(t, "7", captured.Get("position[new_line]"))
}

func TestRESTClient_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"404 Not found"}`))
	}))
	defer srv.Close()

	c := NewRESTClient(srv.URL, "tok", srv.Client())
	_, _, err := c.GetMRWithChanges(context.Background(), "x/y", 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "404")
}
