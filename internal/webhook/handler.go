package webhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/nonchan7720/renovate-self-hosted/internal/config"
	"github.com/nonchan7720/renovate-self-hosted/internal/dispatch"
)

// MaxBodyBytes caps the size of an accepted webhook delivery. GitHub rejects
// payloads above 25 MB, and Renovate's deliveries are far smaller.
const MaxBodyBytes = 5 << 20

// MaxPushCommits is how many commits GitHub includes in a push payload. A
// larger push is delivered truncated, without any field saying so.
const MaxPushCommits = 20

// Enqueuer accepts jobs produced from webhook events.
type Enqueuer interface {
	Enqueue(job dispatch.Job)
}

// EnqueuerFunc adapts a function to the Enqueuer interface.
type EnqueuerFunc func(job dispatch.Job)

// Enqueue implements Enqueuer.
func (f EnqueuerFunc) Enqueue(job dispatch.Job) { f(job) }

// Handler validates GitHub webhook deliveries and enqueues Renovate runs.
type Handler struct {
	secret  string
	trigger config.Trigger
	queue   Enqueuer
	logger  *slog.Logger
}

// NewHandler builds a Handler. A nil logger falls back to slog.Default.
func NewHandler(secret string, trigger config.Trigger, queue Enqueuer, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{secret: secret, trigger: trigger, queue: queue, logger: logger}
}

// result is the JSON body returned for an accepted delivery.
type result struct {
	Status     string   `json:"status"`
	Reason     string   `json:"reason,omitempty"`
	Repository string   `json:"repository,omitempty"`
	Details    []string `json:"details,omitempty"`
}

const (
	statusQueued  = "queued"
	statusIgnored = "ignored"
)

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}

	event := r.Header.Get(EventHeader)
	delivery := r.Header.Get(DeliveryHeader)
	logger := h.logger.With(slog.String("event", event), slog.String("delivery", delivery))

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		logger.Warn("failed to read request body", slog.Any("error", err))
		httpError(w, http.StatusBadRequest, "unreadable request body")
		return
	}

	if err := VerifySignature(h.secret, r.Header.Get(SignatureHeader), body); err != nil {
		logger.Warn("rejected delivery", slog.Any("error", err))
		status := http.StatusUnauthorized
		if errors.Is(err, ErrMissingSignature) {
			status = http.StatusBadRequest
		}
		httpError(w, status, err.Error())
		return
	}

	res, err := h.route(event, delivery, body, logger)
	if err != nil {
		logger.Warn("failed to handle delivery", slog.Any("error", err))
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) route(event, delivery string, body []byte, logger *slog.Logger) (result, error) {
	switch event {
	case "ping":
		return result{Status: statusIgnored, Reason: "ping"}, nil
	case "issues":
		return h.handleIssues(delivery, body, logger)
	case "pull_request":
		return h.handlePullRequest(delivery, body, logger)
	case "push":
		return h.handlePush(delivery, body, logger)
	case "":
		return result{}, errors.New("missing " + EventHeader + " header")
	default:
		return result{Status: statusIgnored, Reason: "unsupported event " + event}, nil
	}
}

// handleIssues reacts to a checkbox being ticked on Renovate's Dependency
// Dashboard issue.
func (h *Handler) handleIssues(delivery string, body []byte, logger *slog.Logger) (result, error) {
	var ev IssuesEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return result{}, fmt.Errorf("decode issues payload: %w", err)
	}
	return h.handleCheckboxEdit(delivery, ev.Action, ev.Repository, ev.Sender, ev.Changes, checkboxEdit{
		subject:   "issue",
		number:    ev.Issue.Number,
		body:      ev.Issue.Body,
		state:     ev.Issue.State,
		author:    ev.Issue.User,
		htmlURL:   ev.Issue.HTMLURL,
		reason:    dispatch.ReasonDashboardCheckbox,
		logMsg:    "dependency dashboard checkbox ticked",
		numberKey: "issue",
	}, logger)
}

// handlePullRequest reacts to a checkbox being ticked in the body of a Renovate
// pull request, such as the rebase/retry box.
func (h *Handler) handlePullRequest(delivery string, body []byte, logger *slog.Logger) (result, error) {
	var ev PullRequestEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return result{}, fmt.Errorf("decode pull_request payload: %w", err)
	}
	return h.handleCheckboxEdit(delivery, ev.Action, ev.Repository, ev.Sender, ev.Changes, checkboxEdit{
		subject:   "pull request",
		number:    ev.PullRequest.Number,
		body:      ev.PullRequest.Body,
		state:     ev.PullRequest.State,
		author:    ev.PullRequest.User,
		htmlURL:   ev.PullRequest.HTMLURL,
		reason:    dispatch.ReasonPullRequestCheckbox,
		logMsg:    "pull request checkbox ticked",
		numberKey: "pull_request",
	}, logger)
}

// checkboxEdit is the part of an edited issue or pull request the checkbox
// rules act on, plus the bits that differ between the two handlers.
type checkboxEdit struct {
	subject   string // "issue" or "pull request", used in the ignore reasons
	number    int
	body      string
	state     string
	author    User
	htmlURL   string
	reason    string // one of the dispatch.Reason* constants
	logMsg    string
	numberKey string // slog key: "issue" or "pull_request"
}

// handleCheckboxEdit is the pipeline shared by handleIssues and
// handlePullRequest: action=="edited" -> author must be a configured Renovate
// bot -> state must be open -> checkEditable -> NewlyChecked -> enqueue a run.
func (h *Handler) handleCheckboxEdit(delivery, action string, repo Repository, sender User, changes Changes, edit checkboxEdit, logger *slog.Logger) (result, error) {
	if action != "edited" {
		return result{Status: statusIgnored, Reason: edit.subject + " action " + action}, nil
	}
	if !h.trigger.IsBot(edit.author.Login) {
		return result{Status: statusIgnored, Reason: edit.subject + " is not owned by Renovate"}, nil
	}
	// Closing the issue or pull request is how a repository opts out, so an
	// edit to a closed one is not a request to run.
	if edit.state != "" && edit.state != "open" {
		return result{Status: statusIgnored, Reason: edit.subject + " is " + edit.state}, nil
	}
	if res, ok := h.checkEditable(repo, sender, changes); !ok {
		return res, nil
	}

	checked := NewlyChecked(changes.Body.From, edit.body)
	if len(checked) == 0 {
		return result{Status: statusIgnored, Reason: "no checkbox was ticked"}, nil
	}

	labels := Labels(checked)
	logger.Info(edit.logMsg,
		slog.String("repository", repo.FullName),
		slog.Int(edit.numberKey, edit.number),
		slog.String("sender", sender.Login),
		slog.Any("checked", labels))

	h.queue.Enqueue(dispatch.Job{
		Repository: repo.FullName,
		Reasons:    []string{edit.reason},
		Details:    labels,
		Deliveries: deliveries(delivery),
		URL:        edit.htmlURL,
	})
	return result{
		Status:     statusQueued,
		Reason:     edit.reason,
		Repository: repo.FullName,
		Details:    labels,
	}, nil
}

// handlePush reacts to Renovate configuration changing on the default branch.
func (h *Handler) handlePush(delivery string, body []byte, logger *slog.Logger) (result, error) {
	if !h.trigger.OnPush {
		return result{Status: statusIgnored, Reason: "push triggers are disabled"}, nil
	}

	var ev PushEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return result{}, fmt.Errorf("decode push payload: %w", err)
	}
	if ev.Deleted {
		return result{Status: statusIgnored, Reason: "branch deletion"}, nil
	}
	if branch, ok := strings.CutPrefix(ev.Ref, "refs/heads/"); !ok || branch != ev.Repository.DefaultBranch {
		return result{Status: statusIgnored, Reason: "push is not on the default branch"}, nil
	}
	if !h.trigger.RepositoryAllowed(ev.Repository.FullName) {
		return result{Status: statusIgnored, Reason: "repository is not allowed"}, nil
	}

	var touched []string
	for _, commit := range ev.Commits {
		for _, path := range commit.Paths() {
			if h.trigger.MatchesPushPath(path) && !slices.Contains(touched, path) {
				touched = append(touched, path)
			}
		}
	}

	// GitHub caps the commits array at MaxPushCommits and sets no flag saying
	// it did. A push at the cap may well have touched a config file in a
	// commit we cannot see, so run rather than silently skip.
	truncated := len(touched) == 0 && len(ev.Commits) >= MaxPushCommits
	if truncated {
		touched = append(touched, fmt.Sprintf("commit list truncated at %d, config file changes cannot be ruled out", MaxPushCommits))
	}

	if len(touched) == 0 {
		return result{Status: statusIgnored, Reason: "no Renovate configuration file changed"}, nil
	}

	if truncated {
		logger.Info("push commit list truncated, running to be safe",
			slog.String("repository", ev.Repository.FullName),
			slog.Int("commits", len(ev.Commits)))
	} else {
		logger.Info("renovate configuration changed",
			slog.String("repository", ev.Repository.FullName),
			slog.Any("paths", touched))
	}

	h.queue.Enqueue(dispatch.Job{
		Repository: ev.Repository.FullName,
		Reasons:    []string{dispatch.ReasonConfigPush},
		Details:    touched,
		Deliveries: deliveries(delivery),
	})
	return result{
		Status:     statusQueued,
		Reason:     dispatch.ReasonConfigPush,
		Repository: ev.Repository.FullName,
		Details:    touched,
	}, nil
}

// checkEditable applies the guards shared by the issue and pull request edit
// handlers. It reports false together with the response to send when the
// delivery must not produce a run.
func (h *Handler) checkEditable(repo Repository, sender User, changes Changes) (result, bool) {
	// Renovate rewrites these bodies itself whenever it finishes a run, which
	// would otherwise bounce straight back to us as another run request.
	if h.trigger.IsBot(sender.Login) {
		return result{Status: statusIgnored, Reason: "edit was made by Renovate itself"}, false
	}
	if !h.trigger.RepositoryAllowed(repo.FullName) {
		return result{Status: statusIgnored, Reason: "repository is not allowed"}, false
	}
	if changes.Body == nil {
		return result{Status: statusIgnored, Reason: "body was not edited"}, false
	}
	return result{}, true
}

func deliveries(id string) []string {
	if id == "" {
		return nil
	}
	return []string{id}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Debug("failed to write response", slog.Any("error", err))
	}
}

func httpError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
