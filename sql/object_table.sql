-- object_table.sql — Object Table（GCS 画像の論理ビュー）作成
-- Requirements: 1.1-1.5   Boundary: ObjectTable
--
-- 冪等性(1.4): CREATE OR REPLACE EXTERNAL TABLE。再実行で後続の埋め込み生成へ影響を与えない。
-- ロケーション(1.3): dataset・接続・バケットは単一 ${REGION} で整合させること（params.example 参照）。
-- 前提: ${CONNECTION_ID} は cloud_resource 型 BigLake 接続。接続 SA に Storage Object Viewer 付与済み（上流 gcp-infrastructure）。
-- 注入する環境依存値: ${DATASET_ID} ${CONNECTION_ID} ${BUCKET_URI}

CREATE OR REPLACE EXTERNAL TABLE `${DATASET_ID}.image_object_table`
WITH CONNECTION `${CONNECTION_ID}`
OPTIONS (
  -- GCS オブジェクトのメタデータ（uri, content_type, size 等）を列として参照可能にする(1.2)
  object_metadata = 'SIMPLE',
  -- 画像バケットのプレフィックスワイルドカードを入力とする(1.1)
  uris = ['${BUCKET_URI}/*']
);

-- 絞り込み条件の明示(1.5):
--   Object Table は content_type 列を持つため、後続 generate_embeddings.sql で
--   非画像オブジェクトを除外できる。SQL 上で適用する絞り込み例:
--     WHERE content_type LIKE 'image/%'
--   拡張子で絞り込む場合の例:
--     WHERE REGEXP_CONTAINS(LOWER(uri), r'\.(jpg|jpeg|png|gif|bmp|webp)$')
--
-- 検証(1.1): オブジェクト件数を取得できること。
--   SELECT COUNT(*) AS object_count FROM `${DATASET_ID}.image_object_table`;
