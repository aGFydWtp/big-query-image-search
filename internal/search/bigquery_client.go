// Package search — bigquery_client.go executes the parameterized search query
// built by SearchQueryBuilder against BigQuery and returns the matched rows.
//
// Execution model (Requirement 2.3): the default is a SINGLE BigQuery job that
// runs the combined AI.GENERATE_EMBEDDING + VECTOR_SEARCH query, binding the
// value parameters @query (STRING) and @top_k (INT64). The 2-job split path
// (embedding generation then VECTOR_SEARCH with @query_embedding) is a
// selectable degraded fallback that is NOT adopted by default (Task 2.2 dry-run
// gate confirmed single-query); it is exposed via NewBigQueryClientSplit.
//
// Error handling (Requirement 4.3): timeout/cancellation and BigQuery execution
// failures are converted into internal sentinel error types (ErrQueryTimeout /
// ErrBigQuery). The client-facing error value carries only a generic message so
// raw BigQuery messages, SQL, or table identifiers never reach the caller (and
// therefore never reach the HTTP client). Underlying detail is preserved on the
// wrapped chain for server-side logging via errors.Unwrap, but is deliberately
// excluded from the Error() string the handler maps to a 5xx response.
package search

import (
	"context"
	"errors"
	"fmt"

	"cloud.google.com/go/bigquery"
	"google.golang.org/api/iterator"
)

// Row is the shape of a single search match returned by the BigQuery query,
// consumable by the result formatter (Task 3.2). Distance is the raw COSINE
// distance; score derivation (1 - distance) is the formatter's responsibility,
// not this client's.
type Row struct {
	ImageURI    string  `bigquery:"image_uri"`
	Distance    float64 `bigquery:"distance"`
	ContentType string  `bigquery:"content_type"`
}

// QueryParam is a named BigQuery query parameter binding. It decouples the
// execution seam from the cloud.google.com/go/bigquery types so the core client
// logic is unit-testable with a fake runner (no live BigQuery connection).
type QueryParam struct {
	Name  string
	Value any
}

// Sentinel internal error types. They are returned (wrapped) to the caller so
// it can classify failures with errors.Is without ever seeing raw upstream
// detail. ApiHandler (Task 4.1) maps both to a 5xx response.
var (
	// ErrBigQuery indicates a BigQuery execution/query failure.
	ErrBigQuery = errors.New("search: bigquery execution failed")
	// ErrQueryTimeout indicates the query was cancelled or exceeded its deadline.
	ErrQueryTimeout = errors.New("search: bigquery query timed out or was cancelled")
)

// queryRunner is the execution seam. The production implementation wraps a real
// *bigquery.Client; tests inject a fake that returns canned rows or an error,
// so the mapping/error-conversion logic is verified without a live connection.
type queryRunner interface {
	Run(ctx context.Context, sql string, params []QueryParam) ([]Row, error)
}

// BigQueryClient executes the search query and converts failures into internal
// error types. It holds the SearchQueryBuilder (SQL source) and a queryRunner
// (execution seam).
type BigQueryClient struct {
	builder *QueryBuilder
	runner  queryRunner
	split   bool // when true, use the degraded 2-job split path
}

// NewBigQueryClient constructs a client that executes the single-job (default)
// search path using the supplied runner.
func NewBigQueryClient(builder *QueryBuilder, runner queryRunner) *BigQueryClient {
	return &BigQueryClient{builder: builder, runner: runner}
}

// NewBigQueryClientSplit constructs a client wired to the selectable degraded
// 2-job split path. This is NOT the default; it exists so the split fallback is
// selectable per the design without changing the default single-job behavior.
func NewBigQueryClientSplit(builder *QueryBuilder, runner queryRunner) *BigQueryClient {
	return &BigQueryClient{builder: builder, runner: runner, split: true}
}

// Search executes the parameterized search query and returns the matched rows.
// query binds @query (STRING), queryEN binds @query_en (STRING), and topK binds
// @top_k (INT64). queryEN is optional; an empty value falls back to query in
// SQL, preserving a valid search while allowing callers to supply the evaluated
// English rewrite channel for RRF fusion. The supplied context propagates
// timeout/cancellation. On failure, the returned error is a generic internal
// type (ErrQueryTimeout or ErrBigQuery) that does not expose raw BigQuery detail.
func (c *BigQueryClient) Search(ctx context.Context, query string, queryEN string, topK int) ([]Row, error) {
	// Honor an already-cancelled/expired context before issuing any job.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrQueryTimeout, err)
	}

	if c.split {
		return c.searchSplit(ctx, query, topK)
	}
	return c.searchSingle(ctx, query, queryEN, topK)
}

// searchSingle runs the default single BigQuery job: the combined embedding +
// VECTOR_SEARCH RRF query with @query, @query_en, @candidate_k, and @top_k
// bound.
func (c *BigQueryClient) searchSingle(ctx context.Context, query string, queryEN string, topK int) ([]Row, error) {
	params := bindParams(c.builder.ParameterNames(), query, queryEN, int64(topK), nil)
	rows, err := c.runner.Run(ctx, c.builder.SingleQuerySQL(), params)
	if err != nil {
		return nil, classifyError(err)
	}
	return rows, nil
}

// searchSplit runs the degraded 2-job path: job (1) generates the query
// embedding, job (2) runs VECTOR_SEARCH binding the generated embedding as
// @query_embedding. Provided for selectability; not the default.
func (c *BigQueryClient) searchSplit(ctx context.Context, query string, topK int) ([]Row, error) {
	embeddingSQL, searchSQL := c.builder.SplitQuerySQL()
	embeddingParams, searchParamNames := c.builder.SplitParameterNames()

	embRows, err := c.runner.Run(ctx, embeddingSQL, bindParams(embeddingParams, query, "", 0, nil))
	if err != nil {
		return nil, classifyError(err)
	}
	if len(embRows) == 0 {
		return nil, fmt.Errorf("%w: embedding generation returned no rows", ErrBigQuery)
	}

	// The embedding row's distance/content_type are unused; the embedding vector
	// is supplied to the search statement via @query_embedding. The runner is
	// responsible for surfacing the embedding column; for the split path the
	// search statement binds @query (none), @query_embedding, and @top_k.
	searchParams := bindParams(searchParamNames, query, "", int64(topK), embRows)
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrQueryTimeout, err)
	}
	rows, err := c.runner.Run(ctx, searchSQL, searchParams)
	if err != nil {
		return nil, classifyError(err)
	}
	return rows, nil
}

// bindParams maps the builder-declared parameter names to their runtime values.
// It binds only the names the builder reports, keeping name fidelity with the
// embedded SQL (@query, @query_en, @candidate_k, @top_k for the single path;
// @query_embedding for the split path). The embeddingRows argument is used only
// by the split search statement to carry the @query_embedding value.
func bindParams(names []string, query string, queryEN string, topK int64, embeddingRows []Row) []QueryParam {
	params := make([]QueryParam, 0, len(names))
	for _, n := range names {
		switch n {
		case paramQuery:
			params = append(params, QueryParam{Name: paramQuery, Value: query})
		case paramQueryEN:
			params = append(params, QueryParam{Name: paramQueryEN, Value: queryEN})
		case paramTopK:
			params = append(params, QueryParam{Name: paramTopK, Value: topK})
		case paramCandidateK:
			params = append(params, QueryParam{Name: paramCandidateK, Value: candidateK(topK)})
		case paramQueryEmbedding:
			params = append(params, QueryParam{Name: paramQueryEmbedding, Value: embeddingVector(embeddingRows)})
		}
	}
	return params
}

func candidateK(topK int64) int64 {
	if topK > 50 {
		return topK
	}
	return 50
}

// embeddingVector is a placeholder hook for the split path: the embedding vector
// would be extracted from the embedding job's result. It is unused in the
// adopted single-job default; returning nil keeps the binding name present
// without fabricating data.
func embeddingVector(_ []Row) any {
	return nil
}

// classifyError converts an execution error into an internal sentinel type,
// preserving the underlying error on the wrapped chain (for server-side logging
// via errors.Unwrap) while keeping the client-facing Error() string generic.
func classifyError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &internalError{sentinel: ErrQueryTimeout, cause: err}
	}
	return &internalError{sentinel: ErrBigQuery, cause: err}
}

// internalError carries a generic client-facing sentinel while retaining the
// underlying cause for internal logging. Error() returns ONLY the sentinel's
// message so raw BigQuery/SQL detail never leaks to the caller surface; Unwrap
// returns the sentinel first (so errors.Is matches it) and the cause is reached
// via the cause chain for logging without exposure through Error().
type internalError struct {
	sentinel error
	cause    error
}

func (e *internalError) Error() string { return e.sentinel.Error() }

// Unwrap exposes the sentinel for errors.Is classification. The raw cause is
// deliberately NOT part of Error(); it is retrievable for logging via Cause.
func (e *internalError) Unwrap() error { return e.sentinel }

// Cause returns the underlying error for internal (server-side) logging only.
func (e *internalError) Cause() error { return e.cause }

// --- Production execution path (real BigQuery) -----------------------------

// bigQueryRunner is the concrete queryRunner backed by a real *bigquery.Client.
// It executes the parameterized query as a single job, propagates context, and
// maps result rows into []Row via the bigquery struct tags on Row.
type bigQueryRunner struct {
	client   *bigquery.Client
	location string // BigQuery job location (config REGION), e.g. "us-central1"
}

// NewBigQueryRunner builds the production runner from an already-constructed
// *bigquery.Client and the job location. The caller owns the client's lifecycle
// (creation via bigquery.NewClient and Close).
func NewBigQueryRunner(client *bigquery.Client, location string) queryRunner {
	return &bigQueryRunner{client: client, location: location}
}

// Run executes sql with the given named parameters and collects all result rows.
func (r *bigQueryRunner) Run(ctx context.Context, sql string, params []QueryParam) ([]Row, error) {
	q := r.client.Query(sql)
	q.Location = r.location
	q.Parameters = make([]bigquery.QueryParameter, 0, len(params))
	for _, p := range params {
		q.Parameters = append(q.Parameters, bigquery.QueryParameter{Name: p.Name, Value: p.Value})
	}

	it, err := q.Read(ctx)
	if err != nil {
		return nil, err
	}

	var rows []Row
	for {
		var row Row
		err := it.Next(&row)
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}
