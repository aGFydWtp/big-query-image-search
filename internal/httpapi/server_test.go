package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealthCheckReturns200 is the authoritative observable completion check for
// task 1.2: a router built by NewRouter must answer the health endpoint with 200.
// Requirement 5.1 (Cloud Run service definition needs a working container) and
// 5.3 (stateless runtime) — the health probe is exercised without binding a port.
func TestHealthCheckReturns200(t *testing.T) {
	router := NewRouter(nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("health check: got status %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestSearchRouteIsRegistered confirms the search route exists as a wiring seam.
// The concrete search handler is task 4.1; here we only assert the route is wired
// (i.e. not a 404 fall-through) so task 4.1 has a registered seam to replace.
func TestSearchRouteIsRegistered(t *testing.T) {
	router := NewRouter(nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/search", nil)
	router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Fatalf("search route not registered: got 404 for POST /search")
	}
}

// TestSearchRouteUsesInjectedHandler verifies the search route is an injectable
// seam: when a handler is supplied to NewRouter it is invoked for /search, which
// is how task 4.1 wires in the real search handler without editing server.go.
func TestSearchRouteUsesInjectedHandler(t *testing.T) {
	const marker = http.StatusTeapot
	injected := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(marker)
	})
	router := NewRouter(injected)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/search", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != marker {
		t.Fatalf("injected search handler not used: got status %d, want %d", rec.Code, marker)
	}
}
