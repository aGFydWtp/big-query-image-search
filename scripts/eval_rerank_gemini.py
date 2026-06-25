#!/usr/bin/env python3
"""(c) Gemini マルチモーダル rerank（本命）— rrf_vec top-N を実画像で並べ替える。

claudedocs/bq-hybrid-maxvsmax-plan.md §2(c) / §5。

rrf_vec（dense JP ⊕ EN書き換え dense の RRF 融合, nDCG@10=0.670）の top-50 候補を
固定し、各候補の **GCS 実画像 + クエリ文** を Gemini vision に渡して pointwise で
関連性を 0〜3 採点 → スコア降順（同点は元の rrf_vec 順）で**非破壊に並べ替える**。

設計判断（pointwise を採用）:
  - 各画像を独立採点するため (qid, image_id) でキャッシュでき、N=10/20/50 の
    スイープで再課金しない（top-50 を 1 回採点 → サブセットで N 別に評価）。
  - listwise（N枚を 1 リクエストで順位付け）は N=50 で画像 50 枚 ≈ 6.5万トークンとなり
    長文脈劣化・順序バイアス・キャッシュ不可。pointwise が品質×コストで優位。

認証: gcloud のユーザーアクセストークン（ADC 再認証不要）を REST に Bearer 付与。
  画像は gs:// fileData で直接参照（ダウンロード不要）。

実行（リポジトリルートから）:
  .venv/bin/python -m scripts.eval_rerank_gemini
  .venv/bin/python -m scripts.eval_rerank_gemini --model gemini-2.5-flash --workers 6
"""

from __future__ import annotations

import argparse
import json
import subprocess
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime
from pathlib import Path
from threading import Lock

from scripts.eval_bq_experiments import RESULTS_DIR
from scripts.rerank_common import (
    CACHE_DIR,
    build_pools,
    evaluate_rankings,
    image_gcs_uri,
)

PROJECT_ID = "image-search-6c457e"
LOCATION = "us-central1"
SCORE_CACHE = CACHE_DIR / "gemini_scores.json"

# gemini-2.5-flash 料金（USD / 1M tokens, 2026-06 時点・要確認）。
# image 入力はトークン換算で text 入力と同単価。
PRICE_IN_PER_1M = 0.30
PRICE_OUT_PER_1M = 2.50

# 本番コスト試算の想定規模（plan §5）。
PROD_SEARCHES_PER_DAY = 5000
PROD_DAYS_PER_MONTH = 30

# 0-3 採点の既定ルーブリック（flash ベースライン互換のため文言を固定）。
RUBRIC_0_3 = (
    "あなたは画像検索の関連性評価者です。検索クエリと 1 枚の画像が与えられます。\n"
    "この画像が検索クエリ「{query}」にどれだけ合致するかを 0〜3 の整数で採点してください。\n"
    "  3: クエリの主題・内容・雰囲気に明確に合致する\n"
    "  2: 概ね合致する（主要素が一致する）\n"
    "  1: わずかに関連する程度\n"
    "  0: 関連しない\n"
    "JSON のみを返してください。"
)


def build_rubric(query: str, scale_max: int) -> str:
    """採点上限 scale_max に応じたルーブリックを返す。

    scale_max=3 は flash ベースラインと完全一致させるため固定文言を使う。
    それ以外（例: 0-10）は同点を減らし順位付けの粒度を上げる目的の汎用版。
    """
    if scale_max == 3:
        return RUBRIC_0_3.format(query=query)
    mid = scale_max // 2
    return (
        "あなたは画像検索の関連性評価者です。検索クエリと 1 枚の画像が与えられます。\n"
        f"この画像が検索クエリ「{query}」にどれだけ合致するかを 0〜{scale_max} の"
        "整数で採点してください。数値が大きいほど関連性が高いことを表します。\n"
        f"  {scale_max}: クエリの主題・内容・雰囲気に完全に合致する\n"
        f"  {mid} 前後: 部分的に合致する（主要素の一部が一致）\n"
        "  0: 関連しない\n"
        "微妙な差も数値に反映し、同点をできるだけ避けてください。JSON のみを返してください。"
    )

RESPONSE_SCHEMA = {
    "type": "OBJECT",
    "properties": {"score": {"type": "INTEGER"}},
    "required": ["score"],
}

_RETRYABLE = {429, 500, 502, 503, 504}


class TokenProvider:
    """gcloud アクセストークンを保持し、必要時に再取得する（~1h で失効）。"""

    def __init__(self) -> None:
        self._lock = Lock()
        self._token = self._fetch()

    @staticmethod
    def _fetch() -> str:
        out = subprocess.run(
            ["gcloud", "auth", "print-access-token"],
            capture_output=True, text=True,
        )
        if out.returncode != 0:
            raise RuntimeError(f"gcloud token 取得失敗: {out.stderr.strip()[:300]}")
        return out.stdout.strip()

    @property
    def token(self) -> str:
        with self._lock:
            return self._token

    def refresh(self) -> str:
        with self._lock:
            self._token = self._fetch()
            return self._token


def score_image(image_id: str, query: str, model: str, thinking: int,
                media_resolution: str | None, tokens: TokenProvider,
                scale_max: int = 3, max_retries: int = 5) -> dict:
    """1 枚の画像を pointwise 採点し {score, prompt_tokens, output_tokens} を返す。"""
    url = (
        f"https://{LOCATION}-aiplatform.googleapis.com/v1/projects/{PROJECT_ID}"
        f"/locations/{LOCATION}/publishers/google/models/{model}:generateContent"
    )
    # thinking 有効時は思考トークンが maxOutputTokens を消費し得るため上限を確保する
    # （JSON 応答は数トークン）。pro は thinkingBudget=0 不可なので呼び出し側で >0 を渡す。
    max_out = 40 if thinking == 0 else thinking + 128
    gen_cfg: dict = {
        "temperature": 0,
        "maxOutputTokens": max_out,
        "responseMimeType": "application/json",
        "responseSchema": RESPONSE_SCHEMA,
        "thinkingConfig": {"thinkingBudget": thinking},
    }
    if media_resolution:
        gen_cfg["mediaResolution"] = media_resolution
    body = json.dumps({
        "contents": [{
            "role": "user",
            "parts": [
                {"fileData": {"fileUri": image_gcs_uri(image_id),
                              "mimeType": "image/jpeg"}},
                {"text": build_rubric(query, scale_max)},
            ],
        }],
        "generationConfig": gen_cfg,
    }).encode("utf-8")

    last_err = ""
    for attempt in range(max_retries + 1):
        req = urllib.request.Request(
            url, data=body,
            headers={"Authorization": f"Bearer {tokens.token}",
                     "Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(req, timeout=120) as resp:
                data = json.loads(resp.read())
            usage = data.get("usageMetadata", {})
            cand = (data.get("candidates") or [{}])[0]
            parts = (cand.get("content") or {}).get("parts") or [{}]
            text = parts[0].get("text", "{}")
            score = int(json.loads(text).get("score", 0))
            score = max(0, min(scale_max, score))
            return {
                "score": score,
                "prompt_tokens": usage.get("promptTokenCount", 0),
                "output_tokens": usage.get("candidatesTokenCount", 0),
            }
        except urllib.error.HTTPError as e:
            last_err = f"HTTP {e.code}"
            if e.code == 401:
                tokens.refresh()
            elif e.code not in _RETRYABLE:
                raise RuntimeError(f"{image_id} '{query}': HTTP {e.code} {e.read()[:200]}")
        except (urllib.error.URLError, json.JSONDecodeError, ValueError, KeyError) as e:
            last_err = str(e)[:120]
        if attempt < max_retries:
            time.sleep(min(2.0 * (2 ** attempt), 30.0))
    raise RuntimeError(f"{image_id} '{query}': リトライ上限 ({last_err})")


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="gemini-2.5-flash")
    ap.add_argument("--depths", default="10,20,50",
                    help="再ランクする top-N（カンマ区切り）")
    ap.add_argument("--thinking", type=int, default=0,
                    help="thinkingBudget（0=思考無効でコスト最小）")
    ap.add_argument("--scale-max", type=int, default=3,
                    help="採点スケール上限（3=0-3 / 10=0-10）。キャッシュキーに含める")
    ap.add_argument("--media-resolution", default=None,
                    help="MEDIA_RESOLUTION_LOW 等。未指定で既定解像度")
    ap.add_argument("--workers", type=int, default=6)
    ap.add_argument("--no-cache", action="store_true", help="スコアキャッシュを無視")
    ap.add_argument("--cache-path", default=None,
                    help="スコアキャッシュのパス（既定 gemini_scores.json）。"
                         "実験を並列実行する際は別ファイルを指定して破損を防ぐ")
    ap.add_argument("--out", default=None,
                    help="出力 JSON パス（未指定で bq-rerank-gemini.json）")
    args = ap.parse_args()

    score_cache_path = Path(args.cache_path) if args.cache_path else SCORE_CACHE

    depths = sorted(int(d) for d in args.depths.split(","))
    max_depth = max(depths)
    pools = build_pools()

    # config 署名をキャッシュキーに含め、model/thinking/scale の変種を安全に共存させる。
    def ckey(qid: str, image_id: str) -> str:
        return f"{args.model}::t{args.thinking}::s{args.scale_max}::{qid}::{image_id}"

    cache: dict[str, dict] = {}
    if score_cache_path.exists() and not args.no_cache:
        cache = json.loads(score_cache_path.read_text(encoding="utf-8"))
    cache_lock = Lock()
    tokens = TokenProvider()

    # --- top-max_depth の候補のみ pointwise 採点（「小さい N から」を実現）-----
    # depths の最大値までの候補だけ採点する。--depths 10 で先行検証 → 10,20,50 で
    # 残りを追加採点（同一キャッシュを再利用）でき、無駄な課金を避ける。
    jobs: list[tuple[str, str, str]] = []  # (cache_key, qid, image_id)
    n_candidates = 0
    for qid, meta in pools.items():
        for image_id in meta["pool"][:max_depth]:
            n_candidates += 1
            key = ckey(qid, image_id)
            if key not in cache:
                jobs.append((key, qid, image_id))

    print(f"採点対象: {len(jobs)} 件（未キャッシュ） / "
          f"top-{max_depth} 候補 {n_candidates} 件", flush=True)

    done = [0]
    def run_job(job: tuple[str, str, str]) -> None:
        key, qid, image_id = job
        res = score_image(image_id, pools[qid]["query"], args.model,
                          args.thinking, args.media_resolution, tokens,
                          scale_max=args.scale_max)
        with cache_lock:
            cache[key] = res
            done[0] += 1
            if done[0] % 50 == 0:
                score_cache_path.parent.mkdir(parents=True, exist_ok=True)
                score_cache_path.write_text(
                    json.dumps(cache, ensure_ascii=False) + "\n", encoding="utf-8")
                print(f"  scored {done[0]}/{len(jobs)}", flush=True)

    if jobs:
        with ThreadPoolExecutor(max_workers=args.workers) as ex:
            list(ex.map(run_job, jobs))
        score_cache_path.parent.mkdir(parents=True, exist_ok=True)
        score_cache_path.write_text(
            json.dumps(cache, ensure_ascii=False) + "\n", encoding="utf-8")

    # --- N 別に再ランク → 評価（同一スコアを再利用）------------------------
    results_by_n: dict[int, dict] = {}
    for n in depths:
        rankings: dict[str, list[str]] = {}
        for qid, meta in pools.items():
            cand = meta["pool"][:n]
            scored = [
                (cache[ckey(qid, cid)]["score"], -i, cid)
                for i, cid in enumerate(cand)
            ]
            # スコア降順、同点は元の rrf_vec 順（-i 降順 = i 昇順）で安定化
            scored.sort(reverse=True)
            reranked = [cid for _, _, cid in scored]
            # n < 50 のときは末尾に残り候補を元順で温存（nDCG@10 には影響しないが
            # ランキングを完全な形で残すため）
            rest = [c for c in meta["pool"] if c not in set(cand)]
            rankings[qid] = reranked + rest
        results_by_n[n] = evaluate_rankings(rankings, pools)

    # base（再ランクなし = rrf_vec 順）も併記
    base_eval = evaluate_rankings({qid: m["pool"] for qid, m in pools.items()}, pools)

    # --- コスト試算（実測トークンから）--------------------------------------
    used = [cache[ckey(qid, cid)]
            for qid, m in pools.items() for cid in m["pool"][:max_depth]]
    avg_in = sum(u["prompt_tokens"] for u in used) / max(1, len(used))
    avg_out = sum(u["output_tokens"] for u in used) / max(1, len(used))
    cost_per_image = (avg_in * PRICE_IN_PER_1M + avg_out * PRICE_OUT_PER_1M) / 1e6
    cost = {}
    for n in depths:
        per_search = cost_per_image * n
        cost[str(n)] = {
            "per_search_usd": per_search,
            "monthly_usd": per_search * PROD_SEARCHES_PER_DAY * PROD_DAYS_PER_MONTH,
        }

    # --- 出力 ---------------------------------------------------------------
    o = base_eval["summary"]["overall"]
    oracle = o["oracle_ndcg@10"]
    print(f"\n## (c) Gemini vision rerank — nDCG@10（model={args.model}, "
          f"thinking={args.thinking}, scale=0-{args.scale_max}, "
          f"media={args.media_resolution or 'default'}）\n")
    print("| N | overall | tag_hit | tag_miss | oracle達成率 |")
    print("|---|---|---|---|---|")
    print(f"| base(rrf_vec) | {o['ndcg@10']:.3f} | "
          f"{base_eval['summary']['tag_hit']['ndcg@10']:.3f} | "
          f"{base_eval['summary']['tag_miss']['ndcg@10']:.3f} | "
          f"{o['ndcg@10']/oracle*100:.1f}% |")
    for n in depths:
        s = results_by_n[n]["summary"]
        print(f"| {n} | {s['overall']['ndcg@10']:.3f} | {s['tag_hit']['ndcg@10']:.3f} | "
              f"{s['tag_miss']['ndcg@10']:.3f} | "
              f"{s['overall']['ndcg@10']/oracle*100:.1f}% |")
    print(f"\noracle 上限 nDCG@10 = {oracle:.3f}")
    print(f"画像平均トークン: in={avg_in:.0f} out={avg_out:.1f}")
    for n in depths:
        c = cost[str(n)]
        print(f"  N={n}: ${c['per_search_usd']:.5f}/検索 → "
              f"${c['monthly_usd']:.0f}/月 (5000検索/日)")

    out = Path(args.out) if args.out else RESULTS_DIR / "bq-rerank-gemini.json"
    out.write_text(json.dumps({
        "_meta": {
            "run_at": datetime.now().isoformat(timespec="seconds"),
            "flavor": "(c) Gemini multimodal rerank over real GCS images",
            "model": args.model,
            "thinking_budget": args.thinking,
            "scale_max": args.scale_max,
            "media_resolution": args.media_resolution or "default",
            "scoring": f"pointwise 0-{args.scale_max}, JSON structured output, gs:// fileData",
            "base_ranking": "rrf_vec = RRF[VECTOR_SEARCH(JP), VECTOR_SEARCH(EN)] top-50",
            "depths": depths,
            "avg_image_input_tokens": avg_in,
            "avg_output_tokens": avg_out,
            "price_usd_per_1m": {"input": PRICE_IN_PER_1M, "output": PRICE_OUT_PER_1M},
            "prod_assumption": {"searches_per_day": PROD_SEARCHES_PER_DAY,
                                "days": PROD_DAYS_PER_MONTH},
        },
        "base_rrf_vec": base_eval["summary"],
        "rerank_by_n": {str(n): results_by_n[n]["summary"] for n in depths},
        "cost_by_n": cost,
        "per_query_by_n": {str(n): results_by_n[n]["per_query"] for n in depths},
    }, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"\nsaved: {out}")


if __name__ == "__main__":
    main()
