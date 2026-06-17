# BigQuery dataset for the image-search system (Requirement 3).
#
# The dataset holds the image-search tables (embedding tables, Object Tables)
# that the ingestion and search features share. This file declares ONLY the
# dataset; tables, Object Tables, and remote models are created by the
# image-ingestion-pipeline spec, not here (design "BigQueryDataset",
# Non-Goals). Its location is derived from the single `region` variable via
# `local.location`, never `var.region` directly, so region consistency with the
# GCS bucket, Vertex AI, and Cloud Run has one source of truth (Requirement 3.2,
# 6.4, design "BigQueryDataset").

resource "google_bigquery_dataset" "image_search" {
  project    = var.project_id
  dataset_id = var.dataset_id
  location   = local.location

  # Safety against destroying existing tables on re-apply (Requirement 3.4):
  # `delete_contents_on_destroy = false` refuses to delete a dataset that still
  # contains tables, and the `prevent_destroy` lifecycle guard makes
  # `terraform destroy`/replace error out instead of dropping the dataset (and
  # its tables) underneath the ingestion/search features. Re-apply is diff-free;
  # removing the dataset is a deliberate code change (drop this block first).
  delete_contents_on_destroy = false

  lifecycle {
    prevent_destroy = true
  }

  # Create only after the required GCP APIs (incl. BigQuery) are enabled
  # (Requirement 1.3, design "ApiEnablement").
  depends_on = [google_project_service.required]
}
