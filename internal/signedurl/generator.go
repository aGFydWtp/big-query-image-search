// Package signedurl issues short-lived V4 signed URLs for matched GCS objects.
//
// Signing is keyless: no service-account private key is held by the service.
// Instead it relies on cloud.google.com/go/storage's SignedURL with a SignBytes
// callback that delegates the RSA signature to the IAM Credentials signBlob API
// for the Cloud Run runtime service account (GoogleAccessID). This requires the
// run SA to hold roles/iam.serviceAccountTokenCreator on itself and
// roles/storage.objectViewer on the images bucket (an upstream IAM prerequisite
// owned by gcp-infrastructure).
//
// Per Requirement 3.3, signing is constrained to the configured images bucket:
// a gs:// URI referencing any other bucket is treated as a per-item failure and
// never signed.
//
// Per Requirement 4.5, signing is best-effort per item: an individual signing
// failure omits the URL for THAT object and is surfaced as a per-item error,
// while sibling objects in the same batch still return their URLs. SignedURLs
// therefore never returns a top-level error for per-item signing failures.
package signedurl

import (
	"context"
	"fmt"
	"strings"
	"time"

	credentials "cloud.google.com/go/iam/credentials/apiv1"
	"cloud.google.com/go/iam/credentials/apiv1/credentialspb"
	"cloud.google.com/go/storage"
)

// SignBytesFunc signs the given bytes and returns the signature. It is the
// injectable seam that decouples the generator from a live IAM call so unit
// tests can mock signing. In production it is backed by IAM signBlob (see
// NewIAMSignBytes).
type SignBytesFunc func([]byte) ([]byte, error)

// Config holds the immutable inputs required to sign URLs.
type Config struct {
	// Bucket is the configured images bucket. Only objects in this bucket are
	// signed (Req 3.3).
	Bucket string
	// GoogleAccessID is the authorizer of the signature, i.e. the Cloud Run
	// runtime service account email.
	GoogleAccessID string
	// Expiry is the validity window of generated URLs.
	Expiry time.Duration
	// SignBytes performs the actual RSA signing. Mandatory; in production it is
	// the IAM signBlob-backed signer from NewIAMSignBytes.
	SignBytes SignBytesFunc
}

// Generator issues V4 signed URLs for objects in the configured images bucket.
type Generator struct {
	cfg Config
}

// New constructs a Generator from the given config.
func New(cfg Config) *Generator {
	return &Generator{cfg: cfg}
}

// Outcome is the per-item result of a batch signing request. It preserves the
// input URI so callers can merge URLs back onto the matching result. On
// success URL is set and Err is nil; on per-item failure URL is empty and Err
// explains the failure (the batch as a whole still succeeds — Req 4.5).
type Outcome struct {
	URI string
	URL string
	Err error
}

// SignedURL issues a single V4 signed URL for the object referenced by gsURI.
//
// gsURI must be a gs://<bucket>/<object> URI whose bucket equals the configured
// images bucket; otherwise an error is returned and nothing is signed (Req 3.3).
func (g *Generator) SignedURL(_ context.Context, gsURI string) (string, error) {
	object, err := g.parseObject(gsURI)
	if err != nil {
		return "", err
	}

	url, err := storage.SignedURL(g.cfg.Bucket, object, &storage.SignedURLOptions{
		GoogleAccessID: g.cfg.GoogleAccessID,
		SignBytes:      g.cfg.SignBytes,
		Method:         "GET",
		Scheme:         storage.SigningSchemeV4,
		Expires:        time.Now().Add(g.cfg.Expiry),
	})
	if err != nil {
		return "", fmt.Errorf("signedurl: signing %q failed: %w", gsURI, err)
	}
	return url, nil
}

// SignedURLs signs each gs:// URI independently and returns one Outcome per
// input in the same order. A failure for any single URI (bad URI, foreign
// bucket, or signing error) is confined to that item's Outcome.Err and does not
// prevent the remaining URIs from being signed (Req 4.5). The returned slice is
// always non-nil and the same length as uris.
func (g *Generator) SignedURLs(ctx context.Context, uris []string) []Outcome {
	outcomes := make([]Outcome, 0, len(uris))
	for _, uri := range uris {
		url, err := g.SignedURL(ctx, uri)
		outcomes = append(outcomes, Outcome{URI: uri, URL: url, Err: err})
	}
	return outcomes
}

// parseObject validates gsURI and extracts the object key, enforcing that the
// URI is a gs:// URI scoped to the configured images bucket (Req 3.3).
func (g *Generator) parseObject(gsURI string) (string, error) {
	const scheme = "gs://"
	if !strings.HasPrefix(gsURI, scheme) {
		return "", fmt.Errorf("signedurl: %q is not a gs:// URI", gsURI)
	}
	rest := strings.TrimPrefix(gsURI, scheme)
	bucket, object, found := strings.Cut(rest, "/")
	if !found || bucket == "" || object == "" {
		return "", fmt.Errorf("signedurl: %q is missing a bucket or object", gsURI)
	}
	if bucket != g.cfg.Bucket {
		return "", fmt.Errorf("signedurl: object bucket %q is not the configured images bucket %q", bucket, g.cfg.Bucket)
	}
	return object, nil
}

// NewIAMSignBytes returns a production SignBytesFunc backed by the IAM
// Credentials signBlob API for the given service account email. It signs bytes
// without any private key: the IAM Credentials service performs the RSA-SHA256
// signature on behalf of the service account (keyless V4 signing).
//
// The returned closure calls signBlob synchronously using ctx for each
// invocation. The caller owns client lifecycle and must Close it.
func NewIAMSignBytes(ctx context.Context, client *credentials.IamCredentialsClient, saEmail string) SignBytesFunc {
	name := fmt.Sprintf("projects/-/serviceAccounts/%s", saEmail)
	return func(b []byte) ([]byte, error) {
		resp, err := client.SignBlob(ctx, &credentialspb.SignBlobRequest{
			Name:    name,
			Payload: b,
		})
		if err != nil {
			return nil, fmt.Errorf("signedurl: IAM signBlob for %q failed: %w", saEmail, err)
		}
		return resp.GetSignedBlob(), nil
	}
}
