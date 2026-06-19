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

// distance ASC == score DESC == most similar first (Req 1.4, 3.4).
func TestFormat_SortsByDistanceAscendingScoreDescending(t *testing.T) {
	// Intentionally out of order.
	rows := []search.Row{
		{ImageURI: "gs://b/mid.jpg", Distance: 0.5},
		{ImageURI: "gs://b/far.jpg", Distance: 1.2},
		{ImageURI: "gs://b/near.jpg", Distance: 0.05},
	}

	got := Format(rows)

	wantOrder := []string{"gs://b/near.jpg", "gs://b/mid.jpg", "gs://b/far.jpg"}
	if len(got) != len(wantOrder) {
		t.Fatalf("expected %d results, got %d", len(wantOrder), len(got))
	}
	for i, want := range wantOrder {
		if got[i].ImageURI != want {
			t.Errorf("result[%d].image_uri = %q, want %q", i, got[i].ImageURI, want)
		}
	}

	// Verify score is strictly descending (most similar first).
	for i := 1; i < len(got); i++ {
		if got[i-1].Score < got[i].Score {
			t.Errorf("scores not descending: result[%d].Score=%v < result[%d].Score=%v",
				i-1, got[i-1].Score, i, got[i].Score)
		}
	}

	// near: 1-0.05=0.95, mid: 1-0.5=0.5, far: 1-1.2=-0.2
	if !almostEqual(got[0].Score, 0.95) {
		t.Errorf("near score = %v, want 0.95", got[0].Score)
	}
	if !almostEqual(got[2].Score, -0.2) {
		t.Errorf("far score = %v, want -0.2", got[2].Score)
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
