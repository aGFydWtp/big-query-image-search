WITH query_inputs AS (
  SELECT 'raw' AS source, @query AS content
  UNION ALL
  SELECT 'rewrite' AS source, @query_en AS content
  FROM UNNEST([1])               -- @query_en が空なら rewrite 行を出さず raw 単一チャネルに縮約
  WHERE NULLIF(@query_en, '') IS NOT NULL
),
query_embeddings AS (
  SELECT source, embedding
  FROM AI.GENERATE_EMBEDDING(
    MODEL `${DATASET_ID}.${MODEL}`,
    TABLE query_inputs,
    STRUCT(3072 AS output_dimensionality)
  )
  WHERE status = ''            -- AI.GENERATE_EMBEDDING の成功行（空文字）のみ
),
vector_matches AS (
  SELECT
    query.source     AS source,
    base.image_uri   AS image_uri,
    base.content_type AS content_type,
    distance,
    ROW_NUMBER() OVER (PARTITION BY query.source ORDER BY distance ASC) AS rank
  FROM VECTOR_SEARCH(
    TABLE `${DATASET_ID}.${TABLE}`,
    'embedding',
    TABLE query_embeddings,
    query_column_to_search => 'embedding',
    top_k => @candidate_k,
    distance_type => 'COSINE'
  )
),
rrf AS (
  SELECT
    image_uri,
    ANY_VALUE(content_type) AS content_type,
    MIN(distance) AS distance,
    SUM(1.0 / (60 + rank)) AS rrf_score
  FROM vector_matches
  GROUP BY image_uri
)
SELECT
  image_uri,
  content_type,
  distance
FROM rrf
ORDER BY rrf_score DESC, distance ASC
LIMIT @top_k;
