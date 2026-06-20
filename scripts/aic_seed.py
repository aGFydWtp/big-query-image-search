"""AIC シード投入スクリプト（使い捨て）。

Art Institute of Chicago (AIC) の Public Domain 画像を取得し、本プロジェクトの
画像バケット（GCS）へアップロードする。アップロード後は既存の取込 SQL
（sql/object_table.sql -> sql/generate_embeddings.sql）を回せば埋め込みが生成され、
テキスト->画像検索の探索対象になる。

参考実装 https://github.com/aGFydWtp/image-search の services/ingestion/aic_seeder.py を
移植し、保存先のみ Firebase Storage -> GCS に差し替えたもの。選定基準は参考と同一。

前提:
  - `gcloud auth application-default login` 済み（ADC で GCS へ書き込む）。
  - 依存: pip install httpx google-cloud-storage

使い方:
  IMAGE_BUCKET="$(terraform -chdir=terraform output -raw image_bucket_name)" \
  SEED_LIMIT=400 SEED_RANDOM_SEED=42 \
  python scripts/aic_seed.py

環境変数ノブ:
  IMAGE_BUCKET           投入先 GCS バケット名（必須。gs:// は付けない）
  SEED_LIMIT             投入上限枚数（既定 400）
  AIC_SEED_PREFIX        投入先 prefix（既定 aic-seed/）
  AIC_QUERIES            検索クエリ（カンマ区切り。未指定で多様な既定セット）
  AIC_MIN_SATURATION     color.s の下限 0-100（既定 50。カラフル度）
  AIC_MIN_DATE           date_start の下限（既定 1860）
  AIC_PAGES              クエリあたり取得ページ数（既定 1、各 100 件）
  AIC_FILTER_PAINTINGS   絵画系 classification のみに絞る（既定 true）
  SEED_RANDOM_SEED       指定で再現可能、未指定なら毎回ランダム
"""

from __future__ import annotations

import logging
import math
import os
import random
import sys
import time
from typing import Any

import httpx
from google.cloud import storage

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(message)s",
)
logger = logging.getLogger("aic_seed")

_AIC_SEARCH_URL = "https://api.artic.edu/api/v1/artworks/search"
_TIMEOUT_SECONDS = 60.0
_POLITE_SLEEP = 0.2
_PAGE_LIMIT = 100  # AIC search の 1 ページ最大

# AIC は AIC-User-Agent を推奨。通常の UA も付ける。
_HEADERS = {
    "User-Agent": (
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
        "AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
    ),
    "AIC-User-Agent": "big-query-image-search-seeder/1.0 (local seeding)",
}

_RETRYABLE_STATUS = {403, 429, 500, 502, 503, 504}
_MAX_RETRIES = 4
_RETRY_BASE_SLEEP = 2.0
_RETRY_CAP_SECONDS = 120.0  # バックオフ上限（CDN ブロックを待ち抜く）

_SEARCH_FIELDS = [
    "id",
    "title",
    "artist_display",
    "date_start",
    "date_end",
    "classification_title",
    "color",
    "image_id",
    "is_public_domain",
]

# 絵画系 classification 判定に使う部分文字列（小文字）
_PAINTING_KEYWORDS = ("painting", "oil", "watercolor", "gouache", "tempera", "drawing")

# カラフル/近代に寄せた多様な主題クエリ（参考実装と同一）
_DEFAULT_QUERIES = [
    "landscape", "portrait", "flowers", "still life", "woman", "city",
    "garden", "dancer", "music", "cafe", "boats", "river", "sea", "bridge",
    "park", "children", "harbor", "sunset", "mountains", "fruit", "interior",
    "horse", "street", "abstract",
]

_CANDIDATE_OVERSAMPLE = 4


class AICThrottledError(Exception):
    """AIC API のスロットリングが解消せず、リトライ上限に達した。"""


class AICClient:
    """Art Institute of Chicago API クライアント。"""

    def __init__(
        self,
        sleep: float = _POLITE_SLEEP,
        max_retries: int = _MAX_RETRIES,
        base_sleep: float = _RETRY_BASE_SLEEP,
    ) -> None:
        self._http = httpx.Client(timeout=_TIMEOUT_SECONDS, headers=_HEADERS)
        self._iiif_url = "https://www.artic.edu/iiif/2"  # 応答 config で上書き
        self._sleep = sleep
        self._max_retries = max_retries
        self._base_sleep = base_sleep

    def close(self) -> None:
        self._http.close()

    def __enter__(self) -> "AICClient":
        return self

    def __exit__(self, *args: object) -> None:
        self.close()

    @property
    def iiif_url(self) -> str:
        return self._iiif_url

    def _request(self, method: str, url: str, **kwargs: Any) -> httpx.Response:
        """403/429/5xx を指数バックオフでリトライする。"""
        for attempt in range(self._max_retries + 1):
            resp = self._http.request(method, url, **kwargs)
            if resp.status_code in _RETRYABLE_STATUS:
                if attempt == self._max_retries:
                    raise AICThrottledError(
                        f"HTTP {resp.status_code} after {attempt} retries: {url}"
                    )
                retry_after = resp.headers.get("Retry-After", "")
                delay = (
                    float(retry_after)
                    if retry_after.isdigit()
                    else min(self._base_sleep * (2**attempt), _RETRY_CAP_SECONDS)
                )
                logger.warning(
                    "AIC throttled (HTTP %d), backoff %.1fs (attempt %d/%d)",
                    resp.status_code, delay, attempt + 1, self._max_retries,
                )
                time.sleep(delay)
                continue
            resp.raise_for_status()
            return resp
        raise AICThrottledError(f"exhausted retries: {url}")  # 到達しない保険

    def search(self, query: str, min_date: int, pages: int = 1) -> list[dict[str, Any]]:
        """Public Domain・画像あり・指定年以降の作品を検索する（最大 pages ページ）。"""
        out: list[dict[str, Any]] = []
        for page in range(1, pages + 1):
            body = {
                "q": query,
                "query": {
                    "bool": {
                        "must": [
                            {"term": {"is_public_domain": True}},
                            {"exists": {"field": "image_id"}},
                            {"range": {"date_start": {"gte": min_date}}},
                        ]
                    }
                },
                "fields": _SEARCH_FIELDS,
                "limit": _PAGE_LIMIT,
                "page": page,
            }
            resp = self._request("POST", _AIC_SEARCH_URL, json=body)
            data = resp.json()
            iiif = (data.get("config") or {}).get("iiif_url")
            if iiif:
                self._iiif_url = iiif
            recs = data.get("data") or []
            if not recs:
                break
            out.extend(recs)
            total_pages = (data.get("pagination") or {}).get("total_pages")
            if total_pages and page >= total_pages:
                break
            time.sleep(self._sleep)
        return out

    def image_url(self, image_id: str) -> str:
        """IIIF 画像 URL を生成する（長辺 843px）。"""
        return f"{self._iiif_url}/{image_id}/full/843,/0/default.jpg"

    def download_image(self, url: str) -> bytes:
        resp = self._request("GET", url)
        return resp.content


class GCSSink:
    """GCS バケットへの画像アップロード（保存先。参考実装の Firebase に相当）。"""

    def __init__(self, bucket_name: str) -> None:
        self._client = storage.Client()
        self._bucket = self._client.bucket(bucket_name)

    def blob_exists(self, blob_path: str) -> bool:
        return self._bucket.blob(blob_path).exists()

    def upload_image(self, blob_path: str, content: bytes, content_type: str) -> None:
        self._bucket.blob(blob_path).upload_from_string(content, content_type=content_type)


class AICSeeder:
    """AIC API -> GCS への画像シード処理。"""

    def __init__(self) -> None:
        bucket = os.environ.get("IMAGE_BUCKET", "").strip()
        if not bucket:
            raise SystemExit(
                "IMAGE_BUCKET が未設定です。"
                ' IMAGE_BUCKET="$(terraform -chdir=terraform output -raw image_bucket_name)" を指定してください。'
            )
        self._limit = int(os.environ.get("SEED_LIMIT", "400"))
        self._prefix = os.environ.get("AIC_SEED_PREFIX", "aic-seed/")
        queries_env = os.environ.get("AIC_QUERIES", "")
        self._queries = (
            [q.strip() for q in queries_env.split(",") if q.strip()]
            if queries_env
            else list(_DEFAULT_QUERIES)
        )
        self._min_saturation = int(os.environ.get("AIC_MIN_SATURATION", "50"))
        self._min_date = int(os.environ.get("AIC_MIN_DATE", "1860"))
        self._pages = max(1, int(os.environ.get("AIC_PAGES", "1")))
        self._sleep = float(os.environ.get("AIC_SLEEP", "0.2"))
        self._filter_paintings = (
            os.environ.get("AIC_FILTER_PAINTINGS", "true").lower() == "true"
        )
        seed_env = os.environ.get("SEED_RANDOM_SEED", "")
        self._rng = random.Random(int(seed_env)) if seed_env.strip() else random.Random()

        self._sink = GCSSink(bucket)
        self._bucket_name = bucket

    def _eligible(self, rec: dict[str, Any]) -> bool:
        """カラフル & 絵画系 & 画像ありの作品か判定する。"""
        if not rec.get("is_public_domain") or not rec.get("image_id"):
            return False
        color = rec.get("color") or {}
        saturation = color.get("s")
        if saturation is None or saturation < self._min_saturation:
            return False
        if self._filter_paintings:
            classification = (rec.get("classification_title") or "").strip().lower()
            if not any(k in classification for k in _PAINTING_KEYWORDS):
                return False
        return True

    def execute(self) -> dict[str, Any]:
        """シード処理を実行し、サマリーを返す。"""
        seen_ids: set[int] = set()
        records: dict[int, dict[str, Any]] = {}
        uploaded = 0
        skipped_existing = 0
        errors = 0

        logger.info(
            "AIC seed start: bucket=%s limit=%d prefix=%s min_sat=%d min_date=%d",
            self._bucket_name, self._limit, self._prefix,
            self._min_saturation, self._min_date,
        )

        with AICClient(sleep=self._sleep) as aic:
            # --- フェーズ1: クエリごとに検索 -> 適格レコードをシャッフルしてサンプリング ---
            per_query_cap = max(
                1, math.ceil(self._limit * _CANDIDATE_OVERSAMPLE / len(self._queries))
            )
            candidate_ids: list[int] = []
            for query in self._queries:
                try:
                    results = aic.search(query, self._min_date, pages=self._pages)
                except (AICThrottledError, httpx.HTTPError) as e:
                    logger.warning("AIC search failed for %s: %s", query, e)
                    continue
                eligible = [r for r in results if self._eligible(r)]
                self._rng.shuffle(eligible)
                sampled = eligible[:per_query_cap]
                for r in sampled:
                    records.setdefault(r["id"], r)
                    candidate_ids.append(r["id"])
                logger.info(
                    "AIC search: query=%s results=%d eligible=%d sampled=%d",
                    query, len(results), len(eligible), len(sampled),
                )

            candidate_ids = list(dict.fromkeys(candidate_ids))
            self._rng.shuffle(candidate_ids)

            # --- フェーズ2: ダウンロード & アップロード ---
            for aic_id in candidate_ids:
                if uploaded >= self._limit:
                    break
                if aic_id in seen_ids:
                    continue
                seen_ids.add(aic_id)
                rec = records[aic_id]
                blob_path = f"{self._prefix}aic-{aic_id}.jpg"

                if self._sink.blob_exists(blob_path):
                    skipped_existing += 1
                    logger.info("Skip existing: %s", blob_path)
                    continue

                time.sleep(self._sleep)
                url = aic.image_url(rec["image_id"])
                try:
                    content = aic.download_image(url)
                    self._sink.upload_image(blob_path, content, "image/jpeg")
                except Exception as e:  # noqa: BLE001 - 1 件失敗で全体を止めない
                    errors += 1
                    logger.warning("Download/upload failed for %d: %s", aic_id, e)
                    continue

                uploaded += 1
                logger.info(
                    "Uploaded [%d/%d]: %s (sat=%s)",
                    uploaded, self._limit, blob_path, (rec.get("color") or {}).get("s"),
                )

            if uploaded < self._limit:
                logger.warning(
                    "Candidate pool exhausted before limit: uploaded=%d < limit=%d "
                    "(AIC_QUERIES を増やすか AIC_MIN_SATURATION を下げる / AIC_PAGES を上げる)",
                    uploaded, self._limit,
                )

        summary = {
            "uploaded": uploaded,
            "skipped_existing": skipped_existing,
            "errors": errors,
            "unique_candidates": len(candidate_ids),
        }
        logger.info("AIC seed finish: %s", summary)
        return summary


def main() -> None:
    seeder = AICSeeder()
    summary = seeder.execute()
    logger.info("AIC seed summary: %s", summary)
    # 1 枚も上がらなかった場合は失敗扱い（手動確認向け）
    if summary["uploaded"] == 0:
        sys.exit(1)


if __name__ == "__main__":
    main()
