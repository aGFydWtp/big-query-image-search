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

	"github.com/aGFydWtp/big-query-image-search/internal/config"
	"github.com/aGFydWtp/big-query-image-search/internal/httpapi"
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

	return httpapi.NewSearchHandler(searchClient, signGen), nil
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
