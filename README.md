# renovate-self-hosted

A small Go service that receives GitHub webhooks and starts self-hosted
[Renovate](https://docs.renovatebot.com/) runs on demand.

Renovate itself is executed by a GitHub Actions workflow in a **separate runner
repository**. This service never runs Renovate: it validates the delivery,
decides whether it really means "run Renovate against this repository", and
triggers the runner workflow through the `workflow_dispatch` API.

```mermaid
flowchart LR
    repos["Managed repositories<br>dashboard, pull requests, config"]
    svc["renovate-webhook<br>this service"]
    runner["Runner repository<br>GitHub Actions"]

    repos -- "webhook" --> svc
    svc -- "workflow_dispatch" --> runner
    runner -- "runs Renovate" --> repos
```

## What triggers a run

| Event | Condition | Reason reported |
| --- | --- | --- |
| `issues` (`edited`) | A checkbox on Renovate's Dependency Dashboard issue went from unticked to ticked | `dependency-dashboard-checkbox` |
| `pull_request` (`edited`) | A checkbox in a Renovate pull request body went from unticked to ticked, for example the rebase/retry box | `pull-request-checkbox` |
| `push` | A Renovate configuration file changed on the default branch | `config-push` |

Everything else is answered with `200 {"status":"ignored"}` and a reason, so
GitHub's delivery log stays green and it is obvious why nothing happened.

Checkbox detection compares the previous body (`changes.body.from`) with the new
one and reports the items that were newly ticked. Items are matched on the HTML
comment Renovate attaches to each control (`rebase-check`, `manual job`,
`unlimit-branch=…`), falling back to the visible label, so reordering the
dashboard does not produce phantom triggers.

Two guards keep the service from chasing its own tail:

- the issue or pull request must be authored by a configured Renovate bot, and
- the edit must **not** have been made by that bot — Renovate rewrites these
  bodies at the end of every run, which would otherwise loop forever.

Events for the same repository that arrive close together are coalesced into a
single run (see `DEBOUNCE_WINDOW`), so ticking five boxes in a row costs one
Renovate run rather than five.

## Configuration

All configuration comes from the environment.

| Variable | Default | Description |
| --- | --- | --- |
| `RENOVATE_WEBHOOK_SECRET` | — | **Required.** Secret shared with the GitHub webhook, used for the `X-Hub-Signature-256` check. |
| `RUNNER_REPOSITORY` | — | **Required.** `owner/repo` of the repository holding the Renovate runner workflow. |
| `GITHUB_TOKEN` | — | **Required** unless `DRY_RUN=true`. Token allowed to dispatch that workflow (`actions: write`). |
| `RENOVATE_WEBHOOK_ADDR` | `:8080` | Listen address. |
| `RENOVATE_WEBHOOK_PATH` | `/webhook` | Path GitHub posts to. |
| `RENOVATE_WEBHOOK_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `RENOVATE_WEBHOOK_SHUTDOWN_TIMEOUT` | `15s` | Graceful shutdown budget. |
| `GITHUB_API_URL` | `https://api.github.com` | Point at `https://<host>/api/v3` for GitHub Enterprise Server. |
| `RUNNER_WORKFLOW` | `renovate.yml` | Workflow file name (or numeric ID) to dispatch. |
| `RUNNER_REF` | `main` | Git ref the workflow runs on. |
| `RUNNER_REPOSITORY_INPUT` | `repositories` | Workflow input that receives the target `owner/repo`. |
| `RUNNER_EXTRA_INPUTS` | — | Extra inputs as `key=value,key=value`. Only inputs the workflow declares may be sent; GitHub rejects the whole dispatch otherwise. |
| `RENOVATE_BOT_LOGINS` | `renovate[bot],renovate-bot` | Accounts whose issues and pull requests count as Renovate's. |
| `ALLOWED_REPOSITORIES` | — | Optional allow list, `owner/repo` or `owner/*`. Empty allows every repository. |
| `TRIGGER_ON_PUSH` | `true` | Whether config pushes trigger a run. |
| `PUSH_CONFIG_PATHS` | `renovate.json`, `renovate.json5`, `.renovaterc*`, `.github/renovate.json*`, `.gitlab/renovate.json` | Paths treated as Renovate configuration. |
| `DEBOUNCE_WINDOW` | `10s` | Quiet period before a repository's run is dispatched. |
| `DEBOUNCE_MAX_WAIT` | `2m` | Upper bound on that quiet period, so a busy repository still runs. |
| `DRY_RUN` | `false` | Log what would be dispatched instead of calling GitHub. |

Endpoints: the webhook path, plus `GET /healthz` and `GET /readyz`.

## Setting up

### 1. Runner repository

Copy [`examples/workflows/renovate.yml`](examples/workflows/renovate.yml) to
`.github/workflows/renovate.yml` in the repository that should execute Renovate,
and add a `RENOVATE_TOKEN` secret there. The file has to be on that repository's
default branch before `workflow_dispatch` will accept it.

### 2. This service

```sh
docker build -t renovate-webhook .
docker run --rm -p 8080:8080 \
  -e RENOVATE_WEBHOOK_SECRET=... \
  -e GITHUB_TOKEN=... \
  -e RUNNER_REPOSITORY=acme/renovate-runner \
  renovate-webhook
```

Start with `-e DRY_RUN=true` to watch the decisions in the logs without
dispatching anything.

### 3. GitHub webhook

Add an organization or repository webhook pointing at the service with content
type `application/json`, the same secret, and these events:

- **Issues** — Dependency Dashboard checkboxes
- **Pull requests** — rebase/retry checkboxes
- **Pushes** — optional, for Renovate config changes

## Development

The toolchain is pinned in [`mise.toml`](mise.toml) (Go 1.26.7, golangci-lint
2.12.2):

```sh
mise install
go test -race ./...
golangci-lint run
```

The service has no third-party dependencies; everything is standard library.

| Package | Responsibility |
| --- | --- |
| `internal/config` | Environment configuration and validation |
| `internal/webhook` | Signature check, event routing, checkbox diffing |
| `internal/queue` | Per-repository debouncing |
| `internal/dispatch` | `workflow_dispatch` client with retries |
| `internal/server` | HTTP server, health probes, graceful shutdown |
