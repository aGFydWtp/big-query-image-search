# Gap Analysis — image-search-api

> 既存コードベース（上流 2 spec 実装済み）と本仕様 requirements/design の実装ギャップ分析。
> 設計判断ではなく、設計・実装フェーズへ引き継ぐ情報と選択肢を提示する。
> 対象コミット時点: 上流 `gcp-infrastructure`（terraform/）・`image-ingestion-pipeline`（sql/, docs/runbook.md）実装済み。本仕様のアプリコード（src/, deploy/）は未着手のグリーンフィールド。

## 1. 現状調査サマリ

- **本仕様のアプリ資産は皆無**: `src/`・`deploy/`・`sql/search.sql`・アプリ用 `docs/runbook.md` セクションは未作成。`package.json`/`go.mod`/`pyproject.toml` 等の言語マニフェストも無く、**完全グリーンフィールド**。design.md「Modified Files: なし」と整合。
- **上流の消費対象は実在し確定済み**:
  - 共有契約テーブル `sql/embeddings_table.sql` → `${DATASET_ID}.image_embeddings`（列 `image_uri` / `embedding` ARRAY<FLOAT64> dim=3072 / `content_type` / `generated_at`）。
  - リモートモデル `sql/remote_model.sql` → **オブジェクト名 `gemini_embedding_model`**、エンドポイント `gemini-embedding-2-preview`（Preview）。
  - VECTOR INDEX `sql/vector_index.sql` → `image_embeddings_idx`、`distance_type='COSINE'`、`index_type='IVF'`。
  - 上流出力 `terraform/outputs.tf` → `image_bucket_name` / `bigquery_dataset_id`（`project.dataset` 形式）/ `cloud_run_service_account_email` / `bigquery_connection_id` / `bigquery_connection_service_account`。
- **再利用すべき既存規約（パターン）**:
  - 環境依存値の `${VAR}` プレースホルダ外部化 + `envsubst` 注入（`sql/params.example`）。本 API の Config もこの output 名・注入規約を踏襲できる。
  - 各ファイル先頭に対応 Requirement ID / design セクションを記すトレーサビリティコメント規約（structure.md）。
  - 単一 `region`（`us-central1`）整合、ハードコード禁止、最小権限という横断ルール（tech.md / structure.md）。

## 2. Requirement → 資産マップ（ギャップタグ）

| Requirement | 必要能力 | 既存資産 | ギャップ |
|---|---|---|---|
| 1.x 検索エンドポイント / req-res 契約 | HTTP サーバ・ハンドラ・JSON スキーマ | なし | **Missing**（全新規） |
| 2.1-2.4 クエリ埋め込み + VECTOR_SEARCH 同一クエリ | `AI.GENERATE_EMBEDDING`（テキスト入力）+ `VECTOR_SEARCH` の単一 SQL | 取込側は `AI.GENERATE_EMBEDDING`（**TABLE 入力**）の実績あり | **Unknown**（テキスト・スカラ入力での関数シグネチャ未確定。後述 R1） |
| 2.5-2.6 共有契約消費・再定義禁止 | モデル/テーブル/列/距離/dataset を注入参照 | 契約値は SQL 資産で確定 | **Constraint**（注入値 `${MODEL}`＝**オブジェクト名 `gemini_embedding_model`**。tasks.md/steering の旧名と不整合。後述 G2） |
| 3.1, 3.4, 3.5 結果整形（URI/スコア/メタ） | 距離→スコア変換・整列・任意メタ | なし | **Missing** |
| 3.2, 3.3 GCS 署名付き URL | Run SA による keyless V4 署名 | **Run SA に GCS 権限・signBlob 権限が未付与** | **Constraint（ブロッカー）**。後述 G1 |
| 4.x エラー処理・境界 | 400/5xx マッピング・詳細非漏洩・空結果 | なし | **Missing** |
| 5.1, 5.3-5.5 Cloud Run デプロイ・最小権限 | Dockerfile / service.yaml / 実行 SA 割当 | Run SA 本体（`cloud_run_sa.tf`）と BQ/Vertex IAM（`iam.tf`）は払い出し済み | **Missing**（デプロイ構成）＋ 5.4 の権限前提に齟齬（G1） |
| 5.2 パラメータ注入 | env 経由設定・起動時検証 | 注入規約（params.example）あり | **Missing**（Config 実装）／パターンは流用可 |
| 5.6 再現可能手順 | runbook（build/deploy/local） | 取込 runbook の体裁あり | **Missing**（API 用節） |

## 3. 重要ギャップ詳細

### G1（ブロッカー）: Cloud Run 実行 SA に署名付き URL 用の権限が無い
`terraform/iam.tf` が Run SA（`google_service_account.cloud_run`）に付与するのは以下 3 つのみ:
- `roles/bigquery.jobUser`（project）
- `roles/bigquery.dataViewer`（dataset スコープ）
- `roles/aiplatform.user`（project）

`storage.*` ロールも `roles/iam.serviceAccountTokenCreator` も**未付与**（`grep` で Run SA への storage/token 付与は 0 件）。一方、design「Allowed Dependencies / Security」と Requirement 5.4 は「実行 SA に**付与済みの**最小権限（GCS 署名/読取）」を前提に署名 URL を発行する設計。Cloud Run はキーファイルを持たないため、V4 署名 URL 生成には **IAM Credentials の `signBlob`（＝Run SA 自身への `roles/iam.serviceAccountTokenCreator`）** が必須で、対象オブジェクト読取に `roles/storage.objectViewer`（images バケットスコープ）も通常必要。

→ **現状の IAM では Requirement 3.2/3.3 が実現不可。** IAM は上流 `gcp-infrastructure` 所有のため、本仕様単独では解消できない。**上流 IAM への追加（Run SA への `serviceAccountTokenCreator` + images バケットの `storage.objectViewer`）が必要で、これは `gcp-infrastructure` の Revalidation Trigger に該当。** 設計フェーズで「上流 IAM 追補」か「署名 URL 機能の段階導入（初期は URI のみ返却）」かを決める必要がある。

### G2: モデル名のドリフト（tasks.md / steering が旧名のまま）
- 実装済み SQL 資産・design.md は **エンドポイント `gemini-embedding-2-preview` / オブジェクト名 `gemini_embedding_model`** に統一済み（image-ingestion-pipeline Task 0, 2026-06-19 で是正）。
- しかし本仕様の `tasks.md`（Task 1.1, 2.2）は依然 **`gemini-embedding-2`** と記載。`.kiro/steering/tech.md`・`roadmap.md` も旧名 `gemini-embedding-2`。
- 機能的には検索 SQL が参照するのは**モデルオブジェクト名 `gemini_embedding_model`**（注入値 `${MODEL}`）であり、エンドポイント名は取込側 DDL の関心事。design はこれを正しく反映済み。**実害は Config/タスク記述の表記揺れ**だが、実装者の混乱・誤ったハードコードを招くため、設計確定時に tasks.md と Config が「注入する値＝オブジェクト名 `gemini_embedding_model`」である点を明示しておくのが安全。

## 4. Research Needed（設計フェーズへ繰り越し）

- **R1（高）: テキストクエリの `AI.GENERATE_EMBEDDING` シグネチャと VECTOR_SEARCH 結合**
  取込は `AI.GENERATE_EMBEDDING(MODEL, TABLE <object_table>, STRUCT(3072 AS output_dimensionality))`（TABLE 入力・出力 `embedding`/`status`、成功は `status=''`）。検索はスカラのテキスト 1 件入力。GoogleSQL での (a) テキスト/サブクエリ入力形、(b) **クエリ側でも `output_dimensionality=3072` を明示し次元一致させる方法**、(c) 得た `embedding` 配列を `VECTOR_SEARCH` のクエリ側へ単一クエリ（CTE/サブクエリ）で渡す具体形、を一次情報で確定する。design の Query Shape（251-254 行）は方針止まりで、`output_dimensionality` の明示が未記載。**要 Context7/公式ドキュメント確認、可能なら実機 dry-run。**
- **R2（中）: 実装言語・フレームワーク・BQ/GCS クライアント選定**
  design は `{ext}` で未確定。署名 URL の keyless 生成（signBlob 経由）の成熟度が SDK で差がある点が選定基準。候補: Python（`google-cloud-bigquery` + `google-cloud-storage`、軽量 HTTP に Flask/FastAPI）/ Go / Node。
- **R3（低）: VECTOR INDEX の populate 下限挙動**
  base table が約 10MB 未満ではインデックス未 populate でブルートフォースにフォールバック（`vector_index.sql` 注記）。検索は機能するがレイテンシ/テスト期待値に影響。テスト戦略で考慮。
- **R4（低）: スコア表現の定義**
  `VECTOR_SEARCH` は COSINE *距離* を返す。Requirement 3.4 の「一貫したスコア表現」を距離のままか `1 - distance` の類似度かを設計で固定。

## 5. 実装アプローチの選択肢

新規サービスのため「既存拡張 vs 新規」の軸は実質**新規一択**だが、*既存規約への準拠度*で 3 案に整理する。

### Option A: 上流規約フル準拠の層分離サービス（design 準拠）
design の File Structure（handler/validation/search_query/bigquery_client/result_formatter/signed_url/config + sql/search.sql + deploy/）をそのまま実装。`${VAR}` 注入・トレーサビリティコメント・最小権限を踏襲。
- ✅ design・steering と完全整合、レビュー容易、テスト境界が明快
- ✅ 上流の注入規約（params.example）と一貫し運用が揃う
- ❌ ファイル数が多くグリーンフィールド初期コストはやや高い
- ❌ G1（署名 URL 権限）未解決のままだと 3.2/3.3 が空洞化

### Option B: 署名 URL を段階導入する MVP 先行
まず URI + スコア返却（Requirement 1/2/3.1/3.4/4/5 のコア）を完成させ、署名 URL（3.2/3.3）は上流 IAM 追補後に後続フェーズで追加。`signed_url` フラグは受理するが初期は常に省略 + 明示。
- ✅ G1 のブロッカーを迂回して検索コア価値を最短で提供
- ✅ 上流 IAM 変更（Revalidation）を並行依存として切り出せる
- ❌ Requirement 3.2/3.3 が初期未充足（spec 完了判定には追補が前提）
- ❌ 機能フラグの一貫性管理が必要

### Option C: 署名方式を keyless 前提で先に固定し IAM 追補を必須化
設計時に「Run SA 自身への `serviceAccountTokenCreator` + images バケット `storage.objectViewer`」を**上流 `gcp-infrastructure` への追補タスク**として明示起票し、A をフル実装。
- ✅ Requirement 全充足を一回で達成、権限設計が明確
- ✅ 最小権限を維持（バケットスコープ + 自己 signBlob のみ）
- ❌ 上流 spec の変更を伴い、本仕様単独で閉じない（依存リードタイム）

## 6. Effort / Risk

| 項目 | 規模 | リスク | 根拠 |
|---|---|---|---|
| Config / HTTP 骨格（T1.x） | S | Low | env 注入は既存規約流用、定型 |
| InputValidation（T2.1） | S | Low | 純ロジック、境界値テストのみ |
| SearchQueryBuilder + sql/search.sql（T2.2） | M | **High** | R1 未解決。単一クエリでのテキスト埋め込み＋次元一致が肝 |
| BigQueryClient（T3.1） | S–M | Medium | クライアント lib 依存（R2）、エラー変換は定型 |
| ResultFormatter（T3.2） | S | Low | 整列・スコア整形（R4 の定義待ち） |
| SignedUrlGenerator（T3.3） | M | **High** | **G1（IAM 未付与）がブロッカー**。keyless 署名の実装差（R2） |
| ApiHandler 統合（T4.1） | M | Medium | 層結線・エラーマッピング、契約固定 |
| Deploy（T5.x） | M | Medium | Cloud Run service.yaml で実行 SA/env 結線、region 整合 |
| **全体** | **M–L** | **Medium–High** | リスクは R1（単一クエリ埋め込み）と G1（署名 URL 権限）に集中 |

## 7. 設計フェーズへの推奨

- **推奨アプローチ: Option C（上流 IAM 追補を明示）**。最小権限を保ったまま Requirement を一回で充足でき、G1 を「曖昧な前提」から「明示された上流追補タスク + Revalidation Trigger」へ昇格できる。上流変更のリードタイムが許容できない場合の代替が Option B（署名 URL 段階導入）。
- **設計で確定すべき主要判断**:
  1. G1 の解消方針（上流 IAM 追補 か 署名 URL 段階導入か）。
  2. R1 のクエリ確定（テキスト `AI.GENERATE_EMBEDDING` の正確なシグネチャ + `output_dimensionality=3072` 明示 + 単一クエリ結合形）。実機 dry-run 推奨。
  3. R2 の言語/SDK 選定（署名 URL keyless 成熟度を基準）。
  4. R4 のスコア意味（距離 or 類似度）の固定。
- **持ち越す Research**: R1（最優先・要一次情報/実機）、R2、R3、R4。
- **整合タスク**: tasks.md / steering（tech.md・roadmap.md）のモデル名表記を `gemini-embedding-2-preview`（エンドポイント）/ `gemini_embedding_model`（注入オブジェクト名）へ揃える（G2）。

---

# 設計フェーズ Discovery & Synthesis（2026-06-19, `/kiro-spec-design` 実行）

## Discovery: R1 確定（一次情報 = cloud.google.com/bigquery/docs）

テキストクエリ埋め込み + `VECTOR_SEARCH` の単一クエリ構文を公式ドキュメントで確定（gap 分析の最優先 Research 項目 R1 を解消）。

- **入力列名は `content` 固定**: `AI.GENERATE_EMBEDDING(MODEL …, (SELECT @query AS content), STRUCT(3072 AS output_dimensionality))`。クエリは `content` という STRING 列を産出する必要がある（公式明記）。
- **関数は `AI.GENERATE_EMBEDDING` で取込側と統一**: 出力列は `embedding`(ARRAY<FLOAT64>) と `status`（成功は空文字 `''`）。旧 `ML.GENERATE_EMBEDDING`（出力 `ml_generate_embedding_result`）とは出力列名が異なるため混用不可。取込が `AI.*` 採用済み → 検索も `AI.*`。
- **次元一致**: クエリ側でも `STRUCT(3072 AS output_dimensionality)` を明示し 3072 に固定。
- **VECTOR_SEARCH 結合**: `column_to_search` は文字列リテラル `'embedding'`。クエリ側 CTE が `embedding` 列を持てば `query_column_to_search` 省略可（明示も可 `query_column_to_search => 'embedding'`）。出力は `base.<col>`（`base.image_uri`, `base.content_type`）と `distance`(FLOAT64)。
- **distance_type は明示 `'COSINE'`**（既定は EUCLIDEAN）。インデックス未 populate でもブルートフォース（厳密最近傍）で動作し失敗しない。
- **スコア表現（R4 確定）**: BigQuery の COSINE は `distance = 1 − cosine_similarity`、distance∈[0,2]。`similarity = 1 - distance`（∈[-1,1]）で類似度復元。`ORDER BY distance ASC`（小さいほど類似）。
- **パラメータ化の境界**: `@query`/`@top_k` はスカラ BQ パラメータでバインド。`MODEL`名・テーブル名・`column_to_search`・`distance_type` は**識別子/関数引数リテラルのためパラメータ化不可** → SearchQueryBuilder が config 注入値からテンプレートレンダリングする（design 方針と一致）。
- **status フィルタ**: CTE 側に `WHERE status = ''` を入れ、埋め込み生成失敗行が VECTOR_SEARCH に渡るのを防ぐ。
- **残 dry-run 項目（GO 前 2 点のみ）**: (a) `@top_k` の名前付き引数 `top_k =>` への束縛可否（環境差。不可なら検証済み整数のテンプレ埋め込みへフォールバック）、(b) 単一クエリ内 `AI.GENERATE_EMBEDDING`(Preview モデル) → `VECTOR_SEARCH` チェーンの dry-run 成否。
- 一次ソース: AI.GENERATE_EMBEDDING / VECTOR_SEARCH / search_functions / vector-search の各公式リファレンス。

## 決定事項（ユーザー判断, 2026-06-19）

- **G1 解消 = Option C（上流 IAM 追補を前提）**: `gcp-infrastructure` に Run SA への以下 2 ロール追加を**明示依存 / Revalidation Trigger** として起票し、本仕様で署名 URL をフル実装する。
  - `roles/iam.serviceAccountTokenCreator`（**Run SA 自身をリソース**として Run SA に付与）— keyless V4 署名（IAM `signBlob`）に必須。
  - `roles/storage.objectViewer`（**images バケットスコープ**）— 署名対象オブジェクトの読取。
  - 現状 `terraform/iam.tf` は未付与のため、これは上流の前提条件であり、追補されるまで署名 URL は機能しない（design の Allowed Dependencies / Upstream Prerequisite に明記）。
- **実装言語 = Go**: 標準 `net/http`（フレームワーク非依存・KISS）、`cloud.google.com/go/bigquery`、`cloud.google.com/go/storage`（`SignedURL` + `SignBytes` で keyless 署名）。単一バイナリ・低コールドスタートで Cloud Run 適合。

## Synthesis 結果

1. **Generalization**: 検索は単一能力（`Search(query, top_k, signed_url) → results`）。署名 URL は任意アドオンで、インターフェースにフラグを設けるのみ（実装は要求分に限定）。過度な一般化はしない。
2. **Build vs. Adopt**: 埋め込み生成は **BigQuery ネイティブ（AI.GENERATE_EMBEDDING）を採用**し専用 Vertex 呼出サービスを作らない（roadmap 決定と一致）。署名 URL は **Go SDK の `SignedURL`+`SignBytes`（IAM signBlob）を採用**しキー管理を排除。HTTP は**標準 `net/http` を採用**し Gin/Echo 等は不採用（依存最小・KISS）。
3. **Simplification**: パッケージは責務単位で凝集（config / httpapi / validation / search / result / signedurl）。`server` と `handler` は `httpapi` パッケージ内に同居させ過分割を避ける。インターフェースは単一実装には付けない（YAGNI）。
