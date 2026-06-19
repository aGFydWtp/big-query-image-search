package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aGFydWtp/big-query-image-search/internal/search"
	"github.com/aGFydWtp/big-query-image-search/internal/signedurl"
)

// fakeSearcher records whether it was called and returns canned rows/error so
// the handler logic is exercised without a live BigQuery connection.
type fakeSearcher struct {
	called    bool
	gotQuery  string
	gotTopK   int
	rows      []search.Row
	err       error
}

func (f *fakeSearcher) Search(_ context.Context, query string, topK int) ([]search.Row, error) {
	f.called = true
	f.gotQuery = query
	f.gotTopK = topK
	return f.rows, f.err
}

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
	h := NewSearchHandler(fs, &fakeSigner{})

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

// TestHandler_SignedURLRequestedFillsURL verifies signed_url=true populates the
// signed_url field on each result via the signer (Req 1.2, 3.2).
func TestHandler_SignedURLRequestedFillsURL(t *testing.T) {
	fs := &fakeSearcher{rows: []search.Row{
		{ImageURI: "gs://b/a.jpg", Distance: 0.1},
	}}
	h := NewSearchHandler(fs, &fakeSigner{})

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
	h := NewSearchHandler(fs, &fakeSigner{})

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
	h := NewSearchHandler(fs, &fakeSigner{})

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
	h := NewSearchHandler(fs, &fakeSigner{})

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

// TestHandler_ZeroRowsReturnsEmptyArray verifies 0 matches yields 200 with
// {"results":[]} (a non-null empty array) per Req 4.4 robustness.
func TestHandler_ZeroRowsReturnsEmptyArray(t *testing.T) {
	fs := &fakeSearcher{rows: nil}
	h := NewSearchHandler(fs, &fakeSigner{})

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
	h := NewSearchHandler(fs, signer)

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
