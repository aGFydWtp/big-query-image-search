# Implementation Plan

- [ ] 1. 基盤: SQL 資産構成とパラメータ外部化の土台整備
- [ ] 1.1 SQL ディレクトリ構成とパラメータ注入の枠組みを作成する
  - `sql/` と `docs/` ディレクトリを作成し、`sql/params.example` に環境依存値（`project_id`, `dataset_id`, `connection_id`, `region`, `bucket_uri`）の入力例とプレースホルダ規約を定義する
  - 全 SQL が同一プレースホルダ規約（例: `${PROJECT_ID}`, `${DATASET_ID}`）で環境依存値を参照し、ハードコードしない方針を `docs/runbook.md` 冒頭に記す
  - 完了条件: `params.example` と `runbook.md` の雛形が存在し、上流出力（dataset/connection/region/bucket）の注入手順が記述されている
  - _Requirements: 6.1, 6.3_

- [ ] 2. 取込入力の定義（独立に作成可能な 2 資産）
- [ ] 2.1 (P) Object Table 作成 SQL を実装する
  - `sql/object_table.sql` に `CREATE OR REPLACE EXTERNAL TABLE` を実装し、上流 BigLake 接続を `WITH CONNECTION` で参照、`OPTIONS(object_metadata='SIMPLE', uris=['${BUCKET_URI}/*'])` を指定する
  - dataset・接続・バケットを単一 `region` と整合させ、後続段で MIME / 拡張子絞り込みを適用できるよう絞り込み条件例をコメントで明示する
  - 完了条件: 実行後に Object Table が作成され `SELECT COUNT(*)` でオブジェクト件数を取得でき、`CREATE OR REPLACE` 再実行が冪等
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5_
  - _Boundary: ObjectTable_

- [ ] 2.2 (P) リモートモデル作成 SQL を実装する
  - `sql/remote_model.sql` に `CREATE OR REPLACE MODEL ... REMOTE WITH CONNECTION` を実装し、`OPTIONS(ENDPOINT='gemini-embedding-2')` を指定する
  - モデルと入力テーブルを同一 `region`・同一 dataset に置く前提をコメントで明示し、Terraform 管理外の SQL 資産であることを記す
  - 完了条件: 実行後にリモートモデルが存在し、Remote endpoint が `gemini-embedding-2` であること、再実行が冪等
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5_
  - _Boundary: RemoteModel_

- [ ] 3. 埋め込み格納先（共有契約スキーマ）の定義
- [ ] 3.1 embeddings テーブル DDL を実装する
  - `sql/embeddings_table.sql` に `CREATE TABLE IF NOT EXISTS` を実装し、列 `image_uri STRING`, `embedding ARRAY<FLOAT64>`, `content_type STRING`, `generated_at TIMESTAMP` を固定する
  - `embedding` 次元を 3072、`image_uri` を論理一意キーとする共有契約をコメントで明記し、既存破壊を避ける作成と明示的再構築手順を区別する
  - 完了条件: 実行後にテーブルが契約どおりのスキーマで作成され、再実行で既存データが破壊されない
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5_
  - _Boundary: EmbeddingsTable_

- [ ] 4. 埋め込み生成バッチ（3 入力の統合点）
- [ ] 4.1 差分埋め込み生成・投入 SQL を実装する
  - `sql/generate_embeddings.sql` に `AI.GENERATE_EMBEDDING(MODEL <remote_model>, TABLE <object_table>)` を実装し、出力列 `embedding` を embeddings テーブルの `embedding` 列、URI を `image_uri` にマッピングする
  - 未処理 URI のみを対象とする `MERGE ON image_uri` で冪等・重複防止を実現し、空結果/失敗行を除外して成功行のみ投入する
  - 完了条件: 初回実行で行数が増加し、同一入力での再実行で重複行が増えない（`MERGE` 冪等）ことを確認できる
  - _Requirements: 4.1, 4.2, 4.3, 4.4_
  - _Boundary: EmbeddingGenerationBatch_
  - _Depends: 2.1, 2.2, 3.1_

- [ ] 4.2 バッチ分割と運用手順を整備する
  - `generate_embeddings.sql` に URI レンジ等での分割実行パラメータ（任意の WHERE 絞り込み）を組み込み、`docs/runbook.md` にスケジュールドクエリ／手動スクリプト両方の実行手順とクォータ・コスト配慮を記す
  - 完了条件: URI レンジ指定で部分投入が成立し、runbook に分割・再実行手順が記載されている
  - _Requirements: 4.5, 4.6_
  - _Boundary: EmbeddingGenerationBatch, IngestionRunbook_
  - _Depends: 4.1_

- [ ] 5. ベクトルインデックス作成
- [ ] 5.1 VECTOR INDEX 作成 SQL を実装する
  - `sql/vector_index.sql` に `embedding` 列への `CREATE VECTOR INDEX`（`IF NOT EXISTS`）を実装し、`OPTIONS(index_type='IVF', distance_type='COSINE')` を指定する
  - base table が約 10 MB 未満では未populate（`BASE_TABLE_TOO_SMALL`）でブルートフォースにフォールバックする旨と、新規行はマネージド非同期更新に依拠する旨をコメント・runbook に明示する
  - 完了条件: 実行後 `INFORMATION_SCHEMA.VECTOR_INDEXES` に COSINE のインデックスが登録され、再実行が冪等
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5, 5.6_
  - _Boundary: VectorIndex_
  - _Depends: 3.1_

- [ ] 6. 検証とエンドツーエンド結線
- [ ] 6.1 検証クエリを実装する
  - `sql/validate.sql` に行数、`ARRAY_LENGTH(embedding)=3072`、`embedding IS NOT NULL`、`INFORMATION_SCHEMA.VECTOR_INDEXES` の coverage/refresh 状態を確認するクエリを実装する
  - 完了条件: 各検証クエリが単独で実行でき、取込結果の健全性（次元・NULL・インデックス状態）を判定できる
  - _Requirements: 6.5, 4.4, 5.3_
  - _Boundary: ValidationQueries_
  - _Depends: 4.1, 5.1_

- [ ] 6.2 エンドツーエンド実行手順を確定し通し検証する
  - `docs/runbook.md` に実行順序（object_table → remote_model → embeddings_table → generate_embeddings → vector_index → validate）、再実行・パラメータ注入の手順を確定し、各 SQL が冪等であることを明記する
  - 順序実行で検索可能な `image_embeddings` + VECTOR INDEX が生成され、`validate.sql` が全チェックを通過することを確認する
  - 完了条件: runbook の順序どおりに実行すると embeddings テーブルと VECTOR INDEX が生成され、検証クエリが全項目を通過する
  - _Requirements: 6.2, 6.4_
  - _Depends: 2.1, 2.2, 3.1, 4.1, 4.2, 5.1, 6.1_
