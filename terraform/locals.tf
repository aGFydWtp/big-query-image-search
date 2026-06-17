# Derived values for the root module.
#
# All resource locations are derived from the single `region` variable to
# guarantee region consistency across GCS, BigQuery, and Cloud Run
# (Requirement 6.4). Downstream resource files (storage.tf, bigquery.tf,
# connection.tf) MUST reference `local.location` rather than `var.region`
# directly so that the derivation has a single source of truth.

locals {
  # Single location used by every regional resource.
  location = var.region

  # Image bucket name: explicit override when provided, otherwise derived from
  # the project ID and naming prefix.
  image_bucket_name = var.image_bucket_name != "" ? var.image_bucket_name : "${var.project_id}-${var.name_prefix}-images"
}
