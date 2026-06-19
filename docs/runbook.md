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

---

## image-search-api: Task 2.2 dry-run ゲート（GO 前・手動再実行）

検索クエリ（クエリ埋め込み + `VECTOR_SEARCH` 単一クエリ）の本番 GO 前ゲート。`image-search-api` 設計の「残 dry-run 項目」(a)(b) を実機確認する。

**前提**: `image-ingestion-pipeline` のリモートモデル `gemini_embedding_model` と テーブル `image_embeddings` がデプロイ済みであること（未デプロイだと dry-run はテーブル/モデル未検出で失敗する）。

```bash
# 値は上流 terraform output / sql/params.env から注入（ハードコード禁止）
#   PROJECT_ID=image-search-6c457e  DATASET=image_search  REGION=us-central1
bq query --project_id="$PROJECT_ID" --use_legacy_sql=false --dry_run \
  --location="$REGION" \
  --parameter='query:STRING:cat' --parameter='top_k:INT64:10' <<SQL
WITH query_embedding AS (
  SELECT embedding
  FROM AI.GENERATE_EMBEDDING(
    MODEL \`${PROJECT_ID}.${DATASET}.gemini_embedding_model\`,
    (SELECT @query AS content),
    STRUCT(3072 AS output_dimensionality)
  )
  WHERE status = ''
)
SELECT base.image_uri AS image_uri, base.content_type AS content_type, distance
FROM VECTOR_SEARCH(
  TABLE \`${PROJECT_ID}.${DATASET}.image_embeddings\`,
  'embedding', TABLE query_embedding,
  query_column_to_search => 'embedding',
  top_k => @top_k, distance_type => 'COSINE'
)
ORDER BY distance ASC;
SQL
```

- **成功** (`Query successfully validated`): 単一クエリ（CTE 結合）形を確定採用（既定）。`SearchQueryBuilder` の既定テンプレートのまま本番可。
- **失敗** がチェーン/Preview モデル制約に起因する場合のみ: 設計の縮退案（2 ジョブ分割: 埋め込み生成 → `@query_embedding` を `VECTOR_SEARCH` に渡す）へ切替える。
- 2026-06-19 実装時点の実機結果: `(a) top_k => @top_k` 束縛は構文受理（肯定）。`(b)` は上流モデル/テーブル未デプロイのためテーブル未検出で停止 → 完全確認はデプロイ後に本手順で再実行（詳細は `research.md`「Task 2.2 dry-run ゲート実機結果」）。

## image-search-api: ビルド・デプロイ・ローカル起動/検証（5.6）

`image-search-api` は `cmd/server`（標準 `net/http`）の単一ステートレスサービス。エンドポイントは `POST /search` と `GET /healthz`。設定は全て環境変数から注入し、コードへハードコードしない。本番 GO 前には上記「Task 2.2 dry-run ゲート」を必ず通すこと（検索 SQL の単一クエリ形を実機確認する前提ゲート）。

### 必須環境変数

`deploy/.env.example` を単一の参照集合とする。全値は環境依存でありコードに焼き込まない（`internal/config` が起動時に必須値をフェイルファスト検証）。

| 変数 | 必須 | 由来 / 値 |
|------|------|-----------|
| `PROJECT_ID`       | 必須 | デプロイ先 GCP プロジェクト id |
| `REGION`           | 必須 | 固定単一リージョン `us-central1`（`gcp-infrastructure` 入力変数由来。region の terraform 出力は無い）。BigQuery ジョブロケーションに使用 |
| `DATASET_ID`       | 必須 | `gcp-infrastructure` output `bigquery_dataset_id`（`project.dataset` 形式） |
| `EMBEDDINGS_TABLE` | 必須 | `image-ingestion-pipeline` 共有契約のテーブル名 `image_embeddings` |
| `IMAGE_BUCKET`     | 必須 | `gcp-infrastructure` output `image_bucket_name`（署名 URL 対象バケット） |
| `RUN_SA_EMAIL`     | 必須 | `gcp-infrastructure` output `cloud_run_service_account_email`。署名 URL の `GoogleAccessID` も兼ねる |
| `MODEL`            | 任意 | リモートモデル**オブジェクト名** `gemini_embedding_model`（**エンドポイント名 `gemini-embedding-2-preview` ではない**）。未設定時はコード既定 `gemini_embedding_model` |
| `SIGNED_URL_EXPIRY`| 任意 | 署名 URL 有効期限（Go duration、例 `15m`）。未設定時は既定 `15m` |
| `PORT`             | 任意 | Cloud Run が実行時注入。ローカルは既定 `8080`。`deploy/service.yaml` には設定しない |

> 表記揺れ防止（G2）: `MODEL` は必ずオブジェクト名 `gemini_embedding_model` を注入する。ingestion 側で作成したモデルオブジェクトと同一でなければ検索 SQL の `MODEL` 参照が解決できない。

### ローカル起動 / 検証手順

1. テンプレートをコピーして実値を記入（`.env.local` はコミットしない）:

   ```bash
   cp deploy/.env.example deploy/.env.local
   # deploy/.env.local を編集し、terraform output 由来の実値を設定:
   #   PROJECT_ID / DATASET_ID / IMAGE_BUCKET / RUN_SA_EMAIL は上流 output から
   #   REGION=us-central1（固定） EMBEDDINGS_TABLE=image_embeddings MODEL=gemini_embedding_model
   ```

2. ビルドして環境変数を注入し起動する:

   ```bash
   go build ./cmd/server                         # コンパイル確認
   set -a && . deploy/.env.local && set +a        # 環境変数を読み込み
   PORT=8080 go run ./cmd/server                  # 8080 で待受
   ```

3. ヘルスチェック（ライブ BigQuery 不要・常に 200）:

   ```bash
   curl -i localhost:8080/healthz
   # => HTTP/1.1 200 OK
   ```

4. サンプル検索（成功時の 200 レスポンス形）:

   ```bash
   curl -s -X POST localhost:8080/search \
     -H 'Content-Type: application/json' \
     -d '{"query":"cat","top_k":5}'
   # 期待する 200 ボディ形:
   # {"results":[{"image_uri":"gs://.../001.jpg","score":0.87,"content_type":"image/jpeg"}]}
   # signed_url:true を付けた場合のみ各結果に "signed_url" が付加されうる（下記 IAM 前提参照）
   ```

5. 入力検証パス（ライブ BigQuery 不要・400 を返す）:

   ```bash
   curl -s -X POST localhost:8080/search -d '{"query":""}'
   # => {"error":{"code":"invalid_request","message":"..."}}
   ```

> **実データの前提**: `/healthz` と空クエリ等の 400 検証パスは上流リソース無しでも動作する（BigQuery を呼ばない）。実際の検索結果を得るには上流の リモートモデル `gemini_embedding_model` と テーブル `image_embeddings` がデプロイ済みである必要がある。未デプロイ時は BigQuery がエラーを返し、ハンドラはそれを 5xx（`{"error":{"code":"internal_error",...}}`）にマップする（内部詳細は非漏洩）。本番投入前は上記「Task 2.2 dry-run ゲート」で検索 SQL を実機確認すること。

### ビルド / デプロイ

ビルドコンテキストはリポジトリルート。イメージはマルチステージ（distroless static・非 root）で `./cmd/server` を静的ビルドする（`deploy/Dockerfile`）。環境固有値はイメージに焼き込まず、全て実行時に環境変数で注入する。

```bash
# 変数はシェル/上流 terraform output から注入（実値はハードコードしない）
REGION=us-central1
PROJECT_ID="$(terraform -chdir=terraform output -raw bigquery_dataset_id | cut -d. -f1)"  # 例
REPO=image-search                 # Artifact Registry リポジトリ名
TAG="$(git rev-parse --short HEAD)"
IMAGE_REF="${REGION}-docker.pkg.dev/${PROJECT_ID}/${REPO}/image-search-api:${TAG}"

# 1) ビルド（-f でルートをコンテキストに）
docker build -f deploy/Dockerfile -t "$IMAGE_REF" .

# 2) Artifact Registry へ push（事前に `gcloud auth configure-docker ${REGION}-docker.pkg.dev`）
docker push "$IMAGE_REF"
```

デプロイは `deploy/service.yaml`（Knative Service 定義・`*_PLACEHOLDER` のみ）のプレースホルダを実値へ置換して反映する。`serviceAccountName` には上流 provisioned の Run SA（`RUN_SA_EMAIL`）を割り当てる（最小権限）。

```bash
# 上流 output から実値を注入（コミット物にはハードコードしない）
RUN_SA_EMAIL="$(terraform -chdir=terraform output -raw cloud_run_service_account_email)"
DATASET_ID="$(terraform -chdir=terraform output -raw bigquery_dataset_id)"
IMAGE_BUCKET="$(terraform -chdir=terraform output -raw image_bucket_name)"

# プレースホルダ置換 → 反映（REGION は service.yaml 内で us-central1 固定）
export IMAGE_REF RUN_SA_EMAIL PROJECT_ID DATASET_ID IMAGE_BUCKET
sed -e "s#IMAGE_REF_PLACEHOLDER#${IMAGE_REF}#g" \
    -e "s#RUN_SA_EMAIL_PLACEHOLDER#${RUN_SA_EMAIL}#g" \
    -e "s#PROJECT_ID_PLACEHOLDER#${PROJECT_ID}#g" \
    -e "s#DATASET_ID_PLACEHOLDER#${DATASET_ID}#g" \
    -e "s#IMAGE_BUCKET_PLACEHOLDER#${IMAGE_BUCKET}#g" \
    deploy/service.yaml | gcloud run services replace /dev/stdin --region "$REGION"
```

> リージョン整合: BigQuery dataset / GCS バケット / リモートモデル / Cloud Run を単一の `us-central1` に統一する（`service.yaml` の `REGION` も `us-central1` 固定）。

### 署名 URL の上流 IAM 前提（部分失敗ハンドリング）

`signed_url:true` での V4 署名 URL 発行は keyless 署名（IAM `signBlob`）を用い、Run SA 自身への `roles/iam.serviceAccountTokenCreator`（リソース＝Run SA 自身）と images バケットスコープの `roles/storage.objectViewer` が前提となる。この IAM 追補は `gcp-infrastructure` 側の作業であり本仕様単独では閉じない依存ブロッカー（Requirement 3.2/3.3 の DoD は追補適用後の実機発行に紐付く）。

追補が未適用の環境では署名が失敗するが、部分失敗ハンドリング（Requirement 4.5）により当該結果は `signed_url` を省略するだけで、他結果（`image_uri`・`score`）は 200 で返る。したがって追補完了前でも検索コア機能（Requirement 1 / 3.1）はローカル起動・検証可能。
