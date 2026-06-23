#!/usr/bin/env python3
"""overall 改善の検証ハーネス（クエリ書き換え / 字句チャネル / RRF 融合）。

scripts/eval_bq_search.py（素の単一ベクトル）に対し、参照リポジトリが overall を
伸ばした 2 レバー —— ③ LLM クエリ書き換え と ② 字句(BM25)＋RRF 融合 —— が
本プロジェクト（gemini-embedding-2）でも効くかを、同一データセット・同一評価セット・
同一指標で検証する。

計測モード:
  raw       : ベクトル検索（生 JP クエリ）。eval_bq_search.py と同等の基準
  rewritten : ベクトル検索（英語リライト）。書き換えがベクトル系統に効くか
  rrf_vec   : RRF[ベクトル(JP) + ベクトル(EN)]
  hybrid    : RRF[ベクトル(JP) + 字句 BM25(JP キャプション)]
  full      : RRF[ベクトル(JP) + ベクトル(EN) + 字句 BM25]

字句チャネルは参照の英語キャプション+タグ+BM25 を本プロジェクトは持たないため、
参照が公開している JP キャプション(eval_captions_ja.json)への char-bigram BM25 で
代替する「天井検証」。プロジェクトに取り込むにはキャプション生成基盤の追加が前提。

実行:
  .venv/bin/python scripts/eval_bq_experiments.py
"""

from __future__ import annotations

import json
import math
import re
from collections import defaultdict
from datetime import datetime
from pathlib import Path

import scripts.eval_bq_search as base

REPO_ROOT = Path(__file__).resolve().parent.parent
REF = Path("/Users/haruki/Documents/docker-dev/hr20k/image-search")
EVAL_SET = REF / "config" / "eval_queries.json"
CAPTIONS_JA = REF / "config" / "eval_captions_ja.json"
REWRITES = REPO_ROOT / "eval" / "rewritten_queries.json"
RESULTS_DIR = REPO_ROOT / "docs" / "eval-results"

K = 10
CAND = 50          # 各系統の候補件数（融合用）
RRF_K = 60         # 参照と同じ


# ---- char-bigram BM25（JP 用・トークナイザ不要）----------------------------
def bigrams(text: str) -> list[str]:
    t = re.sub(r"\s+", "", text)
    return [t[i : i + 2] for i in range(len(t) - 1)] if len(t) >= 2 else [t]


class BM25:
    def __init__(self, docs: dict[str, str], k1: float = 1.5, b: float = 0.75):
        self.ids = list(docs)
        self.k1, self.b = k1, b
        self.tokens = {i: bigrams(docs[i]) for i in self.ids}
        self.len = {i: len(self.tokens[i]) for i in self.ids}
        self.avgdl = sum(self.len.values()) / max(1, len(self.ids))
        df: dict[str, int] = defaultdict(int)
        self.tf: dict[str, dict[str, int]] = {}
        for i in self.ids:
            tf: dict[str, int] = defaultdict(int)
            for tok in self.tokens[i]:
                tf[tok] += 1
            self.tf[i] = tf
            for tok in tf:
                df[tok] += 1
        n = len(self.ids)
        self.idf = {
            tok: math.log(1 + (n - d + 0.5) / (d + 0.5)) for tok, d in df.items()
        }

    def rank(self, query: str, top: int) -> list[str]:
        q = set(bigrams(query))
        scores: list[tuple[str, float]] = []
        for i in self.ids:
            s = 0.0
            tf = self.tf[i]
            dl = self.len[i]
            for tok in q:
                if tok in tf:
                    idf = self.idf.get(tok, 0.0)
                    num = tf[tok] * (self.k1 + 1)
                    den = tf[tok] + self.k1 * (1 - self.b + self.b * dl / self.avgdl)
                    s += idf * num / den
            if s > 0:
                scores.append((i, s))
        scores.sort(key=lambda x: x[1], reverse=True)
        return [i for i, _ in scores[:top]]


def rrf(rankings: list[list[str]], top: int) -> list[str]:
    score: dict[str, float] = defaultdict(float)
    for r in rankings:
        for rank, doc in enumerate(r):
            score[doc] += 1.0 / (RRF_K + rank + 1)
    return [d for d, _ in sorted(score.items(), key=lambda x: x[1], reverse=True)[:top]]


def main() -> None:
    eval_set = json.loads(EVAL_SET.read_text(encoding="utf-8"))
    queries = eval_set["queries"]
    rewrites = json.loads(REWRITES.read_text(encoding="utf-8"))["rewrites"]
    captions = json.loads(CAPTIONS_JA.read_text(encoding="utf-8"))["captions"]

    bm25 = BM25(captions)

    modes = ["raw", "rewritten", "rrf_vec", "hybrid", "full"]
    per_query: dict[str, list[dict]] = {m: [] for m in modes}

    for q in queries:
        relevant = set(q["relevant_ids"])
        jp = q["query"]
        en = rewrites[q["id"]]["query_en"]

        vec_jp = base.bq_search(jp, CAND)
        vec_en = base.bq_search(en, CAND)
        lex = bm25.rank(jp, CAND)

        ranked = {
            "raw": vec_jp,
            "rewritten": vec_en,
            "rrf_vec": rrf([vec_jp, vec_en], CAND),
            "hybrid": rrf([vec_jp, lex], CAND),
            "full": rrf([vec_jp, vec_en, lex], CAND),
        }
        for m in modes:
            r = ranked[m]
            per_query[m].append({
                "id": q["id"], "type": q["type"],
                "precision": base.precision_at_k(r, relevant, K),
                "recall": base.recall_at_k(r, relevant, K),
                "ndcg": base.ndcg_at_k(r, relevant, K),
            })
        print(
            f"{q['id']} [{q['type']:8s}] "
            + " ".join(f"{m}={base.ndcg_at_k(ranked[m], relevant, K):.2f}" for m in modes)
            + f"  '{jp}'",
            flush=True,
        )

    def agg(rows, t=None):
        rs = [r for r in rows if t is None or r["type"] == t]
        return base.aggregate(rs)

    summary = {
        m: {g: agg(per_query[m], None if g == "overall" else g)
            for g in ("overall", "tag_hit", "tag_miss")}
        for m in modes
    }

    print(f"\n## nDCG@{K} 比較\n")
    print("| mode | tag_miss | tag_hit | overall |")
    print("|---|---|---|---|")
    for m in modes:
        s = summary[m]
        print(f"| {m} | {s['tag_miss']['ndcg']:.3f} | {s['tag_hit']['ndcg']:.3f} | {s['overall']['ndcg']:.3f} |")

    out = RESULTS_DIR / "bq-experiments.json"
    out.write_text(json.dumps({
        "_meta": {
            "run_at": datetime.now().isoformat(timespec="seconds"),
            "k": K, "candidates_per_channel": CAND, "rrf_k": RRF_K,
            "engine": "bigquery gemini-embedding-2-preview + VECTOR_SEARCH COSINE",
            "lexical": "char-bigram BM25 over JP captions (eval_captions_ja.json) — 天井検証",
            "modes": modes,
        },
        "summary": summary,
        "per_query": per_query,
    }, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"\nsaved: {out}")


if __name__ == "__main__":
    main()
