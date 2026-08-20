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
