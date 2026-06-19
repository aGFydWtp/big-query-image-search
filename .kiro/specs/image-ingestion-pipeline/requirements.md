# Requirements Document

## Introduction

本仕様 `image-ingestion-pipeline` は、画像セマンティック検索システムの「取込（インジェスト）」層を定義する。GCS バケット上の画像を BigQuery の Object Table で論理参照し、リモートモデル `gemini-embedding-2-preview` を用いて `AI.GENERATE_EMBEDDING` で埋め込みベクトルをバッチ生成し、埋め込みテーブル（embeddings テーブル）へ書き込み、十分なデータが揃った段階で `VECTOR INDEX` を作成する。すべて BigQuery ネイティブ・純 SQL 方式で実装し、常駐サービスを持たない。

このパイプラインを利用するのは、検索システムの運用者（データ取込担当）である。現状は GCS に画像が存在するのみで、検索可能な埋め込みが BigQuery 上に存在せず、検索 API（`image-search-api`）が探索対象とするテーブル・インデックスが未整備である。本仕様により、運用者がコンソール手動操作に頼らず、再現可能な SQL 群で埋め込みテーブルと VECTOR INDEX を生成・再生成できるようになる。

本仕様が定義する embeddings テーブルスキーマ（列名・埋め込み次元）、リモートモデル名（`gemini-embedding-2-preview`）、VECTOR INDEX の距離タイプは、下流の `image-search-api` が消費する共有契約であり、本仕様がその唯一の所有者（source of truth）となる。

## Boundary Context

**所有（This spec owns）**:
- Object Table（GCS 画像の論理ビュー）の DDL
- リモートモデル（`gemini-embedding-2-preview` を参照する BigQuery リモートモデル）の DDL
- embeddings テーブルのスキーマと DDL（列名・埋め込みベクトル次元の確定）
- 埋め込み生成バッチ SQL（`AI.GENERATE_EMBEDDING`）
- VECTOR INDEX の DDL（距離タイプの確定）
- 取込の再実行・冪等運用手順

**境界外（Out of boundary, 別仕様が所有）**:
- GCS バケット・BigQuery dataset・BigLake 接続・IAM・必要 API の作成（`gcp-infrastructure` が所有。本仕様はその出力を消費する）
- 検索クエリ実行・テキスト→画像検索 API・Cloud Run サービス本体（`image-search-api` が所有）
- イベント駆動の自動取込、画像→画像検索

**消費する上流（Allowed dependencies / `gcp-infrastructure` の出力）**:
- BigQuery dataset（`project_id.dataset_id`）
- BigLake 接続（`projects/{project}/locations/{region}/connections/{connection_id}`、`cloud_resource` 型、接続 SA に GCS Object Viewer / Vertex AI User 権限付与済み）
- 画像保管 GCS バケット（公開アクセス抑止済み）
- 単一 `region`（dataset・接続・モデルのリージョン整合の基準）

**隣接（Adjacent）**: `image-search-api`（embeddings テーブルスキーマ・埋め込み次元・モデル名・距離タイプを共有契約として参照）。

## Requirements

### Requirement 1: Object Table（GCS 画像の論理ビュー）

**Objective:** 取込担当者として、GCS バケット上の画像を BigQuery から論理参照したい。これにより、画像を移動・複製せずに埋め込み生成の入力にできる。

#### Acceptance Criteria

1. WHEN Object Table 作成 SQL を実行する THEN image-ingestion-pipeline SHALL 上流が払い出した BigLake 接続を `WITH CONNECTION` で参照し、画像バケットの URI（プレフィックスワイルドカード）を入力とする Object Table を作成する
2. WHEN Object Table を作成する THEN image-ingestion-pipeline SHALL `object_metadata = 'SIMPLE'` を指定し、GCS オブジェクトのメタデータ（URI を含む）を列として参照可能にする
3. WHERE Object Table が参照する dataset・接続・GCS バケットのロケーション THE image-ingestion-pipeline SHALL すべて上流の単一 `region` と整合させる
4. WHEN Object Table 作成 SQL を再実行する THEN image-ingestion-pipeline SHALL `CREATE OR REPLACE` により冪等に再作成し、後続の埋め込み生成へ影響を与えない
5. IF 入力 URI に画像以外のオブジェクトが含まれる可能性がある THEN image-ingestion-pipeline SHALL 後続の埋め込み生成段で画像 MIME / 拡張子による絞り込みを行えるよう、絞り込み条件を SQL 上で明示する

### Requirement 2: リモートモデル（gemini-embedding-2-preview）

**Objective:** 取込担当者として、`gemini-embedding-2-preview` を参照する BigQuery リモートモデルを定義したい。これにより、SQL から画像埋め込みを生成でき、かつ検索側と同一モデル・同一ベクトル空間を保証できる。

#### Acceptance Criteria

1. WHEN リモートモデル作成 SQL を実行する THEN image-ingestion-pipeline SHALL `CREATE MODEL ... REMOTE WITH CONNECTION` で上流の BigLake 接続を参照し、`OPTIONS(ENDPOINT = 'gemini-embedding-2-preview')` を指定したリモートモデルを作成する
2. WHERE リモートモデルの定義 THE image-ingestion-pipeline SHALL リモートモデル DDL を Terraform 管理外（SQL 資産）として扱い、Terraform リソースとして宣言しない
3. WHEN リモートモデルを作成・参照する THEN image-ingestion-pipeline SHALL モデルと入力テーブルが同一 `region`・同一 dataset であることを満たす
4. WHEN リモートモデル作成 SQL を再実行する THEN image-ingestion-pipeline SHALL `CREATE OR REPLACE MODEL` により冪等に再作成する
5. THE image-ingestion-pipeline SHALL 採用モデルを `gemini-embedding-2-preview` に固定し、下流 `image-search-api` がクエリ埋め込みに用いるモデルと同一であることを共有契約として明記する

> **注記（Task 0 実機検証で確定, 2026-06-19）**: 正しいエンドポイント名は `gemini-embedding-2-preview`（素の `gemini-embedding-2` は実在しない）。本モデルは **Preview ステージ**であり、3072 次元のマルチモーダル（画像）埋め込みを提供する一方、BigQuery は「本番安定性には GA エンドポイント推奨」と警告する。GA 安定を優先する場合の代替は `multimodalembedding@001`（GA, 最大 1408 次元）だが、その場合は共有契約の次元を 3072→1408 に変更する必要がある（採用案: A = 3072 次元維持）。`us-central1` での動作は実機で確認済み。

### Requirement 3: embeddings テーブルスキーマ（共有契約）

**Objective:** 取込担当者および下流の検索 API 担当者として、画像 URI・メタデータ・埋め込みベクトルを保持する embeddings テーブルの確定スキーマを共有したい。これにより、取込と検索が同一の列名・次元で連携できる。

#### Acceptance Criteria

1. THE image-ingestion-pipeline SHALL embeddings テーブルの列を以下に固定する: `image_uri STRING`（画像の GCS URI、主キー相当の一意識別子）、`embedding ARRAY<FLOAT64>`（埋め込みベクトル）、`content_type STRING`（MIME タイプ）、`generated_at TIMESTAMP`（生成時刻）
2. THE image-ingestion-pipeline SHALL 埋め込みベクトルの次元を 3072 に固定し、列 `embedding` の全行が同一次元かつ非 NULL であることを共有契約として明記する
3. WHEN embeddings テーブルを作成する THEN image-ingestion-pipeline SHALL 上流の dataset 内に作成し、ロケーションを単一 `region` と整合させる
4. WHERE embeddings テーブルのスキーマ THE image-ingestion-pipeline SHALL 当該スキーマ（列名・型・次元・距離タイプ）を下流 `image-search-api` との唯一の共有契約 source of truth として定義する
5. WHEN embeddings テーブルの DDL を実行する THEN image-ingestion-pipeline SHALL 既存の埋め込みデータを意図せず破壊しないよう、`CREATE TABLE IF NOT EXISTS` もしくは明示的な再構築手順を区別して提供する

### Requirement 4: 埋め込み生成バッチ

**Objective:** 取込担当者として、Object Table の画像から埋め込みをバッチ生成し embeddings テーブルへ投入したい。これにより、検索対象の埋め込みデータを再現可能に整備できる。

#### Acceptance Criteria

1. WHEN 埋め込み生成 SQL を実行する THEN image-ingestion-pipeline SHALL `AI.GENERATE_EMBEDDING` にリモートモデルと Object Table を入力として渡し、画像から埋め込みを生成する
2. WHEN 埋め込み生成結果を書き込む THEN image-ingestion-pipeline SHALL `AI.GENERATE_EMBEDDING` の出力列 `embedding`（`ARRAY<FLOAT64>`）を embeddings テーブルの `embedding` 列へ、Object Table の URI を `image_uri` 列へマッピングする
3. WHILE 既に埋め込み済みの画像が embeddings テーブルに存在する WHEN 生成バッチを再実行する THEN image-ingestion-pipeline SHALL 未処理画像のみを対象とする差分取込（`MERGE` もしくは未処理 URI のフィルタ）により、重複行を生まず冪等に投入する
4. IF 埋め込み生成で一部行が失敗・空結果になる THEN image-ingestion-pipeline SHALL 失敗行を除外して成功行のみを投入し、失敗有無を検証可能にする
5. WHERE 大量画像を扱う場合 THE image-ingestion-pipeline SHALL Vertex AI のクォータ・コストを考慮し、バッチを URI レンジ等で分割実行できる手順を提供する
6. WHEN 生成バッチを運用する THEN image-ingestion-pipeline SHALL スケジュールドクエリまたは手動 SQL スクリプトとして実行できる手順を提供する

### Requirement 5: VECTOR INDEX

**Objective:** 取込担当者として、embeddings テーブルにベクトルインデックスを作成したい。これにより、下流の検索 API が近似最近傍探索を高速・低コストに実行できる。

#### Acceptance Criteria

1. WHEN VECTOR INDEX 作成 SQL を実行する THEN image-ingestion-pipeline SHALL embeddings テーブルの `embedding` 列に対し `CREATE VECTOR INDEX` を実行する
2. THE image-ingestion-pipeline SHALL 距離タイプを `COSINE` に固定し、下流 `image-search-api` の `VECTOR_SEARCH` が用いる距離タイプと一致させることを共有契約として明記する
3. WHILE base table のサイズが BigQuery のインデックス対象下限（約 10 MB）未満である THE image-ingestion-pipeline SHALL インデックスが未populate（`BASE_TABLE_TOO_SMALL`）となりブルートフォースにフォールバックする旨を運用上明示し、十分なデータ投入後にインデックスが有効化されることを保証する
4. WHEN VECTOR INDEX を作成する THEN image-ingestion-pipeline SHALL インデックス種別・距離タイプ・対象列を明示し、検索要件（テキスト→画像のセマンティック類似）に整合させる
5. WHEN VECTOR INDEX 作成 SQL を再実行する THEN image-ingestion-pipeline SHALL `CREATE OR REPLACE VECTOR INDEX` もしくは `IF NOT EXISTS` により冪等に扱う
6. WHEN embeddings テーブルへ新規行が追加される THEN image-ingestion-pipeline SHALL BigQuery のマネージドな非同期インデックス更新に依拠し、手動再構築を必須としない

### Requirement 6: 再実行・運用手順と冪等性

**Objective:** 取込担当者として、取込パイプライン全体を手動コンソール操作に依存せず再現・再実行したい。これにより、初期構築と継続的な画像追加の双方を安全に運用できる。

#### Acceptance Criteria

1. THE image-ingestion-pipeline SHALL 全 DDL/DML を SQL 資産（バージョン管理対象ファイル）として提供し、手動コンソール操作を前提としない
2. WHEN SQL 群を所定の順序（Object Table → リモートモデル → embeddings テーブル → 埋め込み生成 → VECTOR INDEX）で実行する THEN image-ingestion-pipeline SHALL エンドツーエンドで検索可能な embeddings テーブルと VECTOR INDEX を生成する
3. WHERE `project_id`・`dataset_id`・`connection_id`・`region`・バケット URI 等の環境依存値 THE image-ingestion-pipeline SHALL これらを SQL 内のプレースホルダ／パラメータとして外部化し、上流出力から値を注入できるようにする
4. WHEN いずれかの SQL を再実行する THEN image-ingestion-pipeline SHALL 各ステップを冪等に扱い、既存データの意図しない破壊を回避する
5. WHEN 取込結果を検証する THEN image-ingestion-pipeline SHALL 行数・埋め込み次元・NULL 有無・インデックス状態を確認するための検証クエリを提供する
