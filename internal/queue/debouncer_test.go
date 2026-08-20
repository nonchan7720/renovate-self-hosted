package queue_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nonchan7720/renovate-self-hosted/internal/config"
	"github.com/nonchan7720/renovate-self-hosted/internal/dispatch"
	"github.com/nonchan7720/renovate-self-hosted/internal/queue"
)

type collector struct {
	mu   sync.Mutex
	jobs []dispatch.Job
	ch   chan dispatch.Job
}

func newCollector() *collector {
	return &collector{ch: make(chan dispatch.Job, 16)}
}

func (c *collector) Dispatch(_ context.Context, job dispatch.Job) error {
	c.mu.Lock()
	c.jobs = append(c.jobs, job)
	c.mu.Unlock()
	c.ch <- job
	return nil
}

func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.jobs)
}

func (c *collector) await(t *testing.T) dispatch.Job {
	t.Helper()
	select {
	case job := <-c.ch:
		return job
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a dispatch")
		return dispatch.Job{}
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestDebouncerCoalescesRepeatedEvents(t *testing.T) {
	t.Parallel()

	sink := newCollector()
	d := queue.New(config.Debounce{Window: 60 * time.Millisecond, MaxWait: time.Second}, sink, discardLogger())
	t.Cleanup(func() { _ = d.Close(context.Background()) })

	// Someone ticks three dashboard boxes in quick succession.
	d.Enqueue(dispatch.Job{
		Repository: "acme/api",
		Reasons:    []string{dispatch.ReasonDashboardCheckbox},
		Details:    []string{"manual job"},
		Deliveries: []string{"d1"},
		URL:        "https://github.com/acme/api/issues/7",
	})
	d.Enqueue(dispatch.Job{
		Repository: "acme/api",
		Reasons:    []string{dispatch.ReasonDashboardCheckbox},
		Details:    []string{"unlimit-branch=x"},
		Deliveries: []string{"d2"},
	})
	d.Enqueue(dispatch.Job{
		Repository: "acme/api",
		Reasons:    []string{dispatch.ReasonPullRequestCheckbox},
		Details:    []string{"rebase-check"},
		Deliveries: []string{"d3"},
	})

	job := sink.await(t)
	if job.Repository != "acme/api" {
		t.Fatalf("Repository = %q", job.Repository)
	}
	if len(job.Reasons) != 2 {
		t.Errorf("Reasons = %v, want the two distinct reasons", job.Reasons)
	}
	if len(job.Details) != 3 {
		t.Errorf("Details = %v, want all three checkbox labels", job.Details)
	}
	if len(job.Deliveries) != 3 {
		t.Errorf("Deliveries = %v, want all three delivery ids", job.Deliveries)
	}
	if job.URL == "" {
		t.Error("URL was lost while merging")
	}

	time.Sleep(150 * time.Millisecond)
	if got := sink.count(); got != 1 {
		t.Fatalf("dispatched %d times, want 1", got)
	}
}

func TestDebouncerKeepsRepositoriesSeparate(t *testing.T) {
	t.Parallel()

	sink := newCollector()
	d := queue.New(config.Debounce{Window: 30 * time.Millisecond, MaxWait: time.Second}, sink, discardLogger())
	t.Cleanup(func() { _ = d.Close(context.Background()) })

	d.Enqueue(dispatch.Job{Repository: "acme/api", Reasons: []string{dispatch.ReasonManual}})
	d.Enqueue(dispatch.Job{Repository: "acme/web", Reasons: []string{dispatch.ReasonManual}})

	seen := map[string]bool{}
	seen[sink.await(t).Repository] = true
	seen[sink.await(t).Repository] = true

	if !seen["acme/api"] || !seen["acme/web"] {
		t.Fatalf("dispatched %v, want one run per repository", seen)
	}
}

// TestDebouncerHonoursMaxWait makes sure a repository with a steady trickle of
// events is not postponed forever.
func TestDebouncerHonoursMaxWait(t *testing.T) {
	t.Parallel()

	sink := newCollector()
	d := queue.New(config.Debounce{Window: 80 * time.Millisecond, MaxWait: 200 * time.Millisecond}, sink, discardLogger())
	t.Cleanup(func() { _ = d.Close(context.Background()) })

	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 10 {
			d.Enqueue(dispatch.Job{Repository: "acme/api", Reasons: []string{dispatch.ReasonManual}})
			time.Sleep(40 * time.Millisecond)
		}
	}()

	job := sink.await(t)
	<-done

	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("first dispatch took %s, want it capped near MaxWait", elapsed)
	}
	if job.Repository != "acme/api" {
		t.Fatalf("Repository = %q", job.Repository)
	}
}

// timedRecorder is like collector but never blocks the dispatching goroutine,
// so a failed assertion mid-test can never leave Close's wg.Wait hanging on a
// send nobody is left to receive.
type timedRecorder struct {
	mu    sync.Mutex
	jobs  []dispatch.Job
	times []time.Time
}

func (r *timedRecorder) Dispatch(_ context.Context, job dispatch.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs = append(r.jobs, job)
	r.times = append(r.times, time.Now())
	return nil
}

func (r *timedRecorder) snapshot() ([]dispatch.Job, []time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]dispatch.Job(nil), r.jobs...), append([]time.Time(nil), r.times...)
}

// TestDebouncerMergeAfterFire covers the timer firing and its flush blocking
// on d.mu at the exact moment Enqueue takes the merge path. A bare
// timer.Reset there schedules a second firing without retracting the one
// already in flight, so the blocked flush dispatches the instant Enqueue
// releases the lock instead of waiting out the extended delay -- even though
// the merge itself already folded the new reason into the job beforehand, so
// the dispatch is not missing data, only early.
//
// Landing inside that race deliberately is not possible from outside the
// package, so several repositories each sleep a full window -- long enough
// for their timer to plausibly fire and its flush to be scheduled -- and
// then immediately fire a burst of merges, giving that flush many chances to
// be blocked on d.mu exactly when a merge takes the lock. Some bursts will
// still legitimately outrun the window and split across more than one
// dispatch, which is fine; what must never happen, split or not, is a
// dispatch whose newest reason was merged less than half a window before it
// fired, which is the fingerprint of a lost extension.
func TestDebouncerMergeAfterFire(t *testing.T) {
	t.Parallel()

	const (
		window = 5 * time.Millisecond
		repos  = 20
		burst  = 60
		rounds = 3
	)

	rec := &timedRecorder{}
	d := queue.New(config.Debounce{Window: window, MaxWait: time.Second}, rec, discardLogger())
	t.Cleanup(func() { _ = d.Close(context.Background()) })

	var reasonAt sync.Map // reason -> time.Time its Enqueue call was made

	var wg sync.WaitGroup
	for r := range repos {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			repo := fmt.Sprintf("acme/merge-race-%d", r)
			n := 0
			enqueue := func() {
				reason := fmt.Sprintf("%s-%d", repo, n)
				n++
				reasonAt.Store(reason, time.Now())
				d.Enqueue(dispatch.Job{Repository: repo, Reasons: []string{reason}})
			}
			for range rounds {
				enqueue()
				time.Sleep(window)
				for range burst {
					enqueue()
				}
			}
		}(r)
	}
	wg.Wait()
	time.Sleep(2 * window) // let the last legitimate firing land

	jobs, times := rec.snapshot()

	// A lost extension dispatches almost immediately after the merge that
	// should have pushed it out by a further window, so demand comfortably
	// more than zero without requiring the full window (scheduling jitter
	// can delay even a legitimate firing further still).
	const minElapsed = window / 2

	seen := map[string]int{}
	for i, job := range jobs {
		var newest time.Time
		var newestReason string
		for _, reason := range job.Reasons {
			seen[reason]++
			v, ok := reasonAt.Load(reason)
			if !ok {
				t.Fatalf("dispatch contains unknown reason %q", reason)
			}
			if at := v.(time.Time); at.After(newest) {
				newest, newestReason = at, reason
			}
		}
		if elapsed := times[i].Sub(newest); elapsed < minElapsed {
			t.Errorf("dispatch %v arrived %s after %q was merged, want at least %s (an extension was lost)",
				job.Reasons, elapsed, newestReason, minElapsed)
		}
	}
	for r := range repos {
		for n := range rounds * (burst + 1) {
			reason := fmt.Sprintf("acme/merge-race-%d-%d", r, n)
			if seen[reason] != 1 {
				t.Errorf("reason %q dispatched %d times, want exactly 1", reason, seen[reason])
			}
		}
	}
}

func TestDebouncerCloseFlushesPending(t *testing.T) {
	t.Parallel()

	sink := newCollector()
	d := queue.New(config.Debounce{Window: time.Hour, MaxWait: time.Hour}, sink, discardLogger())

	d.Enqueue(dispatch.Job{Repository: "acme/api", Reasons: []string{dispatch.ReasonDashboardCheckbox}})
	if got := d.Pending(); got != 1 {
		t.Fatalf("Pending() = %d, want 1", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Close(ctx); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if got := sink.count(); got != 1 {
		t.Fatalf("dispatched %d times on close, want 1", got)
	}

	// A job arriving after Close must not be silently scheduled forever.
	d.Enqueue(dispatch.Job{Repository: "acme/web"})
	if got := d.Pending(); got != 0 {
		t.Fatalf("Pending() = %d after Close, want 0", got)
	}
}
