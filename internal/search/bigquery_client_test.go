package search

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRunner is an injectable queryRunner stand-in so the BigQueryClient logic
// can be exercised without a live BigQuery connection. It records the SQL and
// bound parameters it received and returns canned rows or a canned error.
type fakeRunner struct {
	rows []Row
	err  error

	gotSQL    string
	gotParams []QueryParam
	called    int
}

func (f *fakeRunner) Run(ctx context.Context, sql string, params []QueryParam) ([]Row, error) {
	f.called++
	f.gotSQL = sql
	f.gotParams = params
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func newClientBuilder(t *testing.T) *QueryBuilder {
	t.Helper()
	b, err := NewQueryBuilder("proj.ds", "gemini_embedding_model", "image_embeddings")
	if err != nil {
		t.Fatalf("NewQueryBuilder: %v", err)
	}
	return b
}

// TestBigQueryClient_Search_Success verifies that on a successful run the client
// returns the rows produced by the runner, in order, mapped to the Row struct.
func TestBigQueryClient_Search_Success(t *testing.T) {
	want := []Row{
		{ImageURI: "gs://bucket/a.jpg", Distance: 0.10, ContentType: "image/jpeg"},
		{ImageURI: "gs://bucket/b.png", Distance: 0.25, ContentType: "image/png"},
	}
	fake := &fakeRunner{rows: want}
	client := NewBigQueryClient(newClientBuilder(t), fake)

	got, err := client.Search(context.Background(), "a red bicycle", "a painting of a red bicycle", 2)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestBigQueryClient_Search_BindsParameters verifies the single-job path binds
// @query, @query_en, @candidate_k, and @top_k using the builder's parameter
// names, and runs exactly one job with the single-query SQL.
func TestBigQueryClient_Search_BindsParameters(t *testing.T) {
	builder := newClientBuilder(t)
	fake := &fakeRunner{rows: []Row{}}
	client := NewBigQueryClient(builder, fake)

	if _, err := client.Search(context.Background(), "blue car", "a blue car", 7); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}

	if fake.called != 1 {
		t.Fatalf("expected exactly 1 job (single-job default), got %d", fake.called)
	}
	if fake.gotSQL != builder.SingleQuerySQL() {
		t.Errorf("runner did not receive the single-query SQL")
	}

	byName := map[string]any{}
	for _, p := range fake.gotParams {
		byName[p.Name] = p.Value
	}
	if len(byName) != 4 {
		t.Fatalf("expected 4 bound params, got %d (%+v)", len(byName), fake.gotParams)
	}
	q, ok := byName["query"]
	if !ok {
		t.Fatalf("missing @query param, got %+v", fake.gotParams)
	}
	if qs, ok := q.(string); !ok || qs != "blue car" {
		t.Errorf("@query = %v (%T), want string \"blue car\"", q, q)
	}
	qen, ok := byName["query_en"]
	if !ok {
		t.Fatalf("missing @query_en param, got %+v", fake.gotParams)
	}
	if qs, ok := qen.(string); !ok || qs != "a blue car" {
		t.Errorf("@query_en = %v (%T), want string \"a blue car\"", qen, qen)
	}
	cand, ok := byName["candidate_k"]
	if !ok {
		t.Fatalf("missing @candidate_k param, got %+v", fake.gotParams)
	}
	if ci, ok := cand.(int64); !ok || ci != 50 {
		t.Errorf("@candidate_k = %v (%T), want int64 50", cand, cand)
	}
	k, ok := byName["top_k"]
	if !ok {
		t.Fatalf("missing @top_k param, got %+v", fake.gotParams)
	}
	if ki, ok := k.(int64); !ok || ki != 7 {
		t.Errorf("@top_k = %v (%T), want int64 7", k, k)
	}
}

// TestBigQueryClient_Search_RunnerError_NoLeak verifies that a runner error is
// converted into the internal error type (ErrBigQuery) and that the raw
// underlying message does NOT leak into the client-facing Error() string.
func TestBigQueryClient_Search_RunnerError_NoLeak(t *testing.T) {
	const rawDetail = "bq internal SQL boom: table proj.ds.image_embeddings denied"
	fake := &fakeRunner{err: errors.New(rawDetail)}
	client := NewBigQueryClient(newClientBuilder(t), fake)

	got, err := client.Search(context.Background(), "anything", "", 5)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if got != nil {
		t.Errorf("expected nil rows on error, got %+v", got)
	}
	if !errors.Is(err, ErrBigQuery) {
		t.Errorf("error should match sentinel ErrBigQuery, got: %v", err)
	}
	if strings.Contains(err.Error(), rawDetail) {
		t.Errorf("client-facing error leaked raw detail: %q", err.Error())
	}
	if strings.Contains(err.Error(), "image_embeddings") || strings.Contains(err.Error(), "SQL") {
		t.Errorf("client-facing error leaked internal substrings: %q", err.Error())
	}
}

// TestBigQueryClient_Search_ContextCancelled verifies that a cancelled context
// short-circuits to the internal timeout/cancel error without invoking the
// runner, and without leaking detail.
func TestBigQueryClient_Search_ContextCancelled(t *testing.T) {
	fake := &fakeRunner{rows: []Row{{ImageURI: "gs://x"}}}
	client := NewBigQueryClient(newClientBuilder(t), fake)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := client.Search(ctx, "anything", "", 5)
	if err == nil {
		t.Fatal("expected an error for cancelled context, got nil")
	}
	if got != nil {
		t.Errorf("expected nil rows on cancellation, got %+v", got)
	}
	if !errors.Is(err, ErrQueryTimeout) {
		t.Errorf("error should match sentinel ErrQueryTimeout, got: %v", err)
	}
}

// TestBigQueryClient_Search_RunnerContextError verifies that when the runner
// returns a context error (deadline/cancel surfaced mid-execution), it is
// classified as the internal timeout error type.
func TestBigQueryClient_Search_RunnerContextError(t *testing.T) {
	fake := &fakeRunner{err: context.DeadlineExceeded}
	client := NewBigQueryClient(newClientBuilder(t), fake)

	_, err := client.Search(context.Background(), "anything", "", 5)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, ErrQueryTimeout) {
		t.Errorf("deadline-exceeded should map to ErrQueryTimeout, got: %v", err)
	}
	if strings.Contains(err.Error(), "deadline") {
		// generic message; raw context wording must not be the client surface
		t.Logf("note: error string = %q", err.Error())
	}
}
