// Package httpapi wires the stateless HTTP surface of the image-search-api:
// routing, the health-check endpoint, and the seam where the search handler
// (task 4.1) is injected. It owns no mutable request state; an *http.ServeMux is
// built per call so the same router is safe to construct in main and in tests.
//
// server.go is responsible for routing / port-agnostic handler assembly /
// health check (Requirement 5.1, 5.3). The concrete search handler lives in
// handler.go (task 4.1) and is supplied to NewRouter as an injectable
// http.Handler, so this file never needs to change to integrate it.
package httpapi

import "net/http"

// healthPath is the liveness/readiness probe path. It returns 200 with no body
// dependencies so a freshly started container reports healthy on Cloud Run.
const healthPath = "/healthz"

// searchPath is the text-to-image search endpoint (request/response contract
// owned by handler.go, task 4.1).
const searchPath = "/search"

// NewRouter assembles the HTTP routing for the service and returns it as an
// http.Handler so it can be exercised with net/http/httptest without binding a
// port. The port is a deployment concern resolved in main (env PORT), not here.
//
// searchHandler is the injectable seam for the search endpoint: main passes the
// real handler (task 4.1) while tests pass a stub. When searchHandler is nil the
// route is still registered with a placeholder that reports the endpoint is not
// yet wired (501), keeping the route a live seam rather than a 404 fall-through.
func NewRouter(searchHandler http.Handler) http.Handler {
	if searchHandler == nil {
		searchHandler = notImplementedSearchHandler()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+healthPath, handleHealth)
	mux.Handle("POST "+searchPath, searchHandler)
	return mux
}

// handleHealth answers the health probe with 200 OK. It reads no external state,
// so it is a true liveness signal for the container.
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// notImplementedSearchHandler is the placeholder occupying the search seam until
// task 4.1 injects the real handler. It returns 501 so the route is demonstrably
// registered (not a 404) while signalling the behavior is not yet implemented.
func notImplementedSearchHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "search handler not yet wired", http.StatusNotImplemented)
	})
}
