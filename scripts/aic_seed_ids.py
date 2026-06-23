"""AIC シード投入スクリプト（ID 指定版・使い捨て）。

eval/aic_corpus_ids.json に列挙した AIC 作品 ID（参照リポジトリ
aGFydWtp/image-search の評価コーパス 380 件と同一集合）を、AIC API から
image_id を引いて 843px 画像を取得し、本プロジェクトの画像バケット（GCS）へ
アップロードする。スコア比較を「同一データセット」で行うためのもの。

scripts/aic_seed.py（ランダムサンプリング版）と異なり、投入対象を ID で固定する。
画像取得・保存仕様（IIIF 843px / blob パス aic-seed/aic-<id>.jpg）は揃えてある。

前提:
  - `gcloud auth application-default login` 済み（ADC で GCS へ書き込む）。
  - 依存: pip install httpx google-cloud-storage

使い方:
  IMAGE_BUCKET="$(terraform -chdir=terraform output -raw image_bucket_name)" \
  python scripts/aic_seed_ids.py

環境変数ノブ:
  IMAGE_BUCKET     投入先 GCS バケット名（必須。gs:// は付けない）
  AIC_SEED_PREFIX  投入先 prefix（既定 aic-seed/）
  IDS_FILE         ID リスト JSON（既定 eval/aic_corpus_ids.json）
"""

from __future__ import annotations

import json
import logging
import os
import sys
import time
from pathlib import Path
from typing import Any

import httpx
from google.cloud import storage

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("aic_seed_ids")

_AIC_ARTWORKS_URL = "https://api.artic.edu/api/v1/artworks"
_TIMEOUT_SECONDS = 60.0
_POLITE_SLEEP = 0.2
_BULK_BATCH = 100  # /artworks?ids= の 1 回上限
_DEFAULT_IIIF = "https://www.artic.edu/iiif/2"

_HEADERS = {
    "User-Agent": (
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
        "AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
    ),
    "AIC-User-Agent": "big-query-image-search-seeder/1.0 (eval corpus seeding)",
}

_RETRYABLE_STATUS = {403, 429, 500, 502, 503, 504}
_MAX_RETRIES = 4
_RETRY_BASE_SLEEP = 2.0
_RETRY_CAP_SECONDS = 120.0

_REPO_ROOT = Path(__file__).resolve().parent.parent


def _request(http: httpx.Client, method: str, url: str, **kwargs: Any) -> httpx.Response:
    """403/429/5xx を指数バックオフでリトライする。"""
    for attempt in range(_MAX_RETRIES + 1):
        resp = http.request(method, url, **kwargs)
        if resp.status_code in _RETRYABLE_STATUS:
            if attempt == _MAX_RETRIES:
                raise RuntimeError(f"HTTP {resp.status_code} after {attempt} retries: {url}")
            retry_after = resp.headers.get("Retry-After", "")
            delay = (
                float(retry_after)
                if retry_after.isdigit()
                else min(_RETRY_BASE_SLEEP * (2**attempt), _RETRY_CAP_SECONDS)
            )
            logger.warning(
                "AIC throttled (HTTP %d), backoff %.1fs (attempt %d/%d)",
                resp.status_code, delay, attempt + 1, _MAX_RETRIES,
            )
            time.sleep(delay)
            continue
        resp.raise_for_status()
        return resp
    raise RuntimeError(f"exhausted retries: {url}")


def _numeric_id(aic_id: str) -> int:
    return int(aic_id.split("-", 1)[1])


def fetch_image_ids(http: httpx.Client, numeric_ids: list[int]) -> tuple[dict[int, str], str]:
    """artwork id -> image_id のマップと IIIF ベース URL を返す。"""
    out: dict[int, str] = {}
    iiif = _DEFAULT_IIIF
    for i in range(0, len(numeric_ids), _BULK_BATCH):
        chunk = numeric_ids[i : i + _BULK_BATCH]
        params = {
            "ids": ",".join(str(x) for x in chunk),
            "fields": "id,image_id,is_public_domain",
            "limit": str(len(chunk)),
        }
        resp = _request(http, "GET", _AIC_ARTWORKS_URL, params=params)
        data = resp.json()
        cfg_iiif = (data.get("config") or {}).get("iiif_url")
        if cfg_iiif:
            iiif = cfg_iiif
        for rec in data.get("data") or []:
            img = rec.get("image_id")
            if img:
                out[rec["id"]] = img
            else:
                logger.warning("no image_id for artwork %s", rec.get("id"))
        time.sleep(_POLITE_SLEEP)
    return out, iiif


def main() -> None:
    bucket_name = os.environ.get("IMAGE_BUCKET", "").strip()
    if not bucket_name:
        raise SystemExit(
            "IMAGE_BUCKET が未設定です。"
            ' IMAGE_BUCKET="$(terraform -chdir=terraform output -raw image_bucket_name)" を指定してください。'
        )
    prefix = os.environ.get("AIC_SEED_PREFIX", "aic-seed/")
    ids_file = Path(os.environ.get("IDS_FILE", _REPO_ROOT / "eval" / "aic_corpus_ids.json"))

    aic_ids: list[str] = json.loads(ids_file.read_text(encoding="utf-8"))["ids"]
    numeric_ids = [_numeric_id(x) for x in aic_ids]
    logger.info("seed targets: %d ids from %s", len(aic_ids), ids_file)

    bucket = storage.Client().bucket(bucket_name)

    uploaded = skipped = errors = missing = 0
    with httpx.Client(timeout=_TIMEOUT_SECONDS, headers=_HEADERS) as http:
        id_to_image, iiif = fetch_image_ids(http, numeric_ids)
        logger.info("resolved image_id for %d/%d artworks", len(id_to_image), len(numeric_ids))

        for aic_id, num in zip(aic_ids, numeric_ids):
            blob_path = f"{prefix}{aic_id}.jpg"
            if bucket.blob(blob_path).exists():
                skipped += 1
                continue
            image_id = id_to_image.get(num)
            if not image_id:
                missing += 1
                logger.warning("skip %s: image_id 未解決", aic_id)
                continue
            url = f"{iiif}/{image_id}/full/843,/0/default.jpg"
            try:
                time.sleep(_POLITE_SLEEP)
                content = _request(http, "GET", url).content
                bucket.blob(blob_path).upload_from_string(content, content_type="image/jpeg")
            except Exception as e:  # noqa: BLE001 - 1 件失敗で全体を止めない
                errors += 1
                logger.warning("download/upload failed for %s: %s", aic_id, e)
                continue
            uploaded += 1
            logger.info("uploaded [%d]: %s", uploaded, blob_path)

    summary = {
        "targets": len(aic_ids),
        "uploaded": uploaded,
        "skipped_existing": skipped,
        "missing_image_id": missing,
        "errors": errors,
    }
    logger.info("AIC seed-by-ids finish: %s", summary)
    if uploaded + skipped == 0:
        sys.exit(1)


if __name__ == "__main__":
    main()
