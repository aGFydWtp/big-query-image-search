# BigLake (Cloud Resource) BigQuery connection for the image-search system
# (Requirement 4).
#
# This connection is the credential boundary that downstream SQL DDL (Object
# Tables and the `gemini-embedding-2` remote model, owned by the
# image-ingestion-pipeline spec) uses to read images from GCS and invoke the
# Vertex AI embedding endpoint. This file declares ONLY the connection so that
# its connection ID and connection service account are provisioned and
# referenceable (Requirement 4.6): NO remote-model DDL, Object Table, or other
# SQL is created here — that boundary belongs to the ingestion spec. IAM grants
# to the connection service account (GCS read, aiplatform user) are added in
# iam.tf (Requirement 4.4, 4.5), and its identifier is exposed in outputs.tf
# (Requirement 4.3).
#
# The GA `google` provider supports `google_bigquery_connection` with the
# `cloud_resource {}` block, so no beta provider is required.

resource "google_bigquery_connection" "biglake" {
  project       = var.project_id
  connection_id = var.connection_id

  # Location is derived from the single `region` variable via `local.location`,
  # never `var.region` directly, so the connection co-locates with the GCS
  # bucket, BigQuery dataset, and Vertex AI endpoint under one source of truth
  # (Requirement 4.2, 6.4, design "BigLakeConnection").
  location = local.location

  # Cloud Resource connection type (Requirement 4.1). The empty block requests a
  # Google-managed service account whose identifier is exposed as
  # `cloud_resource[0].service_account_id` for IAM binding (iam.tf, Req 4.4/4.5)
  # and output (outputs.tf, Req 4.3).
  cloud_resource {}

  # Re-apply safety (Requirement 4.7): `prevent_destroy` makes
  # `terraform destroy`/replace error out instead of dropping the connection
  # underneath the ingestion remote model and IAM bindings that depend on it.
  # Re-apply is diff-free; removing the connection is a deliberate code change
  # (drop this block first).
  lifecycle {
    prevent_destroy = true
  }

  # Create only after the required GCP APIs (incl. BigQuery Connection) are
  # enabled (Requirement 1.3, design "ApiEnablement").
  depends_on = [google_project_service.required]
}
