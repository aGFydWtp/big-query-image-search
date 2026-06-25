#!/usr/bin/env python3
"""(a) Vertex Ranking 用に AIC 本番メタを取得・キャッシュする。

rerank フレーバー (a) は文書テキスト = title + artist_display + classification_title を
使うが、これらは image_embeddings テーブルには無い（aic_seed.py は画像を GCS に
上げるだけでメタを BQ に書いていない）。本スクリプトはコーパス 380 件の aic_id ごとに
AIC artworks API からメタを再取得し eval/aic_corpus_meta.json にキャッシュする。

AIC API は ids 一括取得をサポート（最大 100 件/リクエスト）:
  GET https://api.artic.edu/api/v1/artworks?ids=27,873,...&fields=id,title,...

実行（リポジトリルートから）:
  .venv/bin/python -m scripts.fetch_aic_meta
"""

from __future__ import annotations

import json
import time
import urllib.error
import urllib.request
from datetime import datetime
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
CORPUS_IDS = REPO_ROOT / "eval" / "aic_corpus_ids.json"
OUT = REPO_ROOT / "eval" / "aic_corpus_meta.json"

API = "https://api.artic.edu/api/v1/artworks"
FIELDS = "id,title,artist_display,classification_title"
BATCH = 100
HEADERS = {
    "User-Agent": (
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
        "AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
    ),
    "AIC-User-Agent": "big-query-image-search-rerank/1.0 (metadata fetch)",
}
_RETRYABLE = {403, 429, 500, 502, 503, 504}


def fetch_batch(numeric_ids: list[int], max_retries: int = 4) -> dict[int, dict]:
    ids_param = ",".join(str(i) for i in numeric_ids)
    url = f"{API}?ids={ids_param}&fields={FIELDS}&limit={len(numeric_ids)}"
    for attempt in range(max_retries + 1):
        req = urllib.request.Request(url, headers=HEADERS)
        try:
            with urllib.request.urlopen(req, timeout=60) as resp:
                data = json.loads(resp.read())
            return {rec["id"]: rec for rec in (data.get("data") or [])}
        except urllib.error.HTTPError as e:
            if e.code in _RETRYABLE and attempt < max_retries:
                time.sleep(min(2.0 * (2 ** attempt), 60.0))
                continue
            raise
        except urllib.error.URLError:
            if attempt < max_retries:
                time.sleep(min(2.0 * (2 ** attempt), 60.0))
                continue
            raise
    return {}


def main() -> None:
    ids = json.loads(CORPUS_IDS.read_text(encoding="utf-8"))["ids"]
    # "aic-27" -> 27 の対応表（出力キーは image_id 形式 "aic-27" を維持）
    numeric = {int(i.split("-", 1)[1]): i for i in ids}

    meta: dict[str, dict] = {}
    missing: list[str] = []
    nums = list(numeric)
    for start in range(0, len(nums), BATCH):
        chunk = nums[start:start + BATCH]
        recs = fetch_batch(chunk)
        for num in chunk:
            image_id = numeric[num]
            rec = recs.get(num)
            if rec is None:
                missing.append(image_id)
                continue
            meta[image_id] = {
                "title": (rec.get("title") or "").strip(),
                "artist_display": (rec.get("artist_display") or "").strip(),
                "classification_title": (rec.get("classification_title") or "").strip(),
            }
        print(f"fetched {min(start + BATCH, len(nums))}/{len(nums)}", flush=True)
        time.sleep(0.3)

    OUT.write_text(json.dumps({
        "_meta": {
            "fetched_at": datetime.now().isoformat(timespec="seconds"),
            "source": API,
            "fields": FIELDS,
            "total_corpus": len(ids),
            "fetched": len(meta),
            "missing": missing,
        },
        "meta": meta,
    }, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"\nfetched {len(meta)}/{len(ids)} (missing={len(missing)})")
    print(f"saved: {OUT}")


if __name__ == "__main__":
    main()
