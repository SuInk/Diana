<div align="center">

# Diana

[简体中文](./README.md) · [English](./README.en.md) · **日本語** · [한국어](./README.ko.md) · [Français](./README.fr.md) · [Deutsch](./README.de.md) · [Nederlands](./README.nl.md)

**人のように話し、エージェントのように働く。**

セルフホスト型の単一バイナリ AI エージェント —— メンションなしでも空気を読んでグループチャットに参加し、記憶・画像認識・リンク解析・音楽リクエスト・Web 検索・Skills / MCP・ローカルコーディングエージェントまで対応。データと判断履歴はすべて自分のマシンに留まります。

<sub>OneBot v11 · Telegram · QQ 公式ボット · WeChat · iMessage · 内蔵コンソール —— 1 インスタンスで同時稼働</sub>

[![CI](https://github.com/SuInk/Diana/actions/workflows/ci.yml/badge.svg)](https://github.com/SuInk/Diana/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/SuInk/Diana?color=159f6c)](https://github.com/SuInk/Diana/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Limited%20Redistribution-informational)](./LICENSE)

[公式サイト](https://suink.github.io/Diana/ja/) · [ドキュメント（英語）](https://suink.github.io/Diana/en/deploy.html) · [オンラインデモ](https://suink.github.io/Diana/demo/) · [最新リリース](https://github.com/SuInk/Diana/releases/latest)

[![QQ Group 1127671893](https://img.shields.io/badge/QQ%20Group-1127671893-12B7F5?logo=qq&logoColor=white)](https://qm.qq.com/q/4o6zEBs4h2)

</div>

<br />

<img src="./docs/assets/diana-webui-overview-green.jpg" alt="Diana コンソール概要：チャネル状態、メッセージ統計、システムリソース、リアルタイムイベントストリーム" width="100%" />

## なぜ Diana なのか

セルフホスト型のチャットボットは、たいてい両極端になりがちです。`@` メンションされるまで沈黙するか（あるいは能動返信をオンにした途端にスパム化する）おもちゃか、Python / Node の環境構築と YAML 地獄の末に「なぜ返事しないのか」すら分からないブラックボックスか。

Diana は「自然な会話の空気感」と「実際に仕事をこなすエージェント機能」を 1 つの Go 単一バイナリにまとめました。片側で主要 LLM（OpenAI 互換 API、Gemini、Anthropic）と接続し、もう片側で普段使いのチャットアプリを同時に稼働させ、内蔵 Web コンソールですべてを管理できます。

- **空気を読んで話し、黙るべきときは黙る** —— メンションなしでも自然なタイミングで会話に参加。階層型メモリと編集可能なノートブックでグループの約束事を覚え、画像・動画リンク・音楽リクエスト・スタンプにも対応します。
- **チャットのまま実務まで完結** —— Web 検索、ファイル解析、図表の画像レンダリングを標準内蔵。Skills・MCP・ブラウザツールに加え、ローカルの Claude Code / Codex にコーディング作業を依頼することもできます。
- **数分で起動、原因はすべて可視化** —— ワンコマンドで導入完了、自宅ネットワークでもパブリック IP 不要で動作。ブラウザで設定でき、なぜ返信したか・なぜ見送ったか・消費トークン数までイベントセンターで確認できます。
- **データは手元に、権限には明確な境界を** —— 設定・記憶・ログはすべてローカル SQLite に保存されクラウド依存なし。**セーフモード**をオンにすればコマンド実行・ファイル書き込み・コーディングエージェントを一括遮断でき、プロンプトインジェクション対策も安心です。

## 3 分で動かす

**① インストール。** 以下のコマンドを実行すると、最新安定版のダウンロード・SHA-256 検証・起動まで自動で行います：

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

```sh
# Android Termux（ARM64 のみ・Termux 内で実行、root 不要）
pkg install -y curl tar coreutils && curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

```powershell
# Windows PowerShell
irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1 | iex
```

```sh
# Docker（データ保存先のディレクトリで実行）
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/docker.sh | sh
```

ネイティブインストーラーは管理者権限不要です。**アップグレードも同じコマンドを再実行するだけ**です。詳しい導入手順は[デプロイガイド](https://suink.github.io/Diana/en/deploy.html)をご覧ください。

**または AI アシスタントに Skill で導入を任せる。** コマンド実行可能な AI に以下を貼り付けてください（詳細手順は[インストール Skill](skills/diana-install/SKILL.md)でリリースと同期管理されています）：

```text
Read https://raw.githubusercontent.com/SuInk/Diana/main/skills/diana-install/SKILL.md and follow it to install the latest stable release of Diana on this machine.
```

**手動ダウンロード。** スクリプトを使わず直接バイナリを取得する場合（常に最新安定版）：

| プラットフォーム | ダウンロード | 実行方法 |
| --- | --- | --- |
| Windows x64 | [diana-windows-amd64.exe](https://github.com/SuInk/Diana/releases/latest/download/diana-windows-amd64.exe) | 専用フォルダに置いてダブルクリック（データは隣の `data` フォルダに保存） |
| macOS Apple Silicon | [diana-macos-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-macos-arm64.tar.gz) | 展開して `./run.sh` を実行 |
| macOS Intel | [diana-macos-amd64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-macos-amd64.tar.gz) | 展開して `./run.sh` を実行 |
| Linux x64 | [diana-linux-amd64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-linux-amd64.tar.gz) | 展開して `./run.sh` を実行 |
| Linux ARM64 | [diana-linux-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-linux-arm64.tar.gz) | 展開して `./run.sh` を実行 |
| Android（Termux） | [diana-android-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-android-arm64.tar.gz) | Termux 内で展開して `./run.sh` を実行（サーバー本体であり Android アプリではありません） |

WebUI はバイナリに内蔵されており Node.js は不要です。各ファイルの SHA-256 は同一リリースの [`latest.json`](https://github.com/SuInk/Diana/releases/latest/download/latest.json) および [Releases](https://github.com/SuInk/Diana/releases) ページで確認できます。

**② コンソールにログイン。** ブラウザで `http://127.0.0.1:18080` を開きます。初回生成される管理者ユーザー名・パスワードはターミナルに一度だけ表示されるため、その場で控えてください（Docker は `docker logs diana`）。忘れた場合は `diana passwd` でリセットできます。

**③ 初期設定。** コンソール上で 3 ステップ完了します：

1. **LLM 設定**ページで API キーを入力し、モデル一覧を同期して既定モデルを選択。
2. **ボット**ページでプラットフォームと認証情報を設定（まずは認証不要の「内蔵ボット」で試すことも可能）。
3. メッセージを送ってテスト —— DM は即応答、グループではメンションまたはトリガーワードで反応します。

> [!TIP]
> **別端末からコンソールを開きたい場合：** 既定では `127.0.0.1` のみ待ち受けます。インストールコマンドに `-s -- --host 0.0.0.0`（Windows は `-BindHost 0.0.0.0`）を付けて再実行し、公開時は必ず HTTPS リバースプロキシの背後に配置してください。

## 対応プラットフォーム

有効にしたボット設定はすべて同時稼働し、返信は常に元のチャネルへ返されます。自宅回線や LAN 環境でもパブリック IP なしで利用できます。

| プラットフォーム | 必要な認証情報 | 接続方式 |
| --- | --- | --- |
| **OneBot v11**（SnowLuma 推奨） | 逆方向 WS / 順方向 WS / HTTP API + イベント通知に対応（ボットページで選択） | 方式に依存・LAN 内で利用可能 |
| **Telegram** | BotFather の Bot Token | Diana からアウトバウンド接続（パブリック IP 不要） |
| **QQ 公式ボット** | QQ オープンプラットフォームの AppID + AppSecret | Diana からアウトバウンド接続（パブリック IP 不要） |
| **WeChat** | 入力不要：保存後に「接続」から QR コードをスキャン（Tencent iLink Bot、DM 専用） | Diana からアウトバウンド接続（パブリック IP 不要） |
| **iMessage** | 常時起動 Mac 上の BlueBubbles Server のアドレスとパスワード（[設定ガイド](docs/imessage.md)） | BlueBubbles webhook → Diana（ポーリング併用可） |
| **内蔵ボット** | 入力不要：外部連携なしでコンソールの「チャット」ページからテキスト・音声通話 | コンソール完結・すぐに試用可能 |

## 標準内蔵の機能

プラグインを追加しなくてもバイナリ標準で利用でき、コンソールからオン・オフや調整が可能です：

- **自然な能動会話** —— メンションなしでも会話の流れを見て発言・沈黙を判断。すべての判断理由をイベントセンターに記録します。
- **長期メモリ＆ノートブック** —— 階層型メモリでトークン消費を抑えつつ、グループのルールや好みなどの重要事項は編集・復元可能なノートブックに記録します。
- **内蔵エージェント＆セーフモード** —— ファイル・コマンド・ブラウザツールに加え、Skills や MCP サーバーをオンデマンド読込。ボットごとに**セーフモード**を切り替え、高リスク操作を遮断できます。
- **コーディングエージェント** —— オーナー専用・既定オフ。登録済みリポジトリでの長時間のコード修正・テスト実行をローカルの Claude Code / Codex CLI に委任し、再起動をまたいでも継続、危険操作はチャット上で承認確認します。
- **Web 検索** —— 既定のサンドボックスブラウザによる並列検索に対応し、グローバル設定で Exa・Tavily などの検索プロバイダも利用可能です。
- **リンク＆ファイル解析** —— Bilibili / YouTube / X / Xiaohongshu / Douyin のメディアや X プロフィール、PDF / Office / EPUB ファイルを自動解析します。
- **画像認識・OCR・図表レンダリング** —— ビジョンモデル＋オフライン OCR でテキスト専用モデルにも画像を伝達。表・フローチャート・シーケンス図は Markdown / Mermaid / SVG から画像化して送信します。
- **音楽リクエスト・スタンプ・グループ別ポリシー** —— 複数音源での楽曲再生、会話で覚えたスタンプの返信、グループごとの応答時間帯・人設・許可リスト・ツール権限の制御に対応します。

詳細は[設定ドキュメント](https://suink.github.io/Diana/en/configuration.html)をご覧ください。

## 日常の運用

インストール後は `diana` コマンドをそのまま使えます：

```sh
diana status    # 稼働状態・バージョン・アドレス
diana logs -f   # ログをリアルタイム表示
diana restart   # サービス再起動
diana doctor    # 設定・ディレクトリ・ヘルスチェック
```

Docker 環境ではホスト側で `docker exec diana diana <コマンド>` を実行します。

**パスワードを忘れたとき**：パスワードはデータベースにハッシュとしてのみ保存されているため、どこからも取り出せません。リセットしてください。ワンクリックインストール（Linux / macOS / Windows）では次を実行するだけです：

```sh
diana passwd
```

サービスを停止して新しいランダムパスワードを生成し、一度だけ表示してからサービスを再起動します。ブラウザのセッションはすべてログアウトされますが、ボット・モデル・プラグインなど他のデータには触れません。変更前に `y` の入力を求めます。`-y` で確認を省略できます（スクリプトなど端末がない場合は必須）。`--username 新しい名前` を付けるとユーザー名も同時に変更できます。

Docker はコンテナ内から自分自身を停止できないため、デプロイディレクトリで次を実行します：

```sh
docker compose stop diana
docker compose run --rm diana passwd
docker compose start diana
```

手動で起動したプロセス（`run.sh` や .exe のダブルクリック）は止めません。先に自分で停止してから `diana passwd` を実行してください。

**アップグレード**：完全版 Release パッケージは、インストールコマンドの再実行かコンソールの「アップグレード」で更新します。どちらもデータをバックアップし、パッケージを検証し、ヘルスチェックに失敗すればロールバックします。Docker はホスト側またはオプションのアップデーターでイメージを取得してコンテナを作り直します。Docker イメージの更新はヘルスチェックに失敗しても自動ではロールバックしないので、必要ならホスト側で既知のバージョンタグに戻してください。

**アンインストール**：`diana uninstall` はサービスとプログラムを削除し、データは残します（再インストールすればそのまま続きから使えます）。`diana uninstall --purge` はデータも削除します。元に戻せないため、二度確認を求めます。

## 設定の置き場所

目安は一つ：**普段の設定は Web コンソールで変更し、`config.yaml` はサービス自体の設定だけを扱います。**

`config.yaml`（インストールディレクトリ内）は待ち受けアドレス、ポート、データパスなどの基盤設定を扱い、変更には再起動が必要です。`admin:` セクションで最初の管理者のユーザー名とパスワードを事前設定できますが、データベースに管理者がまだいない間だけ有効で、ワンクリックインストーラーは Diana の起動後に平文のパスワードを削除します。ボットとモデルの設定はデータベースに保存され、コンソールで変更すると即座に反映されます。`config.yaml` の `bot:` / `llm:` セクションは、空のデータベースで初めて起動したときに**一度だけ**取り込まれ、その後は無視されます（起動ログにも明記されます）。無人デプロイ向けの仕組みです。

全項目の説明は [`config.example.yaml`](./config.example.yaml) にあります。

## セキュリティ

- コンソールは初回起動時から認証必須で、IP ごとのログイン失敗制限を備えています。
- 保存済みの API キーやトークンは平文で再表示されません。
- リバースプロキシ配下では `config.yaml` の `trusted_proxies`（または環境変数 `DIANA_TRUSTED_PROXIES`）を必ず設定してください。
- オーナー設定後は 6 桁ワンタイムコードによるチャット経由のクイックログインが利用でき、ログイン元 IP と端末が即時通知されます。

## 開発への参加

バックエンドは Go（Gin + SQLite）、フロントエンドは Vue 3 + TypeScript（`frontend/`）です。

```sh
make dev        # バックエンド :18080 + Vite :5173
go test ./...   # バックエンドテスト
```

PR 歓迎です。詳細は [CONTRIBUTING.md](./CONTRIBUTING.md) および [AGENTS.md](./AGENTS.md) をご覧ください。

## ドキュメント

| | |
| --- | --- |
| [デプロイ](https://suink.github.io/Diana/en/deploy.html) | 各種インストール方法・サーバー運用・初回ログイン |
| [設定](https://suink.github.io/Diana/en/configuration.html) | チャネル・モデル割当・グループポリシー・セキュリティ境界 |
| [内部実装](https://suink.github.io/Diana/en/implementation.html) | アーキテクチャ・応答判定パイプライン・メモリ階層 |
| [パイプライン](https://suink.github.io/Diana/en/pipeline.html) | 応答トリガー・エージェント実行・ブラウザ・送信前チェックのフロー図 |
| [運用](https://suink.github.io/Diana/en/operations.html) | アップデートとロールバック・バックアップ・トラブルシューティング |
| [オンラインデモ](https://suink.github.io/Diana/demo/) | モックデータ入りの実コンソールをブラウザで体験 |

## ライセンス

`Limited Redistribution License (SuInk)` — 詳細は [LICENSE](./LICENSE) をご覧ください。
