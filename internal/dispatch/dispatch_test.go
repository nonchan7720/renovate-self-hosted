package dispatch_test

import (
	"slices"
	"testing"

	"github.com/nonchan7720/renovate-self-hosted/internal/dispatch"
)

func TestJobMerge(t *testing.T) {
	t.Parallel()

	base := dispatch.Job{
		Repository: "acme/api",
		Reasons:    []string{dispatch.ReasonDashboardCheckbox},
		Details:    []string{"manual job"},
		Deliveries: []string{"d1"},
		URL:        "https://github.com/acme/api/issues/7",
	}
	merged := base.Merge(dispatch.Job{
		Repository: "acme/api",
		Reasons:    []string{dispatch.ReasonDashboardCheckbox, dispatch.ReasonPullRequestCheckbox},
		Details:    []string{"manual job", "rebase-check"},
		Deliveries: []string{"d2"},
		URL:        "https://github.com/acme/api/pull/42",
	})

	if want := []string{dispatch.ReasonDashboardCheckbox, dispatch.ReasonPullRequestCheckbox}; !slices.Equal(merged.Reasons, want) {
		t.Errorf("Reasons = %v, want %v", merged.Reasons, want)
	}
	if want := []string{"manual job", "rebase-check"}; !slices.Equal(merged.Details, want) {
		t.Errorf("Details = %v, want %v", merged.Details, want)
	}
	if want := []string{"d1", "d2"}; !slices.Equal(merged.Deliveries, want) {
		t.Errorf("Deliveries = %v, want %v", merged.Deliveries, want)
	}
	if merged.URL != base.URL {
		t.Errorf("URL = %q, want the first URL %q", merged.URL, base.URL)
	}
}

func TestJobMergeKeepsFirstURLWhenEmpty(t *testing.T) {
	t.Parallel()

	merged := dispatch.Job{Repository: "acme/api"}.Merge(dispatch.Job{URL: "https://example.test/pull/1"})
	if merged.URL != "https://example.test/pull/1" {
		t.Errorf("URL = %q, want the incoming URL to fill the gap", merged.URL)
	}
}
