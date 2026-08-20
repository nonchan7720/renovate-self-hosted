package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nonchan7720/renovate-self-hosted/internal/config"
)

func testRunner() config.Runner {
	return config.Runner{
		Repository:      "acme/renovate-runner",
		Workflow:        "renovate.yml",
		Ref:             "main",
		RepositoryInput: "repositories",
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestActions points a dispatcher at srv and makes its retry backoff
// instant so the tests stay fast.
func newTestActions(srv *httptest.Server, runner config.Runner) *Actions {
	a := NewActions(srv.URL, "test-token", runner, srv.Client(), discardLogger())
	a.sleep = func(context.Context, time.Duration) error { return nil }
	return a
}

func TestActionsDispatch(t *testing.T) {
	t.Parallel()

	var gotPath, gotAuth, gotAPIVersion string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAPIVersion = r.Header.Get("X-GitHub-Api-Version")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	runner := testRunner()
	runner.ExtraInputs = map[string]string{"logLevel": "debug"}

	err := newTestActions(srv, runner).Dispatch(t.Context(), Job{
		Repository: "acme/api",
		Reasons:    []string{ReasonDashboardCheckbox},
	})
	if err != nil {
		t.Fatalf("Dispatch() = %v", err)
	}

	if want := "/repos/acme/renovate-runner/actions/workflows/renovate.yml/dispatches"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if want := "Bearer test-token"; gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
	if gotAPIVersion != "2022-11-28" {
		t.Errorf("X-GitHub-Api-Version = %q", gotAPIVersion)
	}
	if got := gotBody["ref"]; got != "main" {
		t.Errorf("ref = %v, want main", got)
	}

	inputs, ok := gotBody["inputs"].(map[string]any)
	if !ok {
		t.Fatalf("inputs = %v, want an object", gotBody["inputs"])
	}
	if got := inputs["repositories"]; got != "acme/api" {
		t.Errorf("inputs.repositories = %v, want acme/api", got)
	}
	if got := inputs["logLevel"]; got != "debug" {
		t.Errorf("inputs.logLevel = %v, want debug", got)
	}
	if len(inputs) != 2 {
		t.Errorf("inputs = %v, want only the declared inputs", inputs)
	}
}

func TestActionsDispatchRetriesServerErrors(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := newTestActions(srv, testRunner()).Dispatch(t.Context(), Job{Repository: "acme/api"}); err != nil {
		t.Fatalf("Dispatch() = %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("made %d calls, want 3", got)
	}
}

func TestActionsDispatchFailures(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		status    int
		body      string
		wantCalls int32
		wantIn    string
	}{
		"workflow missing": {
			status: http.StatusNotFound, body: `{"message":"Not Found"}`,
			wantCalls: 1, wantIn: "404",
		},
		"invalid inputs": {
			status: http.StatusUnprocessableEntity, body: `{"message":"Unexpected inputs provided"}`,
			wantCalls: 1, wantIn: "422",
		},
		"forbidden": {
			status: http.StatusForbidden, body: `{"message":"Resource not accessible"}`,
			wantCalls: 1, wantIn: "403",
		},
		"rate limited": {
			status: http.StatusTooManyRequests, body: `{"message":"slow down"}`,
			wantCalls: maxAttempts, wantIn: "429",
		},
		"server error": {
			status: http.StatusInternalServerError, body: `{"message":"boom"}`,
			wantCalls: maxAttempts, wantIn: "500",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			err := newTestActions(srv, testRunner()).Dispatch(t.Context(), Job{Repository: "acme/api"})
			if err == nil {
				t.Fatal("Dispatch() = nil, want an error")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantIn)
			}
			if got := calls.Load(); got != tc.wantCalls {
				t.Errorf("made %d calls, want %d", got, tc.wantCalls)
			}
		})
	}
}

// TestActionsDispatchRetries403WithRetryAfter covers GitHub's secondary rate
// limit signal: a 403 that carries Retry-After is a burst limit that clears
// on its own, not a permissions failure, so it must be retried rather than
// failing the whole burst of dispatches permanently.
func TestActionsDispatchRetries403WithRetryAfter(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < maxAttempts {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"rate limited"}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := newTestActions(srv, testRunner()).Dispatch(t.Context(), Job{Repository: "acme/api"}); err != nil {
		t.Fatalf("Dispatch() = %v", err)
	}
	if got := calls.Load(); got != maxAttempts {
		t.Fatalf("made %d calls, want %d", got, maxAttempts)
	}
}

// TestActionsDispatchRetries403SecondaryRateLimitBody covers the case where
// GitHub omits Retry-After but says so in the body instead.
func TestActionsDispatchRetries403SecondaryRateLimitBody(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < maxAttempts {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"You have exceeded a secondary rate limit. `+
				`Please wait a few minutes before you try again."}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := newTestActions(srv, testRunner()).Dispatch(t.Context(), Job{Repository: "acme/api"}); err != nil {
		t.Fatalf("Dispatch() = %v", err)
	}
	if got := calls.Load(); got != maxAttempts {
		t.Fatalf("made %d calls, want %d", got, maxAttempts)
	}
}

// TestActionsDispatchDoesNotRetryPlain403 covers the other half of the 403
// split: no Retry-After and no rate-limit wording means the token really
// cannot dispatch that workflow, so retrying would just waste attempts.
func TestActionsDispatchDoesNotRetryPlain403(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"Resource not accessible by integration"}`)
	}))
	defer srv.Close()

	err := newTestActions(srv, testRunner()).Dispatch(t.Context(), Job{Repository: "acme/api"})
	if err == nil {
		t.Fatal("Dispatch() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %v, want it to mention 403", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("made %d calls, want 1", got)
	}
}

// TestActionsDispatchHonoursRetryAfterOverBackoff covers a 429 that tells us
// to wait longer than the exponential backoff would on its own: retrying
// after the backoff's 1s would just land inside the same limit window and
// fail again, so GitHub's advice must win.
func TestActionsDispatchHonoursRetryAfterOverBackoff(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"message":"slow down"}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	a := newTestActions(srv, testRunner())
	var gotDelays []time.Duration
	a.sleep = func(_ context.Context, d time.Duration) error {
		gotDelays = append(gotDelays, d)
		return nil
	}

	if err := a.Dispatch(t.Context(), Job{Repository: "acme/api"}); err != nil {
		t.Fatalf("Dispatch() = %v", err)
	}
	if len(gotDelays) != 1 {
		t.Fatalf("sleep called %d times, want 1", len(gotDelays))
	}
	if gotDelays[0] != 30*time.Second {
		t.Errorf("delay = %v, want 30s (Retry-After should beat the %v backoff)", gotDelays[0], initialDelay)
	}
}

// TestActionsDispatchIgnoresUnparseableRetryAfter covers a header GitHub
// theoretically could send in a form we do not understand: it must not error
// out or wedge the retry loop, just fall back to the normal backoff.
func TestActionsDispatchIgnoresUnparseableRetryAfter(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "not-a-duration")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"message":"slow down"}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	a := newTestActions(srv, testRunner())
	var gotDelays []time.Duration
	a.sleep = func(_ context.Context, d time.Duration) error {
		gotDelays = append(gotDelays, d)
		return nil
	}

	if err := a.Dispatch(t.Context(), Job{Repository: "acme/api"}); err != nil {
		t.Fatalf("Dispatch() = %v", err)
	}
	if len(gotDelays) != 1 {
		t.Fatalf("sleep called %d times, want 1", len(gotDelays))
	}
	if gotDelays[0] != initialDelay {
		t.Errorf("delay = %v, want the normal %v backoff", gotDelays[0], initialDelay)
	}
}

func TestActionsDispatchStopsOnCancelledContext(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := newTestActions(srv, testRunner()).Dispatch(ctx, Job{Repository: "acme/api"}); err == nil {
		t.Fatal("Dispatch() = nil, want an error")
	}
}

func TestDryRunNeverCallsGitHub(t *testing.T) {
	t.Parallel()

	err := NewDryRun(discardLogger()).Dispatch(t.Context(), Job{Repository: "acme/api"})
	if err != nil {
		t.Fatalf("Dispatch() = %v", err)
	}
}
