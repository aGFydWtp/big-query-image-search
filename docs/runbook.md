# image-ingestion-pipeline 運用 Runbook

GCS 画像 → BigQuery 埋め込み → VECTOR INDEX を、純 SQL 資産で再現可能に構築・再実行するための手順。
常駐サービスは持たず、`sql/` 配下の SQL を所定順に実行する。

## パラメータ規約（ハードコード禁止 / 6.3）

全 SQL は環境依存値を `${VAR}` 形式で参照し、ハードコードしない。使用変数:

| 変数 | 由来（上流 gcp-infrastructure の `terraform output`） | 例 |
|------|------------------------------------------------------|----|
| `${PROJECT_ID}`   | `bigquery_dataset_id` の `.` 左側 | `my-project` |
| `${DATASET_ID}`   | `bigquery_dataset_id`（`project.dataset`） | `my-project.image_search` |
| `${CONNECTION_ID}`| `bigquery_connection_id`（完全修飾パス） | `projects/123456789/locations/us-central1/connections/image-search-biglake` |
| `${REGION}`       | `CONNECTION_ID` の `locations/` セグメント | `us-central1` |
| `${BUCKET_URI}`   | `gs://` + `image_bucket_name` | `gs://my-project-imgsearch-images` |

導出例:

```bash
terraform -chdir=terraform output -raw bigquery_dataset_id      # => my-project.image_search
terraform -chdir=terraform output -raw bigquery_connection_id   # => projects/123456789/locations/us-central1/connections/image-search-biglake
terraform -chdir=terraform output -raw image_bucket_name        # => my-project-imgsearch-images
```

`${CONNECTION_ID}` の `region` セグメントと `${DATASET_ID}` の実ロケーションが一致することを必ず確認する
（dataset・接続・モデル・テーブルを単一 `${REGION}` に統一する基準 / 1.3, 2.3, 3.3）。

### パラメータの注入

1. テンプレートをコピーして実値を記入（`sql/params.env` は `.gitignore` 済み）:

   ```bash
   cp sql/params.example sql/params.env
   # sql/params.env を編集して terraform output 由来の実値を設定
   ```

2. 環境変数として読み込み、`envsubst` で `${VAR}` を展開して `bq` に渡す:

   ```bash
   set -a && . sql/params.env && set +a
   envsubst < sql/object_table.sql | bq query --use_legacy_sql=false --project_id="$PROJECT_ID"
   ```

   `envsubst` が無い環境では `sed` で置換してもよい。

## 実行順序（6.2）

依存順に実行する。各 SQL は冪等であり、再実行で既存データを破壊しない（6.4）。

| # | SQL | 役割 | 冪等性 | 依存 |
|---|-----|------|--------|------|
| 1 | `sql/object_table.sql`     | Object Table 作成 | `CREATE OR REPLACE` | 上流出力 |
| 2 | `sql/remote_model.sql`     | リモートモデル作成 | `CREATE OR REPLACE MODEL` | 上流出力 |
| 3 | `sql/embeddings_table.sql` | embeddings テーブル作成 | `CREATE TABLE IF NOT EXISTS` | 上流出力 |
| 4 | `sql/generate_embeddings.sql` | 差分埋め込み生成・投入 | `MERGE ON image_uri` | 1, 2, 3 |
| 5 | `sql/vector_index.sql`     | VECTOR INDEX 作成 | `CREATE VECTOR INDEX IF NOT EXISTS` | 3, 4 |
| 6 | `sql/validate.sql`         | 取込結果の検証 | 読み取りのみ | 4, 5 |

> 1 と 2 は相互独立で順不同。4（生成バッチ）で初めて合流する。

一括実行の例:

```bash
set -a && . sql/params.env && set +a
for f in object_table remote_model embeddings_table generate_embeddings vector_index; do
  echo ">>> running sql/$f.sql"
  envsubst < "sql/$f.sql" | bq query --use_legacy_sql=false --project_id="$PROJECT_ID" || { echo "FAILED: $f"; break; }
done
```

## バッチ分割と継続追加（4.5, 4.6）

- **継続追加**: `sql/generate_embeddings.sql` は `MERGE ON image_uri` の `WHEN NOT MATCHED` のみで未処理 URI だけを投入するため、画像追加後に再実行すれば差分のみ取り込む（重複行を生まない / 4.3）。
- **URI レンジ分割**: 大量画像でクォータ・コストを平準化する場合、`generate_embeddings.sql` の `src` 副問い合わせ `WHERE` に URI レンジ絞り込みを追加して部分投入する。例:

  ```sql
  AND src.uri BETWEEN '${BUCKET_URI}/a' AND '${BUCKET_URI}/n'
  ```

  レンジを変えて複数回実行することで全件を分割投入できる。各回は冪等。
- **スケジュール実行**: `generate_embeddings.sql` をスケジュールドクエリに登録すれば定期取込になる。手動 `bq query` でも同一 SQL を実行できる（4.6）。
- **コスト**: `AI.GENERATE_EMBEDDING` は `WHERE` 通過後の Object Table 行に対し Vertex 呼出を行う。大量時はレンジ分割でクォータ超過・高コストを緩和する。

## 検証（6.5）

```bash
set -a && . sql/params.env && set +a
envsubst < sql/validate.sql | bq query --use_legacy_sql=false --project_id="$PROJECT_ID"
```

判定基準:

- (1) `row_count` > 0（埋め込みが投入されている）
- (2) `null_embedding = 0` かつ `bad_dim = 0`（全行 3072 次元・非 NULL）
- (3) `dup_uris = 0`（`image_uri` 一意・MERGE 冪等）
- (4) `INFORMATION_SCHEMA.VECTOR_INDEXES` に COSINE 索引が登録。`coverage_percentage` は十分なデータ投入後に増加。
  - base table 約 10MB 未満の間は `disable_reason = BASE_TABLE_TOO_SMALL`（想定動作 / ブルートフォースにフォールバック, 5.3）。
- (5) 失敗行の可視化（任意・コスト発生）は `validate.sql` 末尾のコメントを参照。

## 技術前提（Task 0 / 2026-06-19 実機＋公式ドキュメント確定）

- **関数**: `AI.GENERATE_EMBEDDING(MODEL, TABLE <object_table>, STRUCT(3072 AS output_dimensionality))`。Object Table（画像）入力に正式対応。
- **モデル**: `ENDPOINT='gemini-embedding-2-preview'`（**Preview ステージ**）。素の `gemini-embedding-2` は実在しない。`us-central1` 提供は実機確認済み。
  - GA 安定を優先する場合の代替は `multimodalembedding@001`（最大 1408 次元）。採用時は共有契約の次元 3072→1408 変更が必要（現採用: 案A = 3072 維持）。
- **出力列**: 埋め込み `embedding`（`ARRAY<FLOAT64>`）/ ステータス `status`（空文字＝成功）。失敗行は `status=''` フィルタで除外。
- **次元**: 3072（下流 `image-search-api` と共有）。距離タイプ: COSINE。
- **共有契約の所有**: テーブル `image_embeddings`、列 `image_uri`/`embedding`(3072)/`content_type`/`generated_at`、モデルオブジェクト名 `gemini_embedding_model`、距離タイプ COSINE。下流はこれらを再定義せず参照する。

> **未実施・運用者確認事項**: 本リポジトリ確定時点で取込バケットに画像が無いため、実画像での
> `generate_embeddings.sql` 通し実行（実次元 3072・`status` 挙動の実データ確認）は未実施。
> 画像投入後、上記「実行順序」を一巡し `validate.sql` で全項目を確認すること。
