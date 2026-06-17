# Brief: image-ingestion-pipeline

## Problem
GCS 上の画像群を検索可能にするには、各画像をベクトル化して BigQuery に格納し、近傍探索用のインデックスを用意する必要がある。これがなければ検索 API は探索対象を持てない。

## Current State
gcp-infrastructure が GCS バケット・BigQuery dataset・BigLake 接続・IAM を払い出す前提。埋め込み生成・テーブル・インデックスは未作成。

## Desired Outcome
GCS の画像に対してバッチで埋め込みが生成され、embeddings テーブルに格納され、VECTOR INDEX が作成された状態。検索 API はこのテーブル/インデックスに対して `VECTOR_SEARCH` を実行できる。再実行で増分/全件の取込が再現的に行える。

## Approach
BigQuery ネイティブ・純 SQL 方式。GCS バケット上に Object Table（BigLake 接続経由）を定義し、`gemini-embedding-2` を参照する BigQuery リモートモデルを作成、`AI.GENERATE_EMBEDDING` で画像埋め込みをバッチ生成して embeddings テーブル（画像 URI/メタ + 埋め込みベクトル）へ書き込む。十分な行数が揃った段階で `CREATE VECTOR INDEX`（距離タイプは検索要件に合わせ選定）。バッチ実行は SQL スクリプト/スケジュールドクエリ等で運用する。

## Scope
- **In**: Object Table 定義、リモートモデル DDL（gemini-embedding-2）、埋め込み生成 SQL（バッチ）、embeddings テーブルスキーマ、VECTOR INDEX 作成、再実行手順。
- **Out**: 検索クエリ実行/ API、基盤リソース（接続・dataset・GCS）の作成、イベント駆動の自動取込、画像→画像検索。

## Boundary Candidates
- Object Table（GCS 画像の論理ビュー）
- リモートモデル（埋め込みモデル参照）
- 埋め込み生成バッチ（AI.GENERATE_EMBEDDING）
- embeddings テーブルスキーマ（search-api との共有契約）
- VECTOR INDEX

## Out of Boundary
- 検索時のクエリ埋め込み・VECTOR_SEARCH（search-api 所有）
- バケット/接続/IAM の払い出し（gcp-infrastructure 所有）

## Upstream / Downstream
- **Upstream**: gcp-infrastructure（GCS, dataset, BigLake 接続, IAM）。
- **Downstream**: image-search-api（embeddings テーブル + VECTOR INDEX を探索対象として参照）。

## Existing Spec Touchpoints
- **Extends**: なし（新規）
- **Adjacent**: image-search-api（embeddings テーブルスキーマ・埋め込み次元・モデル名が共有契約）

## Constraints
- 埋め込みモデルは `gemini-embedding-2`。検索側と同一モデル・同一次元であること。
- VECTOR INDEX 作成にはデータ行数の下限あり（設計時に最新値を確認）。
- リモートモデル DDL は Terraform 外（SQL）で管理する点を明示。
- 大量画像時のクォータ/コスト/バッチ分割を考慮。
