# IAP（Identity-Aware Proxy）による検索 UI/API の認証ゲート（方式b）。
#
# cloud_run_service.tf の google_cloud_run_v2_service.api で iap_enabled=true にしただけでは
# 「誰も通れない」状態。到達可能にするには 2 種類の IAM 付与が必要:
#   (1) IAP サービスエージェント → Cloud Run へ run.invoker（IAP が認証後に背後の
#       Cloud Run を呼び出すための権限）
#   (2) 実際の閲覧ユーザー → IAP リソースへ roles/iap.httpsResourceAccessor
#
# 認可モデルは iam.tf の方針に揃え、authoritative な *_iam_binding/_iam_policy は使わず
# メンバー単位（非 authoritative）の *_iam_member のみを使う。

# (1) IAP のプロジェクトサービスエージェント（service-<num>@gcp-sa-iap.iam.gserviceaccount.com）を
# 明示的に払い出す。これを先に作っておかないと、直後の run.invoker バインドが
# 「サービスアカウントが存在しない」で失敗しうる。google-beta が必要。
resource "google_project_service_identity" "iap" {
  provider = google-beta
  project  = var.project_id
  service  = "iap.googleapis.com"

  depends_on = [google_project_service.required]
}

# (1) IAP サービスエージェントへ Cloud Run の run.invoker を付与。
# サービス単位（非 authoritative）でバインドし、他メンバーを剥奪しない。
resource "google_cloud_run_v2_service_iam_member" "iap_invoker" {
  project  = google_cloud_run_v2_service.api.project
  location = google_cloud_run_v2_service.api.location
  name     = google_cloud_run_v2_service.api.name
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_project_service_identity.iap.email}"
}

# (2) 閲覧を許可するプリンシパルへ IAP アクセス権（roles/iap.httpsResourceAccessor）。
# var.iap_members が空なら誰にも付与されない（= IAP 有効でも到達不可、明示許可制）。
# 直結 IAP（Cloud Run）用の専用 IAM リソースを用いる。
resource "google_iap_web_cloud_run_service_iam_member" "viewers" {
  for_each = toset(var.iap_members)

  project                = google_cloud_run_v2_service.api.project
  location               = google_cloud_run_v2_service.api.location
  cloud_run_service_name = google_cloud_run_v2_service.api.name
  role                   = "roles/iap.httpsResourceAccessor"
  member                 = each.value

  # ドメイン制限共有の緩和（org_policy.tf）が伝播してからメンバーを追加する。
  # 別テナントの domain:/user: 指定は、緩和前だと
  # constraints/iam.allowedPolicyMemberDomains 違反で失敗するため。
  depends_on = [time_sleep.wait_org_policy_propagation]
}
