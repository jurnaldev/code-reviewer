package mem9

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClient_Create_SendsAPIKeyAndJSON(t *testing.T) {
	var gotKey string
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "m_abc",
		})
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "key", HTTP: srv.Client(), Timeout: time.Second})
	id, err := c.Create(context.Background(), CreateInput{
		Content:  "Prefer X over Y",
		Tags:     []string{"project:g/r", "type:convention"},
		Metadata: map[string]string{"derived_from_mr_iid": "123"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != "m_abc" {
		t.Fatalf("id %q", id)
	}
	if gotKey != "key" {
		t.Fatalf("api key %q", gotKey)
	}
	if !strings.HasSuffix(gotPath, "/v1alpha2/mem9s/memories") {
		t.Fatalf("path %q", gotPath)
	}
	if gotBody["content"] != "Prefer X over Y" {
		t.Fatalf("content %v", gotBody["content"])
	}
}

func TestClient_Search_FiltersByTagsAndQuery(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"memories": []map[string]any{
				{"id": "m1", "content": "rule one", "score": 0.9, "tags": []string{"project:g/r", "type:convention"}},
				{"id": "m2", "content": "rule two", "score": 0.7, "tags": []string{"project:g/r", "type:convention"}},
			},
		})
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, APIKey: "k", HTTP: srv.Client()})
	out, err := c.Search(context.Background(), SearchInput{
		Query: "auth",
		Tags:  []string{"project:g/r", "type:convention"},
		Mode:  "hybrid",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d hits", len(out))
	}
	if out[0].ID != "m1" || out[0].Content != "rule one" {
		t.Fatalf("hit0 %+v", out[0])
	}
	if !strings.Contains(gotURL, "q=auth") {
		t.Fatalf("missing q in %q", gotURL)
	}
	if !strings.Contains(gotURL, "limit=5") {
		t.Fatalf("missing limit in %q", gotURL)
	}
}

func TestClient_Update_PUT(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, APIKey: "k", HTTP: srv.Client()})
	if err := c.Update(context.Background(), "m1", CreateInput{Content: "new"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if gotMethod != "PUT" {
		t.Fatalf("method %q", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/v1alpha2/mem9s/memories/m1") {
		t.Fatalf("path %q", gotPath)
	}
}

func TestClient_Search_Returns_5xxAsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, APIKey: "k", HTTP: srv.Client()})
	_, err := c.Search(context.Background(), SearchInput{})
	if err == nil {
		t.Fatalf("expected err on 500")
	}
}
