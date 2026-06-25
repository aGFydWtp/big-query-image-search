# BigQuery 側 nDCG 最大化プラン（rerank 全振り版）

> **改訂履歴**: 本ファイルは当初「pgvector と同型の hybrid 階段で max vs max 比較」を意図した素案だった。
> 既存の計測資産（`docs/eval-results/bq-experiments.json`, `comparison-vs-reference.md`）と
> コードベース（`internal/rewrite`, `sql/search.sql`）を grill で突き合わせた結果、
> **当初前提の多くが既に検証済み or 反証済み**であることが判明したため、目的とスコープを改訂した。
> 旧版の意図（同型比較）は §8 に縮約して残す。

---

## 0. 目的（確定）

**BigQuery ネイティブ機能で、AIC 380件コーパス・34クエリ評価セットにおける nDCG@10 を最大化する構成を見つける。**

- pgvector との「同型アブレーション比較」は **副次的関心**に格下げ（手段であって目的ではない）。
- 同一コーパス・同一評価セット・同一指標は引き続き厳守（§6）。比較可能性は保つが、
  「pgvector と同じ階段の形」に縛られて効かない構成を作ることはしない。

## 1. grill で確定した事実（着手前の前提）

すべて既存の計測資産・コードから確認済み。新規計測ではない。

| # | 事実 | 出典 |
|---|---|---|
| 1 | **現状 main は素の単一ベクトルではない。** 既に `rrf_vec`（dense ⊕ EN書き換え dense の RRF 融合）を実装・本番搭載済み。 | `sql/search.sql`, `internal/rewrite/rewriter.go` |
| 2 | **実質ベースラインは nDCG@10 = 0.670**（tag_hit 0.757 / tag_miss 0.530）。当初素案の「0.643」は `rrf_vec` 追加**以前**の値で stale。 | `docs/eval-results/bq-experiments.json`（`summary.rrf_vec`） |
| 3 | **lexical チャネルは反証済み。** 字句 BM25 を足した `hybrid` は **0.535（−0.108）**、`full` も 0.626 で `rrf_vec` 0.670 に届かず。しかもこれは理想キャプションを使った**「天井検証」**で、本番搭載可能な実装はこれより弱い。 | `bq-experiments.json`（`summary.hybrid`/`full`, `_meta.lexical`）, `comparison-vs-reference.md` L98–111 |
| 4 | **唯一の未測定レバーは L3 rerank。** 参照 Qdrant が overall 0.851 に到達した主動力も rerank。BQ で nDCG を伸ばす余地が残るとすればここ。 | `comparison-vs-reference.md` L65, L27 |

## 2. 採用方針

- **L1 lexical（字句チャネル追加）は打ち切り。** 事実 #3 より、投資対効果が証拠と逆。
  - 例外：rerank で max を出した後も tag_hit が参照に明確に負ける場合に限り、
    「自由文 BM25」ではなく「英語タグ正規化＋構造化メタ lexical」を将来レバーとして §7 に保留。
- **L3 rerank に全振り。** ただし「reranker が何の信号に対して並べ替えるか」で実装が割れるため、
  **2 系統を比較計測**する：

  | フレーバー | reranker が見る信号 | 本番搭載 | 位置づけ |
  |---|---|---|---|
  | **(a) Vertex Ranking API × 本番メタ** | `title` + `artist_display` + `classification_title`（数語）を結合した文書テキスト | ✅ 可 | 安価な対照。薄いメタでも効くなら最小コストで搭載できる |
  | **(c) Gemini マルチモーダル rerank** | クエリ文 + **実画像そのもの**（IIIF/署名 URL） | ✅ 可 | 本命。画像検索の弁別信号は画像にあり、テキスト基盤を新設せず実画像で再ランクできる |
  | (b) Vertex × 天井キャプション | 参照の JP キャプション | ❌ 不可 | **不採用**。lexical 0.535 と同じ「測れるが載らない」数字 |

- いずれも `rrf_vec`（0.670）の top-N を候補集合とし、それを**非破壊で並べ替える**。

## 3. 着手順（ゲート先行）

rerank は候補集合を**並べ替えるだけ**で、`rrf_vec` が取りこぼした正解は救えない。
よって rerank の nDCG 上限は **候補集合の recall@N で頭打ち**する。
2 系統の reranker を実装する前に、伸びしろの有無を安価に確定する。

### Phase 0 — recall ゲート（reranker 実装ゼロ・API 課金なし）

`rrf_vec` を top_k=50 で引き、以下を計測：

- **recall@10 / recall@20 / recall@50**（overall / tag_hit / tag_miss）
- **oracle nDCG@10**：top-50 候補内の正解を理想順に並べた場合の nDCG@10 上限
  = (a)/(c) いずれの rerank も**理論上これを超えられない**目標値。

実装：`scripts/eval_rerank_gate.py`（既存 `eval_bq_search.py` / `eval_bq_experiments.py` の `bq_search` と RRF を再利用）。

### Phase 1 — (a)+(c) 実装（Phase 0 が GO の場合のみ）

- 候補数 N は Phase 0 の recall@N 曲線が天井に近づく点を採用（過剰な N はコスト増）。
- (a) Vertex Ranking API、(c) Gemini vision の両方で top-N を再ランク → 同一指標で計測。
- (c) は **N を品質×コストのスイープ対象**にする（N=10/20/50 で nDCG とコスト曲線）。

## 4. ゲート判定基準（Phase 0 → Phase 1 の GO/NO-GO）

- **recall@50 ≫ recall@10**（正解が rank 11–50 に居る）→ **GO**。rerank に実伸びしろあり。
  oracle nDCG@10 が現状 0.670 をどれだけ上回るかが、(a)/(c) 投資の上限リターン。
- **recall@50 ≈ recall@10**（正解が top-50 にすら居ない）→ **NO-GO**。
  問題は順序ではなく retrieval（取りこぼし）。rerank は無効で、埋め込み改善・候補拡張に方針転換。

## 5. 計測とアウトプット（Phase 1）

各 reranker 構成について:

1. **品質**: precision@10 / capped recall@10 / nDCG@10 を overall / tag_hit / tag_miss で。
   oracle 上限（Phase 0）との達成率も併記。
2. **レイテンシ**: エンジン純粋 / end-to-end / コールドスタート。ウォーム 50〜100 回、p50/p95/p99。
   rerank は候補数 N に強く依存するため N 別に出す。
3. **コスト**: 1 検索あたり。**(c) Gemini vision × N 枚 × 34 クエリが支配項**になるため、
   想定本番規模（画像1万件・検索1日5千回）での月額を N 別に試算し、品質との曲線で示す。
4. 結果 JSON を `docs/eval-results/` に出力（例: `bq-rerank-vertex.json`, `bq-rerank-gemini.json`）。
5. `comparison-vs-reference.md` に rerank 構成の比較表を追記。

## 6. 同一性の担保（据え置き・絶対条件）

- コーパス: 既存 `image_embeddings`(380件)。再生成しない。
- 評価セット: 既存の 34 クエリ（tag_hit 21 / tag_miss 13）。
  > 注意（grill 指摘）: `scripts/eval_bq_search.py` / `eval_bq_experiments.py` は評価セットを
  > **別リポの絶対パス** `/Users/haruki/Documents/docker-dev/hr20k/image-search/config/eval_queries.json`
  > から読む。本リポ内には存在しないクロスリポ依存である点に留意（移植時は要対応）。
- 指標ロジック: 既存 `eval_bq_search.py`（`precision_at_k` / `recall_at_k` / `ndcg_at_k`）を再利用。
  各構成の top-N image_id 列を差し替えるだけで同一指標が出る形を維持。

## 7. 将来レバー（スコープ外・保留）

- **構造化メタ lexical**: 自由文 BM25（反証済み）ではなく、英語タグ正規化＋`title`/`artist`/`style`/`classification`
  への `SEARCH()` / `ML.TF_IDF`。rerank で max を出してなお tag_hit が参照に負ける場合のみ、英語タグ生成基盤込みで一度だけ検証。
- 参照 Phase3 の「該当なし判定」。記載のみ。
- `gemini-embedding-2-preview` の preview 制約・GA 状況の確認（モデルが動くと全数値が動くため）。

## 8. 旧版の意図（縮約・参考）

当初は pgvector hybrid（L0 Dense → L1 +Lexical RRF → L2 +クエリ書き換え → L3 +Rerank）と
**同型の階段**を BQ に組み、RRF 定数 `k`・候補数・top_k を両実装で揃えて engine 同士を公平比較する狙いだった。
この「同型比較」は事実 #1〜#3 により大きく崩れた（BQ 側は既に rrf_vec まで進んでおり、lexical は逆効果）。
比較可能性（同一コーパス/評価セット/指標）は §6 で維持しつつ、目的は nDCG 最大化（§0）に改めた。
