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
