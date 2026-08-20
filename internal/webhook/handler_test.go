package webhook_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nonchan7720/renovate-self-hosted/internal/config"
	"github.com/nonchan7720/renovate-self-hosted/internal/dispatch"
	"github.com/nonchan7720/renovate-self-hosted/internal/webhook"
)

const testSecret = "webhook-secret"

func testTrigger() config.Trigger {
	return config.Trigger{
		BotLogins: config.DefaultBotLogins,
		OnPush:    true,
		PushPaths: config.DefaultPushPaths,
	}
}

type recorder struct {
	jobs []dispatch.Job
}

func (r *recorder) Enqueue(job dispatch.Job) { r.jobs = append(r.jobs, job) }

func newTestHandler(t *testing.T, trigger config.Trigger) (*webhook.Handler, *recorder) {
	t.Helper()
	queue := &recorder{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return webhook.NewHandler(testSecret, trigger, queue, logger), queue
}

func post(t *testing.T, h http.Handler, event string, payload any, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set(webhook.EventHeader, event)
	req.Header.Set(webhook.DeliveryHeader, "delivery-1")
	req.Header.Set(webhook.SignatureHeader, webhook.Sign(testSecret, body))
	req.Header.Set("Content-Type", "application/json")
	for _, opt := range opts {
		opt(req)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeStatus(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var res struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if res.Error != "" {
		return "error: " + res.Error
	}
	return res.Status
}

func issuePayload(action, sender, oldBody, newBody string) map[string]any {
	payload := map[string]any{
		"action": action,
		"issue": map[string]any{
			"number":   7,
			"title":    "Dependency Dashboard",
			"body":     newBody,
			"state":    "open",
			"html_url": "https://github.com/acme/api/issues/7",
			"user":     map[string]any{"login": "renovate[bot]"},
		},
		"repository": map[string]any{"full_name": "acme/api", "default_branch": "main"},
		"sender":     map[string]any{"login": sender},
	}
	if oldBody != "" {
		payload["changes"] = map[string]any{"body": map[string]any{"from": oldBody}}
	}
	return payload
}

func pullRequestPayload(action, sender, oldBody, newBody string) map[string]any {
	payload := map[string]any{
		"action": action,
		"number": 42,
		"pull_request": map[string]any{
			"number":   42,
			"title":    "Update module golang.org/x/net to v0.30.0",
			"body":     newBody,
			"state":    "open",
			"html_url": "https://github.com/acme/api/pull/42",
			"user":     map[string]any{"login": "renovate[bot]"},
		},
		"repository": map[string]any{"full_name": "acme/api", "default_branch": "main"},
		"sender":     map[string]any{"login": sender},
	}
	if oldBody != "" {
		payload["changes"] = map[string]any{"body": map[string]any{"from": oldBody}}
	}
	return payload
}

func TestHandlerDashboardCheckbox(t *testing.T) {
	t.Parallel()

	h, queue := newTestHandler(t, testTrigger())
	ticked := strings.Replace(dashboardBody, "- [ ] <!-- manual job -->", "- [x] <!-- manual job -->", 1)

	rec := post(t, h, "issues", issuePayload("edited", "alice", dashboardBody, ticked))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (%s)", rec.Code, http.StatusOK, rec.Body)
	}
	if got := decodeStatus(t, rec); got != "queued" {
		t.Fatalf("status = %q, want %q", got, "queued")
	}
	if len(queue.jobs) != 1 {
		t.Fatalf("enqueued %d jobs, want 1", len(queue.jobs))
	}
	job := queue.jobs[0]
	if job.Repository != "acme/api" {
		t.Errorf("Repository = %q, want %q", job.Repository, "acme/api")
	}
	if len(job.Reasons) != 1 || job.Reasons[0] != dispatch.ReasonDashboardCheckbox {
		t.Errorf("Reasons = %v, want [%s]", job.Reasons, dispatch.ReasonDashboardCheckbox)
	}
	if job.URL != "https://github.com/acme/api/issues/7" {
		t.Errorf("URL = %q", job.URL)
	}
}

func TestHandlerPullRequestCheckbox(t *testing.T) {
	t.Parallel()

	const prBody = "- [ ] <!-- rebase-check -->If you want to rebase/retry this PR, check this box\n"
	ticked := strings.Replace(prBody, "- [ ]", "- [x]", 1)

	h, queue := newTestHandler(t, testTrigger())
	rec := post(t, h, "pull_request", pullRequestPayload("edited", "alice", prBody, ticked))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (%s)", rec.Code, http.StatusOK, rec.Body)
	}
	if got := decodeStatus(t, rec); got != "queued" {
		t.Fatalf("status = %q, want %q", got, "queued")
	}
	if len(queue.jobs) != 1 {
		t.Fatalf("enqueued %d jobs, want 1", len(queue.jobs))
	}
	if got := queue.jobs[0].Reasons[0]; got != dispatch.ReasonPullRequestCheckbox {
		t.Errorf("Reason = %q, want %q", got, dispatch.ReasonPullRequestCheckbox)
	}
}

func TestHandlerIgnoredDeliveries(t *testing.T) {
	t.Parallel()

	ticked := strings.Replace(dashboardBody, "- [ ] <!-- manual job -->", "- [x] <!-- manual job -->", 1)

	tests := map[string]struct {
		event   string
		payload any
		trigger config.Trigger
	}{
		"ping": {
			event: "ping", payload: map[string]any{"zen": "Keep it logically awesome."},
		},
		"unsupported event": {
			event: "star", payload: map[string]any{"action": "created"},
		},
		"issue opened": {
			event: "issues", payload: issuePayload("opened", "alice", "", dashboardBody),
		},
		"issue body unchanged": {
			event: "issues", payload: issuePayload("edited", "alice", "", ticked),
		},
		"no checkbox ticked": {
			event: "issues", payload: issuePayload("edited", "alice", dashboardBody, dashboardBody+"\nnote\n"),
		},
		"issue not owned by renovate": {
			event: "issues",
			payload: func() map[string]any {
				p := issuePayload("edited", "alice", dashboardBody, ticked)
				p["issue"].(map[string]any)["user"] = map[string]any{"login": "alice"}
				return p
			}(),
		},
		"renovate edited its own dashboard": {
			event: "issues", payload: issuePayload("edited", "renovate[bot]", dashboardBody, ticked),
		},
		"repository not allowed": {
			event:   "issues",
			payload: issuePayload("edited", "alice", dashboardBody, ticked),
			trigger: config.Trigger{
				BotLogins:           config.DefaultBotLogins,
				AllowedRepositories: []string{"other/repo"},
			},
		},
		"closed pull request": {
			event: "pull_request",
			payload: func() map[string]any {
				p := pullRequestPayload("edited", "alice", "- [ ] <!-- rebase-check -->x\n", "- [x] <!-- rebase-check -->x\n")
				p["pull_request"].(map[string]any)["state"] = "closed"
				return p
			}(),
		},
		"push to a side branch": {
			event: "push",
			payload: map[string]any{
				"ref":        "refs/heads/feature",
				"repository": map[string]any{"full_name": "acme/api", "default_branch": "main"},
				"commits":    []any{map[string]any{"id": "abc", "modified": []string{"renovate.json"}}},
			},
		},
		"push without config changes": {
			event: "push",
			payload: map[string]any{
				"ref":        "refs/heads/main",
				"repository": map[string]any{"full_name": "acme/api", "default_branch": "main"},
				"commits":    []any{map[string]any{"id": "abc", "modified": []string{"README.md"}}},
			},
		},
		"push triggers disabled": {
			event: "push",
			payload: map[string]any{
				"ref":        "refs/heads/main",
				"repository": map[string]any{"full_name": "acme/api", "default_branch": "main"},
				"commits":    []any{map[string]any{"id": "abc", "modified": []string{"renovate.json"}}},
			},
			trigger: config.Trigger{BotLogins: config.DefaultBotLogins, OnPush: false},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			trigger := tc.trigger
			if trigger.BotLogins == nil {
				trigger = testTrigger()
			}
			h, queue := newTestHandler(t, trigger)

			rec := post(t, h, tc.event, tc.payload)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, http.StatusOK, rec.Body)
			}
			if got := decodeStatus(t, rec); got != "ignored" {
				t.Fatalf("status = %q, want %q (%s)", got, "ignored", rec.Body)
			}
			if len(queue.jobs) != 0 {
				t.Fatalf("enqueued %d jobs, want 0: %+v", len(queue.jobs), queue.jobs)
			}
		})
	}
}

func TestHandlerConfigPush(t *testing.T) {
	t.Parallel()

	h, queue := newTestHandler(t, testTrigger())
	rec := post(t, h, "push", map[string]any{
		"ref":        "refs/heads/main",
		"repository": map[string]any{"full_name": "acme/api", "default_branch": "main"},
		"commits": []any{
			map[string]any{"id": "abc", "modified": []string{"README.md", ".github/renovate.json"}},
			map[string]any{"id": "def", "added": []string{".github/renovate.json"}},
		},
	})

	if got := decodeStatus(t, rec); got != "queued" {
		t.Fatalf("status = %q, want %q (%s)", got, "queued", rec.Body)
	}
	if len(queue.jobs) != 1 {
		t.Fatalf("enqueued %d jobs, want 1", len(queue.jobs))
	}
	if got := queue.jobs[0].Details; len(got) != 1 || got[0] != ".github/renovate.json" {
		t.Errorf("Details = %v, want [.github/renovate.json] without duplicates", got)
	}
}

// TestHandlerTruncatedPush covers GitHub capping the commits array at 20 with
// no field saying it did: a config change can hide in a commit the payload
// never carried, so a push at the cap must run rather than be skipped.
func TestHandlerTruncatedPush(t *testing.T) {
	t.Parallel()

	commits := func(n int) []any {
		out := make([]any, 0, n)
		for i := range n {
			out = append(out, map[string]any{
				"id":       fmt.Sprintf("commit-%d", i),
				"modified": []string{fmt.Sprintf("internal/thing%d.go", i)},
			})
		}
		return out
	}
	push := func(n int) map[string]any {
		return map[string]any{
			"ref":        "refs/heads/main",
			"repository": map[string]any{"full_name": "acme/api", "default_branch": "main"},
			"commits":    commits(n),
		}
	}

	t.Run("at the cap", func(t *testing.T) {
		t.Parallel()

		h, queue := newTestHandler(t, testTrigger())
		rec := post(t, h, "push", push(webhook.MaxPushCommits))

		if got := decodeStatus(t, rec); got != "queued" {
			t.Fatalf("status = %q, want %q (%s)", got, "queued", rec.Body)
		}
		if len(queue.jobs) != 1 {
			t.Fatalf("enqueued %d jobs, want 1", len(queue.jobs))
		}
		if got := queue.jobs[0].Details; len(got) != 1 || !strings.Contains(got[0], "truncated") {
			t.Errorf("Details = %v, want the truncation to be explained", got)
		}
	})

	t.Run("below the cap", func(t *testing.T) {
		t.Parallel()

		h, queue := newTestHandler(t, testTrigger())
		rec := post(t, h, "push", push(webhook.MaxPushCommits-1))

		if got := decodeStatus(t, rec); got != "ignored" {
			t.Fatalf("status = %q, want %q (%s)", got, "ignored", rec.Body)
		}
		if len(queue.jobs) != 0 {
			t.Fatalf("enqueued %d jobs, want 0", len(queue.jobs))
		}
	})

	t.Run("at the cap with a real config change", func(t *testing.T) {
		t.Parallel()

		h, queue := newTestHandler(t, testTrigger())
		payload := push(webhook.MaxPushCommits)
		payload["commits"].([]any)[3].(map[string]any)["modified"] = []string{"renovate.json"}

		rec := post(t, h, "push", payload)
		if got := decodeStatus(t, rec); got != "queued" {
			t.Fatalf("status = %q, want %q", got, "queued")
		}
		if got := queue.jobs[0].Details; len(got) != 1 || got[0] != "renovate.json" {
			t.Errorf("Details = %v, want the actual path rather than the truncation note", got)
		}
	})
}

func TestHandlerRejectsBadRequests(t *testing.T) {
	t.Parallel()

	payload := issuePayload("edited", "alice", dashboardBody, dashboardBody)

	t.Run("bad signature", func(t *testing.T) {
		t.Parallel()
		h, queue := newTestHandler(t, testTrigger())
		rec := post(t, h, "issues", payload, func(r *http.Request) {
			r.Header.Set(webhook.SignatureHeader, "sha256=deadbeef")
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if len(queue.jobs) != 0 {
			t.Fatalf("enqueued %d jobs, want 0", len(queue.jobs))
		}
	})

	t.Run("missing signature", func(t *testing.T) {
		t.Parallel()
		h, _ := newTestHandler(t, testTrigger())
		rec := post(t, h, "issues", payload, func(r *http.Request) {
			r.Header.Del(webhook.SignatureHeader)
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("missing event header", func(t *testing.T) {
		t.Parallel()
		h, _ := newTestHandler(t, testTrigger())
		rec := post(t, h, "", payload, func(r *http.Request) {
			r.Header.Del(webhook.EventHeader)
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		t.Parallel()
		h, _ := newTestHandler(t, testTrigger())
		body := []byte("{not json")
		req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
		req.Header.Set(webhook.EventHeader, "issues")
		req.Header.Set(webhook.SignatureHeader, webhook.Sign(testSecret, body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("wrong method", func(t *testing.T) {
		t.Parallel()
		h, _ := newTestHandler(t, testTrigger())
		req := httptest.NewRequest(http.MethodGet, "/webhook", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})
}
