# big-query-image-search

Google Cloud 上に構築する **テキスト → 画像セマンティック検索システム**。
自然言語クエリ（例: 「夕焼けの海」）を投げると、対象画像群の中から意味的に近い画像を返す。

埋め込み生成とベクトル探索を **BigQuery ネイティブ**（純 SQL の `AI.GENERATE_EMBEDDING` + `VECTOR_SEARCH`）に集約し、
取込・検索の双方で同一モデル（`gemini-embedding-2`）・同一ベクトル空間を構造的に保証する。常駐の埋め込みサービスは持たない。

---

## アーキテクチャ

```
                    ┌─────────────────────────── Google Cloud ───────────────────────────┐
                    │                                                                     │
  ブラウザ / API ──▶ │  Cloud Run (image-search-api)  ──┐                                  │
   （IAP 認証）       │   ├─ GET  /        検索 Web UI    │  AI.GENERATE_EMBEDDING           │
                    │   ├─ POST /search  検索 API       ├─▶ BigQuery ─ VECTOR_SEARCH ──┐   │
                    │   └─ GET  /healthz 死活           │   （埋め込み + ベクトル探索）   │   │
                    │                                  │                              ▼   │
                    │  Cloud Storage（画像保管）◀── 署名付き URL ──── 一致画像（image_uri） │
                    │                                                                     │
                    │  バッチ取込: GCS → BigLake Object Table → AI.GENERATE_EMBEDDING（純SQL）│
                    └─────────────────────────────────────────────────────────────────────┘
```

3 つの spec が依存順に責務を分担する（Kiro 方式の Spec-Driven Development）:

1. **gcp-infrastructure** — GCS / BigQuery dataset / BigLake 接続 / IAM / Cloud Run 本体 / IAP を Terraform 管理
2. **image-ingestion-pipeline** — 純 SQL バッチで埋め込みテーブルと VECTOR INDEX を生成（`docs/runbook.md`）
3. **image-search-api** — Cloud Run 上の検索 API（Go）。クエリ埋め込み + `VECTOR_SEARCH` を実行

## 主な機能

- **テキスト → 画像検索 API**（`POST /search`）: 自然言語クエリを埋め込み、近い画像を署名付き URL 付きで返す
- **検索 Web UI**（`GET /`）: Go バイナリに `//go:embed` で同梱した SPA。同一オリジン配信で CORS 不要
- **バッチ画像取込**: 常駐サービス不要。BigQuery ネイティブの純 SQL で埋め込み生成
- **IAP 認証**: Cloud Run 直結 IAP で UI/API を保護（組織外ドメインの公開も可。`docs/` 参照）
- **再現可能なインフラ**: 全リソースを Terraform 管理

## ディレクトリ構成

```
cmd/server/            エントリポイント（main）
internal/
  config/              環境変数の読み込み・検証
  httpapi/             ルーティング・検索ハンドラ・静的 UI 配信（web/ を embed）
  search/              BigQuery 検索（埋め込み + VECTOR_SEARCH）
  signedurl/           GCS V4 署名付き URL（keyless / signBlob）
  result/ validation/  整形・入力検証
terraform/             IaC（GCS/BigQuery/IAM/Cloud Run/IAP/org policy）
sql/                   取込・リモートモデル等の SQL 資産
scripts/               AIC 画像シーダー等の補助スクリプト（Python）
deploy/                Dockerfile
docs/                  runbook ほか
.kiro/                 steering / specs（Spec-Driven Development）
```

## 必要要件

- Go `1.26+`
- Terraform `>= 1.5`（providers: `hashicorp/google` / `google-beta` `>= 5.0`、`hashicorp/time`）
- `gcloud` CLI（認証済み）
- GCP プロジェクト（BigQuery / Cloud Run / Cloud Storage / Vertex AI / IAP 利用可）

## セットアップ

### 1. インフラ（Terraform）

```bash
# バックエンド設定を注入して初期化（bucket/prefix は backend.hcl で指定）
terraform -chdir=terraform init -backend-config=backend.hcl

# 変数を用意（雛形をコピー。機微値は *.tfvars に置く＝git 追跡外）
cp terraform/terraform.tfvars.example terraform/terraform.tfvars

terraform -chdir=terraform plan
terraform -chdir=terraform apply
```

> Cloud Run 本体は Terraform 管理（`google_cloud_run_v2_service`）。既存サービスを取り込む場合は
> `terraform import google_cloud_run_v2_service.api projects/<PROJECT_ID>/locations/<REGION>/services/image-search-api`
> を先に実行する。アプリのイメージ更新は `gcloud run deploy` 側が担い、`image` は `ignore_changes` 対象。

### 2. 取込（埋め込み生成）

`docs/runbook.md` の SQL 実行順に従い、Object Table → 埋め込みテーブル → VECTOR INDEX を生成する。

### 3. アプリのデプロイ（Cloud Run）

```bash
# linux/amd64 でビルド（Dockerfile は COPY . . → go build ./cmd/server。web/ は embed 同梱）
docker build --platform linux/amd64 -f deploy/Dockerfile -t <IMAGE> .
docker push <IMAGE>
gcloud run deploy image-search-api --image <IMAGE> --region <REGION>
```

## 検索 API

### `POST /search`

リクエスト:

```json
{
  "query": "夕焼けの海",
  "query_en": "a seascape at sunset or twilight",
  "top_k": 12,
  "signed_url": true
}
```

`query_en` は任意。指定時は `query`（生クエリ）と `query_en`（英語リライト）の 2 ベクトル検索を
RRF(k=60) で融合する。未指定時はサーバ側 Vertex AI（`gemini-2.5-flash`）が英語リライトを自動生成して
2 系統目に用いる（`REWRITE_ENABLED=false` や生成失敗時は生クエリのみの単一チャネル検索にフォールバック）。

レスポンス:

```json
{
  "results": [
    {
      "image_uri": "gs://<bucket>/path/to/image.jpg",
      "score": 0.83,
      "signed_url": "https://storage.googleapis.com/...",
      "content_type": "image/jpeg"
    }
  ]
}
```

### その他のエンドポイント

| メソッド | パス | 用途 |
|---|---|---|
| GET | `/` | 検索 Web UI（embed 同梱の SPA） |
| POST | `/search` | テキスト → 画像検索 |
| GET | `/healthz` | 死活確認 |

## 環境変数

| 変数 | 説明 |
|---|---|
| `PROJECT_ID` | GCP プロジェクト ID |
| `REGION` | リージョン（`us-central1`） |
| `DATASET_ID` | BigQuery dataset（`project.dataset` 形式） |
| `EMBEDDINGS_TABLE` | 埋め込みテーブル名 |
| `MODEL` | BigQuery リモート埋め込みモデル名 |
| `IMAGE_BUCKET` | 画像保管 GCS バケット |
| `RUN_SA_EMAIL` | Cloud Run 実行 SA（署名付き URL 発行に使用） |
| `SIGNED_URL_EXPIRY` | 署名付き URL の有効期限（例 `15m`） |

## 認証（IAP）

検索 UI/API は Cloud Run 直結 IAP で保護できる。閲覧許可は `roles/iap.httpsResourceAccessor` の付与で制御し、
Terraform の `var.iap_members`（例: `domain:example.com` / `group:` / `user:`）で宣言する。

組織外ドメインのユーザーへ公開する場合は、OAuth 同意画面の External 化・カスタム OAuth クライアント・
IAP 許可ドメイン（run.app ホスト）の設定が追加で必要（一部はコンソール/`gcloud` 依存で Terraform 管理外）。

## 開発

```bash
go test ./...          # テスト
go build ./cmd/server  # ビルド
go vet ./...           # 静的解析
```

## Spec-Driven Development

本リポジトリは Kiro 方式（Requirements → Design → Tasks → Implementation、各フェーズで人間レビュー）で開発する。
steering は `.kiro/steering/`、各機能の仕様は `.kiro/specs/` を参照。spec ドキュメントは日本語で記述する。
