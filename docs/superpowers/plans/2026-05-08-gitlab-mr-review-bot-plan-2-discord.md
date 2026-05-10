# GitLab MR Review Bot — Plan 2: Discord Layer

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wrap the Plan 1 core review engine in a Discord bot. Users invoke `/review <mr-url>` in Discord; the bot creates a job, defers the interaction, runs the orchestrator in a goroutine, edits the interaction message with status updates, and posts a final summary linking back to the GitLab MR. Adds an HTTP retry transport (used for both GitLab and Anthropic clients) since the Plan 1 spec listed retry/backoff under "deferred."

**Architecture:** Single Go binary `cmd/bot`. Pure helpers (URL parse, allowlist, dup-check) testable without a Discord session. A `SessionAPI` interface narrows the discordgo session to what the bot uses, so handler tests can run against a fake session. A new `httpretry` package wraps `http.RoundTripper` with exponential backoff + `Retry-After`. The review orchestrator gains a `RunWithProgress` method so the bot can stream status text into the interaction message; existing `Run(ctx, mrURL)` keeps working for `review-cli`.

**Tech Stack:** Go (existing module), `github.com/bwmarrin/discordgo` for the Discord client, `github.com/google/uuid` for job IDs. Tests use `net/http/httptest`, fake session implementing `SessionAPI`, and the existing `testify/require`.

---

## File Structure

```
internal/
  httpretry/
    transport.go            # RoundTripper wrapper: 429/5xx retry, Retry-After, exp backoff
    transport_test.go
  review/
    orchestrator.go         # MODIFY: add RunWithProgress; Run delegates with nil progress
    orchestrator_test.go    # ADD: TestOrchestrator_Progress
  jobs/
    jobs.go                 # Tracker, Job, Status; Create/Get/Update/FindActive/Cleanup
    jobs_test.go
  discord/
    session.go              # SessionAPI interface + adapter satisfaction check
    helpers.go              # ValidateRequest (URL parse, allowlist, dup) — pure, testable
    helpers_test.go
    bot.go                  # Bot struct, HandleInteraction, runJob goroutine, status ticker
    bot_test.go             # uses fake SessionAPI
  config/
    config.go               # MODIFY: add Discord struct, defaults, validation
    config_test.go          # ADD: TestLoad_DiscordRequired
cmd/
  bot/
    main.go                 # NEW: load config, wire retry transport + clients + bot, start session
  review-cli/
    main.go                 # MODIFY: also use httpretry transport (1-line addition)
config.example.yaml         # MODIFY: add discord section
```

---

## Task 1: HTTP retry transport

**Files:**
- Create: `internal/httpretry/transport.go`
- Create: `internal/httpretry/transport_test.go`

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test, confirm fail**

Run: `go test ./internal/httpretry/...`
Expected: FAIL — package undefined.

- [ ] **Step 3: Implement `transport.go`**

```go
package httpretry

import (
	"net/http"
	"strconv"
	"time"
)

// Transport retries 429 and 5xx responses with exponential backoff,
// honoring the Retry-After header when present.
type Transport struct {
	Inner http.RoundTripper
	Max   int           // max retries (excluding the initial attempt)
	Base  time.Duration // base backoff (doubles each retry)
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	inner := t.Inner
	if inner == nil {
		inner = http.DefaultTransport
	}

	var lastResp *http.Response
	var lastErr error
	delay := t.Base
	if delay <= 0 {
		delay = 100 * time.Millisecond
	}

	for attempt := 0; attempt <= t.Max; attempt++ {
		// Reset body for retries when GetBody is set (set automatically by NewRequest
		// for *bytes.Buffer / *bytes.Reader / *strings.Reader bodies).
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}

		resp, err := inner.RoundTrip(req)
		if err != nil {
			lastErr = err
			if !sleep(req, delay) {
				return nil, err
			}
			delay *= 2
			continue
		}

		if !shouldRetry(resp.StatusCode) {
			return resp, nil
		}

		// Retryable status. If we have retries left, drain + close + sleep.
		lastResp = resp
		if attempt == t.Max {
			return resp, nil
		}
		wait := delay
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
				wait = time.Duration(secs) * time.Second
			}
		}
		_ = resp.Body.Close()
		if !sleep(req, wait) {
			return resp, nil
		}
		delay *= 2
	}

	if lastResp != nil {
		return lastResp, nil
	}
	return nil, lastErr
}

func shouldRetry(code int) bool {
	return code == 429 || (code >= 500 && code <= 599)
}

func sleep(req *http.Request, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-req.Context().Done():
		return false
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/httpretry/... -v`
Expected: 4 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/httpretry
git commit -m "feat(httpretry): RoundTripper with backoff + Retry-After"
```

---

## Task 2: Progress callback on orchestrator

**Files:**
- Modify: `internal/review/orchestrator.go`
- Modify: `internal/review/orchestrator_test.go` (add 1 test)
- Modify: `cmd/review-cli/main.go` (no behavior change; still calls `o.Run`)

- [ ] **Step 1: Append the failing test to `internal/review/orchestrator_test.go`**

Add at end of file:

```go
func TestOrchestrator_RunWithProgress_EmitsStages(t *testing.T) {
	gl := &fakeGL{}
	prov := &fakeProvider{findings: []llm.Finding{
		{Severity: "minor", Category: "style", File: "a.go", Line: 1, Message: "m"},
	}}
	o := New(Config{
		GitLab:        gl,
		Provider:      prov,
		MaxFileTokens: 4000,
		MaxMRTokens:   200000,
		MaxConcurrent: 1,
	})

	var stages []string
	progress := func(stage, msg string) {
		stages = append(stages, stage)
	}
	_, err := o.RunWithProgress(context.Background(), "https://gl/grp/proj/-/merge_requests/9", progress)
	require.NoError(t, err)
	require.Contains(t, stages, "fetching")
	require.Contains(t, stages, "reviewing")
	require.Contains(t, stages, "posting")
	require.Equal(t, "done", stages[len(stages)-1])
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./internal/review/... -run RunWithProgress`
Expected: FAIL — `RunWithProgress` undefined.

- [ ] **Step 3: Modify `internal/review/orchestrator.go`**

Replace the existing `Run` method with:

```go
type ProgressFn func(stage, msg string)

func (o *Orchestrator) Run(ctx context.Context, mrURL string) (*RunResult, error) {
	return o.RunWithProgress(ctx, mrURL, nil)
}

func (o *Orchestrator) RunWithProgress(ctx context.Context, mrURL string, progress ProgressFn) (*RunResult, error) {
	emit := func(stage, msg string) {
		if progress != nil {
			progress(stage, msg)
		}
	}

	ref, err := gitlab.ParseURL(mrURL)
	if err != nil {
		return nil, err
	}

	emit("fetching", "fetching MR")
	mr, changes, err := o.cfg.GitLab.GetMRWithChanges(ctx, ref.ProjectPath, ref.MRIID)
	if err != nil {
		return nil, fmt.Errorf("fetch MR: %w", err)
	}

	type job struct {
		path        string
		hunksByLine map[int]bool
		chunk       chunker.FileChunk
	}
	var jobs []job
	totalTokens := 0
	for _, ch := range changes {
		if ch.DeletedFile {
			continue
		}
		ignored, err := classifier.IsIgnored(ch.NewPath, o.cfg.IgnoreGlobs)
		if err != nil {
			return nil, err
		}
		if ignored || classifier.IsLockfile(ch.NewPath) {
			continue
		}
		full := fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n%s",
			ch.OldPath, ch.NewPath, ch.OldPath, ch.NewPath, ch.Diff)
		pd, err := diff.Parse(full)
		if err != nil {
			continue
		}
		if len(pd.Files) == 0 {
			continue
		}
		fd := pd.Files[0]
		if classifier.IsBinary(fd) {
			continue
		}
		validLines := map[int]bool{}
		for _, h := range fd.Hunks {
			for _, ln := range h.Lines {
				if ln.Kind == '+' || ln.Kind == ' ' {
					validLines[ln.NewLineNo] = true
				}
			}
		}
		for _, c := range chunker.Chunk(fd, o.cfg.MaxFileTokens) {
			t := chunker.EstimateTokens(c.DiffText)
			if totalTokens+t > o.cfg.MaxMRTokens {
				break
			}
			totalTokens += t
			jobs = append(jobs, job{path: ch.NewPath, hunksByLine: validLines, chunk: c})
		}
	}

	emit("reviewing", fmt.Sprintf("reviewing %d chunks", len(jobs)))

	sem := make(chan struct{}, o.cfg.MaxConcurrent)
	var (
		mu       sync.Mutex
		findings []llm.Finding
		errsAny  error
		done     int
	)
	var wg sync.WaitGroup
	total := len(jobs)
	for _, j := range jobs {
		j := j
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			resp, err := o.cfg.Provider.Review(ctx, llm.ReviewRequest{
				SystemPrompt: o.cfg.SystemPrompt,
				FilePath:     j.path,
				DiffChunk:    j.chunk.DiffText,
			})
			mu.Lock()
			defer mu.Unlock()
			done++
			emit("reviewing", fmt.Sprintf("%d/%d chunks reviewed", done, total))
			if err != nil {
				errsAny = err
				return
			}
			for _, f := range resp.Findings {
				if f.File == "" {
					f.File = j.path
				}
				findings = append(findings, f)
			}
		}()
	}
	wg.Wait()

	lineMap := map[string]map[int]bool{}
	for _, j := range jobs {
		if _, ok := lineMap[j.path]; !ok {
			lineMap[j.path] = map[int]bool{}
		}
		for ln := range j.hunksByLine {
			lineMap[j.path][ln] = true
		}
	}

	agg := Aggregate(findings)

	emit("posting", "posting summary")
	if err := o.cfg.GitLab.PostNote(ctx, ref.ProjectPath, ref.MRIID, agg.SummaryBody); err != nil {
		return nil, fmt.Errorf("post summary: %w", err)
	}

	posted := 0
	skipped := 0
	for _, f := range agg.Findings {
		valid := lineMap[f.File] != nil && lineMap[f.File][f.Line]
		if !valid {
			skipped++
			continue
		}
		body := f.Message
		if f.Suggestion != "" {
			body += "\n\n```suggestion\n" + f.Suggestion + "\n```"
		}
		pos := gitlab.Position{
			BaseSHA: mr.BaseSHA, StartSHA: mr.StartSHA, HeadSHA: mr.HeadSHA,
			NewPath: f.File, OldPath: f.File,
			NewLine: f.Line, PositionType: "text",
		}
		if err := o.cfg.GitLab.PostDiscussion(ctx, ref.ProjectPath, ref.MRIID, body, pos); err != nil {
			skipped++
			continue
		}
		posted++
	}
	_ = errsAny

	emit("done", fmt.Sprintf("posted=%d skipped=%d findings=%d", posted, skipped, len(agg.Findings)))

	return &RunResult{
		Findings: len(agg.Findings),
		Posted:   posted,
		Skipped:  skipped,
		WebURL:   mr.WebURL,
		Counts:   agg.Counts,
	}, nil
}
```

(Imports unchanged from existing file: `context`, `fmt`, `sync`, plus the project imports for `chunker`, `classifier`, `diff`, `gitlab`, `llm`.)

- [ ] **Step 4: Run all tests in review package**

Run: `go test ./internal/review/... -v`
Expected: 5 PASS (4 existing + new RunWithProgress test). Existing tests must still pass — `Run` delegates to `RunWithProgress` with nil progress.

- [ ] **Step 5: Run full repo tests**

Run: `go test ./... -count=1`
Expected: ALL PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/review
git commit -m "feat(review): RunWithProgress streams stage/msg callbacks"
```

---

## Task 3: Jobs tracker

**Files:**
- Create: `internal/jobs/jobs.go`
- Create: `internal/jobs/jobs_test.go`

- [ ] **Step 1: Add uuid dep**

Run: `go get github.com/google/uuid`
Expected: go.mod updated.

- [ ] **Step 2: Write failing test `internal/jobs/jobs_test.go`**

```go
package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTracker_CreateAssignsIDAndStatus(t *testing.T) {
	tr := New()
	j := tr.Create("user-1", "https://gl/x/y/-/merge_requests/3")
	require.NotEmpty(t, j.ID)
	require.Equal(t, StatusQueued, j.Status)
	require.Equal(t, "user-1", j.UserID)
	require.WithinDuration(t, time.Now(), j.StartedAt, 2*time.Second)
}

func TestTracker_GetReturnsCopy(t *testing.T) {
	tr := New()
	j := tr.Create("u", "u")
	got, ok := tr.Get(j.ID)
	require.True(t, ok)
	require.Equal(t, j.ID, got.ID)

	got.Status = StatusError // mutate copy
	again, _ := tr.Get(j.ID)
	require.Equal(t, StatusQueued, again.Status, "Get must return a copy")
}

func TestTracker_UpdateAtomic(t *testing.T) {
	tr := New()
	j := tr.Create("u", "u")
	tr.Update(j.ID, func(jj *Job) {
		jj.Status = StatusReviewing
		jj.Progress = "1/3"
	})
	got, _ := tr.Get(j.ID)
	require.Equal(t, StatusReviewing, got.Status)
	require.Equal(t, "1/3", got.Progress)
}

func TestTracker_FindActiveByMRURL(t *testing.T) {
	tr := New()
	j := tr.Create("u", "https://gl/x/y/-/merge_requests/3")

	got, ok := tr.FindActiveByMR("https://gl/x/y/-/merge_requests/3")
	require.True(t, ok)
	require.Equal(t, j.ID, got.ID)

	tr.Update(j.ID, func(jj *Job) { jj.Status = StatusDone })
	_, ok = tr.FindActiveByMR("https://gl/x/y/-/merge_requests/3")
	require.False(t, ok, "completed jobs should not match active lookup")
}

func TestTracker_CleanupRemovesExpired(t *testing.T) {
	tr := New()
	j := tr.Create("u", "u")
	tr.Update(j.ID, func(jj *Job) {
		jj.Status = StatusDone
		jj.EndedAt = time.Now().Add(-2 * time.Hour)
	})
	tr.cleanup(time.Hour)
	_, ok := tr.Get(j.ID)
	require.False(t, ok)
}

func TestTracker_StartCleanerStops(t *testing.T) {
	tr := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tr.StartCleaner(ctx, time.Hour, 50*time.Millisecond)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleaner did not stop on context cancel")
	}
}
```

- [ ] **Step 3: Run, confirm fail**

Run: `go test ./internal/jobs/...`
Expected: FAIL.

- [ ] **Step 4: Implement `internal/jobs/jobs.go`**

```go
package jobs

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusFetching  Status = "fetching"
	StatusReviewing Status = "reviewing"
	StatusPosting   Status = "posting"
	StatusDone      Status = "done"
	StatusError     Status = "error"
)

type Job struct {
	ID         string
	UserID     string
	MRURL      string
	Status     Status
	Progress   string
	StartedAt  time.Time
	EndedAt    time.Time
	Findings   int
	Posted     int
	WebURL     string
	ErrMessage string
}

type Tracker struct {
	mu sync.RWMutex
	m  map[string]*Job
}

func New() *Tracker { return &Tracker{m: map[string]*Job{}} }

func (t *Tracker) Create(userID, mrURL string) Job {
	t.mu.Lock()
	defer t.mu.Unlock()
	j := &Job{
		ID:        uuid.NewString(),
		UserID:    userID,
		MRURL:     mrURL,
		Status:    StatusQueued,
		StartedAt: time.Now(),
	}
	t.m[j.ID] = j
	return *j
}

func (t *Tracker) Get(id string) (Job, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	j, ok := t.m[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

func (t *Tracker) Update(id string, fn func(*Job)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	j, ok := t.m[id]
	if !ok {
		return
	}
	fn(j)
}

func (t *Tracker) FindActiveByMR(mrURL string) (Job, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, j := range t.m {
		if j.MRURL == mrURL && isActive(j.Status) {
			return *j, true
		}
	}
	return Job{}, false
}

func isActive(s Status) bool {
	switch s {
	case StatusQueued, StatusFetching, StatusReviewing, StatusPosting:
		return true
	}
	return false
}

func (t *Tracker) cleanup(ttl time.Duration) {
	cutoff := time.Now().Add(-ttl)
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, j := range t.m {
		if !isActive(j.Status) && !j.EndedAt.IsZero() && j.EndedAt.Before(cutoff) {
			delete(t.m, id)
		}
	}
}

func (t *Tracker) StartCleaner(ctx context.Context, ttl, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.cleanup(ttl)
		}
	}
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/jobs/... -v`
Expected: 6 PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/jobs go.mod go.sum
git commit -m "feat(jobs): in-memory tracker with TTL cleanup"
```

---

## Task 4: Discord helpers + SessionAPI

**Files:**
- Create: `internal/discord/session.go`
- Create: `internal/discord/helpers.go`
- Create: `internal/discord/helpers_test.go`

- [ ] **Step 1: Add discordgo dep**

Run: `go get github.com/bwmarrin/discordgo`
Expected: go.mod updated.

- [ ] **Step 2: Write `internal/discord/session.go`**

```go
package discord

import "github.com/bwmarrin/discordgo"

// SessionAPI is the subset of *discordgo.Session the bot uses.
// A real *discordgo.Session satisfies this interface (compile-time checked below).
type SessionAPI interface {
	InteractionRespond(i *discordgo.Interaction, r *discordgo.InteractionResponse, options ...discordgo.RequestOption) error
	InteractionResponseEdit(i *discordgo.Interaction, r *discordgo.WebhookEdit, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ApplicationCommandBulkOverwrite(appID, guildID string, commands []*discordgo.ApplicationCommand, options ...discordgo.RequestOption) ([]*discordgo.ApplicationCommand, error)
}

var _ SessionAPI = (*discordgo.Session)(nil)
```

- [ ] **Step 3: Write the failing test `internal/discord/helpers_test.go`**

```go
package discord

import (
	"testing"

	"github.com/fahmi/gitlab-mr-review-bot/internal/jobs"
	"github.com/stretchr/testify/require"
)

func TestValidateRequest_OK(t *testing.T) {
	tr := jobs.New()
	v := Validator{
		Tracker:        tr,
		AllowedUserIDs: nil, // empty allowlist => everyone
		AllowedRoleIDs: nil,
	}
	res, err := v.Validate(Request{
		UserID:  "u1",
		RoleIDs: []string{"r1"},
		MRURL:   "https://gl.example.com/team/proj/-/merge_requests/3",
	})
	require.NoError(t, err)
	require.Equal(t, "team/proj", res.ProjectPath)
	require.Equal(t, 3, res.MRIID)
}

func TestValidateRequest_BadURL(t *testing.T) {
	tr := jobs.New()
	v := Validator{Tracker: tr}
	_, err := v.Validate(Request{UserID: "u1", MRURL: "not a url"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrBadURL)
}

func TestValidateRequest_NotAllowedUser(t *testing.T) {
	tr := jobs.New()
	v := Validator{
		Tracker:        tr,
		AllowedUserIDs: map[string]bool{"alice": true},
	}
	_, err := v.Validate(Request{
		UserID: "bob",
		MRURL:  "https://gl/x/y/-/merge_requests/1",
	})
	require.ErrorIs(t, err, ErrNotAllowed)
}

func TestValidateRequest_AllowedByRole(t *testing.T) {
	tr := jobs.New()
	v := Validator{
		Tracker:        tr,
		AllowedRoleIDs: map[string]bool{"reviewers": true},
	}
	_, err := v.Validate(Request{
		UserID:  "bob",
		RoleIDs: []string{"reviewers"},
		MRURL:   "https://gl/x/y/-/merge_requests/1",
	})
	require.NoError(t, err)
}

func TestValidateRequest_Duplicate(t *testing.T) {
	tr := jobs.New()
	tr.Create("u", "https://gl/x/y/-/merge_requests/1")
	v := Validator{Tracker: tr}
	_, err := v.Validate(Request{
		UserID: "u",
		MRURL:  "https://gl/x/y/-/merge_requests/1",
	})
	require.ErrorIs(t, err, ErrDuplicate)
}
```

- [ ] **Step 4: Run, confirm fail**

Run: `go test ./internal/discord/...`
Expected: FAIL — package undefined.

- [ ] **Step 5: Implement `internal/discord/helpers.go`**

```go
package discord

import (
	"errors"
	"fmt"

	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/jobs"
)

var (
	ErrBadURL     = errors.New("invalid MR URL")
	ErrNotAllowed = errors.New("user not allowed")
	ErrDuplicate  = errors.New("review already in progress for this MR")
)

type Request struct {
	UserID  string
	RoleIDs []string
	MRURL   string
}

type ValidatedRequest struct {
	ProjectPath string
	MRIID       int
	BaseURL     string
}

type Validator struct {
	Tracker        *jobs.Tracker
	AllowedUserIDs map[string]bool // empty => allow everyone
	AllowedRoleIDs map[string]bool // empty => no role restriction
}

func (v Validator) Validate(r Request) (ValidatedRequest, error) {
	if !v.allowed(r.UserID, r.RoleIDs) {
		return ValidatedRequest{}, ErrNotAllowed
	}
	ref, err := gitlab.ParseURL(r.MRURL)
	if err != nil {
		return ValidatedRequest{}, fmt.Errorf("%w: %v", ErrBadURL, err)
	}
	if _, ok := v.Tracker.FindActiveByMR(r.MRURL); ok {
		return ValidatedRequest{}, ErrDuplicate
	}
	return ValidatedRequest{
		ProjectPath: ref.ProjectPath,
		MRIID:       ref.MRIID,
		BaseURL:     ref.BaseURL,
	}, nil
}

func (v Validator) allowed(userID string, roleIDs []string) bool {
	hasUserList := len(v.AllowedUserIDs) > 0
	hasRoleList := len(v.AllowedRoleIDs) > 0
	if !hasUserList && !hasRoleList {
		return true
	}
	if hasUserList && v.AllowedUserIDs[userID] {
		return true
	}
	if hasRoleList {
		for _, rid := range roleIDs {
			if v.AllowedRoleIDs[rid] {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/discord/... -v`
Expected: 5 PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/discord go.mod go.sum
git commit -m "feat(discord): SessionAPI interface + request validator"
```

---

## Task 5: Bot interaction handler

**Files:**
- Create: `internal/discord/bot.go`
- Create: `internal/discord/bot_test.go`

- [ ] **Step 1: Write the failing test**

```go
package discord

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/fahmi/gitlab-mr-review-bot/internal/jobs"
	"github.com/fahmi/gitlab-mr-review-bot/internal/review"
	"github.com/stretchr/testify/require"
)

type fakeRunner struct {
	mu       sync.Mutex
	called   bool
	stages   []string
	wantErr  error
	wantRes  *review.RunResult
}

func (f *fakeRunner) RunWithProgress(ctx context.Context, mrURL string, p review.ProgressFn) (*review.RunResult, error) {
	f.mu.Lock()
	f.called = true
	f.mu.Unlock()
	if p != nil {
		p("fetching", "fetching MR")
		p("reviewing", "1/1 chunks reviewed")
		p("posting", "posting summary")
		p("done", "posted=1 skipped=0 findings=1")
	}
	return f.wantRes, f.wantErr
}

type fakeSession struct {
	mu          sync.Mutex
	respCalls   int
	editCalls   int
	lastContent string
}

func (s *fakeSession) InteractionRespond(i *discordgo.Interaction, r *discordgo.InteractionResponse, _ ...discordgo.RequestOption) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.respCalls++
	return nil
}
func (s *fakeSession) InteractionResponseEdit(i *discordgo.Interaction, r *discordgo.WebhookEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.editCalls++
	if r.Content != nil {
		s.lastContent = *r.Content
	}
	return &discordgo.Message{}, nil
}
func (s *fakeSession) ApplicationCommandBulkOverwrite(string, string, []*discordgo.ApplicationCommand, ...discordgo.RequestOption) ([]*discordgo.ApplicationCommand, error) {
	return nil, nil
}

func mkInteraction(userID, mrURL string) *discordgo.Interaction {
	return &discordgo.Interaction{
		ID: "iid", Token: "tok", AppID: "app",
		Member: &discordgo.Member{User: &discordgo.User{ID: userID}, Roles: nil},
		Type:   discordgo.InteractionApplicationCommand,
		Data: discordgo.ApplicationCommandInteractionData{
			Name: "review",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "url", Type: discordgo.ApplicationCommandOptionString, Value: mrURL},
			},
		},
	}
}

func TestBot_HappyPath(t *testing.T) {
	sess := &fakeSession{}
	tr := jobs.New()
	runner := &fakeRunner{wantRes: &review.RunResult{Posted: 1, Skipped: 0, Findings: 1, WebURL: "https://gl/x"}}

	b := &Bot{
		Session: sess, Runner: runner, Jobs: tr,
		Validator:  Validator{Tracker: tr},
		TickEvery:  20 * time.Millisecond,
		JobTimeout: 2 * time.Second,
	}

	done := make(chan struct{})
	b.OnJobDone = func() { close(done) }

	b.HandleInteraction(mkInteraction("u1", "https://gl.example.com/team/proj/-/merge_requests/4"))

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not complete in time")
	}

	require.True(t, runner.called)
	require.GreaterOrEqual(t, sess.respCalls, 1, "must defer ack")
	require.GreaterOrEqual(t, sess.editCalls, 1, "must edit final message")
	require.Contains(t, sess.lastContent, "posted=1")
}

func TestBot_RejectsBadURL(t *testing.T) {
	sess := &fakeSession{}
	tr := jobs.New()
	b := &Bot{Session: sess, Runner: &fakeRunner{}, Jobs: tr, Validator: Validator{Tracker: tr}, TickEvery: time.Second, JobTimeout: time.Second}

	b.HandleInteraction(mkInteraction("u1", "garbage"))

	require.Equal(t, 1, sess.respCalls)
	require.Equal(t, 0, sess.editCalls)
}

func TestBot_RejectsDuplicate(t *testing.T) {
	sess := &fakeSession{}
	tr := jobs.New()
	tr.Create("u1", "https://gl.example.com/team/proj/-/merge_requests/4")

	b := &Bot{Session: sess, Runner: &fakeRunner{}, Jobs: tr, Validator: Validator{Tracker: tr}, TickEvery: time.Second, JobTimeout: time.Second}

	b.HandleInteraction(mkInteraction("u1", "https://gl.example.com/team/proj/-/merge_requests/4"))

	require.Equal(t, 1, sess.respCalls)
	require.Equal(t, 0, sess.editCalls)
}

func TestBot_RunnerError(t *testing.T) {
	sess := &fakeSession{}
	tr := jobs.New()
	runner := &fakeRunner{wantErr: errors.New("boom")}
	b := &Bot{Session: sess, Runner: runner, Jobs: tr, Validator: Validator{Tracker: tr}, TickEvery: 20 * time.Millisecond, JobTimeout: time.Second}

	done := make(chan struct{})
	b.OnJobDone = func() { close(done) }

	b.HandleInteraction(mkInteraction("u1", "https://gl.example.com/team/proj/-/merge_requests/4"))

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not complete")
	}
	require.Contains(t, sess.lastContent, "error")
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./internal/discord/... -run Bot`
Expected: FAIL — Bot, Runner, etc. undefined.

- [ ] **Step 3: Implement `internal/discord/bot.go`**

```go
package discord

import (
	"context"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/fahmi/gitlab-mr-review-bot/internal/jobs"
	"github.com/fahmi/gitlab-mr-review-bot/internal/review"
)

// Runner is satisfied by *review.Orchestrator.
type Runner interface {
	RunWithProgress(ctx context.Context, mrURL string, p review.ProgressFn) (*review.RunResult, error)
}

type Bot struct {
	Session    SessionAPI
	Runner     Runner
	Jobs       *jobs.Tracker
	Validator  Validator
	TickEvery  time.Duration
	JobTimeout time.Duration

	// OnJobDone is called when a job finishes (success or error). Used by tests
	// to synchronize; production code can leave it nil.
	OnJobDone func()
}

// HandleInteraction dispatches the /review slash command. Replies are deferred
// (Discord 3-second ack rule) and the message is edited as the job progresses.
func (b *Bot) HandleInteraction(i *discordgo.Interaction) {
	mrURL, ok := extractURLOption(i)
	if !ok {
		b.replyEphemeral(i, "usage: /review url:<merge-request-url>")
		return
	}
	userID, roles := principalFrom(i)
	vr, err := b.Validator.Validate(Request{UserID: userID, RoleIDs: roles, MRURL: mrURL})
	if err != nil {
		b.replyEphemeral(i, "rejected: "+err.Error())
		return
	}

	job := b.Jobs.Create(userID, mrURL)
	_ = vr // used downstream when GitLab call needs project/iid; orchestrator parses URL itself

	// Defer the interaction so we have up to 15 minutes to edit it.
	if err := b.Session.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		b.Jobs.Update(job.ID, func(j *jobs.Job) {
			j.Status = jobs.StatusError
			j.ErrMessage = err.Error()
			j.EndedAt = time.Now()
		})
		return
	}

	go b.runJob(i, job.ID, mrURL)
}

func (b *Bot) runJob(i *discordgo.Interaction, jobID, mrURL string) {
	defer func() {
		if b.OnJobDone != nil {
			b.OnJobDone()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), b.JobTimeout)
	defer cancel()

	tickerCtx, tickerCancel := context.WithCancel(ctx)
	defer tickerCancel()
	go b.statusTicker(tickerCtx, i, jobID)

	progress := func(stage, msg string) {
		b.Jobs.Update(jobID, func(j *jobs.Job) {
			j.Status = jobs.Status(stage)
			j.Progress = msg
		})
	}

	res, err := b.Runner.RunWithProgress(ctx, mrURL, progress)
	tickerCancel()

	if err != nil {
		b.Jobs.Update(jobID, func(j *jobs.Job) {
			j.Status = jobs.StatusError
			j.ErrMessage = err.Error()
			j.EndedAt = time.Now()
		})
		b.editFinal(i, fmt.Sprintf(":x: review failed — error: %s", err.Error()))
		return
	}

	b.Jobs.Update(jobID, func(j *jobs.Job) {
		j.Status = jobs.StatusDone
		j.Findings = res.Findings
		j.Posted = res.Posted
		j.WebURL = res.WebURL
		j.EndedAt = time.Now()
	})

	final := fmt.Sprintf(":white_check_mark: review done — posted=%d skipped=%d findings=%d\n%s",
		res.Posted, res.Skipped, res.Findings, res.WebURL)
	b.editFinal(i, final)
}

func (b *Bot) statusTicker(ctx context.Context, i *discordgo.Interaction, jobID string) {
	t := time.NewTicker(b.TickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j, ok := b.Jobs.Get(jobID)
			if !ok {
				return
			}
			b.editFinal(i, fmt.Sprintf("status: %s — %s", j.Status, j.Progress))
		}
	}
}

func (b *Bot) editFinal(i *discordgo.Interaction, content string) {
	c := content
	_, _ = b.Session.InteractionResponseEdit(i, &discordgo.WebhookEdit{Content: &c})
}

func (b *Bot) replyEphemeral(i *discordgo.Interaction, content string) {
	_ = b.Session.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func extractURLOption(i *discordgo.Interaction) (string, bool) {
	data, ok := i.Data.(discordgo.ApplicationCommandInteractionData)
	if !ok {
		return "", false
	}
	for _, opt := range data.Options {
		if opt.Name == "url" {
			s, ok := opt.Value.(string)
			if !ok || s == "" {
				return "", false
			}
			return s, true
		}
	}
	return "", false
}

func principalFrom(i *discordgo.Interaction) (string, []string) {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.ID, i.Member.Roles
	}
	if i.User != nil {
		return i.User.ID, nil
	}
	return "", nil
}

```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/discord/... -v`
Expected: ALL PASS (helpers tests + 4 bot tests).

- [ ] **Step 5: Run full repo**

Run: `go test ./... -count=1`
Expected: ALL PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/discord
git commit -m "feat(discord): bot interaction handler with status ticker"
```

---

## Task 6: Discord config + slash command registration

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `config.example.yaml`
- Create: `internal/discord/register.go`
- Create: `internal/discord/register_test.go`

- [ ] **Step 1: Append the failing config test to `internal/config/config_test.go`**

Add at end of file:

```go
func TestLoad_DiscordRequired(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "g")
	t.Setenv("ANTHROPIC_API_KEY", "k")
	t.Setenv("DISCORD_TOKEN", "dt")
	t.Setenv("DISCORD_APP_ID", "did")

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
discord:
  token: env:DISCORD_TOKEN
  app_id: env:DISCORD_APP_ID
  guild_id: ""
  allowed_user_ids: ["alice"]
gitlab: {base_url: https://gl, token: env:GITLAB_TOKEN}
llm: {provider: anthropic, model: m, api_key: env:ANTHROPIC_API_KEY}
`), 0644))

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "dt", cfg.Discord.Token)
	require.Equal(t, "did", cfg.Discord.AppID)
	require.Equal(t, []string{"alice"}, cfg.Discord.AllowedUserIDs)
}

func TestLoad_DiscordTokenRequired(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "g")
	t.Setenv("ANTHROPIC_API_KEY", "k")

	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
discord: {token: "", app_id: did}
gitlab: {base_url: https://gl, token: env:GITLAB_TOKEN}
llm: {provider: anthropic, model: m, api_key: env:ANTHROPIC_API_KEY}
`), 0644))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "discord.token")
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./internal/config/... -run Discord`
Expected: FAIL — `Discord` field undefined.

- [ ] **Step 3: Modify `internal/config/config.go`**

Add the Discord struct + field. Edit the file as follows.

Add to the imports (no change if already present): `"strings"` is already imported.

Add struct field to `Config`:

```go
type Config struct {
	Discord Discord `yaml:"discord"`
	GitLab  GitLab  `yaml:"gitlab"`
	LLM     LLM     `yaml:"llm"`
	Review  Review  `yaml:"review"`
}
```

Add new struct after `GitLab`:

```go
type Discord struct {
	Token          string   `yaml:"token"`
	AppID          string   `yaml:"app_id"`
	GuildID        string   `yaml:"guild_id"`
	AllowedUserIDs []string `yaml:"allowed_user_ids"`
	AllowedRoleIDs []string `yaml:"allowed_role_ids"`
}
```

Update `interpEnvFields` to also interpolate Discord fields:

```go
func interpEnvFields(c *Config) error {
	for _, p := range []*string{&c.GitLab.Token, &c.LLM.APIKey, &c.Discord.Token, &c.Discord.AppID} {
		v, err := interp(*p)
		if err != nil {
			return err
		}
		*p = v
	}
	return nil
}
```

Update `validate` to require Discord token + app id:

```go
func validate(c *Config) error {
	if c.Discord.Token == "" {
		return fmt.Errorf("discord.token required")
	}
	if c.Discord.AppID == "" {
		return fmt.Errorf("discord.app_id required")
	}
	if c.GitLab.BaseURL == "" {
		return fmt.Errorf("gitlab.base_url required")
	}
	if c.GitLab.Token == "" {
		return fmt.Errorf("gitlab.token required")
	}
	if !allowedProviders[c.LLM.Provider] {
		return fmt.Errorf("llm.provider %q not in {anthropic, openai, ollama}", c.LLM.Provider)
	}
	if c.LLM.Model == "" {
		return fmt.Errorf("llm.model required")
	}
	if c.LLM.APIKey == "" && c.LLM.Provider != "ollama" {
		return fmt.Errorf("llm.api_key required for provider %q", c.LLM.Provider)
	}
	return nil
}
```

- [ ] **Step 4: Update existing config tests to include discord block**

The existing `TestLoad_DefaultsAndEnvInterp`, `TestLoad_MissingEnvFails`, and `TestLoad_RejectsUnknownProvider` will start failing because validation now requires Discord. Edit each test's YAML to include a minimal valid discord block:

In `TestLoad_DefaultsAndEnvInterp`, add `t.Setenv("DISCORD_TOKEN", "dt"); t.Setenv("DISCORD_APP_ID", "da")` and prepend to the YAML body:

```
discord:
  token: env:DISCORD_TOKEN
  app_id: env:DISCORD_APP_ID
```

In `TestLoad_MissingEnvFails`, prepend:

```
discord: {token: x, app_id: y}
```

In `TestLoad_RejectsUnknownProvider`, prepend:

```
discord: {token: x, app_id: y}
```

- [ ] **Step 5: Run config tests**

Run: `go test ./internal/config/... -v`
Expected: 5 PASS (3 updated + 2 new).

- [ ] **Step 6: Modify `config.example.yaml`**

Prepend (above `gitlab:`):

```yaml
discord:
  token: env:DISCORD_TOKEN
  app_id: env:DISCORD_APP_ID
  guild_id: ""           # optional: register commands in a single guild for instant rollout
  allowed_user_ids: []   # empty => allow everyone
  allowed_role_ids: []
```

- [ ] **Step 7: Implement `internal/discord/register.go`**

```go
package discord

import "github.com/bwmarrin/discordgo"

const reviewCommandName = "review"

// ReviewCommand returns the slash command definition.
func ReviewCommand() *discordgo.ApplicationCommand {
	return &discordgo.ApplicationCommand{
		Name:        reviewCommandName,
		Description: "Run an AI code review on a GitLab merge request",
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "url",
				Description: "GitLab merge request URL",
				Required:    true,
			},
		},
	}
}

// RegisterCommands installs (overwrites) the slash commands for the application.
// If guildID is non-empty, registration is scoped to that guild (faster rollout
// during development); otherwise commands are registered globally.
func RegisterCommands(s SessionAPI, appID, guildID string) error {
	_, err := s.ApplicationCommandBulkOverwrite(appID, guildID, []*discordgo.ApplicationCommand{ReviewCommand()})
	return err
}
```

- [ ] **Step 8: Write `internal/discord/register_test.go`**

```go
package discord

import (
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/require"
)

type recordingSession struct {
	fakeSession
	gotApp     string
	gotGuild   string
	gotCommands []*discordgo.ApplicationCommand
}

func (r *recordingSession) ApplicationCommandBulkOverwrite(appID, guildID string, cmds []*discordgo.ApplicationCommand, _ ...discordgo.RequestOption) ([]*discordgo.ApplicationCommand, error) {
	r.gotApp = appID
	r.gotGuild = guildID
	r.gotCommands = cmds
	return cmds, nil
}

func TestRegisterCommands_PassesAppGuildAndDefinition(t *testing.T) {
	s := &recordingSession{}
	require.NoError(t, RegisterCommands(s, "appid", "guildid"))
	require.Equal(t, "appid", s.gotApp)
	require.Equal(t, "guildid", s.gotGuild)
	require.Len(t, s.gotCommands, 1)
	require.Equal(t, "review", s.gotCommands[0].Name)
}
```

- [ ] **Step 9: Run discord tests**

Run: `go test ./internal/discord/... -v`
Expected: ALL PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/config internal/discord config.example.yaml
git commit -m "feat(config,discord): discord config + slash command registration"
```

---

## Task 7: cmd/bot main wiring

**Files:**
- Create: `cmd/bot/main.go`
- Modify: `cmd/review-cli/main.go` (1-line tweak: wrap http client with retry transport)
- Modify: `.gitignore` (add `/bot` and `/review-cli`)

- [ ] **Step 1: Update `.gitignore`**

Append two lines:

```
/bot
/review-cli
```

- [ ] **Step 2: Modify `cmd/review-cli/main.go` to use the retry transport**

In the `main` function, replace the line:

```go
hc := &http.Client{Timeout: 60 * time.Second}
```

with:

```go
hc := &http.Client{
	Timeout:   60 * time.Second,
	Transport: &httpretry.Transport{Inner: http.DefaultTransport, Max: 3, Base: 500 * time.Millisecond},
}
```

Add `"github.com/fahmi/gitlab-mr-review-bot/internal/httpretry"` to the imports.

- [ ] **Step 3: Verify review-cli still builds**

Run: `go build ./cmd/review-cli`
Expected: clean build.

- [ ] **Step 4: Create `cmd/bot/main.go`**

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	dg "github.com/fahmi/gitlab-mr-review-bot/internal/discord"
	"github.com/fahmi/gitlab-mr-review-bot/internal/config"
	"github.com/fahmi/gitlab-mr-review-bot/internal/gitlab"
	"github.com/fahmi/gitlab-mr-review-bot/internal/httpretry"
	"github.com/fahmi/gitlab-mr-review-bot/internal/jobs"
	"github.com/fahmi/gitlab-mr-review-bot/internal/llm"
	"github.com/fahmi/gitlab-mr-review-bot/internal/review"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	hc := &http.Client{
		Timeout:   60 * time.Second,
		Transport: &httpretry.Transport{Inner: http.DefaultTransport, Max: 3, Base: 500 * time.Millisecond},
	}
	gl := gitlab.NewRESTClient(cfg.GitLab.BaseURL, cfg.GitLab.Token, hc)

	var prov llm.Provider
	switch cfg.LLM.Provider {
	case "anthropic":
		prov = llm.NewAnthropic(llm.AnthropicConfig{
			APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model, BaseURL: cfg.LLM.BaseURL, HTTP: hc,
		})
	default:
		fmt.Fprintln(os.Stderr, "provider not yet supported:", cfg.LLM.Provider)
		os.Exit(1)
	}

	o := review.New(review.Config{
		GitLab:        gl,
		Provider:      prov,
		MaxFileTokens: cfg.Review.MaxFileTokens,
		MaxMRTokens:   cfg.Review.MaxMRTokens,
		MaxConcurrent: cfg.Review.MaxConcurrentChunks,
		IgnoreGlobs:   cfg.Review.IgnoreGlobs,
	})

	tracker := jobs.New()

	sess, err := discordgo.New("Bot " + cfg.Discord.Token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "discord new:", err)
		os.Exit(1)
	}

	bot := &dg.Bot{
		Session: sess,
		Runner:  o,
		Jobs:    tracker,
		Validator: dg.Validator{
			Tracker:        tracker,
			AllowedUserIDs: toSet(cfg.Discord.AllowedUserIDs),
			AllowedRoleIDs: toSet(cfg.Discord.AllowedRoleIDs),
		},
		TickEvery:  5 * time.Second,
		JobTimeout: cfg.Review.JobTimeout,
	}

	sess.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		bot.HandleInteraction(i.Interaction)
	})
	sess.Identify.Intents = discordgo.IntentsGuilds

	if err := sess.Open(); err != nil {
		fmt.Fprintln(os.Stderr, "discord open:", err)
		os.Exit(1)
	}
	defer func() { _ = sess.Close() }()

	if err := dg.RegisterCommands(sess, cfg.Discord.AppID, cfg.Discord.GuildID); err != nil {
		fmt.Fprintln(os.Stderr, "register commands:", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tracker.StartCleaner(ctx, 24*time.Hour, time.Hour)

	fmt.Println("bot online; awaiting /review interactions")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	fmt.Println("shutting down")
}

func toSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]bool, len(items))
	for _, s := range items {
		out[s] = true
	}
	return out
}
```

- [ ] **Step 5: Build the bot**

Run: `go build ./cmd/bot`
Expected: clean build.

- [ ] **Step 6: Run the full test suite**

Run: `go test ./... -count=1`
Expected: ALL PASS.

- [ ] **Step 7: Update `README.md`**

Add a section above the existing review-cli section:

```markdown
## Running the Discord Bot

1. Create a Discord application at https://discord.com/developers/applications
2. Add a bot user; copy the bot token to `DISCORD_TOKEN`.
3. Copy the application ID to `DISCORD_APP_ID`.
4. Invite the bot to your server with the `applications.commands` and `bot` scopes.
5. Fill in `config.yaml` (see `config.example.yaml`) and the env vars above plus `GITLAB_TOKEN` and your LLM API key.
6. Run:
   ```
   go build ./cmd/bot && ./bot --config config.yaml
   ```
7. In any channel the bot can see, run `/review url:<gitlab-mr-url>`.
```

- [ ] **Step 8: Commit**

```bash
git add cmd/bot cmd/review-cli .gitignore README.md
git commit -m "feat(bot): cmd/bot wires discord, retry transport, jobs, orchestrator"
```

---

## Self-Review Checklist (run after Task 7)

1. **Spec coverage:**
   - HTTP retry on 429 / 5xx — Task 1 ✓
   - Progress signaling from orchestrator — Task 2 ✓
   - In-memory job tracker w/ TTL — Task 3 ✓
   - Slash command `/review url:<...>` — Tasks 4, 5, 6 ✓
   - Allowlist (user/role) — Tasks 4, 6 ✓
   - Duplicate-job rejection — Task 4 ✓
   - 3-second ack via deferred response — Task 5 ✓
   - Status ticker editing the interaction — Task 5 ✓
   - Final summary with MR URL — Task 5 ✓
   - Discord config (token, app id, guild id, allowlists) — Task 6 ✓
   - cmd/bot main wiring — Task 7 ✓
   - Deep mode `--deep` flag — **deferred** (Plan 1 design listed as default-off; no behavior change without orchestrator two-pass support; defer to Plan 3 or follow-up)

2. **Placeholders:** none. All steps include concrete code or commands.

3. **Type consistency:**
   - `review.RunWithProgress(ctx, mrURL, p ProgressFn)` matches calls in fake runner and bot
   - `jobs.Tracker` methods (Create, Get, Update, FindActiveByMR, StartCleaner) consistent across tasks
   - `discord.SessionAPI` methods used in bot.go and register.go match the interface
   - `discord.Bot{Session, Runner, Jobs, Validator, TickEvery, JobTimeout, OnJobDone}` field names match in tests and main wiring
   - `config.Discord` field names match the YAML and `cmd/bot` usage

---

## Followup

- **Plan 3 — Additional LLM adapters:** OpenAI Chat Completions + Ollama. No structural changes to orchestrator. Optionally adds `--deep` two-pass review (small orchestrator change to call Provider once per finding-set in pass 2).
