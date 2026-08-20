// Package config loads the application configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Default values applied when the corresponding environment variable is unset.
const (
	DefaultAddr            = ":8080"
	DefaultPath            = "/webhook"
	DefaultGitHubAPIURL    = "https://api.github.com"
	DefaultWorkflow        = "renovate.yml"
	DefaultRef             = "main"
	DefaultRepositoryInput = "repositories"
	DefaultShutdownTimeout = 15 * time.Second
	DefaultDebounceWindow  = 10 * time.Second
	DefaultDebounceMaxWait = 2 * time.Minute
)

// DefaultBotLogins are the accounts whose issues and pull requests are treated
// as Renovate-owned.
var DefaultBotLogins = []string{"renovate[bot]", "renovate-bot"}

// DefaultPushPaths are the Renovate configuration files that trigger a run when
// they change on the default branch.
var DefaultPushPaths = []string{
	"renovate.json",
	"renovate.json5",
	".renovaterc",
	".renovaterc.json",
	".renovaterc.json5",
	".github/renovate.json",
	".github/renovate.json5",
	".gitlab/renovate.json",
}

// Config is the fully resolved application configuration.
type Config struct {
	Addr            string
	Path            string
	WebhookSecret   string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
	DryRun          bool

	GitHubAPIURL string
	GitHubToken  string

	Runner   Runner
	Trigger  Trigger
	Debounce Debounce
}

// Runner describes the separate repository that actually executes Renovate.
type Runner struct {
	Repository      string
	Workflow        string
	Ref             string
	RepositoryInput string
	ExtraInputs     map[string]string
}

// Trigger describes which incoming events are turned into Renovate runs.
type Trigger struct {
	BotLogins           []string
	AllowedRepositories []string
	OnPush              bool
	PushPaths           []string
}

// Debounce controls how events for the same repository are coalesced.
type Debounce struct {
	Window  time.Duration
	MaxWait time.Duration
}

// IsBot reports whether login belongs to one of the configured Renovate bots.
func (t Trigger) IsBot(login string) bool {
	for _, l := range t.BotLogins {
		if strings.EqualFold(l, login) {
			return true
		}
	}
	return false
}

// RepositoryAllowed reports whether fullName may trigger a Renovate run. An
// empty allow list permits every repository. Entries may be an exact
// "owner/repo" or an "owner/*" wildcard.
func (t Trigger) RepositoryAllowed(fullName string) bool {
	if len(t.AllowedRepositories) == 0 {
		return true
	}
	for _, allowed := range t.AllowedRepositories {
		if strings.EqualFold(allowed, fullName) {
			return true
		}
		if owner, ok := strings.CutSuffix(allowed, "/*"); ok {
			prefix := owner + "/"
			if len(fullName) > len(prefix) && strings.EqualFold(fullName[:len(prefix)], prefix) {
				return true
			}
		}
	}
	return false
}

// MatchesPushPath reports whether path is a Renovate configuration file.
func (t Trigger) MatchesPushPath(path string) bool {
	for _, p := range t.PushPaths {
		if strings.EqualFold(p, path) {
			return true
		}
	}
	return false
}

// Load reads the configuration from the environment and validates it.
func Load() (Config, error) {
	cfg := Config{
		Addr:          envOr("RENOVATE_WEBHOOK_ADDR", DefaultAddr),
		Path:          envOr("RENOVATE_WEBHOOK_PATH", DefaultPath),
		WebhookSecret: os.Getenv("RENOVATE_WEBHOOK_SECRET"),
		GitHubAPIURL:  strings.TrimSuffix(envOr("GITHUB_API_URL", DefaultGitHubAPIURL), "/"),
		GitHubToken:   strings.TrimSpace(os.Getenv("GITHUB_TOKEN")),
		Runner: Runner{
			Repository:      strings.TrimSpace(os.Getenv("RUNNER_REPOSITORY")),
			Workflow:        envOr("RUNNER_WORKFLOW", DefaultWorkflow),
			Ref:             envOr("RUNNER_REF", DefaultRef),
			RepositoryInput: envOr("RUNNER_REPOSITORY_INPUT", DefaultRepositoryInput),
		},
		Trigger: Trigger{
			BotLogins: envList("RENOVATE_BOT_LOGINS", DefaultBotLogins),
			PushPaths: envList("PUSH_CONFIG_PATHS", DefaultPushPaths),
		},
	}

	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	level, err := envLevel("RENOVATE_WEBHOOK_LOG_LEVEL", slog.LevelInfo)
	collect(err)
	cfg.LogLevel = level

	cfg.ShutdownTimeout, err = envDuration("RENOVATE_WEBHOOK_SHUTDOWN_TIMEOUT", DefaultShutdownTimeout)
	collect(err)
	cfg.DryRun, err = envBool("DRY_RUN", false)
	collect(err)
	cfg.Trigger.OnPush, err = envBool("TRIGGER_ON_PUSH", true)
	collect(err)
	cfg.Debounce.Window, err = envDuration("DEBOUNCE_WINDOW", DefaultDebounceWindow)
	collect(err)
	cfg.Debounce.MaxWait, err = envDuration("DEBOUNCE_MAX_WAIT", DefaultDebounceMaxWait)
	collect(err)
	cfg.Runner.ExtraInputs, err = envKeyValues("RUNNER_EXTRA_INPUTS")
	collect(err)
	cfg.Trigger.AllowedRepositories, err = envAllowedRepositories("ALLOWED_REPOSITORIES")
	collect(err)

	if cfg.WebhookSecret == "" {
		collect(errors.New("RENOVATE_WEBHOOK_SECRET is required"))
	}
	if cfg.Runner.Repository == "" {
		collect(errors.New("RUNNER_REPOSITORY is required"))
	} else if owner, repo, ok := strings.Cut(cfg.Runner.Repository, "/"); !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		collect(fmt.Errorf("RUNNER_REPOSITORY must be in owner/repo form, got %q", cfg.Runner.Repository))
	}
	if cfg.GitHubToken == "" && !cfg.DryRun {
		collect(errors.New("GITHUB_TOKEN is required unless DRY_RUN=true"))
	}
	if cfg.Debounce.MaxWait < cfg.Debounce.Window {
		collect(fmt.Errorf("DEBOUNCE_MAX_WAIT (%s) must not be shorter than DEBOUNCE_WINDOW (%s)",
			cfg.Debounce.MaxWait, cfg.Debounce.Window))
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envList(key string, fallback []string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}

// envAllowedRepositories parses ALLOWED_REPOSITORIES like envList, but an
// unset or empty variable is the only way to get the "no restriction"
// fallback. A variable that is set yet parses to no usable entries (e.g. ","
// or " , ") would otherwise fall back to that same nil list, silently turning
// an intended access restriction into "allow everything" — so that case is a
// configuration error instead.
func envAllowedRepositories(key string) ([]string, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil, nil
	}
	list := envList(key, nil)
	if len(list) == 0 {
		return nil, fmt.Errorf("%s: %q does not contain any usable repository entries", key, raw)
	}
	return list, nil
}

// envKeyValues parses "key=value,key=value". A fragment with no "=" continues
// the previous value, so a value may contain commas itself
// ("labels=area/foo,area/bar"). Without that, a perfectly reasonable workflow
// input would stop the whole service from starting.
func envKeyValues(key string) (map[string]string, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil, nil
	}

	out := make(map[string]string)
	previous := ""
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if k = strings.TrimSpace(k); !ok || k == "" {
			if previous == "" {
				return nil, fmt.Errorf("%s: %q is not in key=value form", key, part)
			}
			out[previous] += "," + part
			continue
		}
		previous = k
		out[k] = strings.TrimSpace(v)
	}
	return out, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fallback, fmt.Errorf("%s: %w", key, err)
	}
	if d < 0 {
		return fallback, fmt.Errorf("%s: must not be negative, got %s", key, d)
	}
	return d, nil
}

func envBool(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}

func envLevel(key string, fallback slog.Level) (slog.Level, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return fallback, fmt.Errorf("%s: %w", key, err)
	}
	return level, nil
}
