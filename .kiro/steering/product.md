# Product Overview

Google Cloud 上に構築する **テキスト→画像セマンティック検索システム**。利用者が自然言語クエリ（例:「夕焼けの海」）を投げると、対象画像群の中から意味的に近い画像を返す。フロント UI は持たず、検索 API とバッチ取込基盤を提供する。

## Core Capabilities

- **バッチ画像取込**: GCS 上の画像を BigQuery Object Table 経由で参照し、純 SQL（`AI.GENERATE_EMBEDDING`）で埋め込みを生成・投入する。常駐サービス不要。
- **テキスト→画像検索 API**: Cloud Run 上でクエリ埋め込み + `VECTOR_SEARCH` を実行し、近い画像を返す。
- **単一ベクトル空間の保証**: 取込・検索の双方で同一モデル（`gemini-embedding-2`）・同一次元を用い、テキストと画像を同じ空間に埋め込む（検索精度の前提）。
- **再現可能なインフラ**: 全リソースを Terraform 管理し、手動コンソール操作に依存しない。

## Target Use Cases

- **取込担当者**: 上流が払い出した基盤（dataset・BigLake 接続・バケット）に対し、再現可能な SQL 群で embeddings テーブルと VECTOR INDEX を生成・再生成する。
- **検索利用者**: 検索 API に自然言語クエリを送り、意味的に近い画像（URI）を受け取る。

## Value Proposition

- バッチ取込を BigQuery ネイティブの純 SQL に寄せることで、専用の埋め込みサービスが不要になり、部品数・運用・コストを最小化する。
- 埋め込み生成とベクトル探索を BigQuery に集約し、テキスト・画像が同一モデルで同一ベクトル空間に乗ることを構造的に保証する。

## Scope Boundaries

- **In**: 画像保管（GCS）、バッチ埋め込み生成（`gemini-embedding-2` / BigQuery ネイティブ）、VECTOR INDEX、テキスト→画像検索 API（Cloud Run）、これら全体の Terraform 管理。
- **Out**: フロントエンド検索 UI、画像→画像検索、イベント駆動のリアルタイム自動取込、認証/課金等のエンタープライズ機能、画像アップロード受付 API（明示要求があるまで）。

---
_Focus on patterns and purpose, not exhaustive feature lists. 詳細な機能仕様は `.kiro/specs/` を参照。_
