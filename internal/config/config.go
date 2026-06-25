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
//	REWRITE_ENABLED   (optional) Enable Vertex AI server-side query rewrite for
//	                  the rrf_vec RRF channel (see
//	                  docs/eval-results/comparison-vs-reference.md). Set to
//	                  "false"/"0" to disable and serve raw-only single-channel
//	                  search. Defaults to true.
//	REWRITE_MODEL     (optional) Vertex AI generative model id used by the
//	                  rewriter. Defaults to "gemini-2.5-flash".
//	REWRITE_TIMEOUT   (optional) Per-call rewrite timeout as a Go duration.
//	                  Defaults to 1500ms (rewrite failure degrades to raw-only,
//	                  so a tight ceiling keeps p99 search latency bounded).
//	RERANK_ENABLED    (optional) Enable the Gemini vision rerank stage that
//	                  reorders the rrf_vec top-N by per-image relevance (verified
//	                  winner: overall nDCG@10 0.670 → 0.736, see
//	                  docs/eval-results/comparison-vs-reference.md "rerank Phase
//	                  1b"). Defaults to FALSE because it costs ≈$0.027/search at
//	                  N=50 (≈$4,000/month at 5,000 searches/day); enable
//	                  explicitly and/or lower RERANK_TOP_N to trade quality for
//	                  cost. On any scoring failure the search degrades to the
//	                  rrf_vec order.
//	RERANK_MODEL      (optional) Vertex AI Gemini model used to score images.
//	                  Defaults to "gemini-2.5-flash" (the verified model).
//	RERANK_TOP_N      (optional) Number of top rrf_vec candidates to rerank.
//	                  Defaults to 50. Lower values cut cost proportionally.
//	RERANK_WORKERS    (optional) Max concurrent per-image scoring calls.
//	                  Defaults to 6.
//	RERANK_TIMEOUT    (optional) Per-image scoring call timeout as a Go duration.
//	                  Defaults to 8s.
//
// No environment-dependent value is hardcoded. Only the shared-contract model
// object name default and the signed-URL expiry default are code defaults.
package config

import (
	"fmt"
	"os"
	"strconv"
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

// defaultRewriteModel is the Vertex AI Gemini model used to translate JP
// queries into English image-search descriptions for the rrf_vec channel.
const defaultRewriteModel = "gemini-2.5-flash"

// defaultRewriteTimeout caps the rewrite call so a slow Vertex AI response
// cannot stall the search request. On timeout the handler degrades to
// raw-only via SQL COALESCE.
const defaultRewriteTimeout = 1500 * time.Millisecond

// defaultRerankModel is the Vertex AI Gemini model used to score candidate
// images in the rerank stage (the verified winning model).
const defaultRerankModel = "gemini-2.5-flash"

// defaultRerankTopN is the number of top rrf_vec candidates rescored by the
// rerank stage when RERANK_TOP_N is unset (the verified depth).
const defaultRerankTopN = 50

// defaultRerankWorkers bounds concurrent per-image scoring calls when
// RERANK_WORKERS is unset.
const defaultRerankWorkers = 6

// defaultRerankTimeout caps each per-image scoring call so a slow Vertex AI
// response cannot stall the search request; on failure the search degrades to
// the rrf_vec order.
const defaultRerankTimeout = 8 * time.Second

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
	// RewriteEnabled toggles the Vertex AI server-side query rewrite for the
	// rrf_vec RRF channel (env REWRITE_ENABLED, default true).
	RewriteEnabled bool
	// RewriteModel is the Vertex AI generative model used by the rewriter
	// (env REWRITE_MODEL, default "gemini-2.5-flash").
	RewriteModel string
	// RewriteTimeout caps each rewrite call (env REWRITE_TIMEOUT, default 1500ms).
	RewriteTimeout time.Duration
	// RerankEnabled toggles the Gemini vision rerank stage that reorders the
	// rrf_vec top-N by per-image relevance (env RERANK_ENABLED, default false).
	RerankEnabled bool
	// RerankModel is the Vertex AI Gemini model used to score images
	// (env RERANK_MODEL, default "gemini-2.5-flash").
	RerankModel string
	// RerankTopN is the number of top rrf_vec candidates to rerank
	// (env RERANK_TOP_N, default 50).
	RerankTopN int
	// RerankWorkers bounds concurrent per-image scoring calls
	// (env RERANK_WORKERS, default 6).
	RerankWorkers int
	// RerankTimeout caps each per-image scoring call (env RERANK_TIMEOUT,
	// default 8s).
	RerankTimeout time.Duration
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
		envRewriteEnabled  = "REWRITE_ENABLED"
		envRewriteModel    = "REWRITE_MODEL"
		envRewriteTimeout  = "REWRITE_TIMEOUT"
		envRerankEnabled   = "RERANK_ENABLED"
		envRerankModel     = "RERANK_MODEL"
		envRerankTopN      = "RERANK_TOP_N"
		envRerankWorkers   = "RERANK_WORKERS"
		envRerankTimeout   = "RERANK_TIMEOUT"
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

	// REWRITE_ENABLED: opt-out toggle for the rrf_vec rewrite channel. Default
	// is true (rewrite on) so the verified +0.027 nDCG@10 improvement is the
	// out-of-the-box behavior; explicit "false"/"0" disables.
	cfg.RewriteEnabled = true
	if raw := strings.TrimSpace(os.Getenv(envRewriteEnabled)); raw != "" {
		switch strings.ToLower(raw) {
		case "false", "0", "no", "off":
			cfg.RewriteEnabled = false
		case "true", "1", "yes", "on":
			cfg.RewriteEnabled = true
		default:
			problems = append(problems, fmt.Sprintf("invalid %s %q: expected true/false", envRewriteEnabled, raw))
		}
	}

	cfg.RewriteModel = strings.TrimSpace(os.Getenv(envRewriteModel))
	if cfg.RewriteModel == "" {
		cfg.RewriteModel = defaultRewriteModel
	}

	cfg.RewriteTimeout = defaultRewriteTimeout
	if raw := strings.TrimSpace(os.Getenv(envRewriteTimeout)); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("invalid %s %q: %v", envRewriteTimeout, raw, err))
		} else if d <= 0 {
			problems = append(problems, fmt.Sprintf("invalid %s %q: must be positive", envRewriteTimeout, raw))
		} else {
			cfg.RewriteTimeout = d
		}
	}

	// RERANK_ENABLED: opt-IN toggle (default false) for the Gemini vision rerank
	// stage. Default is off because reranking N=50 costs ≈$0.027/search; operators
	// enable it (and/or tune RERANK_TOP_N) deliberately.
	cfg.RerankEnabled = false
	if raw := strings.TrimSpace(os.Getenv(envRerankEnabled)); raw != "" {
		switch strings.ToLower(raw) {
		case "false", "0", "no", "off":
			cfg.RerankEnabled = false
		case "true", "1", "yes", "on":
			cfg.RerankEnabled = true
		default:
			problems = append(problems, fmt.Sprintf("invalid %s %q: expected true/false", envRerankEnabled, raw))
		}
	}

	cfg.RerankModel = strings.TrimSpace(os.Getenv(envRerankModel))
	if cfg.RerankModel == "" {
		cfg.RerankModel = defaultRerankModel
	}

	cfg.RerankTopN = defaultRerankTopN
	if raw := strings.TrimSpace(os.Getenv(envRerankTopN)); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("invalid %s %q: %v", envRerankTopN, raw, err))
		} else if n <= 0 {
			problems = append(problems, fmt.Sprintf("invalid %s %q: must be positive", envRerankTopN, raw))
		} else {
			cfg.RerankTopN = n
		}
	}

	cfg.RerankWorkers = defaultRerankWorkers
	if raw := strings.TrimSpace(os.Getenv(envRerankWorkers)); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("invalid %s %q: %v", envRerankWorkers, raw, err))
		} else if n <= 0 {
			problems = append(problems, fmt.Sprintf("invalid %s %q: must be positive", envRerankWorkers, raw))
		} else {
			cfg.RerankWorkers = n
		}
	}

	cfg.RerankTimeout = defaultRerankTimeout
	if raw := strings.TrimSpace(os.Getenv(envRerankTimeout)); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("invalid %s %q: %v", envRerankTimeout, raw, err))
		} else if d <= 0 {
			problems = append(problems, fmt.Sprintf("invalid %s %q: must be positive", envRerankTimeout, raw))
		} else {
			cfg.RerankTimeout = d
		}
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("config: %s", strings.Join(problems, "; "))
	}

	return cfg, nil
}
