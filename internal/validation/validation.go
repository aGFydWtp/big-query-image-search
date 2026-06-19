// Package validation performs pure input validation for search requests,
// rejecting or normalizing inputs before any BigQuery query is issued.
//
// It is intentionally free of HTTP and BigQuery concerns: it operates on
// already-parsed request inputs and returns normalized values plus an error the
// caller maps to HTTP 400. The HTTP handler (a separate task) wires this in.
//
// top_k policy (consistent application of the design's Error Handling rules):
//
//	unspecified (nil)        -> DefaultTopK
//	non-positive (0 or < 0)  -> ErrInvalidInput (handler returns 400)
//	above MaxTopK            -> clamped to MaxTopK
//	within [1, MaxTopK]      -> unchanged
//
// An empty or whitespace-only query is rejected with ErrInvalidInput so the
// caller can return 400 and avoid running BigQuery.
package validation

import (
	"errors"
	"fmt"
	"strings"
)

// DefaultTopK is the number of results returned when the request omits top_k.
// It is an application-policy constant (not an environment-specific value), so
// defining it here is correct.
const DefaultTopK = 10

// MaxTopK is the safe upper bound for top_k. Requests above this are clamped to
// MaxTopK rather than rejected, per the design's consistent policy. It is an
// application-policy constant.
const MaxTopK = 100

// ErrInvalidInput is the sentinel wrapped by all validation failures. Callers
// use errors.Is(err, ErrInvalidInput) to map the failure to HTTP 400.
var ErrInvalidInput = errors.New("invalid input")

// ValidatedRequest holds the normalized, validated search inputs.
type ValidatedRequest struct {
	// Query is the non-empty, trimmed query text.
	Query string
	// TopK is the effective result count after applying defaults and clamping.
	TopK int
}

// Validate checks the parsed request inputs and returns normalized values.
//
// query must be non-empty after trimming surrounding whitespace; otherwise an
// error wrapping ErrInvalidInput is returned and the caller must not proceed to
// BigQuery.
//
// topK is the parsed top_k where nil means the request omitted it. A nil topK
// yields DefaultTopK; a non-positive topK is an error; a topK above MaxTopK is
// clamped to MaxTopK; otherwise it is returned unchanged.
func Validate(query string, topK *int) (ValidatedRequest, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return ValidatedRequest{}, fmt.Errorf("%w: query must not be empty", ErrInvalidInput)
	}

	effectiveTopK := DefaultTopK
	if topK != nil {
		v := *topK
		switch {
		case v <= 0:
			return ValidatedRequest{}, fmt.Errorf("%w: top_k must be positive, got %d", ErrInvalidInput, v)
		case v > MaxTopK:
			effectiveTopK = MaxTopK
		default:
			effectiveTopK = v
		}
	}

	return ValidatedRequest{Query: trimmed, TopK: effectiveTopK}, nil
}
