-- vector_index.sql — VECTOR INDEX 作成（COSINE）
-- Requirements: 5.1-5.6   Boundary: VectorIndex   Depends: 3.1
--
-- 対象(5.1): image_embeddings.embedding 列。
-- 距離タイプ(5.2): COSINE に固定（共有契約。下流 image-search-api の VECTOR_SEARCH と一致）。
-- 種別(5.4): IVF（既定）。テキスト→画像のセマンティック類似に整合。
-- 冪等(5.5): CREATE VECTOR INDEX IF NOT EXISTS。
-- populate 下限(5.3): base table が約 10MB 未満ではインデックス未populate（BASE_TABLE_TOO_SMALL）となり、
--   検索はブルートフォースにフォールバックする。十分なデータ投入後に自動有効化される（運用上明示）。
-- 非同期更新(5.6): 新規行は BigQuery のマネージドな非同期更新に依拠し、手動再構築は不要。
-- 注入する環境依存値: ${DATASET_ID}

CREATE VECTOR INDEX IF NOT EXISTS image_embeddings_idx
ON `${DATASET_ID}.image_embeddings`(embedding)
OPTIONS (
  index_type = 'IVF',
  distance_type = 'COSINE'
);

-- 検証(5.1, 5.2, 5.3): COSINE のインデックス登録と coverage/refresh 状態。validate.sql 参照。
--   SELECT index_name, index_status, coverage_percentage, last_refresh_time, disable_reason
--   FROM `${DATASET_ID}`.INFORMATION_SCHEMA.VECTOR_INDEXES
--   WHERE table_name = 'image_embeddings';
