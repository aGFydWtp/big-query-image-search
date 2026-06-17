# Implementation Plan

- [ ] 1. Foundation: Terraform ルートモジュールと共通構成
- [x] 1.1 バージョン制約と provider 設定を定義する
  - `versions.tf` に terraform/google/google-beta のバージョン制約と `required_providers` を記述する
  - `providers.tf` に google / google-beta provider を `project_id` と `region` 変数で設定する
  - 完了条件: `terraform init` が成功し provider が解決される
  - _Requirements: 6.1_
  - _Boundary: RootModuleConfig_

- [ ] 1.2 入力変数とリージョン導出・バリデーションを定義する
  - `variables.tf` に `project_id`・`region`・命名プレフィックス・state バケット等の入力変数を定義する
  - 必須変数に `validation` を設定し、欠落時に apply 前に失敗させる
  - 全リソースのロケーションを単一 `region` から導出する方針を変数として固定する
  - `terraform.tfvars.example` に必須値の雛形を記述する
  - 完了条件: 必須変数を欠いた入力で `terraform plan` がバリデーション失敗する
  - _Requirements: 6.2, 6.4, 6.6_
  - _Boundary: RootModuleConfig_

- [ ] 1.3 リモート state バックエンドを構成する
  - `backend.tf` に GCS リモートバックエンド（バケット + prefix）を設定する
  - 完了条件: `terraform init` でリモート state が初期化され、複数実行者間で共有・ロックされる
  - _Requirements: 6.3_
  - _Boundary: RootModuleConfig_
  - _Depends: 1.1_

- [ ] 1.4 必要 GCP API の有効化を定義する
  - `apis.tf` に `google_project_service` で BigQuery・BigQuery Connection・Vertex AI(aiplatform)・Cloud Run・Cloud Storage・IAM を有効化する
  - 有効化対象 API をコード上の一覧として明示し、再 apply で冪等にする
  - 完了条件: `apply` 後に対象 API がすべて有効化され、再 apply で差分が出ない
  - _Requirements: 1.1, 1.2, 1.4_
  - _Boundary: ApiEnablement_
  - _Depends: 1.1, 1.2_

- [ ] 2. Core: 基盤リソースの払い出し
- [ ] 2.1 (P) 画像保管用 GCS バケットを定義する
  - `storage.tf` に `google_storage_bucket` を `region` 由来 location で作成する
  - `uniform_bucket_level_access` と `public_access_prevention = enforced` で公開アクセスを抑止する
  - 既存バケットを破壊しない安全設定とする
  - 完了条件: `apply` でバケットが作成され、公開アクセス不可かつ再 apply で破壊されない
  - _Requirements: 2.1, 2.2, 2.4, 2.5_
  - _Boundary: ImageBucket_
  - _Depends: 1.4_

- [ ] 2.2 (P) BigQuery dataset を定義する
  - `bigquery.tf` に `google_bigquery_dataset` を `region` 由来 location で作成する
  - 再 apply 時に既存テーブルを破壊しない設定とする
  - 完了条件: `apply` で dataset が作成され、再 apply で既存テーブルが破壊されない
  - _Requirements: 3.1, 3.2, 3.4_
  - _Boundary: BigQueryDataset_
  - _Depends: 1.4_

- [ ] 2.3 (P) BigLake 用 Cloud Resource 接続を定義する
  - `connection.tf` に `google_bigquery_connection` を `cloud_resource {}` 付き・`region` 由来 location で作成する
  - 接続 SA 識別子を属性参照で取得可能にする
  - リモートモデル DDL は作成せず、接続 ID と接続 SA の払い出しに限定する
  - 完了条件: `apply` で接続が作成され、接続 SA 識別子が参照でき、再 apply で破壊されない
  - _Requirements: 4.1, 4.2, 4.3, 4.6, 4.7_
  - _Boundary: BigLakeConnection_
  - _Depends: 1.4_

- [ ] 2.4 (P) Cloud Run 実行用サービスアカウントを定義する
  - `cloud_run_sa.tf` に `google_service_account` を Cloud Run 実行用として作成する
  - Cloud Run サービス本体（コンテナイメージのデプロイ）は作成しない
  - 完了条件: `apply` で SA が作成され、SA メールが参照でき、サービス本体は構成に含まれない
  - _Requirements: 5.1, 5.5_
  - _Boundary: CloudRunServiceAccount_
  - _Depends: 1.4_

- [ ] 3. Integration: 最小権限 IAM のバインド
- [ ] 3.1 接続サービスアカウントへ GCS/Vertex 権限を付与する
  - `iam.tf` に、接続 SA へ画像バケットスコープの `roles/storage.objectViewer` をバインドする
  - 接続 SA へプロジェクトスコープの `roles/aiplatform.user` をバインドする
  - 完了条件: `apply` 後に接続 SA がバケット読取と aiplatform user を持つ
  - _Requirements: 4.4, 4.5_
  - _Boundary: ConnectionIam_
  - _Depends: 2.1, 2.3_

- [ ] 3.2 Cloud Run サービスアカウントへ BigQuery/Vertex 権限を付与する
  - `iam.tf` に、Run SA へ `roles/bigquery.jobUser` とデータ読取（dataset/プロジェクトの `roles/bigquery.dataViewer` 相当）をバインドする
  - Run SA へ `roles/aiplatform.user` をバインドする
  - 完了条件: `apply` 後に Run SA が BigQuery 実行/読取と aiplatform user を持つ
  - _Requirements: 5.2, 5.3_
  - _Boundary: CloudRunIam_
  - _Depends: 2.2, 2.4_

- [ ] 3.3 払い出しリソース識別子の出力を定義する
  - `outputs.tf` に、画像バケット名・dataset（project 修飾識別子）・接続 ID・接続 SA・Cloud Run SA メールを出力する
  - 完了条件: `terraform output` が下流参照に必要な全識別子を非空で返す
  - _Requirements: 2.3, 3.3, 4.3, 5.4, 6.7_
  - _Boundary: OutputsContract_
  - _Depends: 2.1, 2.2, 2.3, 2.4, 3.1_

- [ ] 4. Validation: 構成・冪等・境界の検証
- [ ] 4.1 静的検証と必須変数バリデーションを確認する
  - `terraform validate` の成功を確認する
  - 必須変数を欠いた入力で `plan` がバリデーション失敗することを確認する
  - 全リソースのロケーションが単一 `region` から導出され一致することを確認する
  - 完了条件: validate 成功・必須欠落で plan 失敗・ロケーション一致が確認できる
  - _Requirements: 6.1, 6.4, 6.6, 2.2, 3.2, 4.2_
  - _Boundary: RootModuleConfig_
  - _Depends: 1.2, 2.1, 2.2, 2.3_

- [ ] 4.2 適用・冪等性とリソース作成を検証する
  - 初回 `apply` で API・バケット・dataset・接続・両 SA・IAM・出力が作成されることを確認する
  - 連続 `apply` が冪等で差分ゼロになることを確認する
  - API が有効化された状態でリソース作成が成功する順序を確認する
  - 完了条件: 初回 apply で全リソース作成、2 回目 apply で差分ゼロが確認できる
  - _Requirements: 1.3, 1.4, 6.5_
  - _Depends: 3.1, 3.2, 3.3_

- [ ] 4.3 IAM・公開アクセス・境界を検証する
  - 接続 SA がバケット読取と aiplatform user を持つことを確認する
  - Cloud Run SA が BigQuery 実行/読取と aiplatform user を持つことを確認する
  - バケットが公開アクセス不可であることを確認する
  - リモートモデル DDL・Object Table・Cloud Run サービス本体が構成に含まれないことを確認する
  - 完了条件: 各 SA の権限・公開抑止・境界（DDL/サービス本体非作成）がすべて確認できる
  - _Requirements: 2.4, 4.4, 4.5, 5.2, 5.3, 4.6, 5.5_
  - _Depends: 3.1, 3.2_
