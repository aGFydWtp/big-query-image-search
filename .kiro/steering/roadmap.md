# Roadmap

## Overview
Google Cloud 上にテキスト→画像のセマンティック検索システムを構築する。ユーザーが自然言語クエリ（例:「夕焼けの海」）を投げると、対象画像群の中から意味的に近い画像を返す。技術スタックは Cloud Storage（画像保管）、`gemini-embedding-2`（マルチモーダル埋め込み）、BigQuery `VECTOR_SEARCH`（ベクトル近傍探索）、Cloud Run（検索 API）。インフラはすべて Terraform で管理する。

画像のインジェスト（埋め込み生成→BigQuery 投入）はバッチ方式とし、BigQuery の Object Table（BigLake 接続）+ `AI.GENERATE_EMBEDDING` を使った純 SQL ベースで実装する。これにより専用の埋め込みサービスを持たず、Cloud Run は検索 API のみに集約する。フロント UI は今回のスコープ外。

## Approach Decision
- **Chosen**: BigQuery ネイティブ取込 + Cloud Run 検索 API。バッチ取込は GCS Object Table に対する `AI.GENERATE_EMBEDDING`（リモートモデル `gemini-embedding-2`）で純 SQL 実行し、検索時は Cloud Run から `AI.GENERATE_EMBEDDING` + `VECTOR_SEARCH` を同一クエリで実行する。
- **Why**: バッチ取込で常駐サービスが不要になり、部品数・運用・コストを最小化できる。埋め込み生成とベクトル探索を BigQuery に寄せることで、テキストと画像が同一モデル（同一ベクトル空間）で埋め込まれることを保証しやすい。Terraform 管理対象もシンプルになる。
- **Rejected alternatives**:
  - 専用埋め込みサービス（Cloud Run/Functions で Vertex API を直接呼ぶ取込パイプライン）: 取込側にも常駐/実行基盤が必要になり、部品とコストが増える。バッチ要件では過剰。
  - 旧 `multimodalembedding@001` の採用: 2026-06-24 に SDK アクセス終了予定で新規採用は不適切。後継の `gemini-embedding-2`（マルチモーダル対応）を採用する。

## Scope
- **In**: 画像保管（GCS）、バッチ埋め込み生成（gemini-embedding-2 / BigQuery ネイティブ）、ベクトルインデックス、テキスト→画像検索 API（Cloud Run）、これら全体の Terraform 管理。
- **Out**: フロントエンド検索 UI、画像→画像検索、イベント駆動のリアルタイム自動取込、認証/課金などのエンタープライズ機能（明示要求があるまで）、画像のアップロード受付 API。

## Constraints
- 埋め込みモデルは `gemini-embedding-2`（マルチモーダル）。`multimodalembedding@001` は削除予定のため使用しない。
- テキストと画像は必ず同一の埋め込みモデル・同一次元で埋め込むこと（同一ベクトル空間が検索精度の前提）。
- インフラはすべて Terraform 管理。手動コンソール操作に依存しない。
- BigQuery リモートモデルの作成は SQL DDL であり Terraform リソースではない点に注意（接続/IAM は TF、モデル DDL は別管理が必要）。
- `VECTOR_SEARCH` の精度/性能のため `CREATE VECTOR INDEX` を利用。インデックス作成にはデータ行数の下限がある点に注意（設計時に最新値を確認）。
- リージョン: BigQuery / Vertex AI / Cloud Run / GCS のリージョン整合に注意（モデル提供リージョンを確認）。

## Boundary Strategy
- **Why this split**: 「基盤(IaC)」「データ取込(埋め込み生成)」「検索API」は責務が明確に分かれ、依存も一方向。基盤が固まれば取込と API はそれぞれ独立してレビュー・実装できる。
- **Shared seams to watch**:
  - BigQuery embeddings テーブルのスキーマ（埋め込み次元・カラム名）: ingestion が定義し、search-api が参照する共有契約。
  - BigLake 接続・リモートモデル名・データセット名: infrastructure が払い出し、ingestion と search-api が参照する。
  - IAM（Cloud Run サービスアカウントの BigQuery/Vertex 権限、BQ 接続 SA の GCS/Vertex 権限）: infrastructure が定義、両 spec が前提とする。

## Specs (dependency order)
- [x] gcp-infrastructure -- Terraform 一式（GCS, BigQuery dataset, BigLake 接続/Object Table, Cloud Run サービス基盤, IAM, 必要 API 有効化）。Dependencies: none
- [x] image-ingestion-pipeline -- GCS Object Table から gemini-embedding-2 で埋め込みをバッチ生成し embeddings テーブルへ投入、VECTOR INDEX を作成。Dependencies: gcp-infrastructure
- [x] image-search-api -- Cloud Run 上のテキスト→画像検索 API（クエリ埋め込み + VECTOR_SEARCH + 結果返却）。Dependencies: gcp-infrastructure, image-ingestion-pipeline
