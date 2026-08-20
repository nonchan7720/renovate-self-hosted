package webhook_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/nonchan7720/renovate-self-hosted/internal/webhook"
)

// dashboardBody mirrors the shape of a real Renovate Dependency Dashboard.
const dashboardBody = `This issue lists Renovate updates and detected dependencies.

## Rate-Limited

- [ ] <!-- unlimit-branch=renovate/golang.org-x-net-0.x -->Update module golang.org/x/net to v0.30.0
- [ ] <!-- create-all-rate-limited-prs -->**Create all rate-limited PRs at once** 🚀

## Open

- [ ] <!-- rebase-branch=renovate/actions-checkout-5.x -->Update actions/checkout action to v5

---

- [ ] <!-- manual job -->Check this box to trigger a request for Renovate to run again on this repository
`

func TestParseTaskList(t *testing.T) {
	t.Parallel()

	items := webhook.ParseTaskList(dashboardBody)
	if got, want := len(items), 4; got != want {
		t.Fatalf("ParseTaskList() returned %d items, want %d", got, want)
	}
	if got, want := items[0].Marker, "unlimit-branch=renovate/golang.org-x-net-0.x"; got != want {
		t.Errorf("Marker = %q, want %q", got, want)
	}
	if got, want := items[0].Label, "Update module golang.org/x/net to v0.30.0"; got != want {
		t.Errorf("Label = %q, want %q", got, want)
	}
	for _, item := range items {
		if item.Checked {
			t.Errorf("item %q is checked, want unchecked", item.Key)
		}
	}
}

func TestParseTaskListVariants(t *testing.T) {
	t.Parallel()

	body := "* [x] star bullet\r\n+ [X] plus bullet, upper case\n  - [ ] indented\ntext - [ ] not a list item\n- [] malformed"
	items := webhook.ParseTaskList(body)
	if got, want := len(items), 3; got != want {
		t.Fatalf("ParseTaskList() returned %d items, want %d: %+v", got, want, items)
	}
	if !items[0].Checked || !items[1].Checked || items[2].Checked {
		t.Errorf("unexpected checked states: %+v", items)
	}
	if got, want := items[0].Label, "star bullet"; got != want {
		t.Errorf("Label = %q, want %q (carriage return should be trimmed)", got, want)
	}
}

func TestNewlyChecked(t *testing.T) {
	t.Parallel()

	tick := func(body, marker string) string {
		return replaceOnce(t, body, "- [ ] <!-- "+marker+" -->", "- [x] <!-- "+marker+" -->")
	}

	tests := map[string]struct {
		old, new string
		want     []string
	}{
		"manual run box ticked": {
			old:  dashboardBody,
			new:  tick(dashboardBody, "manual job"),
			want: []string{"manual job"},
		},
		"rate limited branch unlimited": {
			old:  dashboardBody,
			new:  tick(dashboardBody, "unlimit-branch=renovate/golang.org-x-net-0.x"),
			want: []string{"unlimit-branch=renovate/golang.org-x-net-0.x"},
		},
		"two boxes ticked at once": {
			old:  dashboardBody,
			new:  tick(tick(dashboardBody, "manual job"), "create-all-rate-limited-prs"),
			want: []string{"create-all-rate-limited-prs", "manual job"},
		},
		"unrelated edit": {
			old:  dashboardBody,
			new:  dashboardBody + "\nsome extra prose\n",
			want: nil,
		},
		"box unticked": {
			old:  tick(dashboardBody, "manual job"),
			new:  dashboardBody,
			want: nil,
		},
		"already ticked box stays ticked": {
			old:  tick(dashboardBody, "manual job"),
			new:  tick(dashboardBody, "manual job") + "\ntrailing edit\n",
			want: nil,
		},
		"pull request rebase box": {
			old:  "- [ ] <!-- rebase-check -->If you want to rebase/retry this PR, check this box\n",
			new:  "- [x] <!-- rebase-check -->If you want to rebase/retry this PR, check this box\n",
			want: []string{"rebase-check"},
		},
		"checkbox without marker": {
			old:  "- [ ] Do the thing\n",
			new:  "- [x] Do the thing\n",
			want: []string{"do the thing"},
		},
		"box added already ticked": {
			old:  "",
			new:  "- [x] <!-- manual job -->Check this box\n",
			want: []string{"manual job"},
		},
		"renovate rewrote the whole dashboard": {
			old:  dashboardBody,
			new:  "This repository currently has no open or pending branches.\n\n- [ ] <!-- manual job -->Check this box to trigger a request for Renovate to run again on this repository\n",
			want: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var got []string
			for _, item := range webhook.NewlyChecked(tc.old, tc.new) {
				got = append(got, item.Key)
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("NewlyChecked() keys = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestNewlyCheckedRepeatedLabels makes sure identical labels are matched
// position by position rather than collapsing into a single item.
func TestNewlyCheckedRepeatedLabels(t *testing.T) {
	t.Parallel()

	old := "- [ ] retry\n- [ ] retry\n"
	updated := "- [ ] retry\n- [x] retry\n"

	got := webhook.NewlyChecked(old, updated)
	if len(got) != 1 {
		t.Fatalf("NewlyChecked() returned %d items, want 1: %+v", len(got), got)
	}
	if got[0].Label != "retry" {
		t.Fatalf("Label = %q, want %q", got[0].Label, "retry")
	}
}

// TestNewlyCheckedShiftedDuplicateLabels covers the label fallback, where two
// items can share a key. Removing an item shifts the rest up, so the item that
// lands at a given index is not the one that was there before; without the
// tick count guard that shift reads as a fresh tick and fires a run nobody
// asked for.
func TestNewlyCheckedShiftedDuplicateLabels(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		old, new string
		want     int
	}{
		"already ticked item shifts up": {
			old:  "- [ ] retry\n- [x] retry\n",
			new:  "- [x] retry\n",
			want: 0,
		},
		"already ticked item shifts down": {
			old:  "- [x] retry\n",
			new:  "- [ ] retry\n- [x] retry\n",
			want: 0,
		},
		"genuinely ticked among duplicates": {
			old:  "- [ ] retry\n- [ ] retry\n",
			new:  "- [x] retry\n- [ ] retry\n",
			want: 1,
		},
		"second of two ticked": {
			old:  "- [x] retry\n- [ ] retry\n",
			new:  "- [x] retry\n- [x] retry\n",
			want: 1,
		},
		"unrelated item removed": {
			old:  "- [ ] one\n- [ ] retry\n- [ ] retry\n",
			new:  "- [ ] retry\n- [ ] retry\n",
			want: 0,
		},
		// The item count is unchanged here, so positions still mean
		// something: one box was ticked and another unticked in the same
		// edit, and the tick is a real request even though the total did
		// not move.
		"one ticked while another is unticked": {
			old:  "- [x] retry\n- [ ] retry\n",
			new:  "- [ ] retry\n- [x] retry\n",
			want: 1,
		},
		"all ticks moved down by one": {
			old:  "- [x] retry\n- [x] retry\n- [ ] retry\n",
			new:  "- [ ] retry\n- [x] retry\n- [x] retry\n",
			want: 1,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := webhook.NewlyChecked(tc.old, tc.new)
			if len(got) != tc.want {
				t.Fatalf("NewlyChecked() returned %d items, want %d: %+v", len(got), tc.want, got)
			}
		})
	}
}

// TestNewlyCheckedMarkedItemsAreUnaffected makes sure the guard above does not
// weaken the normal path, where Renovate's markers make every key unique.
func TestNewlyCheckedMarkedItemsAreUnaffected(t *testing.T) {
	t.Parallel()

	old := "- [ ] <!-- manual job -->Run again\n- [x] <!-- rebase-check -->Rebase\n"
	updated := "- [x] <!-- manual job -->Run again\n- [x] <!-- rebase-check -->Rebase\n"

	got := webhook.NewlyChecked(old, updated)
	if len(got) != 1 || got[0].Marker != "manual job" {
		t.Fatalf("NewlyChecked() = %+v, want only the manual job box", got)
	}
}

func replaceOnce(t *testing.T, body, old, updated string) string {
	t.Helper()
	out := strings.Replace(body, old, updated, 1)
	if out == body {
		t.Fatalf("fixture does not contain %q", old)
	}
	return out
}
