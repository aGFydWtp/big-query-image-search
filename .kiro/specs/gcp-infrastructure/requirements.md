# Requirements Document

## Introduction

本仕様は、テキスト→画像セマンティック検索システムが依存する GCP 基盤を Terraform で再現的に構築することを目的とする。`terraform apply` のみで、必要 API の有効化・画像保管用 GCS バケット・BigQuery dataset・BigLake 用接続（Cloud Resource connection）と接続サービスアカウントへの最小権限 IAM・Cloud Run 実行用サービスアカウントと最小権限 IAM・state/変数/リージョン整合の構成が払い出される。これにより、取込パイプライン（image-ingestion-pipeline）と検索 API（image-search-api）は、本仕様が払い出したリソースを前提に独立して実装できる。

本仕様はリソース・接続・権限・データセットまでを担い、BigQuery リモートモデル（`gemini-embedding-2`）の SQL DDL・embeddings テーブル・VECTOR INDEX・検索クエリ・アプリケーションコードは対象外とする。

## Boundary Context

- **In scope**:
  - 必要 GCP API の有効化（BigQuery、BigQuery Connection、Vertex AI/aiplatform、Cloud Run、Cloud Storage 等）
  - 画像保管用 GCS バケット
  - BigQuery dataset
  - BigLake 用 BigQuery 接続（Cloud Resource connection）と、その接続サービスアカウントへの GCS 読取・Vertex AI 利用権限付与
  - Cloud Run 実行用サービスアカウントと、その最小権限 IAM（BigQuery / Vertex AI / GCS 等の利用）
  - Terraform の state（リモートバックエンド）・入力変数・環境構成・リージョン整合の定義
  - 払い出したリソース識別子（バケット名・dataset 名・接続 ID・各サービスアカウント等）の出力
- **Out of scope**:
  - BigQuery リモートモデル（`gemini-embedding-2`）の SQL DDL 作成
  - GCS Object Table の作成、embeddings テーブルスキーマ、`CREATE VECTOR INDEX`
  - 埋め込み生成ロジック、検索クエリ、検索 API のアプリケーションコード
  - Cloud Run へのアプリケーションイメージのデプロイ（サービス本体のコンテナ実装は image-search-api が所有）
- **Adjacent expectations**:
  - image-ingestion-pipeline は、本仕様が払い出した GCS バケット・BigQuery dataset・BigLake 接続・接続サービスアカウントの権限を前提に取込を実装する。
  - image-search-api は、本仕様が払い出した Cloud Run 実行用サービスアカウント・IAM・BigQuery dataset を前提に検索 API を実装する。
  - リモートモデルの作成（SQL DDL）は本仕様の責務外であり、接続・IAM の払い出しまでを本仕様が保証する。

## Requirements

### Requirement 1: GCP API の有効化
**Objective:** インフラ管理者として、画像検索システムが依存する GCP API を Terraform で有効化したい。これにより、後続のリソース作成と取込・検索の各機能が API 未有効化で失敗しないようにするため。

#### Acceptance Criteria
1. When `terraform apply` が実行される, the GCP Infrastructure shall BigQuery API・BigQuery Connection API・Vertex AI（aiplatform）API・Cloud Run API・Cloud Storage API・IAM API を有効化する。
2. The GCP Infrastructure shall 有効化対象 API の一覧を構成として明示し、追加・削除を Terraform コード変更で管理する。
3. If 依存リソースが未有効化の API を要求する, then the GCP Infrastructure shall 当該 API の有効化をリソース作成より先に完了させる。
4. While いずれかの対象 API が既に有効化されている, the GCP Infrastructure shall 再 apply 時にエラーや重複を発生させず冪等に振る舞う。

### Requirement 2: 画像保管用 GCS バケット
**Objective:** インフラ管理者として、検索対象画像を保管する GCS バケットを払い出したい。これにより、取込パイプラインが画像を参照し BigLake 経由で読み取れるようにするため。

#### Acceptance Criteria
1. When `terraform apply` が実行される, the GCP Infrastructure shall 画像保管用の GCS バケットを 1 つ作成する。
2. The GCP Infrastructure shall バケットのリージョンを BigQuery・Vertex AI・Cloud Run と整合するロケーションに設定する。
3. The GCP Infrastructure shall 作成したバケット名を出力として公開し、下流仕様が参照できるようにする。
4. The GCP Infrastructure shall バケットへのアクセスを最小権限の IAM で制御し、公開アクセスを許可しない。
5. While 既にバケットが存在する, the GCP Infrastructure shall 再 apply 時にバケットを破壊せず構成差分のみを適用する。

### Requirement 3: BigQuery dataset
**Objective:** インフラ管理者として、埋め込みテーブルや Object Table を格納する BigQuery dataset を払い出したい。これにより、取込・検索の各機能が同一 dataset 上でデータを扱えるようにするため。

#### Acceptance Criteria
1. When `terraform apply` が実行される, the GCP Infrastructure shall 画像検索用の BigQuery dataset を作成する。
2. The GCP Infrastructure shall dataset のロケーションを GCS バケット・Vertex AI・Cloud Run と整合するリージョンに設定する。
3. The GCP Infrastructure shall 作成した dataset 名（および project 修飾済み識別子）を出力として公開する。
4. While 既に dataset が存在する, the GCP Infrastructure shall 再 apply 時に dataset 内の既存テーブルを破壊せず構成差分のみを適用する。

### Requirement 4: BigLake 接続と接続サービスアカウントの権限
**Objective:** インフラ管理者として、BigLake/Object Table とリモートモデルが利用する BigQuery 接続（Cloud Resource connection）を払い出し、その接続サービスアカウントに必要権限を付与したい。これにより、取込側が SQL DDL で Object Table とリモートモデルを作成・実行できるようにするため。

#### Acceptance Criteria
1. When `terraform apply` が実行される, the GCP Infrastructure shall Cloud Resource タイプの BigQuery 接続を作成する。
2. The GCP Infrastructure shall 接続のロケーションを dataset・GCS バケットと整合するリージョンに設定する。
3. When 接続が作成される, the GCP Infrastructure shall 接続に紐づくサービスアカウント識別子を取得し出力として公開する。
4. The GCP Infrastructure shall 接続サービスアカウントに対し、画像保管用 GCS バケットの読取権限を最小権限で付与する。
5. The GCP Infrastructure shall 接続サービスアカウントに対し、Vertex AI（埋め込みモデル呼び出し）を利用するための権限を最小権限で付与する。
6. The GCP Infrastructure shall リモートモデルの SQL DDL を作成しないが、DDL 作成に必要な接続 ID・接続サービスアカウント・付与済み権限を払い出し済みにする。
7. While 既に接続が存在する, the GCP Infrastructure shall 再 apply 時に接続を破壊せず構成差分のみを適用する。

### Requirement 5: Cloud Run 実行用サービスアカウントと IAM
**Objective:** インフラ管理者として、検索 API が実行される Cloud Run の実行用サービスアカウントと最小権限 IAM を払い出したい。これにより、検索 API が BigQuery と Vertex AI を呼び出して検索を実行できるようにするため。

#### Acceptance Criteria
1. When `terraform apply` が実行される, the GCP Infrastructure shall Cloud Run 実行用のサービスアカウントを作成する。
2. The GCP Infrastructure shall 当該サービスアカウントに対し、BigQuery のジョブ実行とデータ読取に必要な権限を最小権限で付与する。
3. The GCP Infrastructure shall 当該サービスアカウントに対し、Vertex AI（クエリ埋め込み生成）を利用するための権限を最小権限で付与する。
4. The GCP Infrastructure shall 作成したサービスアカウントのメールアドレスを出力として公開し、下流仕様が Cloud Run サービスにバインドできるようにする。
5. The GCP Infrastructure shall Cloud Run サービス本体（コンテナイメージのデプロイ）を作成せず、サービスアカウントと IAM の払い出しに責務を限定する。

### Requirement 6: Terraform 構成・state・変数・リージョン整合
**Objective:** インフラ管理者として、基盤を再現的かつ冪等に管理できる Terraform 構成を持ちたい。これにより、手動コンソール操作に依存せず、複数環境やチームで同一基盤を再構築できるようにするため。

#### Acceptance Criteria
1. The GCP Infrastructure shall すべてのリソースを Terraform コードで定義し、手動コンソール操作に依存しない。
2. The GCP Infrastructure shall project ID・リージョン・命名プレフィックス等を入力変数として外部化し、環境ごとに切り替え可能にする。
3. The GCP Infrastructure shall Terraform state をリモートバックエンドで管理し、複数実行者間で state を共有できるようにする。
4. The GCP Infrastructure shall GCS・BigQuery・Vertex AI・Cloud Run のロケーションを単一のリージョン設定から導出し、リージョン整合を保証する。
5. When `terraform apply` が複数回実行される, the GCP Infrastructure shall 同一入力に対して冪等に振る舞い、不要なリソースの再作成を行わない。
6. If 入力変数に必須値が欠落している, then the GCP Infrastructure shall apply 前のバリデーションで失敗し、欠落値を明示する。
7. The GCP Infrastructure shall 払い出した主要リソース識別子（バケット名・dataset・接続 ID・接続サービスアカウント・Cloud Run サービスアカウント）を Terraform 出力として一括公開する。
