# Cloud Run execution service account for the image-search API (Requirement 5).
#
# This file declares ONLY the identity that the downstream Cloud Run service
# (search API, owned by the image-search-api spec) will run as. Creating the SA
# here lets this spec exclusively own the identity and the minimal-privilege IAM
# grants attached to it (iam.tf, Requirement 5.2/5.3), and exposes its email so
# the downstream spec can bind it to a Cloud Run service (outputs.tf,
# Requirement 5.4).
#
# Boundary (Requirement 5.5, design Non-Goals): the Cloud Run service body
# itself — the container image deployment (google_cloud_run_v2_service or
# equivalent) — is INTENTIONALLY NOT created here. Doing so belongs to the
# image-search-api spec. This spec's responsibility is limited to issuing the
# service account and its IAM. No google_cloud_run_service / _v2_service,
# container, or image-deployment resource is declared in this module.

resource "google_service_account" "cloud_run" {
  project = var.project_id

  # account_id is the local part of the SA email; var.run_sa_account_id defaults
  # to "img-search-run" and is validated against GCP SA naming rules in
  # variables.tf. The resulting email is exposed via the `.email` attribute for
  # IAM binding (iam.tf, Requirement 5.2/5.3) and output (outputs.tf, Req 5.4).
  account_id   = var.run_sa_account_id
  display_name = "Image Search Cloud Run runtime"
  description  = "Cloud Run execution service account for the image search API. IAM grants are issued by this spec; the Cloud Run service body is deployed by the image-search-api spec (Requirement 5.1/5.5)."

  # Create only after the required GCP APIs (incl. IAM) are enabled
  # (Requirement 1.3, design "ApiEnablement"); iam.googleapis.com is enabled in
  # apis.tf via google_project_service.required.
  depends_on = [google_project_service.required]
}
