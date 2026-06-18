# Technology Stack

## Architecture

BigQuery ネイティブ取込 + Cloud Run 検索 API。常駐の埋め込みサービスを持たず、取込はバッチ（純 SQL）、検索はリクエスト時に BigQuery で `AI.GENERATE_EMBEDDING` + `VECTOR_SEARCH` を実行する。3 つの spec が依存順（`gcp-infrastructure` → `image-ingestion-pipeline` → `image-search-api`）で責務を分担する。

## Core Technologies

- **IaC**: Terraform `>= 1.5`
- **Providers**: `hashicorp/google`, `hashicorp/google-beta` (`>= 5.0`)、`hashicorp/time` (`>= 0.9`、IAM 伝播待ち用)
- **DWH / 実行基盤**: BigQuery（GoogleSQL）、BigLake Object Table、BigQuery VECTOR INDEX
- **埋め込みモデル**: `gemini-embedding-2`（マルチモーダル）を BigQuery リモートモデルとして参照
- **ストレージ / ランタイム**: Cloud Storage（画像保管）、Cloud Run（検索 API）

## Key Technical Decisions

- **純 SQL バッチ取込**: 専用サービスを置かず `AI.GENERATE_EMBEDDING` で埋め込みを生成する。バッチ要件には専用 Vertex 呼出サービスは過剰。
- **同一モデル・同一次元**: 取込とクエリで同じ `gemini-embedding-2` を共有し、ベクトル空間を一致させる（精度前提）。
- **リモートモデルは SQL DDL、Terraform 管理外**: 接続・IAM は Terraform、リモートモデル DDL は SQL 資産として別管理する。
- **リージョン整合**: 単一 `region` 変数から全リソースロケーションを導出。単一リージョン（`us-central1`）のみ許可し、`region` は許可リスト（モデルエンドポイント提供確認をゲート）で制約する。
- **リモート state**: GCS バックエンド。partial configuration（`backend "gcs" {}`）で、bucket/prefix は `terraform init -backend-config=backend.hcl` で注入する。

## Development Standards

### 入力バリデーション
- 全入力変数に `validation` ブロックを付与する。必須値（例: `project_id`）は default を持たせず、`plan`/`apply` 前に失敗させる。

### 導出の単一ソース
- ロケーションは `local.location` を参照し、リソースから `var.region` を直接参照しない。派生名（バケット名等）も `locals.tf` に集約する。

### 再適用安全性（冪等）
- ステートフルなリソース（バケット・dataset・接続）は `prevent_destroy` + `force_destroy=false` / `delete_contents_on_destroy=false` で誤削除を防ぐ。再 apply は差分ゼロを基本とする。

### IAM
- 非 authoritative な `*_iam_member` のみを使用する（`*_iam_binding` / `*_iam_policy` は他メンバーを上書きするため禁止）。
- 最小権限。可能な限りリソーススコープ（バケット/データセット単位）でバインドし、リソーススコープ細分化が困難な箇所（`roles/aiplatform.user`）のみプロジェクトスコープを許容する。

### API 有効化・結果整合性
- 依存 API は理由コメント付きの明示リストを `for_each` で有効化し、`disable_on_destroy=false`。
- 接続 SA の IAM 伝播遅延は `time_sleep` で吸収し、初回 apply を単発成功させる。

## Common Commands

```bash
# 初期化（バックエンド設定を注入）
terraform -chdir=terraform init -backend-config=backend.hcl
# 計画 / 適用
terraform -chdir=terraform plan
terraform -chdir=terraform apply
# 取込 SQL は docs/runbook.md の実行順に従う（image-ingestion-pipeline 所有）
```

## Spec-Driven Development

Kiro 方式（Requirements → Design → Tasks → Implementation、各フェーズで人間レビュー）。spec ドキュメント（requirements/design/tasks 等）は日本語で記述する（`spec.json` の language 設定に従う）。

---
_Document standards and patterns, not every dependency. 個別リソース定義は `terraform/` を参照。_
