#!/usr/bin/env python3
"""BigQuery 検索（gemini-embedding-2 + VECTOR_SEARCH）を評価セットで計測する。

参照リポジトリ aGFydWtp/image-search と「同一データセット（AIC 380 件）・同一評価セット
（config/eval_queries.json、34 クエリ）・同一指標（precision@k / capped recall@k /
nDCG@k、binary relevance）」でスコアを比較するためのハーネス。

指標関数は参照の scripts/eval_metrics.py と数値同一（ロジックを移植）。
検索は本プロジェクトの sql/search.sql と同じ「クエリ文を AI.GENERATE_EMBEDDING で
3072 次元に埋め込み → image_embeddings に対し VECTOR_SEARCH(COSINE) top_k」。

前提:
  - `gcloud auth application-default login` 済み（bq CLI が利用可能・認証済み）。
  - image_embeddings が参照コーパス 380 件で構成されていること（eval/aic_corpus_ids.json）。

実行:
  .venv/bin/python scripts/eval_bq_search.py --label bq-gemini-embedding-2
"""

from __future__ import annotations

import argparse
import json
import math
import re
import subprocess
import sys
from datetime import datetime
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
DEFAULT_EVAL_SET = Path(
    "/Users/haruki/Documents/docker-dev/hr20k/image-search/config/eval_queries.json"
)
RESULTS_DIR = REPO_ROOT / "docs" / "eval-results"

PROJECT_ID = "image-search-6c457e"
DATASET = "image-search-6c457e.image_search"
MODEL = "gemini_embedding_model"
TABLE = "image_embeddings"

# sql/search.sql と同等。プレースホルダは concrete name で確定。
SEARCH_SQL = f"""
WITH query_embedding AS (
  SELECT embedding
  FROM AI.GENERATE_EMBEDDING(
    MODEL `{DATASET}.{MODEL}`,
    (SELECT @query AS content),
    STRUCT(3072 AS output_dimensionality)
  )
  WHERE status = ''
)
SELECT base.image_uri AS image_uri, distance
FROM VECTOR_SEARCH(
  TABLE `{DATASET}.{TABLE}`,
  'embedding',
  TABLE query_embedding,
  query_column_to_search => 'embedding',
  top_k => @top_k,
  distance_type => 'COSINE'
)
ORDER BY distance ASC;
"""

_ID_RE = re.compile(r"(aic-\d+)\.jpg")


# ---- 指標（参照 scripts/eval_metrics.py と数値同一）-------------------------
def precision_at_k(ranked: list[str], relevant: set[str], k: int) -> float:
    top = ranked[:k]
    if not top:
        return 0.0
    return sum(1 for r in top if r in relevant) / len(top)


def recall_at_k(ranked: list[str], relevant: set[str], k: int) -> float:
    if not relevant:
        return 0.0
    hits = sum(1 for r in ranked[:k] if r in relevant)
    return hits / min(len(relevant), k)


def ndcg_at_k(ranked: list[str], relevant: set[str], k: int) -> float:
    dcg = sum(1.0 / math.log2(i + 2) for i, r in enumerate(ranked[:k]) if r in relevant)
    ideal_hits = min(len(relevant), k)
    idcg = sum(1.0 / math.log2(i + 2) for i in range(ideal_hits))
    return dcg / idcg if idcg > 0 else 0.0


def aggregate(rows: list[dict]) -> dict[str, float]:
    if not rows:
        return {"precision": 0.0, "recall": 0.0, "ndcg": 0.0, "n": 0}
    return {
        "precision": sum(r["precision"] for r in rows) / len(rows),
        "recall": sum(r["recall"] for r in rows) / len(rows),
        "ndcg": sum(r["ndcg"] for r in rows) / len(rows),
        "n": len(rows),
    }


# ---- BQ 検索 ---------------------------------------------------------------
def bq_search(query: str, k: int) -> list[str]:
    """BQ で検索し、ランキング順の aic-id リストを返す。"""
    cmd = [
        "bq", "query",
        "--project_id", PROJECT_ID,
        "--use_legacy_sql=false",
        "--format=json",
        f"--max_rows={k}",
        f"--parameter=query:STRING:{query}",
        f"--parameter=top_k:INT64:{k}",
        SEARCH_SQL,
    ]
    out = subprocess.run(cmd, capture_output=True, text=True)
    if out.returncode != 0:
        raise RuntimeError(f"bq query failed for '{query}': {out.stderr.strip()[:500]}")
    rows = json.loads(out.stdout or "[]")
    ranked: list[str] = []
    for row in rows:
        m = _ID_RE.search(row["image_uri"])
        if m:
            ranked.append(m.group(1))
    return ranked


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--label", default="bq-gemini-embedding-2")
    ap.add_argument("--k", type=int, default=10)
    ap.add_argument("--eval-set", type=Path, default=DEFAULT_EVAL_SET)
    args = ap.parse_args()

    eval_set = json.loads(args.eval_set.read_text(encoding="utf-8"))
    queries = eval_set["queries"]

    per_query: list[dict] = []
    for q in queries:
        relevant = set(q["relevant_ids"])
        ranked = bq_search(q["query"], args.k)
        row = {
            "id": q["id"],
            "query": q["query"],
            "type": q["type"],
            "n_relevant": len(relevant),
            "precision": precision_at_k(ranked, relevant, args.k),
            "recall": recall_at_k(ranked, relevant, args.k),
            "ndcg": ndcg_at_k(ranked, relevant, args.k),
        }
        per_query.append(row)
        print(
            f"{row['id']} [{row['type']:8s}] P@{args.k}={row['precision']:.2f} "
            f"R@{args.k}={row['recall']:.2f} nDCG@{args.k}={row['ndcg']:.2f}  "
            f"'{row['query']}' (rel={row['n_relevant']})",
            flush=True,
        )

    summary = {
        "overall": aggregate(per_query),
        "tag_hit": aggregate([r for r in per_query if r["type"] == "tag_hit"]),
        "tag_miss": aggregate([r for r in per_query if r["type"] == "tag_miss"]),
    }

    print(f"\n## 集計 (label={args.label}, k={args.k})\n")
    print("| group | n | precision@k | recall@k | nDCG@k |")
    print("|---|---|---|---|---|")
    for group in ("overall", "tag_hit", "tag_miss"):
        s = summary[group]
        print(
            f"| {group} | {s['n']} | {s['precision']:.3f} | "
            f"{s['recall']:.3f} | {s['ndcg']:.3f} |"
        )

    RESULTS_DIR.mkdir(parents=True, exist_ok=True)
    out_path = RESULTS_DIR / f"{args.label}.json"
    out_path.write_text(
        json.dumps(
            {
                "_meta": {
                    "run_at": datetime.now().isoformat(timespec="seconds"),
                    "label": args.label,
                    "k": args.k,
                    "engine": "bigquery AI.GENERATE_EMBEDDING(gemini-embedding-2-preview, 3072) + VECTOR_SEARCH COSINE",
                    "corpus": "AIC 380 (eval/aic_corpus_ids.json, 参照と同一)",
                    "eval_set": str(args.eval_set),
                    "eval_set_generated_at": eval_set["_meta"]["generated_at"],
                    "total_queries": len(queries),
                },
                "summary": summary,
                "per_query": per_query,
            },
            ensure_ascii=False,
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )
    print(f"\nsaved: {out_path}")


if __name__ == "__main__":
    main()
