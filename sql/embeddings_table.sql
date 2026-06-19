-- embeddings_table.sql — embeddings テーブル（共有契約スキーマ）作成
-- Requirements: 3.1-3.5   Boundary: EmbeddingsTable
--
-- 共有契約 source of truth(3.4): 列名・型・次元 3072・距離タイプ COSINE は
--   下流 image-search-api との唯一の共有契約。変更は下流再検証 Trigger。
-- 冪等性(3.5): CREATE TABLE IF NOT EXISTS。既存の埋め込みデータを破壊しない。
--   明示的に再構築する場合のみ、別手順として DROP TABLE 後に本 DDL を実行する（区別して運用）。
-- 一意キー(3.1): image_uri を論理一意キー（MERGE キー）とする。
-- 次元・非 NULL(3.2): embedding は全行 3072 次元・非 NULL を generate_embeddings/validate で保証する。
--   （注: BigQuery の ARRAY 列は NOT NULL 制約を付与できないため、列制約ではなく投入条件で担保する）
-- 注入する環境依存値: ${DATASET_ID}

CREATE TABLE IF NOT EXISTS `${DATASET_ID}.image_embeddings` (
  image_uri    STRING NOT NULL OPTIONS(description = '画像の GCS URI。論理一意キー（MERGE キー）'),
  embedding    ARRAY<FLOAT64>  OPTIONS(description = '埋め込みベクトル。次元=3072、全行同一次元・非 NULL（共有契約）'),
  content_type STRING          OPTIONS(description = 'MIME タイプ'),
  generated_at TIMESTAMP       OPTIONS(description = '埋め込み生成時刻')
)
OPTIONS (
  description = 'image-ingestion-pipeline 共有契約テーブル。embedding 次元=3072 / 距離タイプ=COSINE。下流 image-search-api が消費。'
);

-- 検証(3.1, 3.2): スキーマが契約どおりであること。
--   bq show --schema --format=prettyjson "${DATASET_ID}.image_embeddings"
