# Changelog

## 1.0.0 (2026-08-21)


### Features

* add webhook service that dispatches self-hosted Renovate runs ([b854ef3](https://github.com/nonchan7720/renovate-self-hosted/commit/b854ef392dad8b1403ecbb5904e89023b271fc6b))
* **cmd:** wire up the renovate-webhook command ([41d984f](https://github.com/nonchan7720/renovate-self-hosted/commit/41d984fc3efec141838701d35f31497a3d37bc99))
* **config:** load and validate configuration from the environment ([403a492](https://github.com/nonchan7720/renovate-self-hosted/commit/403a4920774ef6c4dfa8838472d1cbde071a068b))
* **dispatch:** start renovate runs through workflow_dispatch ([95a1e6f](https://github.com/nonchan7720/renovate-self-hosted/commit/95a1e6f0807b9216a15555bfba3d7856b6522872))
* **helm:** add a chart for running the service on kubernetes ([1c6036a](https://github.com/nonchan7720/renovate-self-hosted/commit/1c6036a0260ecd1a6a78ce72fbfc1f2fc2edcb95))
* **queue:** coalesce events per repository ([4ee19e9](https://github.com/nonchan7720/renovate-self-hosted/commit/4ee19e90b0ca94d524ca4f75c08fc06c10e8fbc1))
* **server:** expose the webhook endpoint and health probes ([957dcd8](https://github.com/nonchan7720/renovate-self-hosted/commit/957dcd821fb2c27b2a98d44549148d763cc3792f))
* **webhook:** detect newly ticked checkboxes in issue and PR bodies ([a8d80df](https://github.com/nonchan7720/renovate-self-hosted/commit/a8d80dff999029ebf84b79bea0e63b8131fdf236))
* **webhook:** turn dashboard and pull request checkboxes into runs ([f57c6c7](https://github.com/nonchan7720/renovate-self-hosted/commit/f57c6c7726c777b94c5bfa5edcdd3cfa1f17cfaf))
* **webhook:** verify delivery signatures and decode GitHub events ([e9ed9c2](https://github.com/nonchan7720/renovate-self-hosted/commit/e9ed9c2b0ee3aed5bd7fc7d84bab994da783a6d7))


### Bug Fixes

* **config:** allow commas inside RUNNER_EXTRA_INPUTS values ([15e63de](https://github.com/nonchan7720/renovate-self-hosted/commit/15e63de1385c3c0a1b0cd393e6b0ae7b05350d82))
* **config:** reject a malformed RUNNER_REPOSITORY ([1734a7b](https://github.com/nonchan7720/renovate-self-hosted/commit/1734a7bbd78b144b6c6a46ea00b6045d04954188))
* **config:** trim credentials and fail closed on a broken allow list ([dd46b1e](https://github.com/nonchan7720/renovate-self-hosted/commit/dd46b1e819ad21b23474008ba958641c02291e98))
* **dispatch:** retry secondary rate limits and honour Retry-After ([cf75d7a](https://github.com/nonchan7720/renovate-self-hosted/commit/cf75d7a9fe2c23cdf48a114fa0f592c070c77db9))
* **helm:** match wildcard certificates when picking the notes scheme ([7295ee8](https://github.com/nonchan7720/renovate-self-hosted/commit/7295ee8759d6e041c147d8929de850b270dca836))
* **helm:** pick the notes URL scheme per host ([53f292c](https://github.com/nonchan7720/renovate-self-hosted/commit/53f292c03463839e5d92befbdc883668b19e6cd1))
* **queue:** keep the debounce extension when the timer already fired ([5294b6b](https://github.com/nonchan7720/renovate-self-hosted/commit/5294b6b4f2d27f360806492bd78501c5edbe2b51))
* **webhook:** ignore edits to a closed dependency dashboard ([ad3b345](https://github.com/nonchan7720/renovate-self-hosted/commit/ad3b3458b5c008dee6db4afa04052d3ab0280698))
* **webhook:** only let the tick count override stable positions ([9780899](https://github.com/nonchan7720/renovate-self-hosted/commit/9780899381c8a658eba488279933443255a0671b))
* **webhook:** require the tick count to grow before reporting a tick ([6e07fe0](https://github.com/nonchan7720/renovate-self-hosted/commit/6e07fe0141cdf6e39c46e542fe6d5f18fc47af4f))
* **webhook:** run when the push commit list is truncated ([e76cb8f](https://github.com/nonchan7720/renovate-self-hosted/commit/e76cb8f399a9a47e96238d272042a0138a246e35))
