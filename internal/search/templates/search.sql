WITH query_embedding AS (
  SELECT embedding
  FROM AI.GENERATE_EMBEDDING(
    MODEL `${DATASET_ID}.${MODEL}`,
    (SELECT @query AS content),
    STRUCT(3072 AS output_dimensionality)
  )
  WHERE status = ''            -- AI.GENERATE_EMBEDDING の成功行（空文字）のみ
)
SELECT
  base.image_uri    AS image_uri,
  base.content_type AS content_type,
  distance
FROM VECTOR_SEARCH(
  TABLE `${DATASET_ID}.${TABLE}`,
  'embedding',
  TABLE query_embedding,
  query_column_to_search => 'embedding',
  top_k => @top_k,
  distance_type => 'COSINE'
)
ORDER BY distance ASC;         -- COSINE distance は小さいほど類似
