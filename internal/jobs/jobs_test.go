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
