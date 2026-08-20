// Package queue coalesces webhook events into a single Renovate run per
// repository.
package queue

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nonchan7720/renovate-self-hosted/internal/config"
	"github.com/nonchan7720/renovate-self-hosted/internal/dispatch"
)

// DispatchTimeout bounds a single dispatch attempt, including its retries.
const DispatchTimeout = 2 * time.Minute

// Debouncer folds events that arrive close together into one run per
// repository. Ticking several dashboard checkboxes in a row, or pushing a
// stack of commits, then costs a single Renovate run instead of one per event.
type Debouncer struct {
	window     time.Duration
	maxWait    time.Duration
	dispatcher dispatch.Dispatcher
	logger     *slog.Logger
	now        func() time.Time

	mu      sync.Mutex
	pending map[string]*entry
	nextGen uint64
	closed  bool

	wg sync.WaitGroup
}

type entry struct {
	job   dispatch.Job
	timer *time.Timer
	first time.Time
	gen   uint64
}

// New builds a Debouncer. A nil logger falls back to slog.Default.
func New(cfg config.Debounce, dispatcher dispatch.Dispatcher, logger *slog.Logger) *Debouncer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Debouncer{
		window:     cfg.Window,
		maxWait:    cfg.MaxWait,
		dispatcher: dispatcher,
		logger:     logger,
		now:        time.Now,
		pending:    make(map[string]*entry),
	}
}

// Enqueue schedules a run for job.Repository, merging it with any run already
// waiting for that repository.
func (d *Debouncer) Enqueue(job dispatch.Job) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		d.logger.Warn("dropping job, debouncer is closed", slog.String("repository", job.Repository))
		return
	}

	existing, ok := d.pending[job.Repository]
	if !ok {
		d.nextGen++
		e := &entry{job: job, first: d.now(), gen: d.nextGen}
		repository, gen := job.Repository, e.gen
		e.timer = time.AfterFunc(d.window, func() { d.flush(repository, gen) })
		d.pending[job.Repository] = e
		d.logger.Debug("job scheduled",
			slog.String("repository", job.Repository),
			slog.Duration("in", d.window))
		return
	}

	existing.job = existing.job.Merge(job)

	// Extend the quiet period, but never past maxWait after the first event,
	// so a repository with a steady trickle of events still gets a run.
	delay := d.window
	if remaining := d.maxWait - d.now().Sub(existing.first); remaining < delay {
		delay = remaining
	}
	if delay <= 0 {
		delay = 0
	}
	if !existing.timer.Stop() {
		// The timer already fired and its flush is blocked on d.mu, waiting
		// for us to release it. Reset alone would schedule a second firing
		// without cancelling that one, so the extension would be lost the
		// moment we unlock. Bump the gen instead: the in-flight flush will
		// find it stale and no-op, leaving the new timer as the sole source
		// of truth.
		d.nextGen++
		existing.gen = d.nextGen
		repository, gen := job.Repository, existing.gen
		existing.timer = time.AfterFunc(delay, func() { d.flush(repository, gen) })
	} else {
		existing.timer.Reset(delay)
	}
	d.logger.Debug("job merged",
		slog.String("repository", job.Repository),
		slog.Duration("in", delay))
}

// flush dispatches the pending job for repository, if the entry is still the
// one the timer was created for.
func (d *Debouncer) flush(repository string, gen uint64) {
	d.mu.Lock()
	e, ok := d.pending[repository]
	if !ok || e.gen != gen {
		d.mu.Unlock()
		return
	}
	delete(d.pending, repository)
	job := e.job
	d.wg.Add(1)
	d.mu.Unlock()

	go func() {
		defer d.wg.Done()
		d.dispatch(job)
	}()
}

func (d *Debouncer) dispatch(job dispatch.Job) {
	ctx, cancel := context.WithTimeout(context.Background(), DispatchTimeout)
	defer cancel()

	logger := d.logger.With(
		slog.String("repository", job.Repository),
		slog.Any("reasons", job.Reasons))

	if err := d.dispatcher.Dispatch(ctx, job); err != nil {
		logger.Error("failed to dispatch renovate run", slog.Any("error", err))
		return
	}
	logger.Info("dispatched renovate run", slog.Any("details", job.Details))
}

// Pending reports how many repositories are waiting for a run.
func (d *Debouncer) Pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pending)
}

// Close stops accepting new jobs, dispatches everything still pending and
// waits for in-flight dispatches to finish or ctx to be done.
func (d *Debouncer) Close(ctx context.Context) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	jobs := make([]dispatch.Job, 0, len(d.pending))
	for repository, e := range d.pending {
		e.timer.Stop()
		jobs = append(jobs, e.job)
		delete(d.pending, repository)
	}
	d.wg.Add(len(jobs))
	d.mu.Unlock()

	for _, job := range jobs {
		go func() {
			defer d.wg.Done()
			d.dispatch(job)
		}()
	}

	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
