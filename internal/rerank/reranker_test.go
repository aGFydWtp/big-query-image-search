package rerank

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"google.golang.org/genai"

	"github.com/aGFydWtp/big-query-image-search/internal/search"
)

// scoreResponse builds a minimal GenerateContentResponse whose Text() yields the
// structured-output JSON {"score": n} the production path parses.
func scoreResponse(n int) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Parts: []*genai.Part{{Text: `{"score": ` + itoa(n) + `}`}},
			},
		}},
	}
}

// itoa avoids dragging strconv into the test for a tiny non-negative int.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [4]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// scoreByURI fakes the GenAIClient: it returns the score mapped from the image
// URI in the request's fileData part, recording per-URI call counts. A URI in
// failURIs always errors with the configured err.
type scoreByURI struct {
	mu       sync.Mutex
	scores   map[string]int // gs:// URI → score to return
	failURIs map[string]error
	calls    map[string]int
}

func newScoreByURI(scores map[string]int) *scoreByURI {
	return &scoreByURI{scores: scores, failURIs: map[string]error{}, calls: map[string]int{}}
}

func (s *scoreByURI) GenerateContent(_ context.Context, _ string, contents []*genai.Content, _ *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	uri := fileURIOf(contents)
	s.mu.Lock()
	s.calls[uri]++
	s.mu.Unlock()
	if err, ok := s.failURIs[uri]; ok {
		return nil, err
	}
	return scoreResponse(s.scores[uri]), nil
}

// fileURIOf extracts the gs:// fileData URI from a request's contents so the
// fake can map a candidate to its canned score.
func fileURIOf(contents []*genai.Content) string {
	for _, c := range contents {
		for _, p := range c.Parts {
			if p.FileData != nil {
				return p.FileData.FileURI
			}
		}
	}
	return ""
}

func rows(uris ...string) []search.Row {
	out := make([]search.Row, len(uris))
	for i, u := range uris {
		out[i] = search.Row{ImageURI: u, Distance: float64(i) / 100}
	}
	return out
}

func uris(rs []search.Row) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ImageURI
	}
	return out
}

func TestNoop_ReturnsRowsUnchanged(t *testing.T) {
	in := rows("gs://b/a.jpg", "gs://b/b.jpg")
	got, err := Noop{}.Rerank(context.Background(), "海の絵", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"gs://b/a.jpg", "gs://b/b.jpg"}; !equal(uris(got), want) {
		t.Errorf("order = %v, want %v", uris(got), want)
	}
}

// TestRerank_ReordersByScoreDesc verifies the head is sorted by score descending.
func TestRerank_ReordersByScoreDesc(t *testing.T) {
	fc := newScoreByURI(map[string]int{
		"gs://b/a.jpg": 2,
		"gs://b/b.jpg": 9,
		"gs://b/c.jpg": 5,
	})
	r := NewVertexReranker(fc, "gemini-2.5-flash", 50, 4, 0)

	got, err := r.Rerank(context.Background(), "海の絵", rows("gs://b/a.jpg", "gs://b/b.jpg", "gs://b/c.jpg"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"gs://b/b.jpg", "gs://b/c.jpg", "gs://b/a.jpg"}
	if !equal(uris(got), want) {
		t.Errorf("reranked order = %v, want %v", uris(got), want)
	}
}

// TestRerank_TiesPreserveRRFVecOrder verifies equal scores keep the original
// rrf_vec order (stable sort), the verified tie-break.
func TestRerank_TiesPreserveRRFVecOrder(t *testing.T) {
	fc := newScoreByURI(map[string]int{
		"gs://b/a.jpg": 7,
		"gs://b/b.jpg": 7,
		"gs://b/c.jpg": 7,
		"gs://b/d.jpg": 9,
	})
	r := NewVertexReranker(fc, "gemini-2.5-flash", 50, 4, 0)

	in := rows("gs://b/a.jpg", "gs://b/b.jpg", "gs://b/c.jpg", "gs://b/d.jpg")
	got, err := r.Rerank(context.Background(), "x", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// d (9) first; the three 7s keep their input order a,b,c.
	want := []string{"gs://b/d.jpg", "gs://b/a.jpg", "gs://b/b.jpg", "gs://b/c.jpg"}
	if !equal(uris(got), want) {
		t.Errorf("tie order = %v, want %v", uris(got), want)
	}
}

// TestRerank_TopNOnlyRescored verifies only the first topN rows are scored and
// the tail keeps its rrf_vec order appended after the reranked head.
func TestRerank_TopNOnlyRescored(t *testing.T) {
	fc := newScoreByURI(map[string]int{
		"gs://b/a.jpg": 1,
		"gs://b/b.jpg": 8,
	})
	r := NewVertexReranker(fc, "gemini-2.5-flash", 2, 4, 0)

	in := rows("gs://b/a.jpg", "gs://b/b.jpg", "gs://b/c.jpg", "gs://b/d.jpg")
	got, err := r.Rerank(context.Background(), "x", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// head reranked by score (b before a), tail c,d untouched and appended.
	want := []string{"gs://b/b.jpg", "gs://b/a.jpg", "gs://b/c.jpg", "gs://b/d.jpg"}
	if !equal(uris(got), want) {
		t.Errorf("order = %v, want %v", uris(got), want)
	}
	if fc.calls["gs://b/c.jpg"] != 0 || fc.calls["gs://b/d.jpg"] != 0 {
		t.Errorf("tail rows must not be scored; calls c=%d d=%d", fc.calls["gs://b/c.jpg"], fc.calls["gs://b/d.jpg"])
	}
}

// TestRerank_PartialFailureDegradesToInputOrder verifies that a single
// unrecoverable scoring failure abandons the rerank and returns the input order
// with an ErrRerankUpstream error (the handler then serves rrf_vec).
func TestRerank_PartialFailureDegradesToInputOrder(t *testing.T) {
	fc := newScoreByURI(map[string]int{
		"gs://b/a.jpg": 2,
		"gs://b/b.jpg": 9,
	})
	// 400 is non-retryable, so it surfaces immediately as a terminal failure.
	fc.failURIs["gs://b/b.jpg"] = genai.APIError{Code: 400, Message: "bad request"}
	r := NewVertexReranker(fc, "gemini-2.5-flash", 50, 4, 0)

	in := rows("gs://b/a.jpg", "gs://b/b.jpg")
	got, err := r.Rerank(context.Background(), "x", in)
	if err == nil {
		t.Fatal("expected an error on partial scoring failure")
	}
	if !errors.Is(err, ErrRerankUpstream) {
		t.Errorf("error not classified as ErrRerankUpstream: %v", err)
	}
	if !equal(uris(got), []string{"gs://b/a.jpg", "gs://b/b.jpg"}) {
		t.Errorf("degraded order = %v, want input order", uris(got))
	}
}

// TestRerank_RetriesTransientThenSucceeds verifies a retryable (503) failure is
// retried and the eventual success is used.
func TestRerank_RetriesTransientThenSucceeds(t *testing.T) {
	fc := &flakyClient{failTimes: 2, code: 503, score: 6}
	r := NewVertexReranker(fc, "gemini-2.5-flash", 50, 1, 0)

	got, err := r.Rerank(context.Background(), "x", rows("gs://b/a.jpg", "gs://b/b.jpg"))
	if err != nil {
		t.Fatalf("unexpected error after retries: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if fc.calls < 3 {
		t.Errorf("expected at least 3 calls (2 failures + 1 success), got %d", fc.calls)
	}
}

// TestRerank_BlankQueryShortCircuits verifies a blank query returns the input
// unchanged without calling the model.
func TestRerank_BlankQueryShortCircuits(t *testing.T) {
	fc := newScoreByURI(map[string]int{})
	r := NewVertexReranker(fc, "gemini-2.5-flash", 50, 4, 0)

	in := rows("gs://b/a.jpg", "gs://b/b.jpg")
	got, err := r.Rerank(context.Background(), "   ", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !equal(uris(got), []string{"gs://b/a.jpg", "gs://b/b.jpg"}) {
		t.Errorf("order = %v, want input order", uris(got))
	}
	if len(fc.calls) != 0 {
		t.Errorf("blank query must not score any image; calls=%v", fc.calls)
	}
}

// TestRerank_SingleRowShortCircuits verifies a single-row input is returned
// without scoring (nothing to reorder).
func TestRerank_SingleRowShortCircuits(t *testing.T) {
	fc := newScoreByURI(map[string]int{"gs://b/a.jpg": 3})
	r := NewVertexReranker(fc, "gemini-2.5-flash", 50, 4, 0)

	got, err := r.Rerank(context.Background(), "x", rows("gs://b/a.jpg"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(fc.calls) != 0 {
		t.Errorf("single row must short-circuit; got len=%d calls=%v", len(got), fc.calls)
	}
}

// TestRerank_RequestShapeMatchesVerifiedConfig asserts the request carries the
// gs:// fileData image, the 0–10 rubric, and the deterministic/structured config.
func TestRerank_RequestShapeMatchesVerifiedConfig(t *testing.T) {
	cap := &captureClient{score: 4}
	r := NewVertexReranker(cap, "gemini-2.5-flash", 50, 1, 0)

	if _, err := r.Rerank(context.Background(), "海の絵", rows("gs://b/a.jpg", "gs://b/b.jpg")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.gotModel != "gemini-2.5-flash" {
		t.Errorf("model = %q, want gemini-2.5-flash", cap.gotModel)
	}
	// fileData image part present with the gs:// URI.
	if got := fileURIOf(cap.gotContents); !strings.HasPrefix(got, "gs://b/") {
		t.Errorf("request missing gs:// fileData image; got fileURI %q", got)
	}
	// Rubric carries the query and the 0–10 scale directive.
	var text strings.Builder
	for _, c := range cap.gotContents {
		for _, p := range c.Parts {
			text.WriteString(p.Text)
		}
	}
	body := text.String()
	if !strings.Contains(body, "海の絵") {
		t.Errorf("rubric missing query: %q", body)
	}
	if !strings.Contains(body, "0〜10") {
		t.Errorf("rubric missing 0–10 scale directive: %q", body)
	}
	// Verified deterministic / structured-output config.
	cfg := cap.gotConfig
	if cfg == nil || cfg.Temperature == nil || *cfg.Temperature != 0 {
		t.Errorf("temperature must be 0; got %+v", cfg)
	}
	if cfg.ThinkingConfig == nil || cfg.ThinkingConfig.ThinkingBudget == nil || *cfg.ThinkingConfig.ThinkingBudget != 0 {
		t.Errorf("thinkingBudget must be 0; got %+v", cfg.ThinkingConfig)
	}
	if cfg.ResponseMIMEType != "application/json" || cfg.ResponseSchema == nil {
		t.Errorf("structured JSON output not configured; got mime=%q schema=%v", cfg.ResponseMIMEType, cfg.ResponseSchema)
	}
}

// TestRerank_ScoreClampedToScale verifies an out-of-range model score is clamped
// into [0, scaleMax] so a misbehaving model cannot distort ordering by extremes.
func TestRerank_ScoreClampedToScale(t *testing.T) {
	fc := newScoreByURI(map[string]int{
		"gs://b/a.jpg": 99, // clamps to 10
		"gs://b/b.jpg": 10,
	})
	r := NewVertexReranker(fc, "gemini-2.5-flash", 50, 4, 0)

	got, err := r.Rerank(context.Background(), "x", rows("gs://b/a.jpg", "gs://b/b.jpg"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Both clamp to 10 → tie → original order preserved.
	if !equal(uris(got), []string{"gs://b/a.jpg", "gs://b/b.jpg"}) {
		t.Errorf("order = %v, want input order (both clamp to 10)", uris(got))
	}
}

// TestCandidateDepth verifies the depth reported to the handler: 0 for Noop (no
// deeper fetch) and the configured topN for the Vertex reranker.
func TestCandidateDepth(t *testing.T) {
	if got := (Noop{}).CandidateDepth(); got != 0 {
		t.Errorf("Noop.CandidateDepth() = %d, want 0", got)
	}
	r := NewVertexReranker(newScoreByURI(nil), "gemini-2.5-flash", 20, 4, 0)
	if got := r.CandidateDepth(); got != 20 {
		t.Errorf("VertexReranker.CandidateDepth() = %d, want 20", got)
	}
	// A non-positive topN falls back to the verified default (50).
	rd := NewVertexReranker(newScoreByURI(nil), "gemini-2.5-flash", 0, 0, 0)
	if got := rd.CandidateDepth(); got != 50 {
		t.Errorf("default CandidateDepth() = %d, want 50", got)
	}
}

// flakyClient fails the first failTimes calls with an HTTP code, then succeeds.
type flakyClient struct {
	mu        sync.Mutex
	failTimes int
	code      int
	score     int
	calls     int
}

func (f *flakyClient) GenerateContent(_ context.Context, _ string, _ []*genai.Content, _ *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failTimes > 0 {
		f.failTimes--
		return nil, genai.APIError{Code: f.code, Message: "transient"}
	}
	return scoreResponse(f.score), nil
}

// captureClient records the last request so a test can assert its shape.
type captureClient struct {
	mu          sync.Mutex
	score       int
	gotModel    string
	gotContents []*genai.Content
	gotConfig   *genai.GenerateContentConfig
}

func (c *captureClient) GenerateContent(_ context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	c.mu.Lock()
	c.gotModel = model
	c.gotContents = contents
	c.gotConfig = config
	c.mu.Unlock()
	return scoreResponse(c.score), nil
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
