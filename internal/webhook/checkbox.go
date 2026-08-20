package webhook

import (
	"regexp"
	"strings"
)

var (
	taskItemRE    = regexp.MustCompile(`^[ \t]*[-*+][ \t]+\[([ xX])\][ \t]*(.*)$`)
	htmlCommentRE = regexp.MustCompile(`<!--(.*?)-->`)
	whitespaceRE  = regexp.MustCompile(`\s+`)
)

// TaskItem is a single markdown task list entry, such as the "check this box"
// controls Renovate renders on the Dependency Dashboard issue and on its pull
// requests.
type TaskItem struct {
	// Key identifies the item across body revisions. Renovate annotates its
	// checkboxes with an HTML comment (for example "rebase-check"), which is
	// far more stable than the visible label, so that wins when present.
	Key string
	// Marker is the HTML comment Renovate attached to the item, if any.
	Marker string
	// Label is the visible text with HTML comments removed.
	Label string
	// Checked reports whether the box is ticked.
	Checked bool
}

// ParseTaskList extracts every markdown task list item from a issue or pull
// request body, in document order.
func ParseTaskList(body string) []TaskItem {
	if body == "" {
		return nil
	}

	var items []TaskItem
	for line := range strings.SplitSeq(body, "\n") {
		m := taskItemRE.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}

		rest := m[2]
		marker := ""
		if c := htmlCommentRE.FindStringSubmatch(rest); c != nil {
			marker = strings.TrimSpace(c[1])
		}
		label := normalize(htmlCommentRE.ReplaceAllString(rest, " "))

		key := marker
		if key == "" {
			key = label
		}
		items = append(items, TaskItem{
			Key:     strings.ToLower(key),
			Marker:  marker,
			Label:   label,
			Checked: m[1] != " ",
		})
	}
	return items
}

// NewlyChecked reports the task items that are ticked in newBody but were not
// ticked in oldBody. Items that only appear in newBody count as newly ticked:
// a human adding an already ticked box is still a request to act.
//
// Items are matched by key, and by position among the items sharing that key.
// Renovate marks each of its controls with a distinct HTML comment, so its
// keys are unique and the position never matters. Keys can repeat only on the
// label fallback, and there positions are only meaningful while the items
// under that key stay put: adding or removing one shifts the rest, so an item
// that was already ticked can land on an index that used to hold an unticked
// one and read as a fresh tick. When a key gains or loses items, the tick
// count therefore decides instead; when it does not, positions are trustworthy
// and a tick swapped from one item to another is still a tick.
func NewlyChecked(oldBody, newBody string) []TaskItem {
	before := make(map[string][]bool)
	tickedBefore := make(map[string]int)
	for _, item := range ParseTaskList(oldBody) {
		before[item.Key] = append(before[item.Key], item.Checked)
		if item.Checked {
			tickedBefore[item.Key]++
		}
	}

	after := ParseTaskList(newBody)
	tickedAfter := make(map[string]int)
	totalAfter := make(map[string]int)
	for _, item := range after {
		totalAfter[item.Key]++
		if item.Checked {
			tickedAfter[item.Key]++
		}
	}

	seen := make(map[string]int)
	var checked []TaskItem
	for _, item := range after {
		occurrence := seen[item.Key]
		seen[item.Key]++
		if !item.Checked {
			continue
		}
		if prev := before[item.Key]; occurrence < len(prev) && prev[occurrence] {
			continue
		}
		if len(before[item.Key]) != totalAfter[item.Key] && tickedAfter[item.Key] <= tickedBefore[item.Key] {
			continue
		}
		checked = append(checked, item)
	}
	return checked
}

// Labels returns the display labels of the given items, for logging.
func Labels(items []TaskItem) []string {
	labels := make([]string, 0, len(items))
	for _, item := range items {
		switch {
		case item.Label != "":
			labels = append(labels, item.Label)
		case item.Marker != "":
			labels = append(labels, item.Marker)
		default:
			labels = append(labels, "(unnamed checkbox)")
		}
	}
	return labels
}

func normalize(s string) string {
	return strings.TrimSpace(whitespaceRE.ReplaceAllString(s, " "))
}
