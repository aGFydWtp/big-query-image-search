// Command server is the image-search-api entrypoint: it loads configuration
// (fail-fast), assembles the HTTP router, and starts the Cloud Run HTTP server
// on the port supplied via the PORT environment variable.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/aGFydWtp/big-query-image-search/internal/config"
	"github.com/aGFydWtp/big-query-image-search/internal/httpapi"
)

// defaultPort is used when PORT is unset (e.g. local runs). Cloud Run always
// injects PORT, so this default only matters for local development.
const defaultPort = "8080"

func main() {
	// Fail-fast on configuration errors: a misconfigured service must not start
	// and silently serve broken requests.
	if _, err := config.Load(); err != nil {
		log.Fatalf("config load failed: %v", err)
	}

	// The search handler (task 4.1) is not yet available; passing nil registers
	// the route as a not-yet-wired seam. Task 4.1 replaces this argument.
	router := httpapi.NewRouter(nil)

	addr := ":" + port()
	log.Printf("image-search-api listening on %s", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
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
