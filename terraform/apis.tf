# GCP API enablement for the image-search infrastructure (Requirement 1).
#
# The set of services is expressed as an explicit, commented code list and
# enabled via `for_each` over a `toset(...)`. Adding or removing an API is a
# code change to this list (Requirement 1.2). Downstream resource files
# (storage.tf, bigquery.tf, connection.tf, iam.tf) depend on these services so
# that API enablement completes before resource creation (Requirement 1.3,
# wired in the consuming resources / task 4.2).
#
# Idempotency (Requirement 1.4): `google_project_service` is convergent, and
# `disable_on_destroy = false` keeps re-apply diff-free while avoiding disabling
# shared-project APIs on destroy (design "ApiEnablement"). Re-apply against an
# already-enabled API produces no change or error.

locals {
  # Explicit list of APIs the image-search system depends on. Keep one entry
  # per line with a rationale so additions/removals are reviewable diffs.
  required_apis = toset([
    "bigquery.googleapis.com",           # BigQuery: datasets, jobs, queries (Req 1.1)
    "bigqueryconnection.googleapis.com", # BigQuery Connection: BigLake cloud-resource connection (Req 1.1)
    "aiplatform.googleapis.com",         # Vertex AI (aiplatform): embedding model invocation (Req 1.1)
    "run.googleapis.com",                # Cloud Run: search API execution runtime (Req 1.1)
    "storage.googleapis.com",            # Cloud Storage: image bucket (Req 1.1)
    "iam.googleapis.com",                # IAM: service accounts and policy bindings (Req 1.1)
    "iamcredentials.googleapis.com",     # IAM Credentials: signBlob for keyless V4 signed URLs by the Cloud Run SA (image-search-api Req 3.2/3.3)

    # Enablement plumbing: the Service Usage and Cloud Resource Manager APIs are
    # the control-plane services Terraform calls to enable/manage the APIs above
    # and to resolve the project. Including them explicitly avoids a chicken-and
    # -egg failure on a freshly created project.
    "serviceusage.googleapis.com",         # Service Usage: enables the services in this list
    "cloudresourcemanager.googleapis.com", # Cloud Resource Manager: project resolution for enablement
  ])
}

resource "google_project_service" "required" {
  for_each = local.required_apis

  project = var.project_id
  service = each.value

  # Do not disable APIs on destroy: prevents side effects on shared projects and
  # keeps re-apply idempotent (design "ApiEnablement"; Requirement 1.4).
  disable_on_destroy = false
}
