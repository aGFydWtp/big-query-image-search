#!/usr/bin/env python3
"""(c') Gemini listwise rerank — rrf_vec top-N を「1 リクエストで一括順位付け」する対照。

pointwise（eval_rerank_gemini.py）の反証用。N 枚の実画像を 1 リクエストに同梱し、
クエリへの関連性が高い順に **画像ラベルの並び** を返させて再ランクする。

設計上の既知の弱点（plan §2(c) / pointwise 採用理由）:
  - 画像 N 枚で長文脈化（N=20 で ≈3.5万トークン）。順序バイアス・長文脈劣化。
  - (qid, image_id) 単位のキャッシュが効かない（1 クエリ=1 リクエスト）。

そのため N は小さめ（既定 20）で測り、pointwise と同一指標・同一プールで比較する。

認証・プロジェクト・画像参照は eval_rerank_gemini.py と同一（gcloud トークン + gs:// fileData）。

実行:
  .venv/bin/python -m scripts.eval_rerank_listwise --model gemini-2.5-flash --depth 20
"""

from __future__ import annotations

import argparse
import json
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime
from pathlib import Path

from scripts.eval_bq_experiments import RESULTS_DIR
from scripts.eval_rerank_gemini import LOCATION, PROJECT_ID, TokenProvider, _RETRYABLE
from scripts.rerank_common import build_pools, evaluate_rankings, image_gcs_uri

LISTWISE_INSTR = (
    "あなたは画像検索の関連性評価者です。検索クエリ「{query}」に対して、"
    "{n} 枚の候補画像を上で [1]〜[{n}] のラベル付きで提示します。\n"
    "クエリへの関連性が高い順に **すべてのラベル番号を 1 回ずつ** 並べた配列を返してください"
    "（最も合致する画像のラベルを先頭に）。\n"
    "JSON のみ: {{\"ranking\": [<ラベル番号を関連性降順で>]}}"
)

RESPONSE_SCHEMA = {
    "type": "OBJECT",
    "properties": {"ranking": {"type": "ARRAY", "items": {"type": "INTEGER"}}},
    "required": ["ranking"],
}


def rerank_query(cand: list[str], query: str, model: str, thinking: int,
                 tokens: TokenProvider, max_retries: int = 5) -> list[str]:
    """cand（rrf_vec 順 image_id）を listwise で並べ替えて返す。

    モデルが落とした/重複したラベルは無視し、欠落分は元の rrf_vec 順で末尾に温存する
    （= 完全な順列を必ず返す。部分応答でも評価が壊れない）。
    """
    n = len(cand)
    url = (
        f"https://{LOCATION}-aiplatform.googleapis.com/v1/projects/{PROJECT_ID}"
        f"/locations/{LOCATION}/publishers/google/models/{model}:generateContent"
    )
    # [1] <img1> [2] <img2> ... <instruction> の順でインターリーブしラベルと画像を対応づける。
    parts: list[dict] = []
    for i, cid in enumerate(cand, start=1):
        parts.append({"text": f"[{i}]"})
        parts.append({"fileData": {"fileUri": image_gcs_uri(cid),
                                   "mimeType": "image/jpeg"}})
    parts.append({"text": LISTWISE_INSTR.format(query=query, n=n)})

    max_out = 512 if thinking == 0 else thinking + 512
    body = json.dumps({
        "contents": [{"role": "user", "parts": parts}],
        "generationConfig": {
            "temperature": 0,
            "maxOutputTokens": max_out,
            "responseMimeType": "application/json",
            "responseSchema": RESPONSE_SCHEMA,
            "thinkingConfig": {"thinkingBudget": thinking},
        },
    }).encode("utf-8")

    last_err = ""
    for attempt in range(max_retries + 1):
        req = urllib.request.Request(
            url, data=body,
            headers={"Authorization": f"Bearer {tokens.token}",
                     "Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(req, timeout=180) as resp:
                data = json.loads(resp.read())
            cand_obj = (data.get("candidates") or [{}])[0]
            text = ((cand_obj.get("content") or {}).get("parts") or [{}])[0].get("text", "{}")
            order = json.loads(text).get("ranking", [])
            # 1-based ラベル → image_id。重複・範囲外は除外、欠落は元順で末尾補完。
            seen: set[int] = set()
            reranked: list[str] = []
            for lab in order:
                if isinstance(lab, int) and 1 <= lab <= n and lab not in seen:
                    seen.add(lab)
                    reranked.append(cand[lab - 1])
            for i, cid in enumerate(cand, start=1):
                if i not in seen:
                    reranked.append(cid)
            return reranked
        except urllib.error.HTTPError as e:
            last_err = f"HTTP {e.code}"
            if e.code == 401:
                tokens.refresh()
            elif e.code not in _RETRYABLE:
                raise RuntimeError(f"'{query}': HTTP {e.code} {e.read()[:200]}")
        except (urllib.error.URLError, json.JSONDecodeError, ValueError, KeyError) as e:
            last_err = str(e)[:120]
        if attempt < max_retries:
            time.sleep(min(2.0 * (2 ** attempt), 30.0))
    raise RuntimeError(f"'{query}': リトライ上限 ({last_err})")


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="gemini-2.5-flash")
    ap.add_argument("--depth", type=int, default=20, help="listwise で並べ替える top-N")
    ap.add_argument("--thinking", type=int, default=0)
    ap.add_argument("--workers", type=int, default=6)
    ap.add_argument("--out", default=None)
    args = ap.parse_args()

    pools = build_pools()
    tokens = TokenProvider()
    n = args.depth

    def run(qid: str) -> tuple[str, list[str]]:
        meta = pools[qid]
        cand = meta["pool"][:n]
        reranked = rerank_query(cand, meta["query"], args.model, args.thinking, tokens)
        rest = [c for c in meta["pool"] if c not in set(cand)]
        return qid, reranked + rest

    print(f"listwise rerank: {len(pools)} クエリ × top-{n}（model={args.model}, "
          f"thinking={args.thinking}）", flush=True)
    rankings: dict[str, list[str]] = {}
    with ThreadPoolExecutor(max_workers=args.workers) as ex:
        for qid, ranked in ex.map(run, list(pools.keys())):
            rankings[qid] = ranked
            print(f"  done {qid}", flush=True)

    res = evaluate_rankings(rankings, pools)
    base = evaluate_rankings({q: m["pool"] for q, m in pools.items()}, pools)
    o, b = res["summary"], base["summary"]
    oracle = b["overall"]["oracle_ndcg@10"]

    print(f"\n## (c') Gemini listwise rerank — nDCG@10（model={args.model}, "
          f"top-{n}, thinking={args.thinking}）\n")
    print("| 構成 | overall | tag_hit | tag_miss | oracle達成率 |")
    print("|---|---|---|---|---|")
    print(f"| base(rrf_vec) | {b['overall']['ndcg@10']:.3f} | {b['tag_hit']['ndcg@10']:.3f} | "
          f"{b['tag_miss']['ndcg@10']:.3f} | {b['overall']['ndcg@10']/oracle*100:.1f}% |")
    print(f"| listwise N={n} | {o['overall']['ndcg@10']:.3f} | {o['tag_hit']['ndcg@10']:.3f} | "
          f"{o['tag_miss']['ndcg@10']:.3f} | {o['overall']['ndcg@10']/oracle*100:.1f}% |")
    print(f"\noracle 上限 nDCG@10 = {oracle:.3f}")

    out = Path(args.out) if args.out else RESULTS_DIR / "bq-rerank-listwise.json"
    out.write_text(json.dumps({
        "_meta": {
            "run_at": datetime.now().isoformat(timespec="seconds"),
            "flavor": "(c') Gemini listwise rerank (single request per query)",
            "model": args.model,
            "depth": n,
            "thinking_budget": args.thinking,
            "scoring": "listwise ranking of N labeled images, gs:// fileData",
            "base_ranking": "rrf_vec top-50",
        },
        "base_rrf_vec": base["summary"],
        "listwise": res["summary"],
        "per_query": res["per_query"],
    }, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"\nsaved: {out}")


if __name__ == "__main__":
    main()
