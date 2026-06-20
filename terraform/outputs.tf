# 下流が参照する払い出しリソース識別子の一括出力（Requirement 6.7, design "OutputsContract"）。
#
# これらの出力名は安定契約であり、image-ingestion-pipeline / image-search-api の
# 各仕様が `terraform output` で参照する前提のため、名称変更は Revalidation Trigger
# となる（design "OutputsContract" Implementation Notes）。各値は対応リソースの
# 属性を直接参照するため、apply 後は非空であることが保証される。

output "image_bucket_name" {
  # 画像保管バケットのグローバル一意なバケット名（Requirement 2.3, design
  # "ImageBucket" / Logical Data Model）。BigLake 接続経由で BigQuery に画像を
  # 公開する取込・検索処理が参照する。
  description = "画像保管 GCS バケットの名前（下流の取込・検索が参照するバケット識別子）"
  value       = google_storage_bucket.images.name
}

output "bigquery_dataset_id" {
  # project 修飾の dataset 識別子 `project_id.dataset_id`（Requirement 3.3, design
  # "BigQueryDataset" / Logical Data Model: `project_id.dataset_id`）。下流が
  # 埋め込みテーブル・Object Table を作成する際の dataset 参照に用いる。
  description = "BigQuery dataset の project 修飾識別子（形式: project_id.dataset_id）"
  value       = "${google_bigquery_dataset.image_search.project}.${google_bigquery_dataset.image_search.dataset_id}"
}

output "bigquery_connection_id" {
  # BigLake（Cloud Resource）接続の完全修飾 ID
  # `projects/{project}/locations/{region}/connections/{connection_id}`
  # （Requirement 4.3, design "BigLakeConnection" / Logical Data Model）。
  # `google_bigquery_connection.biglake.name` がこの形式を返す。下流の
  # Object Table / remote model DDL がこの ID で接続を参照する。
  description = "BigLake 接続の完全修飾 ID（形式: projects/{project}/locations/{region}/connections/{connection_id}）"
  value       = google_bigquery_connection.biglake.name
}

output "bigquery_connection_service_account" {
  # Cloud Resource 接続が払い出す Google 管理サービスアカウントの識別子
  # （Requirement 4.3, design "BigLakeConnection"）。GCS 読み取り / Vertex AI
  # 呼び出し権限の付与対象であり、下流の参照整合性（IAM バインドの外部キー相当）に
  # 用いられる。
  description = "BigLake 接続の Cloud Resource サービスアカウント識別子（GCS/Vertex 権限付与対象）"
  value       = google_bigquery_connection.biglake.cloud_resource[0].service_account_id
}

output "cloud_run_service_account_email" {
  # Cloud Run 実行サービスアカウントのメールアドレス
  # `name@project.iam.gserviceaccount.com`（Requirement 5.4, design
  # "CloudRunServiceAccount" / Logical Data Model）。下流の image-search-api 仕様が
  # Cloud Run サービスにこの SA をバインドするために参照する。
  description = "Cloud Run 実行サービスアカウントのメールアドレス（下流が Cloud Run サービスへバインドする識別子）"
  value       = google_service_account.cloud_run.email
}

output "cloud_run_service_uri" {
  # IAP ゲート越しにアクセスする検索 UI/API のエンドポイント URL（方式b）。
  description = "Cloud Run 検索 API サービスの URL（IAP 経由でアクセスする入口）"
  value       = google_cloud_run_v2_service.api.uri
}

output "cloud_run_service_name" {
  # 取り込んだ Cloud Run サービス名（IAP IAM 付与・運用参照用）。
  description = "Cloud Run 検索 API サービス名"
  value       = google_cloud_run_v2_service.api.name
}
