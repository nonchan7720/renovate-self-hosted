package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nonchan7720/renovate-self-hosted/internal/config"
)

// setEnv applies the minimum required configuration plus the given overrides.
func setEnv(t *testing.T, overrides map[string]string) {
	t.Helper()

	base := map[string]string{
		"RENOVATE_WEBHOOK_SECRET": "secret",
		"GITHUB_TOKEN":            "token",
		"RUNNER_REPOSITORY":       "acme/renovate-runner",
	}
	for k, v := range overrides {
		base[k] = v
	}
	for k, v := range base {
		if v == "" {
			t.Setenv(k, "")
			continue
		}
		t.Setenv(k, v)
	}
}

func TestLoadDefaults(t *testing.T) {
	setEnv(t, nil)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	if cfg.Addr != config.DefaultAddr {
		t.Errorf("Addr = %q, want %q", cfg.Addr, config.DefaultAddr)
	}
	if cfg.Path != config.DefaultPath {
		t.Errorf("Path = %q, want %q", cfg.Path, config.DefaultPath)
	}
	if cfg.GitHubAPIURL != config.DefaultGitHubAPIURL {
		t.Errorf("GitHubAPIURL = %q, want %q", cfg.GitHubAPIURL, config.DefaultGitHubAPIURL)
	}
	if cfg.Runner.Workflow != config.DefaultWorkflow || cfg.Runner.Ref != config.DefaultRef {
		t.Errorf("Runner = %+v, want the documented defaults", cfg.Runner)
	}
	if cfg.Runner.RepositoryInput != config.DefaultRepositoryInput {
		t.Errorf("RepositoryInput = %q", cfg.Runner.RepositoryInput)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	if !cfg.Trigger.OnPush {
		t.Error("OnPush = false, want push triggers enabled by default")
	}
	if cfg.Debounce.Window != config.DefaultDebounceWindow || cfg.Debounce.MaxWait != config.DefaultDebounceMaxWait {
		t.Errorf("Debounce = %+v, want the documented defaults", cfg.Debounce)
	}
	if cfg.DryRun {
		t.Error("DryRun = true, want false by default")
	}
}

func TestLoadOverrides(t *testing.T) {
	setEnv(t, map[string]string{
		"RENOVATE_WEBHOOK_ADDR":             "127.0.0.1:9000",
		"RENOVATE_WEBHOOK_PATH":             "/hooks/github",
		"RENOVATE_WEBHOOK_LOG_LEVEL":        "debug",
		"RENOVATE_WEBHOOK_SHUTDOWN_TIMEOUT": "5s",
		"GITHUB_API_URL":                    "https://github.example.com/api/v3/",
		"RUNNER_WORKFLOW":                   "run-renovate.yaml",
		"RUNNER_REF":                        "trunk",
		"RUNNER_REPOSITORY_INPUT":           "target",
		"RUNNER_EXTRA_INPUTS":               "logLevel=debug, dryRun=full",
		"RENOVATE_BOT_LOGINS":               "renovate[bot], my-renovate",
		"ALLOWED_REPOSITORIES":              "acme/*, other/one",
		"TRIGGER_ON_PUSH":                   "false",
		"DEBOUNCE_WINDOW":                   "30s",
		"DEBOUNCE_MAX_WAIT":                 "5m",
	})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	if cfg.Addr != "127.0.0.1:9000" || cfg.Path != "/hooks/github" {
		t.Errorf("Addr/Path = %q/%q", cfg.Addr, cfg.Path)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want debug", cfg.LogLevel)
	}
	if cfg.ShutdownTimeout != 5*time.Second {
		t.Errorf("ShutdownTimeout = %v", cfg.ShutdownTimeout)
	}
	if want := "https://github.example.com/api/v3"; cfg.GitHubAPIURL != want {
		t.Errorf("GitHubAPIURL = %q, want %q with the trailing slash trimmed", cfg.GitHubAPIURL, want)
	}
	if cfg.Runner.ExtraInputs["dryRun"] != "full" || cfg.Runner.ExtraInputs["logLevel"] != "debug" {
		t.Errorf("ExtraInputs = %v", cfg.Runner.ExtraInputs)
	}
	if cfg.Trigger.OnPush {
		t.Error("OnPush = true, want it disabled")
	}
	if cfg.Debounce.Window != 30*time.Second || cfg.Debounce.MaxWait != 5*time.Minute {
		t.Errorf("Debounce = %+v", cfg.Debounce)
	}
}

func TestLoadValidation(t *testing.T) {
	tests := map[string]struct {
		env    map[string]string
		wantIn []string
	}{
		"missing secret": {
			env:    map[string]string{"RENOVATE_WEBHOOK_SECRET": ""},
			wantIn: []string{"RENOVATE_WEBHOOK_SECRET"},
		},
		"missing runner repository": {
			env:    map[string]string{"RUNNER_REPOSITORY": ""},
			wantIn: []string{"RUNNER_REPOSITORY"},
		},
		"malformed runner repository": {
			env:    map[string]string{"RUNNER_REPOSITORY": "renovate-runner"},
			wantIn: []string{"owner/repo"},
		},
		"missing token without dry run": {
			env:    map[string]string{"GITHUB_TOKEN": ""},
			wantIn: []string{"GITHUB_TOKEN"},
		},
		"bad duration": {
			env:    map[string]string{"DEBOUNCE_WINDOW": "soon"},
			wantIn: []string{"DEBOUNCE_WINDOW"},
		},
		"negative duration": {
			env:    map[string]string{"DEBOUNCE_WINDOW": "-5s"},
			wantIn: []string{"DEBOUNCE_WINDOW"},
		},
		"bad bool": {
			env:    map[string]string{"TRIGGER_ON_PUSH": "yes please"},
			wantIn: []string{"TRIGGER_ON_PUSH"},
		},
		"bad log level": {
			env:    map[string]string{"RENOVATE_WEBHOOK_LOG_LEVEL": "chatty"},
			wantIn: []string{"RENOVATE_WEBHOOK_LOG_LEVEL"},
		},
		"malformed extra inputs": {
			env:    map[string]string{"RUNNER_EXTRA_INPUTS": "logLevel"},
			wantIn: []string{"RUNNER_EXTRA_INPUTS"},
		},
		"max wait shorter than window": {
			env:    map[string]string{"DEBOUNCE_WINDOW": "5m", "DEBOUNCE_MAX_WAIT": "10s"},
			wantIn: []string{"DEBOUNCE_MAX_WAIT"},
		},
		"several problems at once": {
			env:    map[string]string{"RENOVATE_WEBHOOK_SECRET": "", "RUNNER_REPOSITORY": ""},
			wantIn: []string{"RENOVATE_WEBHOOK_SECRET", "RUNNER_REPOSITORY"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			setEnv(t, tc.env)

			_, err := config.Load()
			if err == nil {
				t.Fatal("Load() = nil, want an error")
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %v, want it to mention %q", err, want)
				}
			}
		})
	}
}

func TestLoadDryRunMakesTokenOptional(t *testing.T) {
	setEnv(t, map[string]string{"GITHUB_TOKEN": "", "DRY_RUN": "true"})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if !cfg.DryRun {
		t.Error("DryRun = false, want true")
	}
}

func TestTriggerIsBot(t *testing.T) {
	t.Parallel()

	trigger := config.Trigger{BotLogins: config.DefaultBotLogins}
	for _, login := range []string{"renovate[bot]", "RENOVATE[BOT]", "renovate-bot"} {
		if !trigger.IsBot(login) {
			t.Errorf("IsBot(%q) = false, want true", login)
		}
	}
	for _, login := range []string{"alice", "dependabot[bot]", ""} {
		if trigger.IsBot(login) {
			t.Errorf("IsBot(%q) = true, want false", login)
		}
	}
}

func TestTriggerRepositoryAllowed(t *testing.T) {
	t.Parallel()

	empty := config.Trigger{}
	if !empty.RepositoryAllowed("anything/at-all") {
		t.Error("an empty allow list should permit every repository")
	}

	trigger := config.Trigger{AllowedRepositories: []string{"acme/*", "other/one"}}
	allowed := []string{"acme/api", "ACME/Api", "other/one"}
	denied := []string{"acme", "acme/", "notacme/api", "other/two", "other"}

	for _, repo := range allowed {
		if !trigger.RepositoryAllowed(repo) {
			t.Errorf("RepositoryAllowed(%q) = false, want true", repo)
		}
	}
	for _, repo := range denied {
		if trigger.RepositoryAllowed(repo) {
			t.Errorf("RepositoryAllowed(%q) = true, want false", repo)
		}
	}
}

func TestTriggerMatchesPushPath(t *testing.T) {
	t.Parallel()

	trigger := config.Trigger{PushPaths: config.DefaultPushPaths}
	if !trigger.MatchesPushPath(".github/renovate.json") {
		t.Error("MatchesPushPath(.github/renovate.json) = false, want true")
	}
	if trigger.MatchesPushPath("internal/config/config.go") {
		t.Error("MatchesPushPath(internal/config/config.go) = true, want false")
	}
}
