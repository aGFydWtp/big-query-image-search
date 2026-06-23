package rewrite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

type fakeClient struct {
	gotModel    string
	gotContents []*genai.Content
	gotConfig   *genai.GenerateContentConfig
	resp        *genai.GenerateContentResponse
	err         error
	calls       int
}

func (f *fakeClient) GenerateContent(_ context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	f.calls++
	f.gotModel = model
	f.gotContents = contents
	f.gotConfig = config
	return f.resp, f.err
}

// textResponse builds a minimal GenerateContentResponse whose Text() concatenates
// to the given string — same shape Vertex AI returns for a one-part text answer.
func textResponse(text string) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Parts: []*genai.Part{{Text: text}},
			},
		}},
	}
}

func TestNoop_ReturnsEmpty(t *testing.T) {
	got, err := Noop{}.Rewrite(context.Background(), "海の絵")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("Noop returned %q, want empty", got)
	}
}

func TestVertexRewriter_TrimsAndReturnsModelText(t *testing.T) {
	fc := &fakeClient{resp: textResponse("  a painting of the sea  \n")}
	r := NewVertexRewriter(fc, "gemini-2.5-flash", 0)

	got, err := r.Rewrite(context.Background(), "海の絵")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "a painting of the sea" {
		t.Errorf("Rewrite = %q, want %q", got, "a painting of the sea")
	}
	if fc.gotModel != "gemini-2.5-flash" {
		t.Errorf("model = %q, want gemini-2.5-flash", fc.gotModel)
	}
	if fc.calls != 1 {
		t.Errorf("calls = %d, want 1", fc.calls)
	}
}

func TestVertexRewriter_PromptIncludesQuery(t *testing.T) {
	fc := &fakeClient{resp: textResponse("ok")}
	r := NewVertexRewriter(fc, "gemini-2.5-flash", 0)

	if _, err := r.Rewrite(context.Background(), "印象派の作品"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fc.gotContents) == 0 || fc.gotContents[0] == nil {
		t.Fatal("contents not forwarded to client")
	}
	var combined strings.Builder
	for _, p := range fc.gotContents[0].Parts {
		combined.WriteString(p.Text)
	}
	body := combined.String()
	if !strings.Contains(body, "印象派の作品") {
		t.Errorf("prompt missing user query: %q", body)
	}
	if !strings.Contains(body, "English") {
		t.Errorf("prompt missing English directive: %q", body)
	}
}

func TestVertexRewriter_BlankQueryShortCircuits(t *testing.T) {
	fc := &fakeClient{}
	r := NewVertexRewriter(fc, "gemini-2.5-flash", 0)

	got, err := r.Rewrite(context.Background(), "   ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if fc.calls != 0 {
		t.Errorf("blank query must not call Vertex AI (calls=%d)", fc.calls)
	}
}

func TestVertexRewriter_UpstreamErrorWrapped(t *testing.T) {
	fc := &fakeClient{err: errors.New("503 unavailable")}
	r := NewVertexRewriter(fc, "gemini-2.5-flash", 0)

	got, err := r.Rewrite(context.Background(), "海")
	if err == nil {
		t.Fatal("expected error from upstream failure")
	}
	if !errors.Is(err, ErrRewriteUpstream) {
		t.Errorf("error not classified as upstream: %v", err)
	}
	if got != "" {
		t.Errorf("rewrite = %q, want empty on error", got)
	}
}

func TestVertexRewriter_TimeoutClassified(t *testing.T) {
	fc := &fakeClient{err: context.DeadlineExceeded}
	r := NewVertexRewriter(fc, "gemini-2.5-flash", 0)

	_, err := r.Rewrite(context.Background(), "海")
	if !errors.Is(err, ErrRewriteTimeout) {
		t.Errorf("expected ErrRewriteTimeout, got: %v", err)
	}
}

func TestVertexRewriter_ContextTimeoutApplied(t *testing.T) {
	slow := slowClient{delay: 100 * time.Millisecond}
	r := NewVertexRewriter(slow, "gemini-2.5-flash", 10*time.Millisecond)

	start := time.Now()
	_, err := r.Rewrite(context.Background(), "海")
	if elapsed := time.Since(start); elapsed > 80*time.Millisecond {
		t.Errorf("timeout not applied; elapsed=%v", elapsed)
	}
	if !errors.Is(err, ErrRewriteTimeout) {
		t.Errorf("expected ErrRewriteTimeout, got: %v", err)
	}
}

type slowClient struct{ delay time.Duration }

func (s slowClient) GenerateContent(ctx context.Context, _ string, _ []*genai.Content, _ *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	select {
	case <-time.After(s.delay):
		return textResponse("late"), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestVertexRewriter_NilResponseTreatedAsEmpty(t *testing.T) {
	fc := &fakeClient{resp: nil}
	r := NewVertexRewriter(fc, "gemini-2.5-flash", 0)

	got, err := r.Rewrite(context.Background(), "海")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
