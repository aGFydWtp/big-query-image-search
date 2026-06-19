-- validate.sql — 取込結果の検証クエリ
-- Requirements: 6.5, 4.4, 5.3   Boundary: ValidationQueries   Depends: 4.1, 5.1
--
-- 各クエリは単独で実行できる。取込結果の健全性（行数・次元・NULL・一意性・索引状態）を判定する。
-- 注入する環境依存値: ${DATASET_ID}

-- (1) 行数: 埋め込みが投入されていること(6.5)。
SELECT COUNT(*) AS row_count
FROM `${DATASET_ID}.image_embeddings`;

-- (2) 次元・NULL 健全性(3.2, 4.4): null_embedding と bad_dim がいずれも 0 であること。
SELECT
  COUNT(*)                                                        AS total,
  COUNTIF(embedding IS NULL OR ARRAY_LENGTH(embedding) = 0)       AS null_embedding,
  COUNTIF(ARRAY_LENGTH(embedding) <> 3072)                        AS bad_dim
FROM `${DATASET_ID}.image_embeddings`;

-- (3) image_uri 一意性（MERGE 冪等の確認, 4.3）: dup_uris が 0 であること。
SELECT COUNT(*) AS dup_uris
FROM (
  SELECT image_uri
  FROM `${DATASET_ID}.image_embeddings`
  GROUP BY image_uri
  HAVING COUNT(*) > 1
);

-- (4) VECTOR INDEX 状態(5.2, 5.3): COSINE 索引の登録・coverage・refresh・無効理由。
--   base table が約10MB未満の間は disable_reason に BASE_TABLE_TOO_SMALL が入り得る（想定動作）。
SELECT
  index_name,
  index_status,
  coverage_percentage,
  last_refresh_time,
  disable_reason
FROM `${DATASET_ID}`.INFORMATION_SCHEMA.VECTOR_INDEXES
WHERE table_name = 'image_embeddings';

-- (5) 失敗行の可視化(4.4) ※任意・コスト発生:
--   ソース（Object Table）に対し総数/成功/失敗を集計する。AI.GENERATE_EMBEDDING を再実行するため
--   Vertex 呼出コストが発生する。バッチ直後の失敗有無確認に用いる。必要時のみコメントを外す。
-- SELECT
--   COUNT(*)              AS total,
--   COUNTIF(status = '')  AS succeeded,
--   COUNTIF(status <> '') AS failed
-- FROM AI.GENERATE_EMBEDDING(
--        MODEL `${DATASET_ID}.gemini_embedding_model`,
--        TABLE `${DATASET_ID}.image_object_table`,
--        STRUCT(3072 AS output_dimensionality))
-- WHERE content_type LIKE 'image/%';
