# renovate-self-hosted

A Go service that receives GitHub webhooks and starts self-hosted Renovate runs
by dispatching a workflow in a separate runner repository. See README.md for
what it does and why; this file is how to work on it.

## Delegate code changes to the implementer agent

**Every code change goes through the `implementer` subagent**
(`.claude/agents/implementer.md`, Sonnet). That covers Go sources and tests,
Helm templates, workflows and examples — features, bug fixes, refactors, added
tests, and fixes for review comments alike. Do not hand-edit those files
yourself, and do not wait to be asked to delegate.

Investigate first, then hand the agent the analysis and the intended change
rather than the raw request — it implements and verifies, you decide what
should change. Review its diff and run the checks yourself before trusting it;
a subagent's report is a claim, not evidence.

Keep git operations out of the agent. Staging, committing, pushing and anything
touching GitHub stay with you.

Read-only work — reading code, answering questions, reviewing, planning — needs
no delegation. Neither do documentation-only edits (README, this file) or the
files under `.claude/`.

## Toolchain

Pinned in `mise.toml`: Go 1.26.7, golangci-lint 2.12.2, Helm 4.2.4. Run
`mise install` to get them.

The default `go` on PATH can be older than the module requires, so prefix Go
commands with `GOTOOLCHAIN=go1.26.7`. Never edit the `go` directive in go.mod to
dodge a toolchain error.

```sh
GOTOOLCHAIN=go1.26.7 go build ./...
GOTOOLCHAIN=go1.26.7 go vet ./...
GOTOOLCHAIN=go1.26.7 go test -race ./...
GOTOOLCHAIN=go1.26.7 gofmt -l .          # must print nothing
golangci-lint run
helm lint deploy/helm/renovate-webhook \
  --set config.runnerRepository=acme/renovate-runner \
  --set secret.webhookSecret=example --set secret.githubToken=example
```

All of these must be clean before a push. CI runs the same set.

## Conventions

- Standard library only. Adding a dependency is a decision, not a detail.
- Comments explain why, never what. A guard gets the failure it prevents; a
  trade-off gets the cost it accepts.
- Tests ship with the change and cover the case that motivated it.
- Exported identifiers, environment variable names and Helm value names are an
  interface. Renaming one is a breaking change.
- README.md and README_ja.md are kept in sync — behaviour documented in one is
  documented in both.

## Git

Semantic commits (`feat:`, `fix:`, `ci:`, `docs:`, `chore:`, with a scope where
it helps). Split by role rather than by file count: one commit per coherent
change, with its tests, so each commit builds and tests green on its own.
