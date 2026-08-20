// Package dispatch turns webhook events into Renovate runs executed by a
// GitHub Actions workflow living in a separate repository.
package dispatch

import (
	"context"
	"slices"
)

// Reasons explaining why a Renovate run was requested.
const (
	ReasonDashboardCheckbox   = "dependency-dashboard-checkbox"
	ReasonPullRequestCheckbox = "pull-request-checkbox"
	ReasonConfigPush          = "config-push"
	ReasonManual              = "manual"
)

// Job is a request to run Renovate against a single repository.
type Job struct {
	// Repository is the "owner/repo" Renovate should process.
	Repository string
	// Reasons lists why the run was requested, in first-seen order.
	Reasons []string
	// Details carries human readable context, such as the checkbox labels
	// that were ticked.
	Details []string
	// Deliveries lists the webhook delivery IDs folded into this job.
	Deliveries []string
	// URL points at the issue, pull request or commit that triggered the run.
	URL string
}

// Merge folds other into j, keeping j's repository and URL. It is used when
// several events for the same repository are coalesced into one run.
func (j Job) Merge(other Job) Job {
	for _, reason := range other.Reasons {
		if !slices.Contains(j.Reasons, reason) {
			j.Reasons = append(j.Reasons, reason)
		}
	}
	for _, detail := range other.Details {
		if !slices.Contains(j.Details, detail) {
			j.Details = append(j.Details, detail)
		}
	}
	j.Deliveries = append(j.Deliveries, other.Deliveries...)
	if j.URL == "" {
		j.URL = other.URL
	}
	return j
}

// Dispatcher starts a Renovate run for a job.
type Dispatcher interface {
	Dispatch(ctx context.Context, job Job) error
}

// DispatcherFunc adapts a function to the Dispatcher interface.
type DispatcherFunc func(ctx context.Context, job Job) error

// Dispatch implements Dispatcher.
func (f DispatcherFunc) Dispatch(ctx context.Context, job Job) error { return f(ctx, job) }
