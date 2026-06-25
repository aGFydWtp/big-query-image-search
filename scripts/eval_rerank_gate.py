#!/usr/bin/env python3
"""rerank 投資の GO/NO-GO を判定する recall ゲート（reranker 実装ゼロ・API 課金なし）。

rerank は候補集合を並べ替えるだけで、rrf_vec が取りこぼした正解は救えない。
よって rerank の nDCG 上限は候補集合の recall@N で頭打ちする。本スクリプトは
(a) Vertex / (c) Gemini の reranker を実装する前に、その伸びしろを安価に確定する。

計測（rrf_vec = RRF[ベクトル(JP) + ベクトル(EN)] の top-50 候補について）:
  - recall@10 / recall@20 / recall@50（overall / tag_hit / tag_miss）
  - oracle nDCG@10 : top-50 候補内の正解を理想順に並べた場合の nDCG@10 上限。
                     (a)/(c) いずれの rerank も理論上これを超えられない目標値。

判定（claudedocs/bq-hybrid-maxvsmax-plan.md §4）:
  recall@50 ≫ recall@10            → GO  : rerank に伸びしろあり
  recall@50 ≈ recall@10            → NO-GO: 取りこぼしで rerank 無効。retrieval を見直す

実行（リポジトリルートから、scripts パッケージ解決のため -m で）:
  .venv/bin/python -m scripts.eval_rerank_gate
"""

from __future__ import annotations

import json
from datetime import datetime
from pathlib import Path

import scripts.eval_bq_search as base
from scripts.eval_bq_experiments import EVAL_SET, REWRITES, RESULTS_DIR, rrf

POOL = 50                      # 候補プール深さ（= rerank する top-N の上限）
DEPTHS = [10, 20, 50]          # recall を測る深さ


def recall_at(ranked: list[str], relevant: set[str], k: int) -> float:
    """capped recall@k = ヒット数 / min(|relevant|, k)。eval_bq_search と同式。"""
    if not relevant:
        return 0.0
    hits = sum(1 for r in ranked[:k] if r in relevant)
    return hits / min(len(relevant), k)


def oracle_ndcg_at_k(pool: list[str], relevant: set[str], k: int) -> float:
    """top-`pool` 内の正解を理想順に並べた場合の nDCG@k 上限。

    nDCG@k は top-k のどれが正解かにしか依存しないため、プール内の正解を
    先頭に詰めた ranking が達成可能な最大値。ndcg_at_k に oracle 順を渡して算出。
    """
    in_pool = [d for d in pool if d in relevant]
    others = [d for d in pool if d not in relevant]
    return base.ndcg_at_k(in_pool + others, relevant, k)


def main() -> None:
    eval_set = json.loads(EVAL_SET.read_text(encoding="utf-8"))
    queries = eval_set["queries"]
    rewrites = json.loads(REWRITES.read_text(encoding="utf-8"))["rewrites"]

    rows: list[dict] = []
    for q in queries:
        relevant = set(q["relevant_ids"])
        jp = q["query"]
        en = rewrites[q["id"]]["query_en"]

        vec_jp = base.bq_search(jp, POOL)
        vec_en = base.bq_search(en, POOL)
        pool = rrf([vec_jp, vec_en], POOL)   # rrf_vec の top-50 候補

        row = {
            "id": q["id"],
            "type": q["type"],
            "n_relevant": len(relevant),
            "ndcg@10": base.ndcg_at_k(pool, relevant, 10),
            "oracle_ndcg@10": oracle_ndcg_at_k(pool, relevant, 10),
        }
        for d in DEPTHS:
            row[f"recall@{d}"] = recall_at(pool, relevant, d)
        rows.append(row)
        print(
            f"{q['id']} [{q['type']:8s}] "
            f"R@10={row['recall@10']:.2f} R@20={row['recall@20']:.2f} R@50={row['recall@50']:.2f} "
            f"nDCG@10={row['ndcg@10']:.2f} oracle={row['oracle_ndcg@10']:.2f}  '{jp}'",
            flush=True,
        )

    def agg(group: str | None) -> dict:
        rs = [r for r in rows if group is None or r["type"] == group]
        n = len(rs)
        keys = [f"recall@{d}" for d in DEPTHS] + ["ndcg@10", "oracle_ndcg@10"]
        out = {"n": n}
        out.update({k: (sum(r[k] for r in rs) / n if n else 0.0) for k in keys})
        return out

    summary = {g: agg(None if g == "overall" else g)
               for g in ("overall", "tag_hit", "tag_miss")}

    print(f"\n## rerank recall ゲート（rrf_vec top-{POOL} 候補プール）\n")
    hdr = "| group | n | " + " | ".join(f"recall@{d}" for d in DEPTHS) + " | nDCG@10 | oracle@10 |"
    print(hdr)
    print("|" + "---|" * (len(DEPTHS) + 4))
    for g in ("overall", "tag_hit", "tag_miss"):
        s = summary[g]
        cells = " | ".join(f"{s[f'recall@{d}']:.3f}" for d in DEPTHS)
        print(f"| {g} | {s['n']} | {cells} | {s['ndcg@10']:.3f} | {s['oracle_ndcg@10']:.3f} |")

    o = summary["overall"]
    headroom_recall = o["recall@50"] - o["recall@10"]
    headroom_ndcg = o["oracle_ndcg@10"] - o["ndcg@10"]
    verdict = "GO" if headroom_recall >= 0.03 else "NO-GO"
    print(f"\noverall recall@10→@50 改善幅: +{headroom_recall:.3f}")
    print(f"overall nDCG@10 現状→oracle 上限: {o['ndcg@10']:.3f} → {o['oracle_ndcg@10']:.3f} (+{headroom_ndcg:.3f})")
    print(f"判定（recall headroom ≥ 0.03 を GO の目安）: {verdict}")

    out = RESULTS_DIR / "bq-rerank-gate.json"
    out.write_text(json.dumps({
        "_meta": {
            "run_at": datetime.now().isoformat(timespec="seconds"),
            "pool": POOL,
            "depths": DEPTHS,
            "base_ranking": "rrf_vec = RRF[VECTOR_SEARCH(JP), VECTOR_SEARCH(EN)]",
            "engine": "bigquery gemini-embedding-2-preview + VECTOR_SEARCH COSINE",
            "purpose": "rerank GO/NO-GO gate: candidate-pool recall ceiling + oracle nDCG@10",
        },
        "summary": summary,
        "per_query": rows,
    }, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"\nsaved: {out}")


if __name__ == "__main__":
    main()
