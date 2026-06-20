# scripts/ — 使い捨てユーティリティ

製品コード（Go）とは別の、一回限りの運用補助スクリプト置き場。

## aic_seed.py — AIC 画像シード投入

Art Institute of Chicago (AIC) の Public Domain 画像を取得し、画像バケット（GCS）へ
アップロードする。検索の動作確認・デモ用のシードデータを用意するためのもの。
参考実装 https://github.com/aGFydWtp/image-search の `aic_seeder.py` を移植し、
保存先のみ Firebase Storage -> GCS に差し替えたもの（選定基準は同一）。

### 手順

```bash
# 1. 依存をインストール（venv 推奨）
python -m venv .venv && source .venv/bin/activate
pip install -r scripts/requirements.txt

# 2. GCS 書き込み用に ADC を用意
gcloud auth application-default login

# 3. シード投入（約 400 枚。SEED_RANDOM_SEED 固定で再現可能）
IMAGE_BUCKET="$(terraform -chdir=terraform output -raw image_bucket_name)" \
SEED_LIMIT=400 SEED_RANDOM_SEED=42 \
python scripts/aic_seed.py
```

画像は `gs://<image_bucket>/aic-seed/aic-<id>.jpg`（content_type `image/jpeg`）に置かれる。
Object Table の `uris=['<bucket>/*']` は `/` を跨いでマッチするため SQL 変更は不要。

### 投入後の埋め込み生成

`docs/runbook.md` の取込手順に従い、既存 SQL を実行する（新規 SQL は不要）:

```bash
set -a && . sql/params.env && set +a
envsubst < sql/object_table.sql       | bq query --use_legacy_sql=false --project_id="$PROJECT_ID"
envsubst < sql/generate_embeddings.sql | bq query --use_legacy_sql=false --project_id="$PROJECT_ID"
envsubst < sql/validate.sql           | bq query --use_legacy_sql=false --project_id="$PROJECT_ID"
```

> VECTOR INDEX は行数下限（約 5,000 行）未満のため 400 枚では作成されない。
> `VECTOR_SEARCH` はインデックス無しでも動作するので、シード段階の検索確認には支障ない。
