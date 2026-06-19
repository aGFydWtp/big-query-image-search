package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/aGFydWtp/big-query-image-search/internal/result"
	"github.com/aGFydWtp/big-query-image-search/internal/search"
	"github.com/aGFydWtp/big-query-image-search/internal/signedurl"
	"github.com/aGFydWtp/big-query-image-search/internal/validation"
)

// searcher is the execution seam for the BigQuery search step. The production
// implementation is *search.BigQueryClient; tests inject a fake so the handler
// flow is verified without a live BigQuery connection.
type searcher interface {
	Search(ctx context.Context, query string, topK int) ([]search.Row, error)
}

// signer is the seam for batch signed-URL generation. The production
// implementation is *signedurl.Generator; tests inject a fake. Per-item
// failures are confined to each Outcome.Err and never fail the request (Req 4.5).
type signer interface {
	SignedURLs(ctx context.Context, uris []string) []signedurl.Outcome
}

// searchRequest is the request body contract for POST /search.
//
// TopK is a *int so an omitted top_k (nil → default) is distinguishable from an
// explicit zero (which validation rejects as a 400). SignedURL gates whether
// per-result signed URLs are generated.
type searchRequest struct {
	Query     string `json:"query"`
	TopK      *int   `json:"top_k"`
	SignedURL bool   `json:"signed_url"`
}

// searchResponse is the success (200) body contract. Results is always a
// non-nil slice so zero matches serialize as [] rather than null (Req 4.4).
type searchResponse struct {
	Results []result.SearchResult `json:"results"`
}

// apiError is the common error envelope used for every 4xx and 5xx response
// (Req 4.6). Code is a stable machine-readable token; Message is human-readable
// and deliberately generic so no internal detail leaks (Req 4.3).
type apiError struct {
	Error apiErrorBody `json:"error"`
}

type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Stable error codes for the discriminable error contract (Req 4.6).
const (
	codeInvalidRequest = "invalid_request"
	codeInternal       = "internal_error"
)

// searchHandler implements the POST /search endpoint over injectable seams.
type searchHandlerImpl struct {
	search searcher
	sign   signer
	// logger sinks server-side diagnostics (e.g. the internal cause of a 5xx)
	// that are deliberately withheld from the client response. Never nil after
	// NewSearchHandler; tests inject a buffer-backed logger to assert the path.
	logger *slog.Logger
}

// NewSearchHandler constructs the search endpoint handler from its seams. main
// supplies the real *search.BigQueryClient and *signedurl.Generator; tests
// supply fakes. Server-side diagnostics go to slog.Default() (Cloud Run routes
// stderr to Cloud Logging), so no logger plumbing is required at the call site.
func NewSearchHandler(s searcher, sg signer) http.Handler {
	return &searchHandlerImpl{search: s, sign: sg, logger: slog.Default()}
}

// causer is the seam over *search.internalError, which retains the raw BigQuery
// failure on Cause() while its Error() returns only a generic sentinel message.
// The handler reads Cause() to log the real reason (e.g. a missing connectionUser
// IAM grant) server-side without ever exposing it in the response (Req 4.3).
type causer interface {
	Cause() error
}

func (h *searchHandlerImpl) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "request body is not valid JSON")
		return
	}

	// Validate before touching BigQuery so an empty query never issues a job
	// (Req 4.1).
	validated, err := validation.Validate(req.Query, req.TopK)
	if err != nil {
		if errors.Is(err, validation.ErrInvalidInput) {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, "query is required and top_k must be a positive integer")
			return
		}
		// Defensive: any non-classified validation failure is treated as a 400.
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "invalid request")
		return
	}

	rows, err := h.search.Search(r.Context(), validated.Query, validated.TopK)
	if err != nil {
		// Record the internal cause server-side ONLY; the client still receives a
		// generic message with no upstream/internal detail (Req 4.3). Without this
		// line, diagnosing a 5xx on Cloud Run requires correlating BigQuery audit
		// logs (e.g. the connectionUser IAM grant case), which slows triage.
		h.logSearchFailure(r.Context(), err)
		writeError(w, http.StatusInternalServerError, codeInternal, "search failed, please retry later")
		return
	}

	results := result.Format(rows)

	if req.SignedURL {
		h.fillSignedURLs(r.Context(), results)
	}

	writeJSON(w, http.StatusOK, searchResponse{Results: results})
}

// logSearchFailure records the underlying reason for a search 5xx to the
// server-side log. It prefers the raw Cause() retained by *search.internalError
// (the real BigQuery/IAM failure) and falls back to err itself for plain errors.
// The logged detail is never written to the client response.
func (h *searchHandlerImpl) logSearchFailure(ctx context.Context, err error) {
	attrs := []slog.Attr{slog.String("error", err.Error())}
	var c causer
	if errors.As(err, &c) {
		if cause := c.Cause(); cause != nil {
			attrs = append(attrs, slog.String("cause", cause.Error()))
		}
	}
	h.logger.LogAttrs(ctx, slog.LevelError, "search failed", attrs...)
}

// fillSignedURLs populates SignedURL on each result via the signer seam.
// Per-item signing failures are tolerated: the failing item simply keeps an
// empty SignedURL (omitted by omitempty) while siblings retain theirs (Req 4.5).
func (h *searchHandlerImpl) fillSignedURLs(ctx context.Context, results []result.SearchResult) {
	if len(results) == 0 {
		return
	}
	uris := make([]string, len(results))
	for i := range results {
		uris[i] = results[i].ImageURI
	}
	outcomes := h.sign.SignedURLs(ctx, uris)
	byURI := make(map[string]string, len(outcomes))
	for _, o := range outcomes {
		if o.Err == nil && o.URL != "" {
			byURI[o.URI] = o.URL
		}
	}
	for i := range results {
		if url, ok := byURI[results[i].ImageURI]; ok {
			results[i].SignedURL = url
		}
	}
}

// writeError writes the common error envelope with the given status, code, and
// generic message. It is the single sink for all 4xx/5xx responses (Req 4.6).
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiError{Error: apiErrorBody{Code: code, Message: message}})
}

// writeJSON serializes v as the response body with the given status. A
// marshalling failure (not expected for these fixed shapes) degrades to a
// generic 500 envelope written as a literal so it cannot recurse.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"internal_error","message":"response encoding failed"}}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
