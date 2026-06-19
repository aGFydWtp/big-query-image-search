package config

import (
	"strings"
	"testing"
	"time"
)

// requiredEnv returns a complete set of environment variables that satisfy all
// required values. Individual tests mutate / drop entries to exercise behavior.
func requiredEnv() map[string]string {
	return map[string]string{
		"PROJECT_ID":       "my-project",
		"REGION":           "us-central1",
		"DATASET_ID":       "my-project.image_search",
		"EMBEDDINGS_TABLE": "image_embeddings",
		"IMAGE_BUCKET":     "my-images-bucket",
		"RUN_SA_EMAIL":     "run-sa@my-project.iam.gserviceaccount.com",
	}
}

func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func TestLoad_AllRequiredPresent(t *testing.T) {
	env := requiredEnv()
	env["MODEL"] = "gemini_embedding_model"
	env["SIGNED_URL_EXPIRY"] = "10m"
	setEnv(t, env)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load() returned nil config without error")
	}

	if cfg.ProjectID != "my-project" {
		t.Errorf("ProjectID = %q, want %q", cfg.ProjectID, "my-project")
	}
	if cfg.Region != "us-central1" {
		t.Errorf("Region = %q, want %q", cfg.Region, "us-central1")
	}
	if cfg.DatasetID != "my-project.image_search" {
		t.Errorf("DatasetID = %q, want %q", cfg.DatasetID, "my-project.image_search")
	}
	if cfg.EmbeddingsTable != "image_embeddings" {
		t.Errorf("EmbeddingsTable = %q, want %q", cfg.EmbeddingsTable, "image_embeddings")
	}
	if cfg.Model != "gemini_embedding_model" {
		t.Errorf("Model = %q, want %q", cfg.Model, "gemini_embedding_model")
	}
	if cfg.ImageBucket != "my-images-bucket" {
		t.Errorf("ImageBucket = %q, want %q", cfg.ImageBucket, "my-images-bucket")
	}
	if cfg.RunSAEmail != "run-sa@my-project.iam.gserviceaccount.com" {
		t.Errorf("RunSAEmail = %q, want %q", cfg.RunSAEmail, "run-sa@my-project.iam.gserviceaccount.com")
	}
	if cfg.SignedURLExpiry != 10*time.Minute {
		t.Errorf("SignedURLExpiry = %v, want %v", cfg.SignedURLExpiry, 10*time.Minute)
	}
}

// TestLoad_ModelDefault verifies the DoD: when MODEL is absent the default is
// the shared-contract model OBJECT name `gemini_embedding_model` (NOT the
// endpoint name gemini-embedding-2-preview).
func TestLoad_ModelDefault(t *testing.T) {
	setEnv(t, requiredEnv()) // MODEL intentionally absent

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.Model != "gemini_embedding_model" {
		t.Errorf("default Model = %q, want %q", cfg.Model, "gemini_embedding_model")
	}
	if strings.Contains(cfg.Model, "gemini-embedding-2-preview") {
		t.Errorf("Model must be the object name, not the endpoint name; got %q", cfg.Model)
	}
}

func TestLoad_SignedURLExpiryDefault(t *testing.T) {
	setEnv(t, requiredEnv()) // SIGNED_URL_EXPIRY intentionally absent

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.SignedURLExpiry != 15*time.Minute {
		t.Errorf("default SignedURLExpiry = %v, want %v", cfg.SignedURLExpiry, 15*time.Minute)
	}
}

func TestLoad_SignedURLExpiryInvalid(t *testing.T) {
	env := requiredEnv()
	env["SIGNED_URL_EXPIRY"] = "not-a-duration"
	setEnv(t, env)

	cfg, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for invalid SIGNED_URL_EXPIRY, got nil")
	}
	if cfg != nil {
		t.Error("Load() should return nil config on error")
	}
	if !strings.Contains(err.Error(), "SIGNED_URL_EXPIRY") {
		t.Errorf("error should name SIGNED_URL_EXPIRY, got: %v", err)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	required := []string{
		"PROJECT_ID",
		"REGION",
		"DATASET_ID",
		"EMBEDDINGS_TABLE",
		"IMAGE_BUCKET",
		"RUN_SA_EMAIL",
	}

	for _, missing := range required {
		t.Run("missing_"+missing, func(t *testing.T) {
			env := requiredEnv()
			delete(env, missing)
			// Ensure the missing var is empty even if present in the real env.
			t.Setenv(missing, "")
			setEnv(t, env)

			cfg, err := Load()
			if err == nil {
				t.Fatalf("Load() expected error when %s missing, got nil", missing)
			}
			if cfg != nil {
				t.Errorf("Load() should return nil config when %s missing", missing)
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("error should name missing var %s, got: %v", missing, err)
			}
		})
	}
}

// TestLoad_AggregatesMissing verifies that multiple missing required vars are
// reported together (clear aggregated error), not one-at-a-time.
func TestLoad_AggregatesMissing(t *testing.T) {
	env := requiredEnv()
	delete(env, "PROJECT_ID")
	delete(env, "REGION")
	t.Setenv("PROJECT_ID", "")
	t.Setenv("REGION", "")
	setEnv(t, env)

	cfg, err := Load()
	if err == nil {
		t.Fatal("Load() expected aggregated error, got nil")
	}
	if cfg != nil {
		t.Error("Load() should return nil config on error")
	}
	if !strings.Contains(err.Error(), "PROJECT_ID") || !strings.Contains(err.Error(), "REGION") {
		t.Errorf("aggregated error should name both PROJECT_ID and REGION, got: %v", err)
	}
}
