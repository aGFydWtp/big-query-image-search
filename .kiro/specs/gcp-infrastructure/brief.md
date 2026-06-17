# Brief: gcp-infrastructure

## Problem
画像検索システムの各機能（取込・検索）が依存する GCP 基盤が存在しない。Cloud Storage・BigQuery・BigLake 接続・Cloud Run・IAM・API 有効化を手動構築すると再現性がなく、Terraform 管理の要件も満たせない。

## Current State
グリーンフィールド。GCP リソース・Terraform コードとも未作成。

## Desired Outcome
`terraform apply` のみで画像検索に必要な基盤一式が再現的に構築される。取込パイプラインと検索 API は、ここで払い出されたリソース（バケット名・データセット・接続・サービスアカウント等）を前提に独立して実装できる。

## Approach
Terraform（google / google-beta provider）で以下を定義する: 必要 API 有効化（BigQuery, BigQuery Connection, Vertex AI/aiplatform, Cloud Run, Storage 等）、画像用 GCS バケット、BigQuery dataset、BigLake 用 `google_bigquery_connection`（Cloud Resource）と接続 SA への GCS 読取・Vertex 利用権限付与、Cloud Run サービスの基盤（サービスアカウント、最小権限の IAM、サービス枠）。リモートモデルや Object Table の DDL はデータ層の責務のため、ここでは接続・権限・データセットまでを払い出す（モデル DDL は ingestion 側で扱う）。

## Scope
- **In**: API 有効化、GCS バケット、BigQuery dataset、BigLake 接続 + 接続 SA への IAM、Cloud Run サービスアカウントと IAM、Terraform の state/変数/環境構成、リージョン整合の定義。
- **Out**: 埋め込み生成ロジック、embeddings テーブル/インデックス、リモートモデル DDL、検索 API の実装。

## Boundary Candidates
- API 有効化・プロジェクト前提
- ストレージ層（GCS バケット）
- データウェアハウス層（BigQuery dataset, BigLake 接続）
- 実行基盤（Cloud Run サービス + SA）
- IAM/権限境界

## Out of Boundary
- BigQuery リモートモデルの SQL DDL（ingestion 所有）
- embeddings テーブルスキーマ・VECTOR INDEX（ingestion 所有）
- 検索クエリ/アプリケーションコード（search-api 所有）

## Upstream / Downstream
- **Upstream**: GCP プロジェクト、Terraform 実行環境/権限。
- **Downstream**: image-ingestion-pipeline（接続・データセット・GCS を利用）、image-search-api（Cloud Run・SA・BigQuery を利用）。

## Existing Spec Touchpoints
- **Extends**: なし（新規）
- **Adjacent**: image-ingestion-pipeline / image-search-api（リソース名・IAM の共有契約に注意）

## Constraints
- すべて Terraform 管理。手動コンソール操作に依存しない。
- BigQuery / Vertex AI / Cloud Run / GCS のリージョン整合。
- リモートモデル作成は SQL DDL で TF リソースではないため、接続・IAM までを TF が担い、モデル DDL の扱いを設計で明示する。
- 最小権限の IAM 設計（接続 SA、Cloud Run SA）。
