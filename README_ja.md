# renovate-self-hosted

*[English](README.md) | 日本語*

GitHub の Webhook を受けて、セルフホストの [Renovate](https://docs.renovatebot.com/)
を必要なときだけ実行させる小さな Go サービスです。

Renovate 自体は**別のランナーリポジトリ**にある GitHub Actions ワークフローが実行します。
このサービスが Renovate を動かすことはありません。配信の検証を行い、それが本当に
「このリポジトリに対して Renovate を実行せよ」という意味なのかを判断し、
`workflow_dispatch` API でランナーのワークフローを起動するところまでが仕事です。

```mermaid
flowchart LR
    repos["管理対象リポジトリ<br>ダッシュボード, PR, 設定"]
    svc["renovate-webhook<br>このサービス"]
    runner["ランナーリポジトリ<br>GitHub Actions"]

    repos -- "webhook" --> svc
    svc -- "workflow_dispatch" --> runner
    runner -- "Renovate を実行" --> repos
```

## 実行のトリガ

| イベント | 条件 | 記録される reason |
| --- | --- | --- |
| `issues` (`edited`) | Renovate の Dependency Dashboard issue のチェックボックスが未チェックからチェックに変わった | `dependency-dashboard-checkbox` |
| `pull_request` (`edited`) | Renovate の PR 本文のチェックボックスが未チェックからチェックに変わった（rebase/retry のボックスなど） | `pull-request-checkbox` |
| `push` | デフォルトブランチへの push で、Renovate の設定ファイルが変更された | `config-push` |
| `push` | デフォルトブランチへの push（それ以外） | `default-branch-push` |

デフォルトブランチへの push は、設定ファイルを変更したものに限らずすべて実行します。
GitHub は PR のマージでも直接の push でも同じ `push` イベントを送ってきますし、
どちらもデフォルトブランチを進めます。開いている Renovate の PR はそのどちらでも
コンフリクトしうるので、リブランチできるのは新しい実行だけです。上の2つの reason の
どちらになるかは、push が `PUSH_CONFIG_PATHS` に含まれるパスに触れたかどうかで決まります。

push のペイロードに含まれるコミットは最大20件（`MaxPushCommits`）で、GitHub が
切り詰めたかどうかを示すフィールドはありません。この上限はもう実行するかどうかを
左右しません — デフォルトブランチへの push なら常に実行されます — 上限が効くのは
パス検出がどこまで遡れるかだけで、上限より前のコミットで設定ファイルが変わっていても
`config-push` ではなく `default-branch-push` として報告されることがあります。

それ以外はすべて `200 {"status":"ignored"}` と理由を返します。GitHub の配信ログが
赤くならず、なぜ何も起きなかったのかがそのまま分かります。

チェックボックスの検出は、以前の本文（`changes.body.from`）と新しい本文を比較して、
**新たにチェックされた項目**だけを報告します。項目の同一性は Renovate が各コントロールに
付ける HTML コメント（`rebase-check`、`manual job`、`unlimit-branch=…`）で判定し、
無い場合のみ表示テキストにフォールバックします。ダッシュボードの並び順が変わっただけで
誤発火することはありません。

自分自身を追いかけてしまわないよう、ガードが2つあります。

- issue または PR の作成者が、設定された Renovate bot であること
- その編集が、その bot によるもので**ない**こと — Renovate は実行のたびにこれらの本文を
  書き換えるため、これが無いと永久にループします

同じリポジトリに対して短時間に届いたイベントは1回の実行にまとめられます
（`DEBOUNCE_WINDOW` を参照）。チェックボックスを5個続けて押しても、Renovate の実行は
5回ではなく1回で済みます。

## 設定

設定はすべて環境変数から読み込みます。

| 変数 | 既定値 | 説明 |
| --- | --- | --- |
| `RENOVATE_WEBHOOK_SECRET` | — | **必須。** GitHub の Webhook と共有するシークレット。`X-Hub-Signature-256` の検証に使います。 |
| `RUNNER_REPOSITORY` | — | **必須。** Renovate 実行ワークフローを持つリポジトリの `owner/repo`。 |
| `GITHUB_APP_ID` | — | `DRY_RUN=true` でない限り**必須**。ランナーのワークフローを dispatch する GitHub App の App ID（または Client ID）。 |
| `GITHUB_APP_PRIVATE_KEY` | — | `DRY_RUN=true` でない限り**必須**。その App の秘密鍵。PEM 形式で、PKCS#1 と PKCS#8 のどちらも可。 |
| `GITHUB_APP_INSTALLATION_ID` | — | トークンを取得するインストール ID。未設定なら `RUNNER_REPOSITORY` 自身のインストールから解決します。Renovate を実行する App とは別の App で dispatch したい場合に設定します。 |
| `RENOVATE_WEBHOOK_ADDR` | `:8080` | 待ち受けアドレス。 |
| `RENOVATE_WEBHOOK_PATH` | `/webhook` | GitHub が POST するパス。 |
| `RENOVATE_WEBHOOK_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error`。 |
| `RENOVATE_WEBHOOK_SHUTDOWN_TIMEOUT` | `15s` | グレースフルシャットダウンの猶予。 |
| `GITHUB_API_URL` | `https://api.github.com` | GitHub Enterprise Server では `https://<host>/api/v3` を指定します。 |
| `RUNNER_WORKFLOW` | `renovate.yml` | dispatch するワークフローのファイル名（または数値 ID）。 |
| `RUNNER_REF` | `main` | ワークフローを実行する Git ref。 |
| `RUNNER_REPOSITORY_INPUT` | `repositories` | 対象の `owner/repo` を受け取るワークフロー入力の名前。 |
| `RUNNER_EXTRA_INPUTS` | — | 追加の入力を `key=value,key=value` 形式で。`=` を含まない断片は直前の値の続きとして扱われるため、値自体にカンマを含められます（`labels=area/foo,area/bar`）。ワークフローが宣言していない入力を送ると GitHub は dispatch 全体を拒否します。 |
| `RENOVATE_BOT_LOGINS` | `renovate[bot],renovate-bot` | issue や PR の作成者として Renovate とみなすアカウント。 |
| `ALLOWED_REPOSITORIES` | — | 任意の許可リスト。`owner/repo` または `owner/*`。未設定ならすべて許可。設定されているのに有効な項目が1つも無い場合は、黙って「全許可」に倒れず起動エラーになります。 |
| `TRIGGER_ON_PUSH` | `true` | デフォルトブランチへの push で実行するかどうか。 |
| `PUSH_CONFIG_PATHS` | `renovate.json`, `renovate.json5`, `.renovaterc*`, `.github/renovate.json*`, `.gitlab/renovate.json` | Renovate の設定ファイルとして扱うパス。 |
| `DEBOUNCE_WINDOW` | `10s` | リポジトリの実行を dispatch するまでの待機時間。 |
| `DEBOUNCE_MAX_WAIT` | `2m` | その待機時間の上限。イベントが途切れないリポジトリでも実行されます。 |
| `DRY_RUN` | `false` | GitHub を呼ばず、dispatch する内容をログに出すだけにします。 |

App に必要な権限は1つだけです。ランナーリポジトリへの **Actions: write**、
`workflow_dispatch` を呼ぶためのものです。Renovate 自体が必要とする権限をそのまま
全部与えてそこで止めてしまいがちですが、それだと dispatch が理由の分かりにくい
403 で失敗します。サービスは自分で JWT に署名してインストールトークンを取得し、
失効前に自動で更新するので、手動でローテーションする必要はありません。

エンドポイントは Webhook のパスに加えて `GET /healthz` と `GET /readyz`。

## セットアップ

### 1. ランナーリポジトリ

[`examples/workflows/renovate.yml`](examples/workflows/renovate.yml) を、Renovate を
実行するリポジトリの `.github/workflows/renovate.yml` にコピーし、そこに
`RENOVATE_TOKEN` シークレットを追加します。`workflow_dispatch` が受け付けるには、
このファイルがそのリポジトリのデフォルトブランチ上に存在している必要があります。

### 2. このサービス

Kubernetes の場合は [`deploy/helm/renovate-webhook`](deploy/helm/renovate-webhook)
のチャートを使います。

```sh
kubectl create secret generic renovate-webhook \
  --from-literal=RENOVATE_WEBHOOK_SECRET=... \
  --from-file=GITHUB_APP_PRIVATE_KEY=./app-private-key.pem

helm install renovate-webhook deploy/helm/renovate-webhook \
  --set config.runnerRepository=acme/renovate-runner \
  --set config.githubAppId=123456 \
  --set secret.existingSecret=renovate-webhook \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=renovate-webhook.example.com
```

チャートに values からシークレットを作らせることもできます
（`secret.webhookSecret`、`secret.githubAppPrivateKey`）。お試しにはこちらが手軽です。
秘密鍵は複数行なので、`--set` ではなく values ファイル（`-f`/`--values`）で渡してください。
`--set` だと PEM の改行が壊れます。

```sh
helm install renovate-webhook deploy/helm/renovate-webhook \
  --set config.runnerRepository=acme/renovate-runner \
  --set config.githubAppId=123456 \
  --set secret.webhookSecret=... \
  -f - <<'EOF'
secret:
  githubAppPrivateKey: |
    -----BEGIN RSA PRIVATE KEY-----
    ...
    -----END RSA PRIVATE KEY-----
EOF
```

`helm install --set config.dryRun=true` にすると、dispatch せずに判断だけをログに出します。
その他の設定は [`values.yaml`](deploy/helm/renovate-webhook/values.yaml) に記載しています。
必須の値が欠けている場合、チャートは CrashLoopBackOff になる代わりにインストール自体を
失敗させます。

`deploymentAnnotations` は Deployment 自身に付くアノテーションで、Pod テンプレートに付く
`podAnnotations` とは別物です。Stakater Reloader のように Pod ではなく Deployment を
監視するコントローラは、シークレットのローテーションに気付いて Pod を入れ替えるために
こちらが必要です。

リリース時にマルチアーキテクチャのイメージが
`ghcr.io/nonchan7720/renovate-self-hosted` に、チャート自体が
`oci://ghcr.io/nonchan7720/charts` に publish されるので、チェックアウトなしで
バージョン指定してインストールできます。

```sh
helm install renovate-webhook oci://ghcr.io/nonchan7720/charts/renovate-webhook \
  --version 0.1.0 \
  --set config.runnerRepository=acme/renovate-runner \
  --set config.githubAppId=123456 \
  --set secret.existingSecret=renovate-webhook
```

Docker で動かす場合:

```sh
docker build -t renovate-webhook .
docker run --rm -p 8080:8080 \
  -e RENOVATE_WEBHOOK_SECRET=... \
  -e GITHUB_APP_ID=... \
  -e GITHUB_APP_PRIVATE_KEY="$(cat app-private-key.pem)" \
  -e RUNNER_REPOSITORY=acme/renovate-runner \
  renovate-webhook
```

まずは `-e DRY_RUN=true` を付けて、何も dispatch せずに判断をログで眺めるのがおすすめです。

#### レプリカ数について

デバウンスの状態はメモリ上にあるため、各レプリカは自分が受け取ったイベントしか
まとめられません。つまり2レプリカあれば、同じリポジトリに対して2回 dispatch され得ます。
ランナー側のワークフローが concurrency group で直列化するので、代償は競合ではなく
「実行が1回余分に走る」ことです。チャートの既定は1レプリカです。

### 3. GitHub の Webhook

organization またはリポジトリの Webhook をこのサービスに向けて追加します。
content type は `application/json`、シークレットは同じものを設定し、
以下のイベントを購読します。

- **Issues** — Dependency Dashboard のチェックボックス
- **Pull requests** — rebase/retry のチェックボックス
- **Pushes** — 任意（`TRIGGER_ON_PUSH`）。デフォルトブランチへの push を拾う場合

GitHub App 自身にこれらのイベントを配信させることもできます。リポジトリごとに
Webhook を設定せずに済みます。App の **Webhook URL** をこのサービスに向け、
同じ3つのイベントを購読させ、**Webhook secret** を `RENOVATE_WEBHOOK_SECRET` と
同じ値にします。App がインストールされているすべてのリポジトリのイベントが、この
1つのエンドポイントに届くようになります。署名検証は変わりません
（`X-Hub-Signature-256`）。サービス側はどちらの配信方法かを区別しませんし、
区別する必要もありません。

## 開発

ツールチェーンは [`mise.toml`](mise.toml) に固定しています
（Go 1.26.7、golangci-lint 2.12.2、Helm 4.2.4）。

```sh
mise install
go test -race ./...
golangci-lint run
helm lint deploy/helm/renovate-webhook \
  --set config.runnerRepository=acme/renovate-runner \
  --set config.githubAppId=example \
  --set secret.webhookSecret=example --set secret.githubAppPrivateKey=example
```

リリースは [release-please](https://github.com/googleapis/release-please) が管理します。
`main` へのマージでコミットメッセージからリリース PR が維持され、その PR をマージすると
バージョンがタグ付けされ、イメージとチャートが publish されます。チャートの `version` と
`appVersion` もタグに追従します。

イメージのタグは必ずどちらか一方だけです。デプロイされているビルドが何なのかが
常に一意に分かります。

| タグ | publish されるタイミング | 用途 |
| --- | --- | --- |
| コミットハッシュ（例: `1a2b3c4`） | `main` への push ごと | ステージング |
| バージョン（例: `0.1.0`） | リリース公開時 | プロダクション |

`latest` や可動の major / minor タグは意図的に持ちません。ステージングは
`--set image.tag=1a2b3c4` でハッシュを指定し、プロダクションはチャートの `appVersion`
から解決されるので指定不要です。チャート自体はバージョンが `Chart.yaml` 由来のため、
リリース時のみ publish されます。

サードパーティ依存はありません。すべて標準ライブラリです。

| パッケージ | 役割 |
| --- | --- |
| `internal/config` | 環境変数からの設定読み込みと検証 |
| `internal/webhook` | 署名検証、イベントのルーティング、チェックボックスの差分検出 |
| `internal/githubapp` | GitHub App の JWT 署名とインストールトークンのキャッシュ |
| `internal/queue` | リポジトリ単位のデバウンス |
| `internal/dispatch` | リトライ付きの `workflow_dispatch` クライアント |
| `internal/server` | HTTP サーバ、ヘルスチェック、グレースフルシャットダウン |
| `deploy/helm` | Kubernetes で動かすための Helm チャート |
