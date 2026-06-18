# Project Structure

## Organization Philosophy

**Spec-driven（Kiro）+ フラットな Terraform ルートモジュール**。システムを 3 つの spec に分割し、一方向依存（基盤 → 取込 → 検索 API）で責務を切る。各 spec は明確に異なるレイヤーを所有し、共有契約（seam）経由でのみ連携する。

## Directory Patterns

### Terraform ルートモジュール
**Location**: `terraform/`
**Purpose**: フラットな単一ルートモジュール。リソースドメインごとに 1 ファイル（`storage.tf`, `bigquery.tf`, `connection.tf`, `iam.tf`, `apis.tf`）。横断的関心事は `variables.tf` / `locals.tf` / `outputs.tf` / `versions.tf` / `backend.tf` に集約。
**Rule**: 各ファイルは自ドメインのみを宣言する（例: `bigquery.tf` は dataset のみ宣言し、テーブルやモデルは取込 spec の SQL に委ねる）。

### Spec ドキュメント
**Location**: `.kiro/specs/{feature}/`
**Purpose**: 機能ごとの `requirements.md` / `design.md` / `tasks.md` / `spec.json`、必要に応じ `brief.md`。

### ステアリング（プロジェクトメモリ）
**Location**: `.kiro/steering/`
**Purpose**: `product.md` / `tech.md` / `structure.md`（コア）と `roadmap.md` 等のカスタム。全 spec が参照する横断ルール・文脈。

### 取込 SQL 資産（予定）
**Location**: `sql/` + `docs/`
**Purpose**: Object Table / リモートモデル DDL / embeddings テーブル / 生成バッチ / VECTOR INDEX の SQL と runbook（`image-ingestion-pipeline` が所有）。

## Spec Boundary Ownership

- **gcp-infrastructure**: 全 Terraform（GCS バケット、BigQuery dataset、BigLake 接続、IAM、API 有効化、Cloud Run 実行 SA）。
- **image-ingestion-pipeline**: SQL 資産（Object Table、リモートモデル DDL、embeddings テーブル、埋め込み生成バッチ、VECTOR INDEX）。
- **image-search-api**: Cloud Run 上のテキスト→画像検索サービス本体。

### Shared Contracts（seams）
1 つの spec が所有し、他が `terraform output` または共有契約として消費する境界:
- embeddings テーブルのスキーマ・埋め込み次元・距離タイプ（ingestion 所有 → search-api 参照）
- リモートモデル名 `gemini-embedding-2` / モデルオブジェクト名（ingestion 所有 → search-api 参照）
- 接続 ID・dataset ID・バケット名（infrastructure が払い出し → 両 spec が消費）

## Naming Conventions

- **TF ファイル**: ドメイン単位の snake_case（`cloud_run_sa.tf` 等）。
- **TF リソースローカル名**: 役割を表す簡潔名（`google_storage_bucket.images`）。
- **GCP リソース名**: ハイフン区切り小文字（接続 ID）、BigQuery 識別子はアンダースコア（dataset_id）。派生名は `locals.tf` で生成。
- **Outputs**: 安定契約名。名称変更は下流の Revalidation Trigger（`outputs.tf` 冒頭コメント参照）。

## Code Organization Principles

- **導出の単一ソース**: ロケーション・派生名は `locals.tf` に集約し、各リソースから直接の変数参照を避ける。
- **トレーサビリティ**: 各ファイル/リソースの先頭コメントに対応する Requirement ID と design セクションを明記する。
- **境界の厳守**: ファイル/spec は自境界のみを宣言・実装し、上流の出力は消費のみ・下流の契約は破壊しない。

---
_Document patterns, not file trees. パターンに従う新規ファイル追加では本書の更新は不要。_
