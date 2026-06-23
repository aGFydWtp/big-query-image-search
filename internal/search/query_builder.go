// Package search assembles the BigQuery (GoogleSQL) search query that consumes
// the image-ingestion-pipeline shared contract: it pairs AI.GENERATE_EMBEDDING
// (remote model object, output_dimensionality=3072) with VECTOR_SEARCH
// (distance_type COSINE) over the embeddings table.
//
// Identifier values (dataset, remote-model object name, table name) are NOT
// hardcoded here; they flow in from config injection and are rendered into the
// `${...}` placeholders of the embedded SQL template. The value parameters
// `@query` and `@top_k` are left intact as BigQuery parameter references and are
// bound at execution time by the BigQuery client (task 3.1), because GoogleSQL
// cannot parameter-bind identifiers/function-argument literals.
package search

import (
	_ "embed"
	"fmt"
	"strings"
)

// searchSQLTemplate is the canonical single-query (CTE join) template. It is the
// single source of the SQL structure and is kept byte-identical to the
// repository-root spec artifact sql/search.sql (guarded by a consistency test).
// go:embed cannot traverse parent directories, so the template is co-located.
//
//go:embed templates/search.sql
var searchSQLTemplate string

// Placeholder keys rendered from injected config values. These are template
// substitution markers, not the identifier values themselves (which arrive via
// NewQueryBuilder), so they are not a hardcoded source of truth.
const (
	placeholderDataset = "${DATASET_ID}"
	placeholderModel   = "${MODEL}"
	placeholderTable   = "${TABLE}"
)

// Parameter names expected by the rendered queries. They mirror the BigQuery
// parameter references embedded in the SQL so the client can bind them without
// re-deriving literals.
const (
	paramQuery          = "query"           // @query  (single + split embedding)
	paramQueryEN        = "query_en"        // @query_en (single RRF rewrite channel)
	paramTopK           = "top_k"           // @top_k  (single + split search)
	paramCandidateK     = "candidate_k"     // @candidate_k (single RRF per-channel candidates)
	paramQueryEmbedding = "query_embedding" // @query_embedding (split search only)
)

// QueryBuilder renders the search SQL from injected identifier values. The
// single-query (CTE) shape is the default/adopted form (Task 2.2 dry-run gate);
// the 2-job split is provided as a selectable degraded alternative.
type QueryBuilder struct {
	dataset string // project-qualified dataset, e.g. "project.dataset"
	model   string // remote model object name, e.g. "gemini_embedding_model"
	table   string // embeddings table name, e.g. "image_embeddings"
}

// NewQueryBuilder constructs a QueryBuilder from injected config values. All
// three identifiers are required: they cannot be parameter-bound, so an empty
// value would render structurally invalid SQL — fail fast instead.
func NewQueryBuilder(dataset, model, table string) (*QueryBuilder, error) {
	var missing []string
	if strings.TrimSpace(dataset) == "" {
		missing = append(missing, "dataset")
	}
	if strings.TrimSpace(model) == "" {
		missing = append(missing, "model")
	}
	if strings.TrimSpace(table) == "" {
		missing = append(missing, "table")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("search: NewQueryBuilder missing required identifier(s): %s", strings.Join(missing, ", "))
	}
	return &QueryBuilder{dataset: dataset, model: model, table: table}, nil
}

// renderIdentifiers substitutes the `${...}` identifier placeholders with the
// injected config values. The `@query`/`@top_k` value references are left
// untouched for BigQuery parameter binding.
func (b *QueryBuilder) renderIdentifiers(tmpl string) string {
	r := strings.NewReplacer(
		placeholderDataset, b.dataset,
		placeholderModel, b.model,
		placeholderTable, b.table,
	)
	return r.Replace(tmpl)
}

// SingleQuerySQL returns the default single-query (CTE join) SQL, rendered from
// the injected identifiers. AI.GENERATE_EMBEDDING and VECTOR_SEARCH are joined
// in one statement; @query/@top_k remain as parameter references.
func (b *QueryBuilder) SingleQuerySQL() string {
	return b.renderIdentifiers(searchSQLTemplate)
}

// ParameterNames lists the BigQuery parameter names the single-query path binds.
func (b *QueryBuilder) ParameterNames() []string {
	return []string{paramQuery, paramQueryEN, paramCandidateK, paramTopK}
}

// splitEmbeddingTemplate is the degraded path statement (1): generate the query
// embedding only, producing the `embedding` column (ARRAY<FLOAT64>).
const splitEmbeddingTemplate = "SELECT embedding\n" +
	"FROM AI.GENERATE_EMBEDDING(\n" +
	"  MODEL `${DATASET_ID}.${MODEL}`,\n" +
	"  (SELECT @query AS content),\n" +
	"  STRUCT(3072 AS output_dimensionality)\n" +
	")\n" +
	"WHERE status = '';\n"

// splitSearchTemplate is the degraded path statement (2): run VECTOR_SEARCH with
// the previously generated embedding supplied as the @query_embedding parameter
// (ARRAY<FLOAT64>). Score semantics, distance type, and dimension are unchanged.
const splitSearchTemplate = "SELECT\n" +
	"  base.image_uri    AS image_uri,\n" +
	"  base.content_type AS content_type,\n" +
	"  distance\n" +
	"FROM VECTOR_SEARCH(\n" +
	"  TABLE `${DATASET_ID}.${TABLE}`,\n" +
	"  'embedding',\n" +
	"  (SELECT @query_embedding AS embedding),\n" +
	"  query_column_to_search => 'embedding',\n" +
	"  top_k => @top_k,\n" +
	"  distance_type => 'COSINE'\n" +
	")\n" +
	"ORDER BY distance ASC;\n"

// SplitQuerySQL returns the degraded 2-job split as two separate statements:
// (1) embedding generation, (2) VECTOR_SEARCH taking @query_embedding. This is a
// selectable alternative, NOT the default; it is used only if the single-query
// chain fails the dry-run gate (Task 2.2 currently confirms single-query).
func (b *QueryBuilder) SplitQuerySQL() (embeddingSQL string, searchSQL string) {
	return b.renderIdentifiers(splitEmbeddingTemplate), b.renderIdentifiers(splitSearchTemplate)
}

// SplitParameterNames lists the parameters for the split path: the embedding
// statement binds @query; the search statement binds @query_embedding and
// @top_k.
func (b *QueryBuilder) SplitParameterNames() (embeddingParams []string, searchParams []string) {
	return []string{paramQuery}, []string{paramQueryEmbedding, paramTopK}
}
