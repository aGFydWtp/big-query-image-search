# Image storage bucket for the image-search system (Requirement 2).
#
# The bucket holds the source images that BigLake exposes to BigQuery. Its name
# is globally unique (`local.image_bucket_name`: explicit override or the
# `${project_id}-${name_prefix}-images` derivation) and its location is derived
# from the single `region` variable via `local.location`, never `var.region`
# directly, so region consistency has one source of truth (Requirement 6.4,
# design "ImageBucket").

resource "google_storage_bucket" "images" {
  project  = var.project_id
  name     = local.image_bucket_name
  location = local.location

  # Public-access prevention (Requirement 2.4): enforce uniform bucket-level
  # access and the organization-independent `enforced` public-access-prevention
  # setting. Together these disable ACL-based and allUsers/allAuthenticatedUsers
  # grants, so the bucket cannot be made public by accident.
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"

  # Safety against destroying an existing bucket (Requirement 2.5):
  # `force_destroy = false` refuses to delete a non-empty bucket, and the
  # `prevent_destroy` lifecycle guard makes `terraform destroy`/replace error
  # out instead of deleting stored images. Re-apply is diff-free; removing the
  # bucket is a deliberate code change (drop this block first).
  force_destroy = false

  lifecycle {
    prevent_destroy = true
  }

  # Create only after the required GCP APIs (incl. Cloud Storage) are enabled
  # (Requirement 1.3, design "ApiEnablement").
  depends_on = [google_project_service.required]
}
