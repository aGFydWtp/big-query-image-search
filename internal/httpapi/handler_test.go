package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/aGFydWtp/big-query-image-search/internal/rerank"
	"github.com/aGFydWtp/big-query-image-search/internal/search"
	"github.com/aGFydWtp/big-query-image-search/internal/signedurl"
)

// fakeSearcher records whether it was called and returns canned rows/error so
// the handler logic is exercised without a live BigQuery connection.
type fakeSearcher struct {
	called     bool
	gotQuery   string
	gotQueryEN string
	gotTopK    int
	rows       []search.Row
	err        error
}

func (f *fakeSearcher) Search(_ context.Context, query string, queryEN string, topK int) ([]search.Row, error) {
	f.called = true
	f.gotQuery = query
	f.gotQueryEN = queryEN
	f.gotTopK = topK
	return f.rows, f.err
}

// noopRewriter is a deterministic stand-in for the production rewriter used by
// tests that don't care about the rrf_vec channel — it returns "" so the SQL
// falls back to a raw-only search via COALESCE.
type noopRewriter struct{}

func (noopRewriter) Rewrite(_ context.Context, _ string) (string, error) { return "", nil }

// fakeRewriter records the call and returns canned (rewrite, err) so handler
// tests can assert both the success path (rewrite forwarded as query_en) and
// the failure-fallback path (raw-only search even when rewrite errored).
type fakeRewriter struct {
	gotQuery string
	called   bool
	rewrite  string
	err      error
}

func (f *fakeRewriter) Rewrite(_ context.Context, q string) (string, error) {
	f.called = true
	f.gotQuery = q
	return f.rewrite, f.err
}

// fakeReranker reorders rows by a fixed URI→rank map (lower rank first) so the
// handler's rerank wiring is verified without a live Gemini call. When err is
// set it returns the input rows unchanged plus the error, exercising the
// degrade-to-rrf_vec fallback.
type fakeReranker struct {
	called  bool
	gotRows int            // number of rows the handler passed in (candidate-pool depth)
	depth   int            // value returned by CandidateDepth()
	order   map[string]int // URI → sort key (ascending)
	err     error
}

func (f *fakeReranker) Rerank(_ context.Context, _ string, rows []search.Row) ([]search.Row, error) {
	f.called = true
	f.gotRows = len(rows)
	if f.err != nil {
		return rows, f.err
	}
	out := append([]search.Row(nil), rows...)
	sort.SliceStable(out, func(i, j int) bool { return f.order[out[i].ImageURI] < f.order[out[j].ImageURI] })
	return out, nil
}

func (f *fakeReranker) CandidateDepth() int { return f.depth }

// fakeSigner returns one Outcome per input URI, signing every URI to a fixed
// URL unless its URI is listed in failURIs (per-item failure).
type fakeSigner struct {
	failURIs map[string]bool
}

func (s *fakeSigner) SignedURLs(_ context.Context, uris []string) []signedurl.Outcome {
	out := make([]signedurl.Outcome, 0, len(uris))
	for _, u := range uris {
		if s.failURIs[u] {
			out = append(out, signedurl.Outcome{URI: u, Err: errors.New("signing failed")})
			continue
		}
		out = append(out, signedurl.Outcome{URI: u, URL: "https://signed.example/" + u})
	}
	return out
}

// decodeError unmarshals the common error envelope and fails the test if the
// shape does not match the stable contract {error:{code,message}}.
func decodeError(t *testing.T, body []byte) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("error body is not valid JSON: %v (body=%s)", err, body)
	}
	if env.Error.Code == "" {
		t.Errorf("error envelope missing non-empty code: %s", body)
	}
	if env.Error.Message == "" {
		t.Errorf("error envelope missing non-empty message: %s", body)
	}
	return env
}

// errorEnvelope mirrors the wire contract for assertions in tests.
type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func newRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// TestHandler_ValidQueryReturns200WithResults verifies a normal request yields
// 200 and a results array where each item carries image_uri and score (Req 1.1,
// 1.2, 1.5).
func TestHandler_ValidQueryReturns200WithResults(t *testing.T) {
	fs := &fakeSearcher{rows: []search.Row{
		{ImageURI: "gs://b/a.jpg", Distance: 0.1, ContentType: "image/jpeg"},
		{ImageURI: "gs://b/b.jpg", Distance: 0.2, ContentType: "image/png"},
	}}
	h := NewSearchHandler(fs, nil, nil, &fakeSigner{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(`{"query":"a dog"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !fs.called {
		t.Error("searcher was not called for a valid query")
	}
	if fs.gotQuery != "a dog" {
		t.Errorf("searcher got query %q, want %q", fs.gotQuery, "a dog")
	}
	if fs.gotQueryEN != "" {
		t.Errorf("searcher got query_en %q, want empty fallback", fs.gotQueryEN)
	}

	var resp struct {
		Results []struct {
			ImageURI  string  `json:"image_uri"`
			Score     float64 `json:"score"`
			SignedURL string  `json:"signed_url"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v (body=%s)", err, rec.Body.String())
	}
	if len(resp.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(resp.Results))
	}
	for i, r := range resp.Results {
		if r.ImageURI == "" {
			t.Errorf("result[%d] missing image_uri", i)
		}
		if r.Score == 0 {
			t.Errorf("result[%d] missing score", i)
		}
		// signed_url not requested → must be absent.
		if r.SignedURL != "" {
			t.Errorf("result[%d] has signed_url %q but signed_url was not requested", i, r.SignedURL)
		}
	}
}

func TestHandler_PassesEnglishRewriteToSearcher(t *testing.T) {
	fs := &fakeSearcher{rows: []search.Row{{ImageURI: "gs://b/a.jpg", Distance: 0.1}}}
	fr := &fakeRewriter{rewrite: "should-not-be-used"}
	h := NewSearchHandler(fs, fr, nil, &fakeSigner{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(`{"query":"海の絵","query_en":"a painting of the sea"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if fs.gotQuery != "海の絵" {
		t.Errorf("searcher got query %q, want %q", fs.gotQuery, "海の絵")
	}
	if fs.gotQueryEN != "a painting of the sea" {
		t.Errorf("searcher got query_en %q, want %q", fs.gotQueryEN, "a painting of the sea")
	}
	if fr.called {
		t.Error("rewriter must NOT be called when client supplied query_en (manual override)")
	}
}

// TestHandler_AutoRewriteSuppliesQueryEN verifies that when the client omits
// query_en, the handler invokes the server-side rewriter and forwards the
// generated English description to the searcher as the rrf_vec channel
// (docs/eval-results/comparison-vs-reference.md: overall +0.027).
func TestHandler_AutoRewriteSuppliesQueryEN(t *testing.T) {
	fs := &fakeSearcher{rows: []search.Row{{ImageURI: "gs://b/a.jpg", Distance: 0.1}}}
	fr := &fakeRewriter{rewrite: "a painting of the sea"}
	h := NewSearchHandler(fs, fr, nil, &fakeSigner{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(`{"query":"海の絵"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !fr.called {
		t.Fatal("rewriter was not invoked when client omitted query_en")
	}
	if fr.gotQuery != "海の絵" {
		t.Errorf("rewriter got query %q, want %q", fr.gotQuery, "海の絵")
	}
	if fs.gotQueryEN != "a painting of the sea" {
		t.Errorf("searcher got query_en %q, want %q (rewriter output not forwarded)", fs.gotQueryEN, "a painting of the sea")
	}
}

// TestHandler_RewriteFailureFallsBackToRawOnly verifies a rewriter error does
// NOT fail the request: search still runs with query_en="" so the SQL
// query_inputs CTE suppresses the rewrite row and collapses to the raw-only
// single-channel path documented in comparison-vs-reference.md.
func TestHandler_RewriteFailureFallsBackToRawOnly(t *testing.T) {
	fs := &fakeSearcher{rows: []search.Row{{ImageURI: "gs://b/a.jpg", Distance: 0.1}}}
	fr := &fakeRewriter{err: errors.New("rewrite: upstream failure: 503")}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	h := &searchHandlerImpl{search: fs, rewrite: fr, rerank: rerank.Noop{}, sign: &fakeSigner{}, logger: logger}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(`{"query":"海の絵"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (rewrite failure must not surface to client; body=%s)", rec.Code, rec.Body.String())
	}
	if fs.gotQueryEN != "" {
		t.Errorf("searcher got query_en %q on rewrite failure, want empty (raw-only fallback)", fs.gotQueryEN)
	}
	if !strings.Contains(logBuf.String(), "rewrite failed") {
		t.Errorf("server-side log missing rewrite-failure warning; got: %s", logBuf.String())
	}
}

// TestHandler_RerankReordersResults verifies the rerank stage reorders the
// search results before they are formatted and returned (the verified Gemini
// vision rerank). The fake reranks b ahead of a despite a's better rrf_vec rank.
func TestHandler_RerankReordersResults(t *testing.T) {
	fs := &fakeSearcher{rows: []search.Row{
		{ImageURI: "gs://b/a.jpg", Distance: 0.1},
		{ImageURI: "gs://b/b.jpg", Distance: 0.2},
	}}
	rr := &fakeReranker{order: map[string]int{"gs://b/b.jpg": 0, "gs://b/a.jpg": 1}}
	h := NewSearchHandler(fs, nil, rr, &fakeSigner{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(`{"query":"a dog"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !rr.called {
		t.Error("reranker was not invoked")
	}
	var resp struct {
		Results []struct {
			ImageURI string `json:"image_uri"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if len(resp.Results) != 2 || resp.Results[0].ImageURI != "gs://b/b.jpg" {
		t.Errorf("rerank order not applied; got %+v", resp.Results)
	}
}

// TestHandler_RerankFetchesDeeperPoolAndTruncates verifies the verified-uplift
// flow: when the reranker reports a candidate depth above top_k, the handler
// fetches that deeper pool, lets the rerank promote a candidate from beyond
// top_k, and truncates the response back to top_k. Here top_k defaults to 10,
// depth is 50, search returns 12 rows, and the rerank moves the 12th row to the
// front so it must appear in the returned top-10.
func TestHandler_RerankFetchesDeeperPoolAndTruncates(t *testing.T) {
	rows := make([]search.Row, 12)
	order := map[string]int{}
	for i := range rows {
		uri := "gs://b/" + string(rune('a'+i)) + ".jpg"
		rows[i] = search.Row{ImageURI: uri, Distance: float64(i) / 100}
		order[uri] = i // identity order...
	}
	promoted := rows[11].ImageURI
	order[promoted] = -1 // ...except the 12th row is promoted to the front.

	fs := &fakeSearcher{rows: rows}
	rr := &fakeReranker{depth: 50, order: order}
	h := NewSearchHandler(fs, nil, rr, &fakeSigner{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(`{"query":"a dog"}`)) // top_k omitted → DefaultTopK (10)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if fs.gotTopK != 50 {
		t.Errorf("searcher fetchK = %d, want 50 (deeper candidate pool for rerank)", fs.gotTopK)
	}
	if rr.gotRows != 12 {
		t.Errorf("reranker saw %d rows, want 12 (full fetched pool)", rr.gotRows)
	}
	var resp struct {
		Results []struct {
			ImageURI string `json:"image_uri"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if len(resp.Results) != 10 {
		t.Fatalf("response not truncated to top_k; got %d results, want 10", len(resp.Results))
	}
	if resp.Results[0].ImageURI != promoted {
		t.Errorf("promoted candidate not surfaced into top_k; first = %q, want %q", resp.Results[0].ImageURI, promoted)
	}
}

// TestHandler_RerankFailureFallsBackToRRFVec verifies a rerank error does NOT
// fail the request: the rrf_vec (search) order is served and the failure is
// logged at warn.
func TestHandler_RerankFailureFallsBackToRRFVec(t *testing.T) {
	fs := &fakeSearcher{rows: []search.Row{
		{ImageURI: "gs://b/a.jpg", Distance: 0.1},
		{ImageURI: "gs://b/b.jpg", Distance: 0.2},
	}}
	rr := &fakeReranker{err: errors.New("rerank: upstream failure: 503")}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	h := &searchHandlerImpl{search: fs, rewrite: noopRewriter{}, rerank: rr, sign: &fakeSigner{}, logger: logger}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(`{"query":"a dog"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (rerank failure must not surface; body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Results []struct {
			ImageURI string `json:"image_uri"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if len(resp.Results) != 2 || resp.Results[0].ImageURI != "gs://b/a.jpg" {
		t.Errorf("rerank failure must serve rrf_vec order; got %+v", resp.Results)
	}
	if !strings.Contains(logBuf.String(), "rerank failed") {
		t.Errorf("server-side log missing rerank-failure warning; got: %s", logBuf.String())
	}
}

// TestHandler_SignedURLRequestedFillsURL verifies signed_url=true populates the
// signed_url field on each result via the signer (Req 1.2, 3.2).
func TestHandler_SignedURLRequestedFillsURL(t *testing.T) {
	fs := &fakeSearcher{rows: []search.Row{
		{ImageURI: "gs://b/a.jpg", Distance: 0.1},
	}}
	h := NewSearchHandler(fs, nil, nil, &fakeSigner{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(`{"query":"cat","signed_url":true}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Results []struct {
			SignedURL string `json:"signed_url"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(resp.Results))
	}
	if resp.Results[0].SignedURL == "" {
		t.Error("signed_url requested but result has empty signed_url")
	}
}

// TestHandler_EmptyQueryReturns400AndSkipsSearch verifies an empty query yields
// 400 and the searcher is NOT invoked (Req 4.1).
func TestHandler_EmptyQueryReturns400AndSkipsSearch(t *testing.T) {
	fs := &fakeSearcher{}
	h := NewSearchHandler(fs, nil, nil, &fakeSigner{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(`{"query":"   "}`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if fs.called {
		t.Error("searcher was called on an empty query; BigQuery must not run (Req 4.1)")
	}
	decodeError(t, rec.Body.Bytes())
}

// TestHandler_MalformedBodyReturns400 verifies a malformed JSON body yields 400
// with the common error structure and does not run search.
func TestHandler_MalformedBodyReturns400(t *testing.T) {
	fs := &fakeSearcher{}
	h := NewSearchHandler(fs, nil, nil, &fakeSigner{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(`{"query":`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if fs.called {
		t.Error("searcher was called on a malformed body")
	}
	decodeError(t, rec.Body.Bytes())
}

// TestHandler_InternalErrorReturns5xxNoDetailLeak verifies a searcher failure
// maps to 5xx, the common error structure is present, and the raw error string
// does NOT leak into the response body (Req 4.3, 4.6).
func TestHandler_InternalErrorReturns5xxNoDetailLeak(t *testing.T) {
	const rawDetail = "raw bq detail XYZ"
	fs := &fakeSearcher{err: errors.New(rawDetail)}
	h := NewSearchHandler(fs, nil, nil, &fakeSigner{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(`{"query":"a dog"}`))

	if rec.Code < 500 || rec.Code > 599 {
		t.Fatalf("status = %d, want 5xx (body=%s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "XYZ") {
		t.Errorf("response leaked raw internal detail: %s", rec.Body.String())
	}
	decodeError(t, rec.Body.Bytes())
}

// causeErr mirrors *search.internalError for the handler's logging seam: Error()
// returns only a generic sentinel message (no leak) while Cause() retains the raw
// underlying reason that must appear in the server-side log only.
type causeErr struct {
	sentinel string
	cause    error
}

func (e *causeErr) Error() string { return e.sentinel }
func (e *causeErr) Cause() error  { return e.cause }

// TestHandler_InternalErrorLogsCauseServerSideOnly verifies the 5xx path records
// the raw internal cause (via Cause()) to the server-side log while still keeping
// it out of the client response body. This guards the diagnostic path that lets a
// Cloud Run 500 be triaged without reaching for BigQuery audit logs.
func TestHandler_InternalErrorLogsCauseServerSideOnly(t *testing.T) {
	const rawCause = "bigquery: connectionUser permission denied SECRET-DETAIL"
	const sentinel = "search: bigquery execution failed"

	fs := &fakeSearcher{err: &causeErr{sentinel: sentinel, cause: errors.New(rawCause)}}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	h := &searchHandlerImpl{search: fs, rewrite: noopRewriter{}, rerank: rerank.Noop{}, sign: &fakeSigner{}, logger: logger}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(`{"query":"a dog"}`))

	if rec.Code < 500 || rec.Code > 599 {
		t.Fatalf("status = %d, want 5xx (body=%s)", rec.Code, rec.Body.String())
	}
	// Client response must not leak the raw cause.
	if strings.Contains(rec.Body.String(), "SECRET-DETAIL") {
		t.Errorf("response leaked raw internal cause: %s", rec.Body.String())
	}
	// Server-side log MUST carry the raw cause for diagnosis.
	logged := logBuf.String()
	if logged == "" {
		t.Fatal("expected a server-side log line for the 5xx, got none")
	}
	if !strings.Contains(logged, "SECRET-DETAIL") {
		t.Errorf("server-side log missing raw cause detail; got: %s", logged)
	}
	if !strings.Contains(logged, "search failed") {
		t.Errorf("server-side log missing the failure message; got: %s", logged)
	}
}

// TestHandler_InternalErrorLogsPlainError verifies a plain error (no Cause()) is
// still logged server-side via its Error() string, so the diagnostic path does
// not depend on the *search.internalError type being present.
func TestHandler_InternalErrorLogsPlainError(t *testing.T) {
	fs := &fakeSearcher{err: errors.New("plain failure DIAG-TOKEN")}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	h := &searchHandlerImpl{search: fs, rewrite: noopRewriter{}, rerank: rerank.Noop{}, sign: &fakeSigner{}, logger: logger}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(`{"query":"a dog"}`))

	if rec.Code < 500 || rec.Code > 599 {
		t.Fatalf("status = %d, want 5xx (body=%s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "DIAG-TOKEN") {
		t.Errorf("response leaked raw error detail: %s", rec.Body.String())
	}
	if !strings.Contains(logBuf.String(), "DIAG-TOKEN") {
		t.Errorf("server-side log missing the error detail; got: %s", logBuf.String())
	}
}

// TestHandler_ZeroRowsReturnsEmptyArray verifies 0 matches yields 200 with
// {"results":[]} (a non-null empty array) per Req 4.4 robustness.
func TestHandler_ZeroRowsReturnsEmptyArray(t *testing.T) {
	fs := &fakeSearcher{rows: nil}
	h := NewSearchHandler(fs, nil, nil, &fakeSigner{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(`{"query":"nothing matches"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := strings.ReplaceAll(rec.Body.String(), " ", "")
	if !strings.Contains(body, `"results":[]`) {
		t.Errorf("zero rows must serialize results as [], got %s", rec.Body.String())
	}
	if strings.Contains(body, `"results":null`) {
		t.Errorf("results must be [] not null, got %s", rec.Body.String())
	}
}

// TestHandler_SignerPartialFailureStillReturns200 verifies a per-item signing
// failure does not fail the whole request: the failing item omits signed_url
// while siblings still carry it, and overall status is 200 (Req 4.5).
func TestHandler_SignerPartialFailureStillReturns200(t *testing.T) {
	fs := &fakeSearcher{rows: []search.Row{
		{ImageURI: "gs://b/ok.jpg", Distance: 0.1},
		{ImageURI: "gs://b/bad.jpg", Distance: 0.2},
	}}
	signer := &fakeSigner{failURIs: map[string]bool{"gs://b/bad.jpg": true}}
	h := NewSearchHandler(fs, nil, nil, signer)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(`{"query":"x","signed_url":true}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Results []struct {
			ImageURI  string `json:"image_uri"`
			SignedURL string `json:"signed_url"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(resp.Results))
	}
	byURI := map[string]string{}
	for _, r := range resp.Results {
		byURI[r.ImageURI] = r.SignedURL
	}
	if byURI["gs://b/ok.jpg"] == "" {
		t.Error("healthy item lost its signed_url due to a sibling failure")
	}
	if byURI["gs://b/bad.jpg"] != "" {
		t.Errorf("failing item should omit signed_url, got %q", byURI["gs://b/bad.jpg"])
	}
}
