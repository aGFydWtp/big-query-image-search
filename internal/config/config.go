// Package config loads environment-dependent runtime settings from environment
// variables and validates them at startup (fail-fast).
//
// Environment variables:
//
//	PROJECT_ID        (required) GCP project id.
//	REGION            (required) BigQuery / GCS / remote-model location. Fixed
//	                  value us-central1, supplied by gcp-infrastructure input
//	                  variable. Read from env; fail-fast if unset.
//	DATASET_ID        (required) BigQuery dataset, project-qualified
//	                  (e.g. "project.dataset"). From gcp-infrastructure output
//	                  bigquery_dataset_id.
//	EMBEDDINGS_TABLE  (required) Embeddings table name (shared contract:
//	                  image_embeddings).
//	IMAGE_BUCKET      (required) Target GCS bucket for signed URLs. From
//	                  gcp-infrastructure output image_bucket_name.
//	RUN_SA_EMAIL      (required) Cloud Run runtime service account email. From
//	                  gcp-infrastructure output cloud_run_service_account_email.
//	MODEL             (optional) Remote model OBJECT name. Defaults to the shared
//	                  contract object name "gemini_embedding_model" (NOT the
//	                  endpoint name "gemini-embedding-2-preview").
//	SIGNED_URL_EXPIRY (optional) Signed URL validity as a Go duration
//	                  (e.g. "15m"). Defaults to 15m.
//
// No environment-dependent value is hardcoded. Only the shared-contract model
// object name default and the signed-URL expiry default are code defaults.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// defaultModel is the shared-contract remote model OBJECT name created by the
// image-ingestion-pipeline. It is NOT the endpoint name
// "gemini-embedding-2-preview"; the search SQL MODEL reference must resolve to
// the same object as the ingestion side.
const defaultModel = "gemini_embedding_model"

// defaultSignedURLExpiry is the default validity window for generated GCS V4
// signed URLs when SIGNED_URL_EXPIRY is unset.
const defaultSignedURLExpiry = 15 * time.Minute

// Config holds the resolved runtime configuration.
type Config struct {
	// ProjectID is the GCP project id (env PROJECT_ID).
	ProjectID string
	// Region is the BigQuery job / resource location (env REGION, fixed us-central1).
	Region string
	// DatasetID is the project-qualified BigQuery dataset (env DATASET_ID).
	DatasetID string
	// EmbeddingsTable is the embeddings table name (env EMBEDDINGS_TABLE).
	EmbeddingsTable string
	// Model is the remote model object name (env MODEL, default gemini_embedding_model).
	Model string
	// ImageBucket is the target GCS bucket for signed URLs (env IMAGE_BUCKET).
	ImageBucket string
	// RunSAEmail is the Cloud Run runtime service account email (env RUN_SA_EMAIL).
	RunSAEmail string
	// SignedURLExpiry is the signed URL validity window (env SIGNED_URL_EXPIRY, default 15m).
	SignedURLExpiry time.Duration
}

// Load reads configuration from environment variables and validates it.
//
// On success it returns a populated *Config and a nil error. If any required
// variable is missing or any value fails to parse, it returns a nil *Config and
// a clear aggregated error naming every offending variable, so the caller (main)
// can fail-fast at startup.
func Load() (*Config, error) {
	const (
		envProjectID       = "PROJECT_ID"
		envRegion          = "REGION"
		envDatasetID       = "DATASET_ID"
		envEmbeddingsTable = "EMBEDDINGS_TABLE"
		envImageBucket     = "IMAGE_BUCKET"
		envRunSAEmail      = "RUN_SA_EMAIL"
		envModel           = "MODEL"
		envSignedURLExpiry = "SIGNED_URL_EXPIRY"
	)

	var problems []string

	requireEnv := func(name string) string {
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" {
			problems = append(problems, fmt.Sprintf("missing required environment variable %s", name))
		}
		return v
	}

	cfg := &Config{
		ProjectID:       requireEnv(envProjectID),
		Region:          requireEnv(envRegion),
		DatasetID:       requireEnv(envDatasetID),
		EmbeddingsTable: requireEnv(envEmbeddingsTable),
		ImageBucket:     requireEnv(envImageBucket),
		RunSAEmail:      requireEnv(envRunSAEmail),
	}

	// MODEL is optional: it carries the single shared-contract object name, so a
	// code default is acceptable (it is not an environment-specific value).
	cfg.Model = strings.TrimSpace(os.Getenv(envModel))
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}

	// SIGNED_URL_EXPIRY is optional with a sensible default; an explicitly set
	// but unparseable value is an error.
	cfg.SignedURLExpiry = defaultSignedURLExpiry
	if raw := strings.TrimSpace(os.Getenv(envSignedURLExpiry)); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("invalid %s %q: %v", envSignedURLExpiry, raw, err))
		} else if d <= 0 {
			problems = append(problems, fmt.Sprintf("invalid %s %q: must be positive", envSignedURLExpiry, raw))
		} else {
			cfg.SignedURLExpiry = d
		}
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("config: %s", strings.Join(problems, "; "))
	}

	return cfg, nil
}
