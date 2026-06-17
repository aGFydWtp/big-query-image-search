# 接続サービスアカウントへの最小権限 IAM バインド（design "ConnectionIam"、Requirement 4.4 / 4.5）。
#
# BigLake 接続（connection.tf の google_bigquery_connection.biglake）には Google
# 管理のサービスアカウントが払い出される。その識別子は
# `cloud_resource[0].service_account_id` で参照でき、ここでメンバー文字列
# `serviceAccount:<service_account_id>` として 2 つのロールにバインドする。
#
# 認可モデルの方針（design "State Management"）:
# - authoritative な *_iam_binding / *_iam_policy は使用しない。これらは対象
#   ロールのメンバー集合を丸ごと上書きし、同一バケット/プロジェクトの他メンバー
#   （他の SA や人間オペレーター）を意図せず剥奪するため。
# - 代わりにメンバー単位（非 authoritative）の *_iam_member を使い、当該 SA の
#   バインドだけを加減する。
#
# NOTE: Cloud Run 実行 SA への IAM バインド（CloudRunIam, Requirement 5.2/5.3）は
# 別タスクでこの iam.tf に追記される。本ブロックは接続 SA のバインドのみ。

# Requirement 4.4: 接続 SA へ画像保管バケットの読取権限。
# バケットスコープの `roles/storage.objectViewer` をバケット単位でバインドする
# ことで、最小権限（プロジェクト全体ではなく当該バケットのオブジェクト読取のみ）
# を維持する。bucket は名前参照（google_storage_bucket.images.name）で渡す。
resource "google_storage_bucket_iam_member" "connection_sa_image_object_viewer" {
  bucket = google_storage_bucket.images.name
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${google_bigquery_connection.biglake.cloud_resource[0].service_account_id}"
}

# Requirement 4.5: 接続 SA へ Vertex AI（埋め込みモデル呼び出し）利用権限。
# CAVEAT: `roles/aiplatform.user` はプロジェクトスコープでバインドする。Vertex AI
# の IAM はモデル/エンドポイント単位のリソーススコープ細分化が困難なため、design
# "ConnectionIam"（Risks）に従いプロジェクトスコープを許容する。付与ロールは
# aiplatform.user に限定し、過剰な editor/owner 等は付与しない。
resource "google_project_iam_member" "connection_sa_aiplatform_user" {
  project = var.project_id
  role    = "roles/aiplatform.user"
  member  = "serviceAccount:${google_bigquery_connection.biglake.cloud_resource[0].service_account_id}"
}
