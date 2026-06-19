package signedurl

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	testBucket = "my-images-bucket"
	testSA     = "run-sa@my-project.iam.gserviceaccount.com"
	testExpiry = 15 * time.Minute
)

// newTestGenerator builds a Generator with an injected SignBytes seam so unit
// tests never touch a live IAM Credentials API. The signer is deterministic.
func newTestGenerator(signer SignBytesFunc) *Generator {
	return New(Config{
		Bucket:         testBucket,
		GoogleAccessID: testSA,
		Expiry:         testExpiry,
		SignBytes:      signer,
	})
}

// okSigner returns deterministic non-empty bytes so storage.SignedURL produces
// a real V4 URL without any network call.
func okSigner(b []byte) ([]byte, error) {
	// Returning a fixed 256-byte blob is sufficient for V4 hex-encoding.
	out := make([]byte, 256)
	for i := range out {
		out[i] = byte(i)
	}
	return out, nil
}

// TestSignedURL_SuccessProducesV4URL verifies a gs:// URI in the configured
// bucket yields a non-empty V4 signed URL containing the V4 markers, the run SA
// in the credential, and an expiry (Req 3.2, 3.3).
func TestSignedURL_SuccessProducesV4URL(t *testing.T) {
	g := newTestGenerator(okSigner)

	got, err := g.SignedURL(context.Background(), "gs://"+testBucket+"/path/to/image.jpg")
	if err != nil {
		t.Fatalf("SignedURL returned unexpected error: %v", err)
	}
	if got == "" {
		t.Fatal("SignedURL returned empty URL on success")
	}

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("returned URL is not parseable: %v", err)
	}
	q := u.Query()

	if alg := q.Get("X-Goog-Algorithm"); alg != "GOOG4-RSA-SHA256" {
		t.Errorf("X-Goog-Algorithm = %q, want GOOG4-RSA-SHA256 (V4 signing)", alg)
	}
	if exp := q.Get("X-Goog-Expires"); exp == "" {
		t.Error("X-Goog-Expires missing; expiry not set on V4 URL")
	}
	cred := q.Get("X-Goog-Credential")
	if cred == "" {
		t.Error("X-Goog-Credential missing on V4 URL")
	}
	if !strings.Contains(cred, testSA) {
		t.Errorf("X-Goog-Credential = %q, want it to contain run SA %q", cred, testSA)
	}
	// Path must reference the requested object in the configured bucket.
	if !strings.Contains(u.Path, "path/to/image.jpg") {
		t.Errorf("URL path = %q, want it to reference the object key", u.Path)
	}
}

// TestSignedURL_RejectsForeignBucket verifies a gs:// URI whose bucket differs
// from the configured images bucket is refused (not signed) (Req 3.3).
func TestSignedURL_RejectsForeignBucket(t *testing.T) {
	g := newTestGenerator(okSigner)

	_, err := g.SignedURL(context.Background(), "gs://some-other-bucket/image.jpg")
	if err == nil {
		t.Fatal("SignedURL accepted a foreign bucket; want an error")
	}
}

// TestSignedURL_RejectsNonGSURI verifies a non gs:// URI is refused.
func TestSignedURL_RejectsNonGSURI(t *testing.T) {
	g := newTestGenerator(okSigner)

	_, err := g.SignedURL(context.Background(), "https://example.com/image.jpg")
	if err == nil {
		t.Fatal("SignedURL accepted a non gs:// URI; want an error")
	}
}

// TestSignedURLs_PartialFailure is the MANDATORY partial-failure DoD (Req 4.5):
// when SignBytes fails for ONE object, the helper still returns signed URLs for
// the others and returns NO top-level error; the failed item simply lacks a URL
// and carries a per-item error indicator.
func TestSignedURLs_PartialFailure(t *testing.T) {
	const failingURI = "gs://" + testBucket + "/fails.jpg"

	// signer fails only when signing the failing object. The signing payload is
	// opaque bytes, so we make the signer fail on the Nth invocation deterministically.
	// Simpler: fail on a specific call counter tied to the failing item's position.
	failOnCall := 2
	call := 0
	signer := func(b []byte) ([]byte, error) {
		call++
		if call == failOnCall {
			return nil, errors.New("injected signBlob failure")
		}
		return okSigner(b)
	}

	g := newTestGenerator(signer)

	uris := []string{
		"gs://" + testBucket + "/ok-1.jpg",
		failingURI, // 2nd call fails
		"gs://" + testBucket + "/ok-3.jpg",
	}

	outcomes := g.SignedURLs(context.Background(), uris)

	if len(outcomes) != len(uris) {
		t.Fatalf("SignedURLs returned %d outcomes, want %d", len(outcomes), len(uris))
	}

	// The two healthy items must have URLs and no error.
	for _, idx := range []int{0, 2} {
		if outcomes[idx].Err != nil {
			t.Errorf("outcome[%d] (%s) unexpected error: %v", idx, uris[idx], outcomes[idx].Err)
		}
		if outcomes[idx].URL == "" {
			t.Errorf("outcome[%d] (%s) has empty URL; one failure must not block other results", idx, uris[idx])
		}
	}

	// The failing item must lack a URL and carry an error indicator.
	if outcomes[1].URL != "" {
		t.Errorf("outcome[1] (%s) should have no URL on signing failure, got %q", failingURI, outcomes[1].URL)
	}
	if outcomes[1].Err == nil {
		t.Errorf("outcome[1] (%s) should carry a per-item error indicator", failingURI)
	}

	// Each outcome must map back to its input URI in order.
	for i, o := range outcomes {
		if o.URI != uris[i] {
			t.Errorf("outcome[%d].URI = %q, want %q (order must be preserved)", i, o.URI, uris[i])
		}
	}
}

// TestSignedURLs_ForeignBucketIsPerItemFailure verifies a foreign-bucket URI in
// a batch is a per-item failure (no URL, error set) and does not block siblings.
func TestSignedURLs_ForeignBucketIsPerItemFailure(t *testing.T) {
	g := newTestGenerator(okSigner)

	uris := []string{
		"gs://" + testBucket + "/ok.jpg",
		"gs://other-bucket/nope.jpg",
	}

	outcomes := g.SignedURLs(context.Background(), uris)

	if outcomes[0].URL == "" || outcomes[0].Err != nil {
		t.Errorf("outcome[0] should succeed: url=%q err=%v", outcomes[0].URL, outcomes[0].Err)
	}
	if outcomes[1].URL != "" {
		t.Errorf("outcome[1] foreign bucket should have no URL, got %q", outcomes[1].URL)
	}
	if outcomes[1].Err == nil {
		t.Error("outcome[1] foreign bucket should carry an error")
	}
}
