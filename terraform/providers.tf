# billing_project + user_project_override:
# ADC（gcloud auth application-default のユーザー認証）では、一部 API（orgpolicy 等）の
# 呼び出しにクォータ/課金プロジェクトの明示が必要。これが無いと Google デフォルトの
# プロジェクト(764086051850)に属性付けされ「requires a quota project」403 になる。
# user_project_override=true で全 API 呼び出しに X-Goog-User-Project=billing_project を
# 付与し、本プロジェクト(image-search-6c457e)へ属性付けする。
provider "google" {
  project               = var.project_id
  region                = var.region
  billing_project       = var.project_id
  user_project_override = true
}

provider "google-beta" {
  project               = var.project_id
  region                = var.region
  billing_project       = var.project_id
  user_project_override = true
}
