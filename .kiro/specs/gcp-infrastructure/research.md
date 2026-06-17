# Research & Design Decisions

## Summary
- **Feature**: `gcp-infrastructure`
- **Discovery Scope**: New Feature（グリーンフィールド。GCP リソース・Terraform コードとも未作成）
- **Key Findings**:
  - BigLake/Object Table とリモートモデルが共有する接続は `google_bigquery_connection`（`cloud_resource` ブロック）で表現でき、接続に紐づくサービスアカウントは属性 `cloud_resource[0].service_account_id` から取得できる。これにより接続 SA への IAM 付与を Terraform で完結できる。
  - リモートモデル（`gemini-embedding-2`）の作成は `CREATE MODEL ... REMOTE WITH CONNECTION` の SQL DDL であり、Terraform リソースではない。本仕様は接続・IAM・dataset までを払い出し、DDL は image-ingestion-pipeline が所有する。
  - GCS / BigQuery / Vertex AI / Cloud Run はリージョン整合が検索精度・実行可能性の前提となるため、単一の `region`/`location` 変数から各リソースのロケーションを導出する設計とする。
  - 必要 API は `google_project_service` で宣言的に有効化し、各リソースに `depends_on` で順序を保証することで「API 未有効化によるリソース作成失敗」を回避する。

## Research Log

### BigLake 接続と接続サービスアカウント
- **Context**: 取込側が Object Table とリモートモデルを作成・実行するために、GCS 読取と Vertex AI 利用権限を持つ接続 SA が必要。本仕様は接続と権限の払い出しまでを担う。
- **Sources Consulted**: Terraform `google_bigquery_connection`（`cloud_resource`）、BigQuery BigLake / Object Table 接続権限の標準ガイダンス、Vertex AI 連携の IAM 要件。
- **Findings**:
  - `google_bigquery_connection` に `cloud_resource {}` を指定すると GCP が接続専用 SA を自動生成する。SA 識別子は `cloud_resource[0].service_account_id` で参照可能。
  - 接続 SA への GCS 読取は `roles/storage.objectViewer`（対象バケットにスコープ）、Vertex AI 連携は `roles/aiplatform.user` を最小権限として付与する。
- **Implications**: 接続作成 → SA 取得 → バケット/プロジェクトレベルの IAM バインドという順序依存が発生する。Terraform の暗黙的依存（属性参照）で順序が保証される。

### リモートモデル DDL の責務境界
- **Context**: roadmap・brief とも「リモートモデル作成は SQL DDL であり TF リソースではない」点を制約として明示している。
- **Findings**: リモートモデル・Object Table・embeddings テーブル・VECTOR INDEX はすべて SQL で作成される。これらは取込/検索のデータ層責務。
- **Implications**: 本仕様は「接続 ID・接続 SA・付与済み権限・dataset」までを契約として下流に渡す。DDL を本仕様に取り込まない（Out of Boundary）。

### リージョン整合
- **Context**: roadmap が「BigQuery / Vertex AI / Cloud Run / GCS のリージョン整合に注意」と制約。
- **Findings**: 各リソースのロケーション指定が分散すると不整合が起きやすい。
- **Implications**: 単一 `region` 変数から GCS location・BigQuery dataset location・接続 location・Cloud Run location を導出する。Vertex AI のモデル提供リージョンに合わせて `region` を選ぶ前提を変数バリデーションとドキュメントで明示する。

### Cloud Run の責務範囲
- **Context**: brief は「Cloud Run サービスの基盤（サービスアカウント、最小権限 IAM、サービス枠）」を In scope とし、アプリ実装を Out とする。
- **Findings**: コンテナイメージは image-search-api が所有するため、本仕様で `google_cloud_run_v2_service` を作成するとイメージ未確定で循環依存になる。
- **Implications**: 本仕様は Cloud Run 実行用 SA と IAM の払い出しに限定し、サービス本体（イメージデプロイ）は下流に委譲する。

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Notes |
|--------|-------------|-----------|---------------------|-------|
| 単一ルートモジュール + 機能別 .tf 分割 | apis.tf / storage.tf / bigquery.tf / iam.tf 等にファイル分割した単一ルート | 構成が単純、`terraform apply` 一発、レビュー容易 | 環境数が増えると重複しやすい | 本仕様の規模（数リソース）に最適 |
| 再利用可能子モジュール群 | storage/bigquery/iam を子モジュール化 | 多環境再利用に強い | 初期スコープには過剰、間接層が増える | YAGNI により不採用 |
| 環境別ワークスペース + tfvars | 単一コードを workspace で切替 | 環境分離 | 現スコープは単一環境想定 | 変数外部化で十分 |

## Design Decisions

### Decision: 単一ルートモジュール + 機能別ファイル分割
- **Context**: 数リソース規模の基盤を再現的・冪等に管理する。
- **Alternatives Considered**:
  1. 子モジュール分割 — 多環境再利用に強いが現スコープには過剰。
  2. 単一ファイル `main.tf` — 小さいがレビュー性・保守性が低い。
- **Selected Approach**: 単一ルートモジュールを機能別 `.tf` に分割（apis / storage / bigquery / connection / iam / cloud_run_sa / variables / outputs / backend / versions）。
- **Rationale**: KISS/YAGNI。`terraform apply` 一発で完結し、責務がファイル単位で明確。
- **Trade-offs**: 多環境展開時はリファクタが必要だが、現要件では不要。

### Decision: 接続 SA への IAM をバケットスコープで最小付与
- **Context**: 接続 SA は GCS 読取と Vertex 利用のみ必要。
- **Selected Approach**: `roles/storage.objectViewer` を対象バケットリソースにバインド、`roles/aiplatform.user` をプロジェクトにバインド。
- **Rationale**: 最小権限。バケット限定で過剰権限を避ける。
- **Trade-offs**: Vertex 権限はプロジェクトスコープになる（API がリソーススコープ IAM を細分化しにくいため）。

### Decision: リージョンを単一変数から導出
- **Context**: リージョン整合が検索精度・実行可能性の前提。
- **Selected Approach**: `region` 変数を単一の真実とし、全リソースのロケーションを導出。Vertex モデル提供リージョンに合うことを変数バリデーション/ドキュメントで示す。
- **Trade-offs**: BigQuery のマルチリージョン（US/EU）と Vertex のリージョンが完全一致しないケースは、変数とバリデーションで運用者に明示して回避する。

## Risks & Mitigations
- リスク: API 有効化直後のリソース作成が伝播遅延で失敗 → `depends_on` と属性参照で順序保証し、必要に応じ再 apply で冪等回復。
- リスク: BigQuery ロケーションと Vertex リージョンの不整合 → 単一 `region` 変数 + バリデーション + ドキュメントで整合を強制。
- リスク: 接続 SA 権限の過剰付与 → バケットスコープ IAM と最小ロール選定で緩和。
- リスク: state の競合 → リモートバックエンド（GCS）で state を共有しロックを利用。

## References
- Terraform Google Provider: `google_project_service`, `google_storage_bucket`, `google_bigquery_dataset`, `google_bigquery_connection`, `google_service_account`, `google_project_iam_member`, `google_storage_bucket_iam_member` — リソース定義の正準ソース。
- BigQuery BigLake / Object Table 接続と接続 SA への IAM 付与 — 接続 SA の権限要件。
- roadmap.md / brief.md — スコープ・制約・境界の一次情報。
