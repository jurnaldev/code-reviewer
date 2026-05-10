package httpretry

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTransport_RetriesOn5xxThenSucceeds(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		require.Equal(t, "hello", string(body))
		if atomic.AddInt32(&n, 1) < 3 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := &http.Client{Transport: &Transport{Inner: http.DefaultTransport, Max: 3, Base: time.Millisecond}}
	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader("hello"))
	resp, err := c.Do(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	require.EqualValues(t, 3, atomic.LoadInt32(&n))
}

func TestTransport_RespectsRetryAfter(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.Header().Set("Retry-After", "1") // 1 second
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := &http.Client{Transport: &Transport{Inner: http.DefaultTransport, Max: 2, Base: time.Millisecond}}
	start := time.Now()
	resp, err := c.Do(mustReq(t, "GET", srv.URL, ""))
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	require.GreaterOrEqual(t, time.Since(start), 900*time.Millisecond, "must honor Retry-After")
}

func TestTransport_GivesUpAfterMax(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()

	c := &http.Client{Transport: &Transport{Inner: http.DefaultTransport, Max: 2, Base: time.Millisecond}}
	resp, err := c.Do(mustReq(t, "GET", srv.URL, ""))
	require.NoError(t, err)
	require.Equal(t, 503, resp.StatusCode)
}

func TestTransport_PassesThroughOn2xx(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := &http.Client{Transport: &Transport{Inner: http.DefaultTransport, Max: 3, Base: time.Millisecond}}
	resp, err := c.Do(mustReq(t, "GET", srv.URL, ""))
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	require.EqualValues(t, 1, atomic.LoadInt32(&n))
}

func mustReq(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	var r *http.Request
	var err error
	if body == "" {
		r, err = http.NewRequest(method, url, nil)
	} else {
		r, err = http.NewRequest(method, url, strings.NewReader(body))
	}
	require.NoError(t, err)
	return r
}
