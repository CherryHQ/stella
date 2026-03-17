---
title: デプロイ
---

2つのデプロイ方法があります: **バイナリ**(直接インストール)と**Docker**。

## バイナリ

### リリースからインストール

[GitHub Releases](https://github.com/vaayne/anna/releases)からビルド済みのバイナリをダウンロードします。バイナリはlinux、macOS、Windows(amd64/arm64)で利用可能です。

```bash
# 例: Linux amd64
curl -LO https://github.com/vaayne/anna/releases/latest/download/anna_linux_amd64.tar.gz
tar xzf anna_linux_amd64.tar.gz
chmod +x anna
sudo mv anna /usr/local/bin/
```

### ソースからビルド

```bash
go install github.com/vaayne/anna@latest
# または
git clone https://github.com/vaayne/anna.git
cd anna && go build -o anna .
```

### 実行

onboardコマンドを実行して管理パネルを開き、anna(プロバイダー、チャネル、エージェントなど)を設定します:

```bash
anna onboard
```

これにより、APIキー、チャネル、エージェントプロファイルを設定するローカルWebUIが起動します。すべての設定は`~/.anna/anna.db`に保存されます -- 手動での設定ファイルは不要です。

gatewayデーモンを起動します:

```bash
anna gateway
```

gatewayと並行して管理パネルを提供する(ランタイム設定変更のため)には:

```bash
anna gateway --admin-port 8080
```

または、対話型CLIを使用します:

```bash
anna chat
```

### バージョンと自動アップグレード

```bash
anna version
anna upgrade
anna upgrade --install-dir "$HOME/.local/bin"
```

`anna upgrade`は、GitHubから最新の安定版リリースを取得し、現在のOS/アーキテクチャに一致するアーカイブをダウンロードし、デフォルトでバイナリを`$HOME/.local/bin`にインストールします。

### Systemdサービス(Linux)

```ini
# /etc/systemd/system/anna.service
[Unit]
Description=anna gateway
After=network.target

[Service]
Type=simple
User=anna
WorkingDirectory=/home/anna
ExecStart=/usr/local/bin/anna gateway --admin-port 8080
Restart=on-failure
RestartSec=5

# APIキー -- その他のすべての設定はデータベースに保存
Environment=ANTHROPIC_API_KEY=sk-...
Environment=ANNA_HOME=/home/anna/.anna

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now anna
```

すべての設定(チャネル、エージェント、スケジューラジョブ)は`anna.db`に保存されます。管理には`anna onboard`または管理パネルを使用します。

## Docker

イメージは`ghcr.io/vaayne/anna`として`linux/amd64`および`linux/arm64`用に公開されています。

### タグ

| タグ           | 説明                 |
| -------------- | -------------------- |
| `latest`       | 最新の安定版リリース |
| `v1.2.3`       | 特定のバージョン     |
| `sha-<commit>` | 特定のコミット       |

### クイックスタート

まず、onboardを実行してannaを設定します:

```bash
docker run -it --rm \
  -v ~/.anna:/home/nonroot/.anna \
  -p 8080:8080 \
  ghcr.io/vaayne/anna:latest \
  anna onboard
```

次にgatewayを起動します:

```bash
docker run -d \
  --name anna \
  -v ~/.anna:/home/nonroot/.anna \
  -e ANTHROPIC_API_KEY=sk-... \
  ghcr.io/vaayne/anna:latest
```

コンテナは`nonroot`ユーザーとして実行されます。データベース、スキル、キャッシュを永続化するために`~/.anna`をマウントします。コンテナ内のデータディレクトリを変更するには`ANNA_HOME`を設定できます。

### Docker Compose

```yaml
# docker-compose.yml
services:
  anna:
    image: ghcr.io/vaayne/anna:latest
    restart: unless-stopped
    volumes:
      - ./anna-data:/home/nonroot/.anna
    environment:
      - ANTHROPIC_API_KEY=sk-...
      # - OPENAI_API_KEY=sk-...
```

```bash
docker compose up -d
```

初期設定を実行するには、`docker compose exec anna anna onboard`を使用するか、`--admin-port 8080`でgatewayを起動してWebUIを介して設定します。

### ローカルでビルド

```bash
# 単一プラットフォーム
docker build -t anna .

# マルチプラットフォーム
docker buildx build --platform linux/amd64,linux/arm64 -t anna .
```

## ボリュームとデータ

すべてのデータはannaホームディレクトリ(デフォルトは`~/.anna`、`ANNA_HOME`で設定可能)の下に存在します。

| パス                                    | 目的                                                                |
| --------------------------------------- | ------------------------------------------------------------------- |
| `~/.anna/anna.db`                       | 単一データベース(設定、メモリ、スケジューラ)                        |
| `~/.anna/workspaces/{agent-id}/skills/` | エージェントごとのインストール済みスキル                            |
| `~/.anna/workspaces/{agent-id}/SOUL.md` | オプションのエージェントごとのソウル/アイデンティティオーバーライド |
| `~/.anna/cache/`                        | モデルキャッシュ(再生成可能、削除可能)                              |

`anna.db`ファイルは、バックアップすべき唯一の重要なデータです。すべての設定、メッセージ履歴、サマリ、スケジューラジョブが含まれます。

## 環境変数

設定は管理パネル(`anna onboard`または`--admin-port`経由)を通じて管理されます。サポートされる環境変数は少数のみです:

| 変数                | 必須   | 説明                                          |
| ------------------- | ------ | --------------------------------------------- |
| `ANNA_HOME`         | いいえ | annaホームディレクトリ(デフォルトは`~/.anna`) |
| `ANTHROPIC_API_KEY` | はい\* | Anthropicプロバイダーキー                     |
| `OPENAI_API_KEY`    | はい\* | OpenAIプロバイダーキー                        |

\* 少なくとも1つのプロバイダーキーが必要です。APIキーは管理パネルを介して設定することもできます。

## ヘルスチェック

gatewayはstdoutにログを出力します。実行中であることを確認するには:

```bash
# バイナリ
anna gateway  # ログがターミナルに表示されます

# Docker
docker logs anna
```
