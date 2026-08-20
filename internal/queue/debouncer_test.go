package queue_test

import (
	"context"
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
