package search

import (
	"strings"
	"testing"
)

// injected identifier values used across tests. These are deliberately NOT the
// production literals: the tests prove the rendered SQL reflects whatever the
// builder is constructed with (config-driven, not hardcoded in source).
const (
	testDataset = "proj.ds"
	testModel   = "gemini_embedding_model"
	testTable   = "image_embeddings"
)

func newTestBuilder(t *testing.T) *QueryBuilder {
	t.Helper()
	b, err := NewQueryBuilder(testDataset, testModel, testTable)
	if err != nil {
		t.Fatalf("NewQueryBuilder() unexpected error: %v", err)
	}
	if b == nil {
		t.Fatal("NewQueryBuilder() returned nil builder without error")
	}
	return b
}

// TestNewQueryBuilder_RejectsEmptyConfig verifies the builder fails fast when an
// identifier value is missing, since identifiers cannot be parameter-bound and
// an empty value would render invalid SQL.
func TestNewQueryBuilder_RejectsEmptyConfig(t *testing.T) {
	cases := []struct {
		name                  string
		dataset, model, table string
	}{
		{"empty dataset", "", testModel, testTable},
		{"empty model", testDataset, "", testTable},
		{"empty table", testDataset, testModel, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewQueryBuilder(tc.dataset, tc.model, tc.table); err == nil {
				t.Errorf("NewQueryBuilder(%q,%q,%q) expected error, got nil", tc.dataset, tc.model, tc.table)
			}
		})
	}
}

// TestSingleQuery_RendersInjectedIdentifiers verifies the default single-query
// (CTE) shape is rendered from injected config values: dataset.model reference,
// dataset.table reference, COSINE distance, and the explicit 3072 dimension.
func TestSingleQuery_RendersInjectedIdentifiers(t *testing.T) {
	b := newTestBuilder(t)
	sql := b.SingleQuerySQL()

	wants := []string{
		"`proj.ds.gemini_embedding_model`", // ${DATASET_ID}.${MODEL}
		"`proj.ds.image_embeddings`",       // ${DATASET_ID}.${TABLE}
		"distance_type => 'COSINE'",
		"3072 AS output_dimensionality",
		"AI.GENERATE_EMBEDDING",
		"VECTOR_SEARCH",
		"SUM(1.0 / (60 + rank)) AS rrf_score",
		"WHERE NULLIF(@query_en, '') IS NOT NULL",
		"WHERE status = ''",
	}
	for _, w := range wants {
		if !strings.Contains(sql, w) {
			t.Errorf("single-query SQL missing %q\n--- SQL ---\n%s", w, sql)
		}
	}
}

// TestSingleQuery_IsSingleCTEStatement verifies the default path is ONE
// statement joining the CTE into VECTOR_SEARCH (the adopted single-query shape
// from the Task 2.2 dry-run gate), not the 2-job split.
func TestSingleQuery_IsSingleCTEStatement(t *testing.T) {
	b := newTestBuilder(t)
	sql := b.SingleQuerySQL()

	if !strings.Contains(sql, "WITH query_inputs AS") {
		t.Errorf("single-query SQL must use the CTE `WITH query_inputs AS`\n%s", sql)
	}
	// Exactly one statement-terminating semicolon (single query, not split).
	if n := strings.Count(sql, ";"); n != 1 {
		t.Errorf("single-query SQL should be a single statement (1 semicolon), got %d\n%s", n, sql)
	}
	// The embedding CTE feeds VECTOR_SEARCH via `TABLE query_embeddings`.
	if !strings.Contains(sql, "TABLE query_embeddings") {
		t.Errorf("single-query SQL must join the CTE into VECTOR_SEARCH via `TABLE query_embeddings`\n%s", sql)
	}
}

// TestSingleQuery_PreservesParameterReferences verifies @query/@query_en/
// @candidate_k/@top_k remain as BigQuery parameter references (bound at
// execution time by the client), NOT substituted into the rendered SQL.
func TestSingleQuery_PreservesParameterReferences(t *testing.T) {
	b := newTestBuilder(t)
	sql := b.SingleQuerySQL()

	if !strings.Contains(sql, "@query") {
		t.Errorf("single-query SQL must preserve @query parameter reference\n%s", sql)
	}
	if !strings.Contains(sql, "@query_en") {
		t.Errorf("single-query SQL must preserve @query_en parameter reference\n%s", sql)
	}
	if !strings.Contains(sql, "top_k => @candidate_k") {
		t.Errorf("single-query SQL must preserve @candidate_k parameter reference bound to top_k =>\n%s", sql)
	}
	if !strings.Contains(sql, "LIMIT @top_k") {
		t.Errorf("single-query SQL must preserve @top_k parameter reference bound to LIMIT\n%s", sql)
	}
}

// TestSingleQuery_ConfigDriven_NoHardcoding verifies that injecting a DIFFERENT
// dataset/model/table produces SQL reflecting THOSE values — proving the
// identifiers flow from config injection and are not hardcoded constants.
func TestSingleQuery_ConfigDriven_NoHardcoding(t *testing.T) {
	b, err := NewQueryBuilder("other.dataset", "custom_model", "custom_table")
	if err != nil {
		t.Fatalf("NewQueryBuilder() unexpected error: %v", err)
	}
	sql := b.SingleQuerySQL()

	if !strings.Contains(sql, "`other.dataset.custom_model`") {
		t.Errorf("rendered SQL did not reflect injected model identifier\n%s", sql)
	}
	if !strings.Contains(sql, "`other.dataset.custom_table`") {
		t.Errorf("rendered SQL did not reflect injected table identifier\n%s", sql)
	}
	// The default test identifiers must NOT leak when other values are injected.
	if strings.Contains(sql, "gemini_embedding_model") || strings.Contains(sql, "image_embeddings") {
		t.Errorf("rendered SQL leaked default identifiers; values are not config-driven\n%s", sql)
	}
}

// TestParameterNames exposes the parameter names the single-query path expects,
// so the BigQuery client (task 3.1) can bind them without re-deriving literals.
func TestParameterNames(t *testing.T) {
	b := newTestBuilder(t)
	names := b.ParameterNames()
	want := map[string]bool{"query": true, "query_en": true, "candidate_k": true, "top_k": true}
	if len(names) != len(want) {
		t.Fatalf("ParameterNames() = %v, want keys %v", names, want)
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected parameter name %q (want one of %v)", n, want)
		}
	}
}

// --- Degraded (2-job split) alternative path ---

// TestSplitQuery_RendersTwoSeparateStatements verifies the selectable degraded
// path renders two separate statements: (1) embedding generation producing
// `embedding`, (2) a VECTOR_SEARCH taking the @query_embedding parameter.
func TestSplitQuery_RendersTwoSeparateStatements(t *testing.T) {
	b := newTestBuilder(t)
	emb, search := b.SplitQuerySQL()

	// (1) embedding-generation query.
	if !strings.Contains(emb, "AI.GENERATE_EMBEDDING") {
		t.Errorf("split embedding query must call AI.GENERATE_EMBEDDING\n%s", emb)
	}
	if !strings.Contains(emb, "`proj.ds.gemini_embedding_model`") {
		t.Errorf("split embedding query must reference injected dataset.model\n%s", emb)
	}
	if !strings.Contains(emb, "3072 AS output_dimensionality") {
		t.Errorf("split embedding query must pin output_dimensionality=3072\n%s", emb)
	}
	if strings.Contains(emb, "VECTOR_SEARCH") {
		t.Errorf("split embedding query must NOT contain VECTOR_SEARCH (it is statement 1)\n%s", emb)
	}

	// (2) VECTOR_SEARCH query taking @query_embedding.
	if !strings.Contains(search, "VECTOR_SEARCH") {
		t.Errorf("split search query must call VECTOR_SEARCH\n%s", search)
	}
	if !strings.Contains(search, "`proj.ds.image_embeddings`") {
		t.Errorf("split search query must reference injected dataset.table\n%s", search)
	}
	if !strings.Contains(search, "@query_embedding") {
		t.Errorf("split search query must take the @query_embedding parameter\n%s", search)
	}
	if !strings.Contains(search, "distance_type => 'COSINE'") {
		t.Errorf("split search query must keep distance_type => 'COSINE'\n%s", search)
	}
	if !strings.Contains(search, "top_k => @top_k") {
		t.Errorf("split search query must keep top_k => @top_k\n%s", search)
	}
	if strings.Contains(search, "AI.GENERATE_EMBEDDING") {
		t.Errorf("split search query must NOT regenerate the embedding (it is statement 2)\n%s", search)
	}
}
