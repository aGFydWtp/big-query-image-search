# Brief: image-search-api

## Problem
ユーザーがテキストで画像を検索できる入口がない。embeddings テーブルと VECTOR INDEX があっても、テキストクエリを受け取り埋め込み・近傍探索・結果返却を行う API が必要。

## Current State
gcp-infrastructure が Cloud Run サービス基盤・SA・IAM を、image-ingestion-pipeline が embeddings テーブル・VECTOR INDEX を提供する前提。検索 API は未実装。

## Desired Outcome
Cloud Run 上の HTTP API が自然言語クエリを受け取り、`gemini-embedding-2` でクエリを埋め込み、BigQuery `VECTOR_SEARCH` で近傍画像を取得し、ランキングされた結果（画像 URI / 署名付き URL・スコア等）を返す。取込側と同一ベクトル空間で検索される。

## Approach
Cloud Run にバックエンド API を実装。検索リクエストごとに、BigQuery の `AI.GENERATE_EMBEDDING`（取込と同一の `gemini-embedding-2`）でクエリテキストを埋め込み、同一クエリ内で `VECTOR_SEARCH` を embeddings テーブル/インデックスに対して実行（往復削減）。結果の画像 URI から必要に応じて GCS 署名付き URL を生成して返却。Cloud Run SA は BigQuery 実行・Vertex 利用・GCS 署名権限を最小限で持つ。

## Scope
- **In**: 検索エンドポイント（テキスト→画像）、クエリ埋め込み + VECTOR_SEARCH 実行、結果整形（URI/署名 URL/スコア）、top-k・距離タイプ等のパラメータ、Cloud Run へのデプロイ定義、基本的なエラー処理。
- **Out**: 埋め込みの事前生成（ingestion 所有）、基盤リソース作成（infrastructure 所有）、フロント UI、認証/レート制限などのエンタープライズ機能（明示要求まで）。

## Boundary Candidates
- HTTP API レイヤ（リクエスト/レスポンス契約）
- クエリ埋め込み + VECTOR_SEARCH 実行ロジック
- 結果整形・署名 URL 生成
- Cloud Run デプロイ/ランタイム構成

## Out of Boundary
- embeddings テーブル/インデックスの生成・更新（ingestion 所有）
- GCS バケット・BigQuery dataset・SA/IAM の払い出し（infrastructure 所有）

## Upstream / Downstream
- **Upstream**: gcp-infrastructure（Cloud Run, SA, IAM, BigQuery）、image-ingestion-pipeline（embeddings テーブル + VECTOR INDEX）。
- **Downstream**: 将来のフロント UI / 外部クライアント（API 利用者）。

## Existing Spec Touchpoints
- **Extends**: なし（新規）
- **Adjacent**: image-ingestion-pipeline（embeddings テーブルスキーマ・埋め込み次元・モデル名の共有契約に厳密に従う）

## Constraints
- 検索時の埋め込みは取込と同一の `gemini-embedding-2`・同一次元。不一致は検索を破綻させる。
- `VECTOR_SEARCH` の距離タイプは取込時のインデックス定義と整合させる。
- Cloud Run はステートレス。レイテンシ最適化（同一クエリでの埋め込み+探索）を考慮。
- 最小権限の SA 設計。
