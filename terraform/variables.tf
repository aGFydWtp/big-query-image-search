# Input variables for the GCP image-search infrastructure root module.
#
# Required values (no default) fail `terraform plan`/`apply` before any provider
# call, satisfying Requirement 6.6. Region consistency (Requirement 6.4) is
# enforced by constraining `region` to a maintained allow-list of locations where
# the downstream embedding model (gemini-embedding-2 / Vertex text-embedding
# endpoint used by BigQuery AI.GENERATE_EMBEDDING) is served and where BigQuery
# single-region, Cloud Run, and GCS can co-locate. See locals.tf for the single
# source of location derivation.

variable "project_id" {
  description = "GCP project ID that owns all provisioned resources. Required; leaving it empty fails validation before apply."
  type        = string

  validation {
    condition     = length(trimspace(var.project_id)) > 0
    error_message = "project_id must be a non-empty GCP project ID."
  }
}

variable "region" {
  description = "Single GCP region from which all resource locations (GCS, BigQuery dataset/connection, Cloud Run) are derived. Constrained to regions where the gemini-embedding-2 embedding model is served so that AI.GENERATE_EMBEDDING and VECTOR_SEARCH co-locate (Requirement 6.4)."
  type        = string
  default     = "us-central1"

  # Maintained allow-list (Revalidation Trigger: any region change requires
  # re-confirming model endpoint availability). Per Google Cloud docs, the
  # gemini-embedding-2 model is served in the `US` multi-region and the
  # `us-central1` single-region; the BigQuery model and input table must be in
  # the same region. This spec standardizes on single-region locations only
  # (multi-region `US` is intentionally excluded to avoid co-location conflicts
  # with the single-region Vertex endpoint). Add regions only after confirming
  # the model is served there as a single-region location.
  validation {
    condition     = contains(["us-central1"], var.region)
    error_message = "region must be one of the embedding-model-supported single-region locations: us-central1. Update this allow-list only after confirming gemini-embedding-2 endpoint availability for the new region."
  }
}

variable "name_prefix" {
  description = "Naming prefix applied to derived resource names (e.g. the default image bucket name). Lowercase letters, digits, and hyphens only."
  type        = string
  default     = "imgsearch"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,28}[a-z0-9]$", var.name_prefix))
    error_message = "name_prefix must be 3-30 chars, lowercase letters/digits/hyphens, starting with a letter and not ending with a hyphen."
  }
}

variable "dataset_id" {
  description = "BigQuery dataset ID that holds image-search tables. BigQuery dataset IDs allow letters, digits, and underscores only."
  type        = string
  default     = "image_search"

  validation {
    condition     = can(regex("^[A-Za-z0-9_]+$", var.dataset_id)) && length(var.dataset_id) <= 1024
    error_message = "dataset_id must contain only letters, digits, and underscores (max 1024 chars)."
  }
}

variable "connection_id" {
  description = "BigQuery (BigLake) cloud-resource connection ID used by AI.GENERATE_EMBEDDING. Lowercase letters, digits, and hyphens."
  type        = string
  default     = "image-search-biglake"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,98}[a-z0-9]$", var.connection_id))
    error_message = "connection_id must be lowercase letters/digits/hyphens, starting with a letter and not ending with a hyphen."
  }
}

variable "run_sa_account_id" {
  description = "Account ID (local part) for the Cloud Run execution service account. Must satisfy GCP service-account naming: 6-30 chars, lowercase letter start, lowercase letters/digits/hyphens."
  type        = string
  default     = "img-search-run"

  validation {
    condition     = can(regex("^[a-z]([a-z0-9-]{4,28})[a-z0-9]$", var.run_sa_account_id))
    error_message = "run_sa_account_id must be 6-30 chars: start with a lowercase letter, contain only lowercase letters/digits/hyphens, and not end with a hyphen."
  }
}

variable "image_bucket_name" {
  description = "Explicit name for the image storage GCS bucket. When empty, the name is derived as \"<project_id>-<name_prefix>-images\" (see locals.tf). Provide a value to override the derived name."
  type        = string
  default     = ""

  validation {
    condition     = var.image_bucket_name == "" || can(regex("^[a-z0-9][a-z0-9._-]{1,61}[a-z0-9]$", var.image_bucket_name))
    error_message = "image_bucket_name must be empty (to derive automatically) or a valid GCS bucket name (3-63 chars, lowercase letters/digits/hyphens/underscores/dots)."
  }
}
