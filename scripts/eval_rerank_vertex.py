#!/usr/bin/env python3
"""(a) Vertex Ranking API rerank（安価な対照）— rrf_vec top-N をメタで並べ替える。

claudedocs/bq-hybrid-maxvsmax-plan.md §2(a) / §5。

rrf_vec（nDCG@10=0.670）の top-50 候補を固定し、各候補の本番メタ
（title + artist_display + classification_title）を文書テキストとして
Vertex Ranking API（Discovery Engine semantic-ranker-default-004）で
クエリ↔文書の関連度を採点 → スコア降順（同点は元の rrf_vec 順）で**非破壊に並べ替える**。

メタは英語（AIC 由来）なので、クエリは英語リライト(en)を主、生 JP(jp)を対照として
両方計測する（rrf_vec が JP/EN 両系統を融合しているのと整合）。

認証: gcloud のユーザーアクセストークン（ADC 再認証不要）を REST に Bearer 付与。

実行（リポジトリルートから）:
  .venv/bin/python -m scripts.eval_rerank_vertex
"""

from __future__ import annotations

import argparse
import json
import subprocess
import time
import urllib.error
import urllib.request
from datetime import datetime

from scripts.eval_bq_experiments import RESULTS_DIR
from scripts.rerank_common import CACHE_DIR, REPO_ROOT, build_pools, evaluate_rankings

PROJECT_ID = "image-search-6c457e"
META_PATH = REPO_ROOT / "eval" / "aic_corpus_meta.json"
SCORE_CACHE = CACHE_DIR / "vertex_scores.json"

RANK_URL = (
    f"https://discoveryengine.googleapis.com/v1/projects/{PROJECT_ID}"
    "/locations/global/rankingConfigs/default_ranking_config:rank"
)

# Ranking API 料金（2026-06 時点・要確認）: 1 クエリ = 最大 100 文書で 1 課金単位、
# $1 / 1000 クエリ（= $0.001/クエリ）。N>100 は 100 文書ごとに +1 単位。
PRICE_PER_QUERY = 0.001
PROD_SEARCHES_PER_DAY = 5000
PROD_DAYS_PER_MONTH = 30

_RETRYABLE = {429, 500, 502, 503, 504}


def get_token() -> str:
    out = subprocess.run(["gcloud", "auth", "print-access-token"],
                         capture_output=True, text=True)
    if out.returncode != 0:
        raise RuntimeError(f"gcloud token 取得失敗: {out.stderr.strip()[:300]}")
    return out.stdout.strip()


def doc_text(m: dict) -> str:
    """artist_display + classification_title を content に結合。"""
    return " ".join(p for p in (m["artist_display"], m["classification_title"]) if p)


def rank_query(query: str, records: list[dict], model: str, token: str,
               max_retries: int = 5) -> dict[str, float]:
    """records=[{id,title,content}] を Ranking API で採点し id->score を返す。"""
    body = json.dumps({
        "model": model,
        "query": query,
        "records": records,
        "topN": len(records),
    }).encode("utf-8")
    last_err = ""
    for attempt in range(max_retries + 1):
        req = urllib.request.Request(
            RANK_URL, data=body,
            headers={"Authorization": f"Bearer {token}",
                     "Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(req, timeout=120) as resp:
                data = json.loads(resp.read())
            return {r["id"]: r.get("score", 0.0) for r in data.get("records", [])}
        except urllib.error.HTTPError as e:
            last_err = f"HTTP {e.code}: {e.read()[:200]}"
            if e.code not in _RETRYABLE:
                raise RuntimeError(f"rank '{query}': {last_err}")
        except (urllib.error.URLError, json.JSONDecodeError, KeyError) as e:
            last_err = str(e)[:120]
        if attempt < max_retries:
            time.sleep(min(2.0 * (2 ** attempt), 30.0))
    raise RuntimeError(f"rank '{query}': リトライ上限 ({last_err})")


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="semantic-ranker-default-004")
    ap.add_argument("--depths", default="10,20,50")
    ap.add_argument("--langs", default="en,jp", help="ランキングに使うクエリ言語")
    ap.add_argument("--no-cache", action="store_true")
    args = ap.parse_args()

    depths = sorted(int(d) for d in args.depths.split(","))
    langs = [l.strip() for l in args.langs.split(",") if l.strip()]
    pools = build_pools()
    meta = json.loads(META_PATH.read_text(encoding="utf-8"))["meta"]

    cache: dict[str, float] = {}
    if SCORE_CACHE.exists() and not args.no_cache:
        cache = json.loads(SCORE_CACHE.read_text(encoding="utf-8"))
    token = get_token()

    # --- 各クエリ×言語で top-50 を 1 回採点（キャッシュ優先）-----------------
    for lang in langs:
        for qid, m in pools.items():
            keys = [f"{args.model}::{lang}::{qid}::{cid}" for cid in m["pool"]]
            if all(k in cache for k in keys):
                continue
            query = m["query"] if lang == "jp" else m["en"]
            records = []
            for cid in m["pool"]:
                if cid not in meta:
                    continue
                records.append({
                    "id": cid,
                    "title": meta[cid]["title"],
                    "content": doc_text(meta[cid]),
                })
            scores = rank_query(query, records, args.model, token)
            for cid in m["pool"]:
                cache[f"{args.model}::{lang}::{qid}::{cid}"] = scores.get(cid, 0.0)
            print(f"rank [{lang}] {qid} ranked {len(records)}  '{query[:40]}'", flush=True)
        CACHE_DIR.mkdir(parents=True, exist_ok=True)
        SCORE_CACHE.write_text(json.dumps(cache, ensure_ascii=False) + "\n",
                               encoding="utf-8")

    base_eval = evaluate_rankings({qid: m["pool"] for qid, m in pools.items()}, pools)
    oracle = base_eval["summary"]["overall"]["oracle_ndcg@10"]

    # --- 言語別 × N 別に再ランク → 評価 ------------------------------------
    by_lang: dict[str, dict] = {}
    for lang in langs:
        results_by_n: dict[str, dict] = {}
        for n in depths:
            rankings: dict[str, list[str]] = {}
            for qid, m in pools.items():
                cand = m["pool"][:n]
                scored = [
                    (cache.get(f"{args.model}::{lang}::{qid}::{cid}", 0.0), -i, cid)
                    for i, cid in enumerate(cand)
                ]
                scored.sort(reverse=True)
                reranked = [cid for _, _, cid in scored]
                rest = [c for c in m["pool"] if c not in set(cand)]
                rankings[qid] = reranked + rest
            results_by_n[str(n)] = evaluate_rankings(rankings, pools)
        by_lang[lang] = results_by_n

    # --- コスト試算（1 検索 = 1 ランキングコール = ceil(N/100) 課金単位）-----
    import math
    cost = {str(n): {
        "per_search_usd": math.ceil(n / 100) * PRICE_PER_QUERY,
        "monthly_usd": math.ceil(n / 100) * PRICE_PER_QUERY
                       * PROD_SEARCHES_PER_DAY * PROD_DAYS_PER_MONTH,
    } for n in depths}

    # --- 出力 ---------------------------------------------------------------
    bo = base_eval["summary"]
    print(f"\n## (a) Vertex Ranking rerank — nDCG@10（model={args.model}）\n")
    print("| config | overall | tag_hit | tag_miss | oracle達成率 |")
    print("|---|---|---|---|---|")
    print(f"| base(rrf_vec) | {bo['overall']['ndcg@10']:.3f} | {bo['tag_hit']['ndcg@10']:.3f} | "
          f"{bo['tag_miss']['ndcg@10']:.3f} | {bo['overall']['ndcg@10']/oracle*100:.1f}% |")
    for lang in langs:
        for n in depths:
            s = by_lang[lang][str(n)]["summary"]
            print(f"| {lang} N={n} | {s['overall']['ndcg@10']:.3f} | {s['tag_hit']['ndcg@10']:.3f} | "
                  f"{s['tag_miss']['ndcg@10']:.3f} | {s['overall']['ndcg@10']/oracle*100:.1f}% |")
    print(f"\noracle 上限 nDCG@10 = {oracle:.3f}")
    for n in depths:
        c = cost[str(n)]
        print(f"  N={n}: ${c['per_search_usd']:.5f}/検索 → ${c['monthly_usd']:.0f}/月")

    out = RESULTS_DIR / "bq-rerank-vertex.json"
    out.write_text(json.dumps({
        "_meta": {
            "run_at": datetime.now().isoformat(timespec="seconds"),
            "flavor": "(a) Vertex Ranking API rerank over production metadata",
            "model": args.model,
            "doc_text": "title (record.title) + artist_display + classification_title (record.content)",
            "base_ranking": "rrf_vec = RRF[VECTOR_SEARCH(JP), VECTOR_SEARCH(EN)] top-50",
            "langs": langs,
            "depths": depths,
            "price_usd_per_query_unit": PRICE_PER_QUERY,
            "price_note": "1 query = up to 100 docs = 1 billable unit",
            "prod_assumption": {"searches_per_day": PROD_SEARCHES_PER_DAY,
                                "days": PROD_DAYS_PER_MONTH},
        },
        "base_rrf_vec": bo,
        "by_lang": {lang: {n: by_lang[lang][n]["summary"] for n in by_lang[lang]}
                    for lang in langs},
        "cost_by_n": cost,
        "per_query_by_lang_n": {
            lang: {n: by_lang[lang][n]["per_query"] for n in by_lang[lang]}
            for lang in langs
        },
    }, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"\nsaved: {out}")


if __name__ == "__main__":
    main()
