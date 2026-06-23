// Command server is the image-search-api entrypoint: it loads configuration
// (fail-fast), assembles the HTTP router, and starts the Cloud Run HTTP server
// on the port supplied via the PORT environment variable.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"cloud.google.com/go/bigquery"
	credentials "cloud.google.com/go/iam/credentials/apiv1"
	"google.golang.org/genai"

	"github.com/aGFydWtp/big-query-image-search/internal/config"
	"github.com/aGFydWtp/big-query-image-search/internal/httpapi"
	"github.com/aGFydWtp/big-query-image-search/internal/rewrite"
	"github.com/aGFydWtp/big-query-image-search/internal/search"
	"github.com/aGFydWtp/big-query-image-search/internal/signedurl"
)

// defaultPort is used when PORT is unset (e.g. local runs). Cloud Run always
// injects PORT, so this default only matters for local development.
const defaultPort = "8080"

func main() {
	// Fail-fast on configuration errors: a misconfigured service must not start
	// and silently serve broken requests.
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config load failed: %v", err)
	}

	ctx := context.Background()

	// Assemble the real search/signing dependency chain. Cloud client
	// construction failures are fatal: a service that cannot reach its
	// dependencies must not start and silently serve broken requests.
	searchHandler, err := buildSearchHandler(ctx, cfg)
	if err != nil {
		log.Fatalf("dependency wiring failed: %v", err)
	}

	router := httpapi.NewRouter(searchHandler)

	addr := ":" + port()
	log.Printf("image-search-api listening on %s", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}

// buildSearchHandler constructs the real search and signed-URL dependency chain
// from config and returns the wired POST /search handler. It keeps the cloud
// clients alive for the process lifetime (no Close): they are owned by the
// long-running server.
func buildSearchHandler(ctx context.Context, cfg *config.Config) (http.Handler, error) {
	builder, err := search.NewQueryBuilder(cfg.DatasetID, cfg.Model, cfg.EmbeddingsTable)
	if err != nil {
		return nil, err
	}

	bqClient, err := bigquery.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		return nil, err
	}
	runner := search.NewBigQueryRunner(bqClient, cfg.Region)
	searchClient := search.NewBigQueryClient(builder, runner)

	iamClient, err := credentials.NewIamCredentialsClient(ctx)
	if err != nil {
		return nil, err
	}
	signGen := signedurl.New(signedurl.Config{
		Bucket:         cfg.ImageBucket,
		GoogleAccessID: cfg.RunSAEmail,
		Expiry:         cfg.SignedURLExpiry,
		SignBytes:      signedurl.NewIAMSignBytes(ctx, iamClient, cfg.RunSAEmail),
	})

	rewriter, err := buildRewriter(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return httpapi.NewSearchHandler(searchClient, rewriter, signGen), nil
}

// buildRewriter wires the Vertex AI Gemini query rewriter that supplies the
// rrf_vec channel (docs/eval-results/comparison-vs-reference.md). When
// REWRITE_ENABLED=false it returns a Noop so the handler degrades to raw-only
// single-channel search; otherwise it constructs a genai.Client bound to
// Vertex AI in the configured project/region using Application Default
// Credentials (Cloud Run injects the runtime SA).
func buildRewriter(ctx context.Context, cfg *config.Config) (rewrite.Rewriter, error) {
	if !cfg.RewriteEnabled {
		return rewrite.Noop{}, nil
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  cfg.ProjectID,
		Location: cfg.Region,
	})
	if err != nil {
		return nil, err
	}
	return rewrite.NewVertexRewriter(client.Models, cfg.RewriteModel, cfg.RewriteTimeout), nil
}

// port resolves the listen port from the PORT env var (Cloud Run convention),
// falling back to defaultPort for local runs. It is read here in main, never
// hardcoded in the handler layer.
func port() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return defaultPort
}
