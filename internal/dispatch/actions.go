package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nonchan7720/renovate-self-hosted/internal/config"
)

// Retry policy for a single dispatch.
const (
	maxAttempts  = 3
	initialDelay = time.Second
	requestLimit = 30 * time.Second
)

// Actions starts Renovate by triggering a workflow_dispatch on the repository
// that hosts the self-hosted runner. The runner lives in its own repository, so
// this service never executes Renovate itself.
type Actions struct {
	client *http.Client
	apiURL string
	token  string
	runner config.Runner
	logger *slog.Logger
	sleep  func(ctx context.Context, d time.Duration) error
}

// NewActions builds an Actions dispatcher. A nil client or logger falls back to
// a sensible default.
func NewActions(apiURL, token string, runner config.Runner, client *http.Client, logger *slog.Logger) *Actions {
	if client == nil {
		client = &http.Client{Timeout: requestLimit}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Actions{
		client: client,
		apiURL: strings.TrimSuffix(apiURL, "/"),
		token:  token,
		runner: runner,
		logger: logger,
		sleep:  sleepCtx,
	}
}

// Dispatch implements Dispatcher. It retries transient failures before giving
// up, so a brief GitHub outage does not silently drop a run.
func (a *Actions) Dispatch(ctx context.Context, job Job) error {
	payload, err := json.Marshal(a.payload(job))
	if err != nil {
		return fmt.Errorf("encode workflow dispatch payload: %w", err)
	}

	endpoint := fmt.Sprintf("%s/repos/%s/actions/workflows/%s/dispatches",
		a.apiURL, a.runner.Repository, url.PathEscape(a.runner.Workflow))

	delay := initialDelay
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		retryable, err := a.do(ctx, endpoint, payload)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable || attempt == maxAttempts {
			break
		}
		a.logger.Warn("retrying workflow dispatch",
			slog.String("repository", job.Repository),
			slog.Int("attempt", attempt),
			slog.Duration("in", delay),
			slog.Any("error", err))
		if err := a.sleep(ctx, delay); err != nil {
			return err
		}
		delay *= 2
	}
	return fmt.Errorf("dispatch workflow for %s: %w", job.Repository, lastErr)
}

// payload builds the workflow_dispatch body. Only the configured inputs are
// sent: GitHub rejects the whole request when it carries an input the workflow
// does not declare.
func (a *Actions) payload(job Job) map[string]any {
	inputs := make(map[string]string, len(a.runner.ExtraInputs)+1)
	for k, v := range a.runner.ExtraInputs {
		inputs[k] = v
	}
	if a.runner.RepositoryInput != "" {
		inputs[a.runner.RepositoryInput] = job.Repository
	}
	return map[string]any{"ref": a.runner.Ref, "inputs": inputs}
}

// do performs a single attempt and reports whether the failure is worth
// retrying.
func (a *Actions) do(ctx context.Context, endpoint string, payload []byte) (retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}

	res, err := a.client.Do(req)
	if err != nil {
		return ctx.Err() == nil, fmt.Errorf("call github: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}()

	if res.StatusCode == http.StatusNoContent {
		return false, nil
	}

	body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
	message := strings.TrimSpace(string(body))
	switch res.StatusCode {
	case http.StatusNotFound:
		return false, fmt.Errorf("github returned 404: workflow %q on %q not found, "+
			"or the token cannot see it: %s", a.runner.Workflow, a.runner.Repository, message)
	case http.StatusUnprocessableEntity:
		return false, fmt.Errorf("github returned 422: ref %q or the workflow inputs are invalid: %s",
			a.runner.Ref, message)
	case http.StatusTooManyRequests:
		return true, fmt.Errorf("github returned 429: %s", message)
	}
	if res.StatusCode >= 500 {
		return true, fmt.Errorf("github returned %d: %s", res.StatusCode, message)
	}
	return false, fmt.Errorf("github returned %d: %s", res.StatusCode, message)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// NewDryRun returns a Dispatcher that only logs what it would have started.
func NewDryRun(logger *slog.Logger) Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return DispatcherFunc(func(_ context.Context, job Job) error {
		logger.Info("dry run: would dispatch renovate",
			slog.String("repository", job.Repository),
			slog.Any("reasons", job.Reasons),
			slog.Any("details", job.Details))
		return nil
	})
}
