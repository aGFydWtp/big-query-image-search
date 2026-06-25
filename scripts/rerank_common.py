#!/usr/bin/env python3
"""rerank 計測の共有土台（候補プール構築＋キャッシュ＋指標）。

(a) Vertex Ranking / (c) Gemini vision の両リランカーが**同一の候補プール**を
非破壊で並べ替えられるよう、rrf_vec の top-N 候補を 1 か所で構築・キャッシュする。

候補プール = rrf([VECTOR_SEARCH(JP), VECTOR_SEARCH(EN)], POOL)
（scripts/eval_rerank_gate.py / eval_bq_experiments.py と同一定義）。

bq_search と rrf は決定的なので、プールはキャッシュから再現可能。再ランクの
スイープ（N=10/20/50）でも BQ を再実行せず、同一プールを共有する。

指標（precision@k / capped recall@k / nDCG@k）と oracle nDCG は
eval_bq_search.py / eval_rerank_gate.py のロジックをそのまま再利用する。
"""

from __future__ import annotations

import json
from pathlib import Path

import scripts.eval_bq_search as base
from scripts.eval_bq_experiments import EVAL_SET, REWRITES, rrf

REPO_ROOT = Path(__file__).resolve().parent.parent
CACHE_DIR = REPO_ROOT / "eval" / "rerank_cache"
POOL_CACHE = CACHE_DIR / "pools.json"

# 画像 GCS URI 復元用（params.env の BUCKET_URI と一致）。
BUCKET_URI = "gs://image-search-6c457e-imgsearch-images"
IMAGE_PREFIX = "aic-seed"

POOL = 50  # 候補プール深さ（= 再ランクする top-N の上限）


def image_gcs_uri(image_id: str) -> str:
    """aic-NNNNN -> gs://.../aic-seed/aic-NNNNN.jpg（投入時のパス規則）。"""
    return f"{BUCKET_URI}/{IMAGE_PREFIX}/{image_id}.jpg"


def oracle_ndcg_at_k(pool: list[str], relevant: set[str], k: int) -> float:
    """top-`pool` 内の正解を理想順に並べた場合の nDCG@k 上限（gate と同式）。"""
    in_pool = [d for d in pool if d in relevant]
    others = [d for d in pool if d not in relevant]
    return base.ndcg_at_k(in_pool + others, relevant, k)


def build_pools(pool_size: int = POOL, *, use_cache: bool = True) -> dict[str, dict]:
    """各クエリの候補プールを構築（or キャッシュから読込）して返す。

    返り値: {qid: {"id","query","en","type","relevant":[...],"pool":[...]}}
    pool は rrf_vec 順（再ランク前の基準順）の image_id リスト。
    """
    if use_cache and POOL_CACHE.exists():
        cached = json.loads(POOL_CACHE.read_text(encoding="utf-8"))
        if cached.get("_meta", {}).get("pool_size") == pool_size:
            return cached["pools"]

    eval_set = json.loads(EVAL_SET.read_text(encoding="utf-8"))
    rewrites = json.loads(REWRITES.read_text(encoding="utf-8"))["rewrites"]

    pools: dict[str, dict] = {}
    for q in eval_set["queries"]:
        jp = q["query"]
        en = rewrites[q["id"]]["query_en"]
        vec_jp = base.bq_search(jp, pool_size)
        vec_en = base.bq_search(en, pool_size)
        pool = rrf([vec_jp, vec_en], pool_size)
        pools[q["id"]] = {
            "id": q["id"],
            "query": jp,
            "en": en,
            "type": q["type"],
            "relevant": q["relevant_ids"],
            "pool": pool,
        }
        print(f"pool {q['id']} [{q['type']:8s}] |pool|={len(pool)}  '{jp}'", flush=True)

    CACHE_DIR.mkdir(parents=True, exist_ok=True)
    POOL_CACHE.write_text(
        json.dumps({"_meta": {"pool_size": pool_size}, "pools": pools},
                   ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    return pools


def evaluate_rankings(rankings: dict[str, list[str]], pools: dict[str, dict],
                      k: int = 10) -> dict:
    """qid -> 並べ替え後 ranking を受け取り、summary（overall/tag_hit/tag_miss）と
    per_query を返す。指標は base と同一、oracle 上限も併記する。"""
    per_query: list[dict] = []
    for qid, ranked in rankings.items():
        meta = pools[qid]
        relevant = set(meta["relevant"])
        per_query.append({
            "id": qid,
            "type": meta["type"],
            "n_relevant": len(relevant),
            "precision@10": base.precision_at_k(ranked, relevant, k),
            "recall@10": base.recall_at_k(ranked, relevant, k),
            "ndcg@10": base.ndcg_at_k(ranked, relevant, k),
            "oracle_ndcg@10": oracle_ndcg_at_k(meta["pool"], relevant, k),
        })

    def agg(group: str | None) -> dict:
        rs = [r for r in per_query if group is None or r["type"] == group]
        n = len(rs)
        keys = ["precision@10", "recall@10", "ndcg@10", "oracle_ndcg@10"]
        out = {"n": n}
        out.update({key: (sum(r[key] for r in rs) / n if n else 0.0) for key in keys})
        return out

    summary = {g: agg(None if g == "overall" else g)
               for g in ("overall", "tag_hit", "tag_miss")}
    return {"summary": summary, "per_query": per_query}
