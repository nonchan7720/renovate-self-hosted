package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
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

// TokenSource supplies the bearer token used to authenticate dispatch
// requests to GitHub. Kept as an interface, rather than importing
// internal/githubapp's concrete type, so internal/dispatch does not depend on
// internal/githubapp and tests can substitute their own source.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Actions starts Renovate by triggering a workflow_dispatch on the repository
// that hosts the self-hosted runner. The runner lives in its own repository, so
// this service never executes Renovate itself.
type Actions struct {
	client *http.Client
	apiURL string
	tokens TokenSource
	runner config.Runner
	logger *slog.Logger
	sleep  func(ctx context.Context, d time.Duration) error
}

// NewActions builds an Actions dispatcher. A nil client or logger falls back to
// a sensible default.
func NewActions(apiURL string, tokens TokenSource, runner config.Runner, client *http.Client, logger *slog.Logger) *Actions {
	if client == nil {
		client = &http.Client{Timeout: requestLimit}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Actions{
		client: client,
		apiURL: strings.TrimSuffix(apiURL, "/"),
		tokens: tokens,
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
		retryable, retryAfter, err := a.do(ctx, endpoint, payload)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable || attempt == maxAttempts {
			break
		}
		// GitHub's advice trumps our guess: a secondary rate limit clears on
		// its own schedule, and backing off less than that just spends the
		// attempt budget on requests that are certain to be rejected again.
		wait := delay
		if retryAfter > wait {
			wait = retryAfter
		}
		a.logger.Warn("retrying workflow dispatch",
			slog.String("repository", job.Repository),
			slog.Int("attempt", attempt),
			slog.Duration("in", wait),
			slog.Any("error", err))
		if err := a.sleep(ctx, wait); err != nil {
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
// retrying, and how long GitHub asked us to wait before trying again.
func (a *Actions) do(ctx context.Context, endpoint string, payload []byte) (retryable bool, retryAfter time.Duration, err error) {
	// Minting the token is itself a call to GitHub, so a failure here is the
	// same transient kind as a dispatch-endpoint blip and gets the same retry.
	token, err := a.tokens.Token(ctx)
	if err != nil {
		return ctx.Err() == nil, 0, fmt.Errorf("get github token: %w", err)
	}
	if token == "" {
		// A GitHub App token is never legitimately empty; treat it as the
		// configuration bug it is rather than sending a request GitHub would reject.
		return false, 0, errors.New("github token source returned an empty token")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return false, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := a.client.Do(req)
	if err != nil {
		return ctx.Err() == nil, 0, fmt.Errorf("call github: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}()

	if res.StatusCode == http.StatusNoContent {
		return false, 0, nil
	}

	body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
	message := strings.TrimSpace(string(body))
	retryAfter = parseRetryAfter(res.Header.Get("Retry-After"))
	switch res.StatusCode {
	case http.StatusNotFound:
		return false, 0, fmt.Errorf("github returned 404: workflow %q on %q not found, "+
			"or the token cannot see it: %s", a.runner.Workflow, a.runner.Repository, message)
	case http.StatusUnprocessableEntity:
		return false, 0, fmt.Errorf("github returned 422: ref %q or the workflow inputs are invalid: %s",
			a.runner.Ref, message)
	case http.StatusForbidden:
		// GitHub reuses 403 for two unrelated things: a token that lacks
		// permission on this workflow, and a secondary rate limit tripped by
		// bursty dispatches across many repositories. Only the latter clears
		// on its own, so retrying the former would just burn attempts on a
		// request that can never succeed. Treat it as a rate limit only when
		// GitHub actually signals one, either with Retry-After or with the
		// wording it uses for abuse detection.
		if retryAfter > 0 || isSecondaryRateLimit(message) {
			return true, retryAfter, fmt.Errorf("github returned 403 (secondary rate limit): %s", message)
		}
		return false, 0, fmt.Errorf("github returned 403: the token likely cannot dispatch that workflow: %s", message)
	case http.StatusTooManyRequests:
		return true, retryAfter, fmt.Errorf("github returned 429: %s", message)
	}
	if res.StatusCode >= 500 {
		return true, retryAfter, fmt.Errorf("github returned %d: %s", res.StatusCode, message)
	}
	return false, 0, fmt.Errorf("github returned %d: %s", res.StatusCode, message)
}

// isSecondaryRateLimit reports whether message is GitHub's wording for a
// secondary rate limit or abuse-detection response, the two 403 cases that
// clear on their own rather than indicating a permissions problem.
func isSecondaryRateLimit(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "secondary rate limit") || strings.Contains(lower, "abuse detection")
}

// parseRetryAfter reads GitHub's Retry-After header, sent as an integer
// number of seconds or, occasionally, an HTTP-date. A missing, empty,
// unparseable, or negative value means GitHub gave no advice, so callers fall
// back to their own backoff rather than treating this as an error.
func parseRetryAfter(raw string) time.Duration {
	if raw == "" {
		return 0
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(raw); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
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
