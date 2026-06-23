# スコア比較: BigQuery (gemini-embedding-2) vs 参照リポジトリ (Qdrant ハイブリッド)

計測日: 2026-06-23

## 目的

本プロジェクト（BigQuery `AI.GENERATE_EMBEDDING` + `VECTOR_SEARCH`）の検索品質を、参照リポジトリ
[aGFydWtp/image-search](https://github.com/aGFydWtp/image-search) の
[`docs/search-quality-improvement.md`](https://github.com/aGFydWtp/image-search/blob/main/docs/search-quality-improvement.md)
と**同一データセット・同一評価セット・同一指標**で比較する。

## 比較条件（同一性の担保）

| 項目 | 内容 |
|---|---|
| コーパス | AIC（Art Institute of Chicago）Public Domain 作品 **380 件**。参照の `artworks_aic_v1` と同一集合（`eval/aic_corpus_ids.json`、出典: 参照 `config/eval_captions_ja.json`）。同じ IIIF 843px 画像を GCS へ投入 |
| 評価セット | 参照 `config/eval_queries.json`（**34 クエリ**、tag_hit 21 / tag_miss 13）。正解集合・分類とも参照と完全同一 |
| 指標 | precision@10 / capped recall@10 / nDCG@10（binary relevance）。参照 `scripts/eval_metrics.py` と数値同一のロジックを移植（`scripts/eval_bq_search.py`） |
| 計測対象（本プロジェクト） | クエリ文を `gemini-embedding-2-preview`（3072 次元・マルチモーダル）で埋め込み → `image_embeddings`(380件) に対し `VECTOR_SEARCH(COSINE)` top_k=10。**クエリ書き換え・ハイブリッド(BM25)・タグ prefilter・リランカーはすべて無し**（素の単一ベクトル text→image 検索） |

## 結果（nDCG@10）

| 構成 | tag_miss | tag_hit | overall | 備考 |
|---|---|---|---|---|
| 参照 baseline（SigLIP2 text-text、純ベクトル）※Phase0.5 再計測 | 0.109 | 0.779 | 0.523 | 日本語クエリ→英語キャプションの text-text 照合 |
| 参照 Phase 1（LLM 書き換え + image_semantic RRF 融合） | 0.284 | 0.827 | 0.620 | 再インデックス不要のクイックウィン |
| 参照 Phase 2 hybrid（e5 + SigLIP2 + BM25 を RRF 融合）★参照ベスト | **0.647** | **0.978** | **0.851** | 再インデックス＋3 系統ハイブリッド |
| **本プロジェクト BQ gemini-embedding-2（単一ベクトル・素）** | **0.507** | 0.726 | 0.643 | 書き換え・ハイブリッド・フィルタなし |

参考: precision@10 / recall@10（本プロジェクト）

| group | n | precision@10 | recall@10 | nDCG@10 |
|---|---|---|---|---|
| overall | 34 | 0.597 | 0.645 | 0.643 |
| tag_hit | 21 | 0.657 | 0.735 | 0.726 |
| tag_miss | 13 | 0.500 | 0.500 | 0.507 |

結果 JSON: [`bq-gemini-embedding-2.json`](bq-gemini-embedding-2.json)

## 解釈

### 1. tag_miss（雰囲気系・純粋な意味検索）が圧倒的に強い

本プロジェクトの素の単一ベクトル検索（tag_miss nDCG **0.507**）は、参照の baseline（0.109）・Phase 1（0.284）を**大きく上回り**、参照が再インデックス＋3 系統ハイブリッドまでやって到達した Phase 2（0.647）に迫る。

参照ドキュメントの根本原因分析が「SigLIP 系テキストエンコーダの text-text 照合は弱い／本来の得意領域は text→image」と指摘していた点を、別の角度から裏付ける結果。`gemini-embedding-2` はマルチモーダル埋め込みで**真の text→image クロスモーダル検索を 1 ベクトルで成立**させるため、参照が書き換え・RRF・再インデックスを積み上げて稼いだ改善の大半を、素のクエリ・単一ベクトルだけで得られている。

### 2. tag_hit は参照に劣る

本プロジェクトの tag_hit nDCG **0.726** は、参照（baseline 0.779 〜 Phase 2 0.978）を下回る。参照はタグ prefilter＋BM25 字句一致＋RRF を持ち、**正解集合がタグ由来である tag_hit クエリ**を取りこぼさない。本プロジェクトは字句／タグチャネルを持たない純ベクトルのため、固有名詞・様式名などの字句一致で差がつく。

> 注: 参照ドキュメント自身が「tag_hit は正解集合＝フィルタと同じタグという循環がある」と明記している。tag_hit の絶対値はタグ／字句ベース手法に構造的に有利で、この差の一部は評価バイアス由来。

### 3. per-query の傾向（tag_miss）

- 強い: 「印象派の作品」nDCG **1.00**、「優雅で上品な作品」0.91、「アールヌーヴォー様式の作品」0.89 — 様式・主題が視覚特徴に直結する語
- 弱い: 「厳かな雰囲気の絵」0.00、「ノスタルジックな雰囲気」0.14 — 抽象的な情緒語

抽象情緒語の取りこぼしは、参照が Phase 3 として残した「該当なし判定」やクエリ書き換えで改善余地がある領域。

## まとめ

- **同一データセット・同一評価セット・同一指標**で計測。比較は公平な土俵で成立している。
- 位置づけとしては「本プロジェクト = 参照の `image_semantic`（text→image 単系統）に相当する素の構成」。それが**素のままで参照 Phase 1 を超え、tag_miss では参照ベストの 8 割の水準**に達するのは、`gemini-embedding-2` の埋め込み品質が高いことを示す。
- 一方、overall（0.643）で参照 Phase 2（0.851）に届かない主因は **tag_hit の字句／タグチャネル不在**。本プロジェクトで overall を伸ばすなら、参照と同じく「クエリ書き換え」「BM25 等の字句チャネル」「該当なし判定」の追加が有効と見込まれる。

### 注意・限界

- `gemini-embedding-2-preview` は Preview ステージのモデル。
- 比較は「素の単一ベクトル」vs「参照の多段パイプライン」であり、**モデル単体の差とパイプライン設計の差が混在**する。参照は単系統 image_semantic の単独スコアを公開していないため、完全な「モデル対モデル」分離はできていない。
- 正解集合は参照の Qdrant タグ／キャプションからルールベースで決定論生成されたもの。tag_hit のバイアス（上記）に留意。
- レイテンシ・コストは本比較の対象外。

## overall 改善の検証 (2026-06-23)

参照が overall を伸ばした 2 レバー —— ③ LLM クエリ書き換え、② 字句(BM25)＋RRF 融合 —— が
本プロジェクト（gemini-embedding-2）でも効くかを、同一条件で検証した（`scripts/eval_bq_experiments.py`）。

### 計測モード

| mode | 構成 |
|---|---|
| raw | ベクトル検索（生 JP クエリ）= 上記基準 |
| rewritten | ベクトル検索（英語リライト `eval/rewritten_queries.json`） |
| rrf_vec | RRF[ ベクトル(JP) + ベクトル(EN) ] |
| hybrid | RRF[ ベクトル(JP) + 字句 BM25 ] |
| full | RRF[ ベクトル(JP) + ベクトル(EN) + 字句 BM25 ] |

字句チャネルは本プロジェクトに存在しない（キャプション基盤を持たない）ため、参照公開の JP キャプション
（`eval_captions_ja.json`）への char-bigram BM25 で代替した**天井検証**。RRF は参照同様 k=60、各系統候補 50 件。

### 結果（nDCG@10）

| mode | tag_miss | tag_hit | overall | 対 raw |
|---|---|---|---|---|
| raw | 0.507 | 0.726 | 0.643 | — |
| rewritten | 0.530 | 0.714 | 0.643 | ±0 |
| **rrf_vec** | 0.530 | **0.757** | **0.670** | **+0.027** ✅ |
| hybrid | 0.442 | 0.593 | 0.535 | −0.108 ❌ |
| full | 0.522 | 0.691 | 0.626 | −0.017 ❌ |

結果 JSON: [`bq-experiments.json`](bq-experiments.json)

### 結論

- **rrf_vec（生 JP ベクトル + 英語リライトベクトルの RRF 融合）が唯一の正味改善**。overall 0.643 → **0.670**。tag_miss・tag_hit とも改善。
  生 JP は具体物クエリ（女性・船・橋）に強く、英語リライトは様式・情緒クエリに強い。両者を融合すると双方を取りこぼさない。
  **新インフラ不要・追加コストはクエリ埋め込み 1 本分のみ**で、本プロジェクトに即適用できる現実的な改善。
- 英語リライト単独（rewritten）は tag_miss を上げ tag_hit を下げ、差し引き中立。参照 Phase 2 の「dense 系統は raw 直接が最良、書き換えは融合の一系統として使う」という知見と整合。
- **素朴な字句チャネルは逆効果**（hybrid 0.535、full 0.626）。JP キャプションへの char-bigram BM25 は自由文ゆえ誤マッチが多く、RRF で良い系統を希釈する。
  参照の字句利得は「英語にキュレーションされたタグ＋タグ prefilter＋BM25」という構成と、正解集合がそのタグ由来であること（評価バイアス）に支えられたもので、**素朴な字句追加では再現しない**。本プロジェクトで字句利得を得るには、参照同様のキャプション/タグ生成基盤の追加が前提となる。

### 推奨

1. **即時**: 検索 API に「生クエリ + 英語リライト」の 2 ベクトル RRF 融合を導入（rrf_vec）。overall +0.027 を低コストで得られる。
2. **中期**: tag_hit をさらに伸ばすなら、キャプション/タグ生成（VLM）＋字句チャネルの追加を検討。ただし naive 実装は逆効果のため、英語タグへの正規化と prefilter 設計が必須。
3. 抽象情緒語（「厳かな」「ノスタルジック」）の取りこぼしは、参照 Phase 3「該当なし判定」と合わせて別途対処。

## 再現手順

```bash
# 1) 参照と同一の 380 件を GCS へ投入（既存はスキップ）
IMAGE_BUCKET="$(terraform -chdir=terraform output -raw image_bucket_name)" \
  .venv/bin/python scripts/aic_seed_ids.py

# 2) 埋め込み生成（MERGE）。その後コーパスを厳密に 380 件へ揃える
#    （DELETE: image_uri の aic-id が eval/aic_corpus_ids.json に無い行を削除）

# 3) 基準計測（34 クエリ・素の単一ベクトル）
.venv/bin/python scripts/eval_bq_search.py --label bq-gemini-embedding-2

# 4) 改善検証（書き換え / 字句 / RRF 融合の 5 モード）
.venv/bin/python -m scripts.eval_bq_experiments
```

> 運用メモ: GCS バケットには過去の random seed 由来の余剰 blob が残存していた（計 716）。
> 2026-06-23 に参照外 336 件を削除し、**GCS・`image_embeddings` とも厳密に 380 件**へ揃えた。
> これにより `generate_embeddings.sql` の MERGE を再実行してもコーパスは 380 件で安定する（再投入なし）。
