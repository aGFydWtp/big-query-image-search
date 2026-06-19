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

# 接続 SA 伝播待ち（コールドスタートでの単一 apply 成功のため）。
# BigLake 接続（cloud_resource{}）の Google 管理 SA は、接続作成時に識別子は即時
# 返るが、IAM プリンシパルとして参照可能になるまで数十秒の伝播遅延がある。これを
# 待たずに後続の *_iam_member を作成すると "Service account ... does not exist" で
# 失敗する（結果整合性）。接続作成後に固定時間待機し、接続 SA を参照する 2 つの
# バインド（4.4 / 4.5）をこの待機に依存させることで、初回 apply での全リソース作成
# を成立させる（Requirement 4.4/4.5、design "ConnectionIam"）。
resource "time_sleep" "wait_connection_sa_propagation" {
  depends_on      = [google_bigquery_connection.biglake]
  create_duration = "90s"
}

# Requirement 4.4: 接続 SA へ画像保管バケットの読取権限。
# バケットスコープの `roles/storage.objectViewer` をバケット単位でバインドする
# ことで、最小権限（プロジェクト全体ではなく当該バケットのオブジェクト読取のみ）
# を維持する。bucket は名前参照（google_storage_bucket.images.name）で渡す。
resource "google_storage_bucket_iam_member" "connection_sa_image_object_viewer" {
  bucket     = google_storage_bucket.images.name
  role       = "roles/storage.objectViewer"
  member     = "serviceAccount:${google_bigquery_connection.biglake.cloud_resource[0].service_account_id}"
  depends_on = [time_sleep.wait_connection_sa_propagation]
}

# Requirement 4.5: 接続 SA へ Vertex AI（埋め込みモデル呼び出し）利用権限。
# CAVEAT: `roles/aiplatform.user` はプロジェクトスコープでバインドする。Vertex AI
# の IAM はモデル/エンドポイント単位のリソーススコープ細分化が困難なため、design
# "ConnectionIam"（Risks）に従いプロジェクトスコープを許容する。付与ロールは
# aiplatform.user に限定し、過剰な editor/owner 等は付与しない。
resource "google_project_iam_member" "connection_sa_aiplatform_user" {
  project    = var.project_id
  role       = "roles/aiplatform.user"
  member     = "serviceAccount:${google_bigquery_connection.biglake.cloud_resource[0].service_account_id}"
  depends_on = [time_sleep.wait_connection_sa_propagation]
}

# Cloud Run 実行 SA への最小権限 IAM バインド（design "CloudRunIam"、Requirement 5.2/5.3）。
#
# 検索 API が実行される Cloud Run 実行 SA（cloud_run_sa.tf の
# google_service_account.cloud_run）に、BigQuery のジョブ実行・データ読取と
# Vertex AI（クエリ埋め込み生成）の利用権限を付与する。メンバー文字列は
# `serviceAccount:<email>`（google_service_account.cloud_run.email）。
#
# 認可モデルの方針は上記接続 SA バインドと同一: authoritative な
# *_iam_binding / *_iam_policy は使わず、メンバー単位（非 authoritative）の
# *_iam_member のみを使い、当該 SA のバインドだけを加減する。

# Requirement 5.2（ジョブ実行）: Run SA へ BigQuery のクエリジョブ実行権限。
# `roles/bigquery.jobUser` はジョブ作成に必要なプロジェクトスコープのロール
# （ジョブはプロジェクト単位のリソースであり、データ読取は別ロールで分離する）。
# 最小権限のため editor/admin ではなく jobUser に限定する。
resource "google_project_iam_member" "run_sa_bigquery_job_user" {
  project = var.project_id
  role    = "roles/bigquery.jobUser"
  member  = "serviceAccount:${google_service_account.cloud_run.email}"
}

# Requirement 5.2（データ読取）: Run SA へ画像検索 dataset のデータ読取権限。
# プロジェクト全体ではなく当該 dataset 単位で `roles/bigquery.dataViewer` を
# バインドし、最小権限（他 dataset を読めない）を維持する。dataset_id /
# project は dataset リソース（bigquery.tf の google_bigquery_dataset.image_search）
# から参照する。
resource "google_bigquery_dataset_iam_member" "run_sa_dataset_data_viewer" {
  project    = google_bigquery_dataset.image_search.project
  dataset_id = google_bigquery_dataset.image_search.dataset_id
  role       = "roles/bigquery.dataViewer"
  member     = "serviceAccount:${google_service_account.cloud_run.email}"
}

# Requirement 5.2/5.3（接続使用）: Run SA へ BigLake 接続の使用権限。
# 検索 API の AI.GENERATE_EMBEDDING は接続バックドのリモートモデルを呼び出すため、
# 呼び出し元（Run SA）に接続使用権 `bigquery.connections.use` が必要になる
# （dry-run では露見せず、実行時に "does not have bigquery.connections.use" で失敗する）。
# dataViewer / jobUser / aiplatform.user だけでは不足する、リモートモデル実行の前提権限。
# 接続単位の非 authoritative バインド（connectionUser）で最小権限を維持する。
resource "google_bigquery_connection_iam_member" "run_sa_connection_user" {
  project       = google_bigquery_connection.biglake.project
  location      = google_bigquery_connection.biglake.location
  connection_id = google_bigquery_connection.biglake.connection_id
  role          = "roles/bigquery.connectionUser"
  member        = "serviceAccount:${google_service_account.cloud_run.email}"
}

# Requirement 5.3: Run SA へ Vertex AI（クエリ埋め込み生成）利用権限。
# CAVEAT: 接続 SA と同様、`roles/aiplatform.user` はモデル/エンドポイント単位の
# リソーススコープ細分化が困難なためプロジェクトスコープでバインドする。付与
# ロールは aiplatform.user に限定し、過剰な editor/owner 等は付与しない。
resource "google_project_iam_member" "run_sa_aiplatform_user" {
  project = var.project_id
  role    = "roles/aiplatform.user"
  member  = "serviceAccount:${google_service_account.cloud_run.email}"
}

# 署名付き URL（keyless V4）のための Run SA 追補（design "CloudRunIam"／本 spec の
# Revalidation, 下流 image-search-api Requirement 3.2/3.3/5.4）。
#
# 検索 API は一致画像へ有効期限付きアクセスを与えるため GCS V4 署名 URL を発行する。
# Cloud Run は SA 秘密鍵を持たないため、署名は IAM Credentials の signBlob（keyless）
# で行う。これには以下 2 つが必要で、いずれも非 authoritative な *_iam_member で
# 最小権限（自己 signBlob ＋ images バケットスコープ読取）を維持する。接続 SA と異なり
# cloud_run SA は本モジュールが直接作成するため伝播待ち（time_sleep）は不要。

# Requirement 3.3（署名対象オブジェクト読取）: Run SA へ画像バケットの読取権限。
# 接続 SA と同じくバケットスコープの `roles/storage.objectViewer` をバインドし、
# プロジェクト全体ではなく当該バケットのオブジェクト読取のみへ限定する。
resource "google_storage_bucket_iam_member" "run_sa_image_object_viewer" {
  bucket = google_storage_bucket.images.name
  role   = "roles/storage.objectViewer"
  member = "serviceAccount:${google_service_account.cloud_run.email}"
}

# Requirement 3.2（keyless 署名）: Run SA 自身への `roles/iam.serviceAccountTokenCreator`。
# V4 署名 URL 生成時、SignBytes が IAM Credentials の signBlob を当該 SA に対して呼ぶため、
# Run SA は「自分自身をリソースとして」tokenCreator を持つ必要がある（自己署名）。
# project スコープではなく当該 SA リソースに限定して最小権限を保つ。
resource "google_service_account_iam_member" "run_sa_self_token_creator" {
  service_account_id = google_service_account.cloud_run.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${google_service_account.cloud_run.email}"
}
