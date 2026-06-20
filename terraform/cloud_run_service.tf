# Cloud Run 検索 API サービス本体（方式b: 完全 IaC 化）。
#
# 設計境界の変更（重要）: cloud_run_sa.tf の Boundary コメントでは、Cloud Run サービス
# 本体（コンテナデプロイ）は本 spec の管理外（image-search-api spec が gcloud デプロイで
# 所有）とされていた。本ファイルは方式 b の選択に従いその境界を意図的に上書きし、本体を
# 本モジュールに取り込んで IAP（iap_enabled）まで IaC で宣言する。
#
# 既存の稼働サービスを破壊せず取り込むため、apply 前に terraform import が必須:
#   terraform import google_cloud_run_v2_service.api \
#     projects/<project_id>/locations/<region>/services/image-search-api
#
# 実構成は稼働中サービス（generation 2 / revision image-search-api-00002-zf9）の
# `gcloud run services describe` 出力を忠実に写経している。差分が出ないことを
# `terraform plan` で確認してから apply すること（iap_enabled=true のみが意図した差分）。

resource "google_cloud_run_v2_service" "api" {
  name     = "image-search-api"
  location = local.location
  project  = var.project_id

  # 稼働構成: ingress=all。
  ingress = "INGRESS_TRAFFIC_ALL"

  # 方式b の主目的: IAP をサービス本体で有効化する。アクセス制御（誰が通れるか）は
  # iap.tf の google_iap_web_cloud_run_service_iam_member で付与する。
  iap_enabled = true

  # 既存サービスの誤削除防止（provider 既定 true を明示）。destroy したい場合のみ false。
  deletion_protection = true

  template {
    # Cloud Run 実行 SA（cloud_run_sa.tf で本モジュールが払い出す ID）。
    service_account = google_service_account.cloud_run.email

    # 稼働構成: timeoutSeconds=300, containerConcurrency=80。
    timeout                          = "300s"
    max_instance_request_concurrency = 80

    # 稼働構成: minScale=0, maxScale=10。
    scaling {
      min_instance_count = 0
      max_instance_count = 10
    }

    containers {
      # import 時点の基準イメージ。日常のアプリリリースは gcloud run deploy 側が
      # 更新するため、下部 lifecycle.ignore_changes で image をドリフト対象外にする。
      image = var.api_image

      # 稼働構成: containerPort=8080, name=http1。
      ports {
        container_port = 8080
        name           = "http1"
      }

      # 稼働構成: cpu=1, memory=512Mi。
      resources {
        limits = {
          cpu    = "1"
          memory = "512Mi"
        }
      }

      # アプリ環境変数（稼働構成を写経）。インフラ由来の値は対応リソースを参照して
      # 参照整合性を担保し、アプリ定数は変数（既定値=現行）で表現する。
      env {
        name  = "PROJECT_ID"
        value = var.project_id
      }
      env {
        name  = "REGION"
        value = var.region
      }
      env {
        name  = "DATASET_ID"
        value = "${google_bigquery_dataset.image_search.project}.${google_bigquery_dataset.image_search.dataset_id}"
      }
      env {
        name  = "EMBEDDINGS_TABLE"
        value = var.embeddings_table
      }
      env {
        name  = "MODEL"
        value = var.embedding_model
      }
      env {
        name  = "IMAGE_BUCKET"
        value = google_storage_bucket.images.name
      }
      env {
        name  = "RUN_SA_EMAIL"
        value = google_service_account.cloud_run.email
      }
      env {
        name  = "SIGNED_URL_EXPIRY"
        value = var.signed_url_expiry
      }

      # 稼働構成: TCP startup probe（port 8080, failureThreshold=1,
      # periodSeconds=240, timeoutSeconds=240）。
      startup_probe {
        tcp_socket {
          port = 8080
        }
        failure_threshold = 1
        period_seconds    = 240
        timeout_seconds   = 240
      }
    }
  }

  # 100% を最新リビジョンへ（稼働構成）。
  traffic {
    type    = "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST"
    percent = 100
  }

  # API 有効化後に作成（iap/run/iam 等）。
  depends_on = [google_project_service.required]

  lifecycle {
    # アプリのリリース運用（gcloud run deploy によるイメージ更新やクライアント
    # アノテーション付与）と、本モジュールのインフラ定義を両立させるためのドリフト抑止。
    # - image: 日常デプロイで変わる（commit ハッシュタグ）。TF では追わない。
    # - client / client_version: gcloud が付与するメタデータ。
    # - template[0].labels / annotations: gcloud が付与する nonce 等。
    ignore_changes = [
      template[0].containers[0].image,
      client,
      client_version,
      template[0].labels,
      template[0].annotations,
    ]
  }
}
