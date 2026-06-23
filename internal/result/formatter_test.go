package result

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/aGFydWtp/big-query-image-search/internal/search"
)

const floatTolerance = 1e-9

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) <= floatTolerance
}

// score = 1 - distance per row (Req 3.1, 3.4).
func TestFormat_ScoreIsOneMinusDistance(t *testing.T) {
	rows := []search.Row{
		{ImageURI: "gs://bucket/a.jpg", Distance: 0.1, ContentType: "image/jpeg"},
	}

	got := Format(rows)

	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if !almostEqual(got[0].Score, 0.9) {
		t.Errorf("score = %v, want 0.9 (1 - distance 0.1)", got[0].Score)
	}
	if got[0].ImageURI != "gs://bucket/a.jpg" {
		t.Errorf("image_uri = %q, want gs://bucket/a.jpg", got[0].ImageURI)
	}
}

// Format preserves the BigQuery row order (the search SQL already orders by
// rrf_score DESC, distance ASC — the RRF fusion rank). The formatter MUST NOT
// re-sort by distance, otherwise the RRF ranking is discarded whenever the
// rewrite channel diverges from raw distance order (Req 1.4, 3.4).
func TestFormat_PreservesBigQueryRowOrder(t *testing.T) {
	// Rows as BigQuery returns them in RRF fusion order, which need NOT be
	// monotonic in distance: a smaller-distance row can rank LOWER when the
	// other channel disagrees. A distance-based re-sort would reorder these.
	rows := []search.Row{
		{ImageURI: "gs://b/first.jpg", Distance: 0.5},
		{ImageURI: "gs://b/second.jpg", Distance: 0.05}, // smaller distance, but RRF-ranked 2nd
		{ImageURI: "gs://b/third.jpg", Distance: 0.3},
	}

	got := Format(rows)

	wantOrder := []string{"gs://b/first.jpg", "gs://b/second.jpg", "gs://b/third.jpg"}
	if len(got) != len(wantOrder) {
		t.Fatalf("expected %d results, got %d", len(wantOrder), len(got))
	}
	for i, want := range wantOrder {
		if got[i].ImageURI != want {
			t.Errorf("result[%d].image_uri = %q, want %q (formatter must preserve BigQuery RRF order, not re-sort by distance)",
				i, got[i].ImageURI, want)
		}
	}

	// score is still 1 - distance per row, regardless of position.
	if !almostEqual(got[1].Score, 0.95) {
		t.Errorf("second row score = %v, want 0.95 (1 - 0.05)", got[1].Score)
	}
}

// content_type present is carried into result (Req 3.5).
func TestFormat_ContentTypeCarriedWhenPresent(t *testing.T) {
	rows := []search.Row{
		{ImageURI: "gs://b/a.png", Distance: 0.2, ContentType: "image/png"},
	}

	got := Format(rows)

	if got[0].ContentType != "image/png" {
		t.Errorf("content_type = %q, want image/png", got[0].ContentType)
	}
}

// content_type absent → omitempty drops the key from JSON (Req 3.5).
func TestFormat_ContentTypeOmittedWhenAbsent(t *testing.T) {
	rows := []search.Row{
		{ImageURI: "gs://b/a.png", Distance: 0.2}, // no ContentType
	}

	got := Format(rows)

	b, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	s := string(b)
	if strings.Contains(s, "content_type") {
		t.Errorf("expected content_type to be omitted, got JSON: %s", s)
	}
	// signed_url is left empty by the formatter; must also be omitted.
	if strings.Contains(s, "signed_url") {
		t.Errorf("expected signed_url to be omitted (formatter does not set it), got JSON: %s", s)
	}
}

// signed_url is never populated by the formatter (task 3.3/4.1 fills it).
func TestFormat_DoesNotSetSignedURL(t *testing.T) {
	rows := []search.Row{
		{ImageURI: "gs://b/a.png", Distance: 0.2, ContentType: "image/png"},
	}

	got := Format(rows)

	if got[0].SignedURL != "" {
		t.Errorf("signed_url = %q, want empty (formatter must not set it)", got[0].SignedURL)
	}
}

// 0 rows → empty NON-NIL slice; JSON marshals to [] not null (Req 4.4).
func TestFormat_ZeroRowsIsEmptyNonNilSlice(t *testing.T) {
	got := Format([]search.Row{})

	if got == nil {
		t.Fatal("expected non-nil slice for 0 rows, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got len %d", len(got))
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if string(b) != "[]" {
		t.Errorf("json.Marshal = %s, want []", string(b))
	}
}

// nil input is also treated as 0 rows → [] not null (Req 4.4 robustness).
func TestFormat_NilRowsIsEmptyNonNilSlice(t *testing.T) {
	got := Format(nil)

	if got == nil {
		t.Fatal("expected non-nil slice for nil rows, got nil")
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if string(b) != "[]" {
		t.Errorf("json.Marshal = %s, want []", string(b))
	}
}
