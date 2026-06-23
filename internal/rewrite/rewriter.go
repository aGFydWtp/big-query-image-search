// Package rewrite produces an English rephrasing of a user query for the
// "rrf_vec" RRF fusion channel verified in
// docs/eval-results/comparison-vs-reference.md (overall nDCG@10 +0.027 over the
// raw single-vector baseline). The handler passes the rewrite to the BigQuery
// search statement as @query_en; that statement runs VECTOR_SEARCH twice (once
// for the raw JP query, once for the English rewrite) and fuses the rankings
// with RRF (k=60).
//
// Failure is non-fatal by design: on any rewrite error the caller is expected
// to fall back to the raw-only path. An empty @query_en suppresses the rewrite
// row in the SQL template's query_inputs CTE, collapsing the fused query into
// the single-channel baseline (one embedding + one VECTOR_SEARCH) without a
// separate code path.
package rewrite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"
)

// Rewriter rewrites a user query into an English image-search description.
// An empty return value signals "no rewrite available"; callers MUST treat it
// as a graceful fallback to the raw query rather than an error.
type Rewriter interface {
	Rewrite(ctx context.Context, query string) (string, error)
}

// Noop is the disabled rewriter: it always returns an empty rewrite and no
// error, which lets the SQL fall back to the raw-only single-channel search.
// Wired in main.go when REWRITE_ENABLED=false (and used in tests).
type Noop struct{}

// Rewrite returns the empty string for any query.
func (Noop) Rewrite(_ context.Context, _ string) (string, error) { return "", nil }

// promptTemplate keeps the rewrite focused on producing an English description
// suitable for embedding-based image retrieval, mirroring the manual rewrites
// in eval/rewritten_queries.json that the experiment harness fed to bq_search
// for the rewritten / rrf_vec / full modes.
//
// "Output ONLY ..." is load-bearing: GenerateContentResponse.Text() concatenates
// every text part and is fed directly into AI.GENERATE_EMBEDDING — any preamble
// (quotes, "Translation:", a code fence) would pollute the embedding input.
const promptTemplate = "Rewrite the following image search query as a concise English description " +
	"suitable for embedding-based image search over a museum painting corpus. " +
	"Output ONLY the English description as plain text — no quotes, no preamble, no explanation.\n\n" +
	"Query: %s"

// GenAIClient is the seam over *genai.Client.Models so the production path
// (real Vertex AI) and tests (in-memory fake) share the same Rewriter logic
// without dragging an HTTP/RPC connection into unit tests.
type GenAIClient interface {
	GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
}

// VertexRewriter calls Vertex AI Gemini through the unified genai SDK to
// translate a JP user query into an English image-search description.
type VertexRewriter struct {
	client  GenAIClient
	model   string
	timeout time.Duration
	cfg     *genai.GenerateContentConfig
}

// NewVertexRewriter wires a VertexRewriter from a Models seam, a model id
// (e.g. "gemini-2.5-flash"), and a per-call timeout. The generation config is
// fixed at construction: temperature=0 and a tight output cap, because the
// rewrite must be deterministic and short enough to embed (the manual rewrites
// in eval/rewritten_queries.json are ~10–20 English tokens).
func NewVertexRewriter(client GenAIClient, model string, timeout time.Duration) *VertexRewriter {
	return &VertexRewriter{
		client:  client,
		model:   model,
		timeout: timeout,
		cfg: &genai.GenerateContentConfig{
			Temperature:     genai.Ptr[float32](0.0),
			MaxOutputTokens: 96,
		},
	}
}

// Rewrite returns the English description for query, or "" with a non-nil error
// on failure. A blank input short-circuits to "" so the call site never burns a
// Vertex AI request on the validation no-op path.
func (r *VertexRewriter) Rewrite(ctx context.Context, query string) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", nil
	}
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}
	resp, err := r.client.GenerateContent(ctx, r.model, genai.Text(fmt.Sprintf(promptTemplate, query)), r.cfg)
	if err != nil {
		return "", classifyError(err)
	}
	if resp == nil {
		return "", nil
	}
	return strings.TrimSpace(resp.Text()), nil
}

// ErrRewriteTimeout reports a rewrite that exceeded its deadline or was
// cancelled. It is surfaced separately from ErrRewriteUpstream so callers can
// classify the failure mode; the handler currently treats both as a raw-only
// fallback and logs at warn.
var ErrRewriteTimeout = errors.New("rewrite: timeout or cancelled")

// ErrRewriteUpstream reports a Vertex AI / network failure during rewrite.
var ErrRewriteUpstream = errors.New("rewrite: upstream failure")

func classifyError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %v", ErrRewriteTimeout, err)
	}
	return fmt.Errorf("%w: %v", ErrRewriteUpstream, err)
}
