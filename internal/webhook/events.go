package webhook

// Header names carried by every GitHub webhook delivery.
const (
	EventHeader    = "X-GitHub-Event"
	DeliveryHeader = "X-GitHub-Delivery"
)

// User is the subset of a GitHub account we care about.
type User struct {
	Login string `json:"login"`
}

// Repository is the subset of a repository payload we care about.
type Repository struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
}

// Changes carries the previous values of an edited issue or pull request.
type Changes struct {
	Body *struct {
		From string `json:"from"`
	} `json:"body"`
}

// Issue is the subset of an issue payload we care about.
type Issue struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	User    User   `json:"user"`
}

// PullRequest is the subset of a pull request payload we care about.
type PullRequest struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	HTMLURL string `json:"html_url"`
	User    User   `json:"user"`
}

// IssuesEvent is the payload of the "issues" event.
type IssuesEvent struct {
	Action     string     `json:"action"`
	Issue      Issue      `json:"issue"`
	Changes    Changes    `json:"changes"`
	Repository Repository `json:"repository"`
	Sender     User       `json:"sender"`
}

// PullRequestEvent is the payload of the "pull_request" event.
type PullRequestEvent struct {
	Action      string      `json:"action"`
	Number      int         `json:"number"`
	PullRequest PullRequest `json:"pull_request"`
	Changes     Changes     `json:"changes"`
	Repository  Repository  `json:"repository"`
	Sender      User        `json:"sender"`
}

// Commit is the subset of a pushed commit we care about.
type Commit struct {
	ID       string   `json:"id"`
	Added    []string `json:"added"`
	Modified []string `json:"modified"`
	Removed  []string `json:"removed"`
}

// Paths returns every path touched by the commit.
func (c Commit) Paths() []string {
	paths := make([]string, 0, len(c.Added)+len(c.Modified)+len(c.Removed))
	paths = append(paths, c.Added...)
	paths = append(paths, c.Modified...)
	paths = append(paths, c.Removed...)
	return paths
}

// PushEvent is the payload of the "push" event.
type PushEvent struct {
	Ref        string     `json:"ref"`
	Deleted    bool       `json:"deleted"`
	Commits    []Commit   `json:"commits"`
	Repository Repository `json:"repository"`
	Sender     User       `json:"sender"`
}
