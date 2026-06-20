package httpapi

import (
	"embed"
	"io/fs"
	"net/http"
)

// webAssets holds the embedded search SPA (index.html, css/, js/). Embedding
// keeps the UI inside the single server binary, so the static files ship with
// the same Cloud Run image as the API and are served same-origin (no CORS).
//
//go:embed web
var webAssets embed.FS

// staticHandler serves the embedded SPA at the site root: GET / returns
// index.html and GET /css/* , /js/* return the assets it references. It is
// mounted on the catch-all "GET /" route, which the ServeMux only reaches after
// the more specific /healthz and /search routes (longest-pattern wins).
func staticHandler() http.Handler {
	sub, err := fs.Sub(webAssets, "web")
	if err != nil {
		// The embed path is resolved at build time; reaching here would mean the
		// embedded tree is missing, which is a build-time inconsistency, not a
		// runtime condition.
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}
