-- remote_model.sql — リモートモデル（gemini-embedding-2-preview 参照）作成
-- Requirements: 2.1-2.5   Boundary: RemoteModel
--
-- Terraform 管理外の SQL 資産として扱う(2.2)。
-- 冪等性(2.4): CREATE OR REPLACE MODEL。
-- ロケーション(2.3): モデルと入力テーブルは同一 ${REGION}・同一 dataset。
-- 共有契約(2.5): モデルオブジェクト名 gemini_embedding_model を下流 image-search-api が
--   クエリ埋め込みで同一参照する。オブジェクト名は変更しないこと。
--
-- 注記（Task 0, 2026-06-19 実機＋公式ドキュメントで確定）:
--   - 正しいエンドポイント名は gemini-embedding-2-preview（Preview ステージ）。
--     素の 'gemini-embedding-2' は実在せずモデル作成不可。
--   - us-central1 での作成は実機確認済み。3072 次元のマルチモーダル（画像）埋め込みに対応。
--   - 本番安定性を最優先する場合の GA 代替: ENDPOINT='multimodalembedding@001'（最大1408次元）。
--     ただし共有契約の次元 3072→1408 変更が必要（採用案A: 3072 維持）。
-- 注入する環境依存値: ${DATASET_ID} ${CONNECTION_ID}

CREATE OR REPLACE MODEL `${DATASET_ID}.gemini_embedding_model`
REMOTE WITH CONNECTION `${CONNECTION_ID}`
OPTIONS (
  ENDPOINT = 'gemini-embedding-2-preview'
);

-- 検証(2.1, 2.5): モデルが存在しエンドポイントが gemini-embedding-2-preview であること。
--   bq show --model --format=prettyjson "${DATASET_ID}.gemini_embedding_model"
