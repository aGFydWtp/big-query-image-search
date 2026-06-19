-- generate_embeddings.sql — 差分埋め込み生成・投入（AI.GENERATE_EMBEDDING + MERGE）
-- Requirements: 4.1-4.6   Boundary: EmbeddingGenerationBatch   Depends: 2.1, 2.2, 3.1
--
-- 3 入力の統合点: Object Table（画像）/ リモートモデル / embeddings テーブル。
-- 関数(4.1): AI.GENERATE_EMBEDDING(MODEL, TABLE <object_table>, STRUCT(3072 AS output_dimensionality))。
--   Object Table 入力では content 列は不要。出力列は embedding(ARRAY<FLOAT64>) と status(STRING)。
--   （Task 0 確定: status='' が成功。ml_generate_embedding_status は旧 ML.* 用で使用しない）
-- マッピング(4.2): 出力 embedding → image_embeddings.embedding、Object Table の uri → image_uri。
-- 冪等・差分(4.3): MERGE ON image_uri の WHEN NOT MATCHED のみ。既存 URI は再投入せず重複を生まない。
--   失敗した URI は未投入のため NOT MATCHED として再実行時に再取込される。
-- 失敗行除外(4.4): status='' AND embedding IS NOT NULL AND ARRAY_LENGTH(embedding)=3072 のみ投入。
-- バッチ分割(4.5): 下記 source の WHERE に URI レンジ等の絞り込みを追加し部分投入する（runbook 参照）。
-- 運用(4.6): スケジュールドクエリ／手動 bq query の双方で実行可能（runbook 参照）。
--
-- コスト注記: AI.GENERATE_EMBEDDING は WHERE で残った Object Table 行に対し Vertex 呼出を行う。
--   大量画像・継続追加では URI レンジ(4.5)で分割し、クォータ・コストを平準化する。
-- 注入する環境依存値: ${DATASET_ID}（バッチ分割時は ${BUCKET_URI} も）

MERGE `${DATASET_ID}.image_embeddings` AS tgt
USING (
  SELECT
    src.uri          AS image_uri,
    src.embedding    AS embedding,
    src.content_type AS content_type
  FROM AI.GENERATE_EMBEDDING(
         MODEL `${DATASET_ID}.gemini_embedding_model`,
         TABLE `${DATASET_ID}.image_object_table`,
         STRUCT(3072 AS output_dimensionality)
       ) AS src
  WHERE
    -- 失敗・空・次元不正行を除外し成功行のみ投入(4.4)
    src.status = ''
    AND src.embedding IS NOT NULL
    AND ARRAY_LENGTH(src.embedding) = 3072
    -- 非画像オブジェクトを除外(1.5)
    AND src.content_type LIKE 'image/%'
    -- ▼ バッチ分割(4.5): 必要に応じ URI レンジ等で絞り込む。例:
    --   AND src.uri BETWEEN '${BUCKET_URI}/a' AND '${BUCKET_URI}/n'
) AS src
ON tgt.image_uri = src.image_uri
WHEN NOT MATCHED THEN
  INSERT (image_uri, embedding, content_type, generated_at)
  VALUES (src.image_uri, src.embedding, src.content_type, CURRENT_TIMESTAMP());

-- 検証(4.3): 同一入力での再実行で行数が増えないこと（MERGE 冪等）。validate.sql 参照。
