// Package rerank reorders the top-N rrf_vec search candidates by Gemini vision
// relevance — the winning configuration verified in
// docs/eval-results/comparison-vs-reference.md ("rerank Phase 1b": overall
// nDCG@10 0.670 → 0.736, i.e. 76.4% of the 0.963 oracle ceiling).
//
// For each candidate the real GCS image (referenced by a gs:// fileData part, so
// the image is never downloaded into the service) plus the user query are sent
// to Gemini for a pointwise relevance score on a 0–10 integer scale. The 0–10
// scale is load-bearing: it reduces ties versus 0–3 so the model's fine-grained
// judgement actually reaches the ranking (0–3 / pro / thinking / listwise all
// underperformed and were refuted). Candidates are then sorted by score
// descending, ties broken by the original rrf_vec order (stable sort).
//
// Cost: at N=50 ≈ $0.027 / search ≈ $4,000 / month at 5,000 searches/day, so the
// stage is OFF by default (config RERANK_ENABLED) and N is configurable
// (RERANK_TOP_N) to trade quality for cost.
//
// Failure is non-fatal by design: if any candidate fails to score after retries,
// Rerank returns the input order unchanged together with a non-nil error, and
// the handler degrades to the rrf_vec ranking (logging at warn) rather than
// serving a partially-reranked list of uncertain quality. This mirrors the
// all-or-nothing degrade of the rewrite (rrf_vec) channel.
package rerank

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"

	"github.com/aGFydWtp/big-query-image-search/internal/search"
)

// scaleMax is the pointwise scoring ceiling (0–scaleMax). 10 is the verified
// winning scale; it is fixed in code rather than configurable so the proven
// configuration cannot be silently weakened by an env override.
const scaleMax = 10

// imageMIMEType is the MIME type advertised for the gs:// fileData part. The
// seed corpus is JPEG (gs://.../aic-seed/<id>.jpg).
const imageMIMEType = "image/jpeg"

// defaultTopN / defaultWorkers are the in-package fallbacks applied when the
// constructor receives a non-positive value, so a misconfigured caller still
// gets the verified depth and a bounded fan-out rather than 0.
const (
	defaultTopN    = 50
	defaultWorkers = 6
)

// maxRetries bounds per-image scoring retries on transient (retryable) failures.
const maxRetries = 3

// ErrRerankUpstream reports that reranking could not complete (a candidate
// failed to score after retries). It is wrapped around the underlying cause so
// callers can classify the failure with errors.Is while the handler degrades to
// the rrf_vec order.
var ErrRerankUpstream = errors.New("rerank: upstream failure")

// retryableCodes are the HTTP status codes treated as transient for scoring
// retries, mirroring scripts/eval_rerank_gemini.py (_RETRYABLE).
var retryableCodes = map[int]bool{429: true, 500: true, 502: true, 503: true, 504: true}

// Reranker reorders search rows by Gemini vision relevance. An implementation
// MUST be non-destructive on the input slice's element order semantics: it
// returns a reordered copy and treats failure as a graceful fallback to the
// input order (signalled by a non-nil error).
type Reranker interface {
	Rerank(ctx context.Context, query string, rows []search.Row) ([]search.Row, error)
	// CandidateDepth is the number of candidates the reranker wants to rescore.
	// The caller fetches at least this many rows so a relevant image ranked
	// beyond the user's top_k by rrf_vec can still be promoted into it — the
	// source of the verified uplift (reranking top-50, returning top-10). A
	// disabled reranker returns 0 (no deeper fetch needed).
	CandidateDepth() int
}

// Noop is the disabled reranker: it returns rows unchanged with no error. Wired
// in main.go when RERANK_ENABLED=false (and used in tests) so the handler serves
// the rrf_vec ranking without a separate code path.
type Noop struct{}

// Rerank returns rows unchanged.
func (Noop) Rerank(_ context.Context, _ string, rows []search.Row) ([]search.Row, error) {
	return rows, nil
}

// CandidateDepth is 0: the disabled reranker needs no deeper candidate fetch.
func (Noop) CandidateDepth() int { return 0 }

// GenAIClient is the seam over *genai.Client.Models so the production path (real
// Vertex AI) and tests (in-memory fake) share the same scoring logic without an
// HTTP/RPC connection. It mirrors rewrite.GenAIClient.
type GenAIClient interface {
	GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
}

// VertexReranker scores the top-N candidates through Gemini and reorders them.
type VertexReranker struct {
	client  GenAIClient
	model   string
	topN    int
	workers int
	timeout time.Duration // per-image call timeout (0 = no per-call deadline)
	cfg     *genai.GenerateContentConfig
}

// NewVertexReranker wires a VertexReranker from a Models seam, a model id
// (e.g. "gemini-2.5-flash"), the rerank depth topN, the concurrent worker cap,
// and a per-image call timeout. The generation config is fixed at construction
// to the verified winning configuration: temperature=0, thinkingBudget=0, and
// JSON structured output {"score": int} — so the result is deterministic and
// cheap (no thinking tokens).
func NewVertexReranker(client GenAIClient, model string, topN, workers int, timeout time.Duration) *VertexReranker {
	if topN <= 0 {
		topN = defaultTopN
	}
	if workers <= 0 {
		workers = defaultWorkers
	}
	return &VertexReranker{
		client:  client,
		model:   model,
		topN:    topN,
		workers: workers,
		timeout: timeout,
		cfg: &genai.GenerateContentConfig{
			Temperature:      genai.Ptr[float32](0.0),
			MaxOutputTokens:  40,
			ResponseMIMEType: "application/json",
			ResponseSchema: &genai.Schema{
				Type:       genai.TypeObject,
				Properties: map[string]*genai.Schema{"score": {Type: genai.TypeInteger}},
				Required:   []string{"score"},
			},
			ThinkingConfig: &genai.ThinkingConfig{ThinkingBudget: genai.Ptr[int32](0)},
		},
	}
}

// CandidateDepth returns the configured rerank depth (topN): the caller should
// fetch at least this many candidates so promotion from beyond the user's top_k
// is possible.
func (r *VertexReranker) CandidateDepth() int { return r.topN }

// Rerank scores the top-N candidates concurrently and returns rows reordered by
// score descending (ties keep the original rrf_vec order). On any per-image
// scoring failure it returns the input rows unchanged with an ErrRerankUpstream
// error so the caller degrades to the rrf_vec ranking. A blank query, an empty
// or single-row input short-circuits to the input unchanged.
func (r *VertexReranker) Rerank(ctx context.Context, query string, rows []search.Row) ([]search.Row, error) {
	if len(rows) <= 1 || strings.TrimSpace(query) == "" {
		return rows, nil
	}

	n := r.topN
	if n > len(rows) {
		n = len(rows)
	}
	head := rows[:n]

	scores := make([]int, n)
	errs := make([]error, n)
	sem := make(chan struct{}, r.workers)
	var wg sync.WaitGroup
	for i := range head {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			scores[i], errs[i] = r.scoreImage(ctx, query, head[i].ImageURI)
		}(i)
	}
	wg.Wait()

	// All-or-nothing degrade: a single unrecoverable failure abandons the rerank
	// rather than emitting a partially-scored ranking of uncertain quality.
	for _, e := range errs {
		if e != nil {
			return rows, fmt.Errorf("%w: %v", ErrRerankUpstream, e)
		}
	}

	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	// Stable sort by score desc: equal scores retain ascending original index,
	// i.e. the original rrf_vec order — the verified tie-break.
	sort.SliceStable(order, func(a, b int) bool {
		return scores[order[a]] > scores[order[b]]
	})

	out := make([]search.Row, 0, len(rows))
	for _, idx := range order {
		out = append(out, head[idx])
	}
	out = append(out, rows[n:]...) // candidates beyond top-N keep their rrf_vec order
	return out, nil
}

// scoreImage requests a single pointwise 0–scaleMax relevance score for one
// candidate image, retrying transient failures with exponential backoff. The
// gs:// URI is passed by reference (fileData), so the image is never downloaded
// into the service.
func (r *VertexReranker) scoreImage(ctx context.Context, query, imageURI string) (int, error) {
	contents := []*genai.Content{{
		Role: "user",
		Parts: []*genai.Part{
			{FileData: &genai.FileData{FileURI: imageURI, MIMEType: imageMIMEType}},
			{Text: buildRubric(query)},
		},
	}}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		callCtx := ctx
		var cancel context.CancelFunc
		if r.timeout > 0 {
			callCtx, cancel = context.WithTimeout(ctx, r.timeout)
		}
		resp, err := r.client.GenerateContent(callCtx, r.model, contents, r.cfg)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			return parseScore(resp)
		}
		lastErr = err

		// A cancelled/expired PARENT context is terminal — stop retrying.
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if !retryable(err) {
			return 0, err
		}
		if attempt < maxRetries {
			backoff := time.Duration(int64(500*time.Millisecond) << attempt)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
	}
	return 0, lastErr
}

// retryable reports whether err is a transient failure worth retrying. A
// per-call timeout (context.DeadlineExceeded from r.timeout, with the parent
// context still live) and the retryable HTTP status codes are retried; an
// unclassified transport error is retried conservatively, while a definitive
// non-retryable API error (e.g. 400/403/404) is not.
func retryable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		return retryableCodes[apiErr.Code]
	}
	return true
}

// parseScore extracts the integer score from the structured JSON response and
// clamps it to [0, scaleMax]. A nil response or unparseable body is an error so
// the caller degrades rather than silently treating a malformed answer as 0.
func parseScore(resp *genai.GenerateContentResponse) (int, error) {
	if resp == nil {
		return 0, errors.New("nil response")
	}
	var parsed struct {
		Score int `json:"score"`
	}
	if err := json.Unmarshal([]byte(resp.Text()), &parsed); err != nil {
		return 0, fmt.Errorf("parse score JSON: %w", err)
	}
	s := parsed.Score
	if s < 0 {
		s = 0
	}
	if s > scaleMax {
		s = scaleMax
	}
	return s, nil
}

// buildRubric renders the pointwise relevance rubric for query at scale 0–10,
// mirroring scripts/eval_rerank_gemini.py build_rubric(scale_max=10): the
// generic (non 0–3) branch that asks the model to reflect subtle differences in
// the number and avoid ties.
func buildRubric(query string) string {
	return "あなたは画像検索の関連性評価者です。検索クエリと 1 枚の画像が与えられます。\n" +
		"この画像が検索クエリ「" + query + "」にどれだけ合致するかを 0〜10 の" +
		"整数で採点してください。数値が大きいほど関連性が高いことを表します。\n" +
		"  10: クエリの主題・内容・雰囲気に完全に合致する\n" +
		"  5 前後: 部分的に合致する（主要素の一部が一致）\n" +
		"  0: 関連しない\n" +
		"微妙な差も数値に反映し、同点をできるだけ避けてください。JSON のみを返してください。"
}
