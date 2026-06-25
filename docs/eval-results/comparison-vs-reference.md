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

## rerank recall ゲート（2026-06-26）

次の改善レバーとして **L3 rerank** に投資する前に、伸びしろの有無を安価に確定するゲートを実施
（reranker 実装ゼロ・API 課金なし）。rerank は候補集合を並べ替えるだけなので、nDCG 上限は
候補プールの recall で頭打ちする。`rrf_vec`（= 現状ベスト 0.670）の **top-50 候補プール**について、
recall@N と「oracle nDCG@10（プール内の正解を理想順に並べた場合の上限）」を計測した。

| group | recall@10 | recall@20 | recall@50 | 現状 nDCG@10 | **oracle nDCG@10** |
|---|---|---|---|---|---|
| overall | 0.665 | 0.633 | 0.753 | 0.670 | **0.963** |
| tag_hit | 0.766 | 0.730 | 0.842 | 0.757 | 0.995 |
| tag_miss | 0.500 | 0.477 | 0.609 | 0.530 | 0.913 |

結果 JSON: [`bq-rerank-gate.json`](bq-rerank-gate.json) / 計測: `.venv/bin/python -m scripts.eval_rerank_gate`

### 判定: **GO**

- **rerank の伸びしろは大きい**。完璧な reranker が top-50 を理想順に並べれば nDCG@10 は
  **0.670 → 0.963（+0.293）**。正解は候補プールに入っており、順序が悪いだけ。
- **oracle 上限 0.963 は参照 Qdrant Phase 2（0.851）すら上回る**。retrieval も lexical もいじらず、
  既存 `rrf_vec` の候補に rerank を載せるだけで、理論上は参照ベストを超えうる。
- 最大の鉱脈は **tag_miss**（0.530 → oracle 0.913）。抽象クエリは正解が候補に居るのに沈んでいる。
- 留保: oracle は完璧 reranker の上限で、実装は一部しか取れない。また q19「ノスタルジック」
  (recall@50=0.14)・q22「厳かな雰囲気」(0.29) は **top-50 にすら正解が不足**＝rerank では救えず
  retrieval 改善が別途必要（少数）。34 クエリは小標本につき方向性の判断材料。

### 次フェーズ（Phase 1）

`rrf_vec` top-N を非破壊で再ランクする 2 系統を同一指標で計測（→ `claudedocs/bq-hybrid-maxvsmax-plan.md`）:
- **(a) Vertex Ranking API × 本番メタ**（title+artist+classification）— 安価な対照。
- **(c) Gemini マルチモーダル × 実画像**（IIIF/署名 URL）— 本命。N を品質×コストでスイープ。

## rerank Phase 1 結果 (2026-06-26)

Phase 0 ゲートが GO（oracle nDCG@10=0.963）と判定したため、`rrf_vec` の top-N を
**非破壊で並べ替える** 2 系統を同一指標で計測した（`claudedocs/bq-hybrid-maxvsmax-plan.md`）。
両系統とも候補プールは `rrf_vec` top-50（gate と同一・`eval/rerank_cache/pools.json` に
キャッシュ、再ランク前の base nDCG@10=0.670 が gate と一致することを確認）。
N=10/20/50 は top-50 を 1 回採点し、サブセットで導出（スイープでの再課金なし）。

### (c) Gemini マルチモーダル × 実画像 ★本命・正味改善

`gemini-2.5-flash` に GCS 実画像（`gs://` 直接参照）＋クエリ文を渡し、各画像を pointwise で
0–3 採点 → スコア降順（同点は `rrf_vec` 順）で再ランク。thinking=0。

| 構成 | tag_miss | tag_hit | overall | 対 base | oracle達成率 |
|---|---|---|---|---|---|
| base (rrf_vec) | 0.530 | 0.757 | 0.670 | — | 69.5% |
| Gemini N=10 | 0.528 | 0.767 | 0.676 | +0.006 | 70.1% |
| Gemini N=20 | 0.550 | 0.793 | 0.700 | +0.030 ✅ | 72.7% |
| **Gemini N=50** | **0.576** | **0.808** | **0.719** | **+0.049** ✅ | 74.6% |

結果 JSON: [`bq-rerank-gemini.json`](bq-rerank-gemini.json) / 計測: `.venv/bin/python -m scripts.eval_rerank_gemini`

- 実画像 rerank は **N が大きいほど伸びる**（N=50 が最良）。候補を多く並べ替えるほど oracle 頭打ちに近づく。overall 0.670 → **0.719**、tag_hit・tag_miss とも改善。
- ただし **oracle 0.963 の 74.6% 止まり**。flash の pointwise 0–3 採点では、頭打ち余地（+0.293）の約 1/6（+0.049）しか取れていない。参照 Phase2(0.851) も未達。
- コスト: 画像 ≈1731 input tokens/枚（843px・既定解像度＋日本語ルーブリック）、出力 ≈6 tokens。$0.30/$2.50 per 1M（gemini-2.5-flash, 2026-06）。**N=50 で $0.0267/検索 → 5000検索/日で ≈$4,000/月**、N=20 で ≈$1,600/月。

### (a) Vertex Ranking API × 本番メタ — 反証（劣化）

`semantic-ranker-default-004` に文書テキスト（title + artist_display + classification_title）を
渡して再ランク。メタは image_embeddings に無いため AIC API から再取得（`eval/aic_corpus_meta.json`、
380 件・欠損 0）。クエリは英語リライト(en) を主、生 JP(jp) を対照に両計測。

| 構成 | tag_miss | tag_hit | overall | 対 base | oracle達成率 |
|---|---|---|---|---|---|
| base (rrf_vec) | 0.530 | 0.757 | 0.670 | — | 69.5% |
| en N=10 | 0.531 | 0.740 | 0.660 | −0.010 ❌ | 68.5% |
| en N=20 | 0.501 | 0.666 | 0.603 | −0.067 ❌ | 62.6% |
| en N=50 | 0.481 | 0.583 | 0.544 | −0.126 ❌ | 56.5% |
| jp N=10 | 0.526 | 0.728 | 0.651 | −0.019 ❌ | 67.6% |

結果 JSON: [`bq-rerank-vertex.json`](bq-rerank-vertex.json) / 計測: `.venv/bin/python -m scripts.eval_rerank_vertex`

- **全構成が base を下回り、N が増えるほど悪化**。数語の薄いテキストメタは画像検索の弁別信号として弱く、良好な `rrf_vec` 順を崩す方向に働く。
- en > jp（英語メタには英語クエリがやや有利）だが、どちらも負け。
- 既出の「素朴な字句チャネルは逆効果（hybrid 0.535）」と同じ結論を別経路で再確認 ——
  **画像検索の弁別信号はテキストメタではなく画像にある**。
- コスト: 1 検索 = 1 ランキングコール（N≤100 で 1 課金単位 ≈$0.001）→ ≈$150/月。安価だが品質で負けるため不採用。

### Phase 1 結論

- **実画像 Gemini 再ランク (c) のみが正味改善**（overall +0.049, N=50）。本番搭載候補。コスト×品質の選択肢は N=20（+0.030, ≈$1,600/月）/ N=50（+0.049, ≈$4,000/月）。
- **(a) Vertex Ranking は反証（劣化）**。lexical(hybrid) 反証に続き「テキスト信号での rerank/融合は効かない」を補強。
- (c) は oracle の 74.6% 止まり。残レバー（より強いモデル / thinking / listwise / 採点スケール細分化）を Phase 1b で計測 → **採点スケール細分化のみが正味改善**、他 3 つは反証（下記）。
- 救えない少数（rerank 対象外＝retrieval 課題）: q19「ノスタルジック」(recall@50=0.14)・q22「厳かな雰囲気」(0.29) は候補プールに正解が不足。
- 留保: 34 クエリは小標本。pointwise スコアは temperature=0 だがモデル更新で変動しうる。

## rerank Phase 1b: oracle 接近の追加検証 (2026-06-26)

Phase 1 の (c) が oracle 0.963 の 74.6% 止まりだったため、4 つの残レバーを同一プール・
同一指標で計測した。**結論: 採点スケール細分化（0–3→0–10）のみが正味改善。より強いモデル・
thinking・listwise はいずれも反証（base の良好な `rrf_vec` 順を崩す方向）**。

| レバー | 構成（N=50） | tag_miss | tag_hit | overall | 対 flash 0–3 | oracle達成率 |
|---|---|---|---|---|---|---|
| — | base (rrf_vec) | 0.530 | 0.757 | 0.670 | −0.049 | 69.5% |
| Phase 1 | flash 0–3 t0（前ベスト） | 0.576 | 0.808 | 0.719 | — | 74.6% |
| **4. スケール 0–10** | **flash 0–10 t0** | **0.606** | **0.817** | **0.736** | **+0.017 ✅** | **76.4%** |
| 4. スケール 0–100 | flash 0–100 t0 | 0.654 | 0.788 | 0.737 | +0.018 | 76.5% |
| 2. thinking | flash 0–3 t512 | 0.551 | 0.796 | 0.702 | −0.017 ❌ | 72.9% |
| 1. 強モデル | pro 0–3 t128 | 0.531 | 0.803 | 0.699 | −0.020 ❌ | 72.6% |
| 3. listwise | flash listwise（N=20のみ） | 0.550 | 0.765 | 0.683※ | (N=20比で負け) ❌ | 70.9% |

※ listwise は N=20。同 N の pointwise（0–3=0.700 / 0–10=0.716）に対し 0.683 と劣る。

結果 JSON: [`bq-rerank-gemini-s10.json`](bq-rerank-gemini-s10.json) / [`-s100`](bq-rerank-gemini-s100.json)
/ [`-think`](bq-rerank-gemini-think.json) / [`-pro`](bq-rerank-gemini-pro.json)
/ [`bq-rerank-listwise.json`](bq-rerank-listwise.json)

- **採点スケール細分化が唯一効くレバー**。0–3 は同点が多く、同点は `rrf_vec` 順に丸められて
  モデルの細かい関連性判断が捨てられる。0–10 にすると同点が減り、判断が順位へ反映 → overall
  0.719 → **0.736**（tag_hit・tag_miss とも改善）。コストは flash 据え置き（≈$4,000/月 @N=50）。
- **0–100 は頭打ち**。overall は 0–10 と実質同値（+0.001）。tag_miss は 0.654 と更に伸びるが
  tag_hit が 0.788 へ低下しトレードオフ。粒度の限界効用は 0–10 で尽きる → **0–10 を採用**。
- **より強いモデル（pro）は反証**。pro は `thinkingBudget=0` を許可せず（HTTP 400）、最小 128 でも
  flash 0–3 を下回る（0.699 < 0.719）。特に tag_miss 0.531 と雰囲気系で弱い。深い推論は不要。
- **thinking も反証**。flash に t512 を与えると 0.702 へ劣化。pointwise 関連性採点は素早い直観が良く、
  熟考はむしろノイズ。pro・thinking が共に負けたのは「画像検索の弁別は深い推論より校正された
  関連性スコアにある」という (a) Vertex 反証と同じ示唆。
- **listwise も反証**。N 枚一括順位付けは base は上回るが pointwise に負け（順序バイアス・長文脈）。
  pointwise 採用の設計判断（plan §2(c)）をデータで裏付け。N=50 は ≈6.5万トークンで未計測。
- 救えない少数（q19「ノスタルジック」・q22「厳かな雰囲気」）は依然 retrieval 課題で rerank 対象外。

### Phase 1b 結論

- **本番搭載の勝ち構成 = `gemini-2.5-flash` pointwise 採点スケール 0–10・thinking=0・N=50**。
  overall nDCG@10 = **0.736**（rrf_vec base 0.670 比 +0.066、Phase 1 ベスト 0.719 比 +0.017）。
  oracle 0.963 の **76.4%**。コストは 0–3 と同等（flash・画像トークン据え置き）。
- 実装変更は **採点ルーブリックの 0–3 → 0–10 化のみ**（モデル・候補プール・融合は不変）。
  internal/ 検索 API への組込みは別タスク（rerank ルーブリックを 0–10 にするだけ）。
- oracle まで残り +0.227。スケール粒度では埋まらず、モデル/thinking/listwise は逆効果のため、
  さらなる接近は **候補プール（retrieval）側の改善**（救えない少数の recall）が次の主戦場。

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
