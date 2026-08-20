// Command renovate-webhook receives GitHub webhooks and starts self-hosted
// Renovate runs by dispatching a GitHub Actions workflow in the runner
// repository.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nonchan7720/renovate-self-hosted/internal/config"
	"github.com/nonchan7720/renovate-self-hosted/internal/dispatch"
	"github.com/nonchan7720/renovate-self-hosted/internal/queue"
	"github.com/nonchan7720/renovate-self-hosted/internal/server"
	"github.com/nonchan7720/renovate-self-hosted/internal/webhook"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration:\n%w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	var dispatcher dispatch.Dispatcher
	if cfg.DryRun {
		dispatcher = dispatch.NewDryRun(logger)
	} else {
		dispatcher = dispatch.NewActions(cfg.GitHubAPIURL, cfg.GitHubToken, cfg.Runner, nil, logger)
	}

	debouncer := queue.New(cfg.Debounce, dispatcher, logger)
	handler := webhook.NewHandler(cfg.WebhookSecret, cfg.Trigger, debouncer, logger)
	srv := server.New(cfg, handler, logger)

	logger.Info("starting renovate-webhook",
		slog.String("path", cfg.Path),
		slog.String("runner_repository", cfg.Runner.Repository),
		slog.String("runner_workflow", cfg.Runner.Workflow),
		slog.String("runner_ref", cfg.Runner.Ref),
		slog.Bool("dry_run", cfg.DryRun),
		slog.Bool("trigger_on_push", cfg.Trigger.OnPush),
		slog.Duration("debounce_window", cfg.Debounce.Window))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := server.Run(ctx, srv, cfg.ShutdownTimeout, logger)

	// Give whatever is still waiting in the debouncer a chance to run rather
	// than losing a checkbox someone just ticked.
	drainCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout+queue.DispatchTimeout)
	defer cancel()
	if err := debouncer.Close(drainCtx); err != nil {
		logger.Warn("failed to drain pending renovate runs", slog.Any("error", err))
	}

	if serveErr != nil {
		return serveErr
	}
	logger.Info("stopped")
	return nil
}
