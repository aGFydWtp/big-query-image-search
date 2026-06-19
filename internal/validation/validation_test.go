package validation

import (
	"errors"
	"testing"
)

func intPtr(v int) *int { return &v }

func TestValidate(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		topK        *int
		wantErr     bool
		wantInvalid bool // expect errors.Is(err, ErrInvalidInput)
		wantQuery   string
		wantTopK    int
	}{
		{
			name:        "empty query is rejected",
			query:       "",
			topK:        nil,
			wantErr:     true,
			wantInvalid: true,
		},
		{
			name:        "whitespace-only query is rejected",
			query:       "   \t\n ",
			topK:        nil,
			wantErr:     true,
			wantInvalid: true,
		},
		{
			name:      "unspecified top_k uses default",
			query:     "a cat",
			topK:      nil,
			wantErr:   false,
			wantQuery: "a cat",
			wantTopK:  DefaultTopK,
		},
		{
			name:        "top_k zero is rejected",
			query:       "a cat",
			topK:        intPtr(0),
			wantErr:     true,
			wantInvalid: true,
		},
		{
			name:        "negative top_k is rejected",
			query:       "a cat",
			topK:        intPtr(-5),
			wantErr:     true,
			wantInvalid: true,
		},
		{
			name:      "top_k above max is clamped to max",
			query:     "a cat",
			topK:      intPtr(MaxTopK + 1),
			wantErr:   false,
			wantQuery: "a cat",
			wantTopK:  MaxTopK,
		},
		{
			name:      "top_k equal to max is unchanged",
			query:     "a cat",
			topK:      intPtr(MaxTopK),
			wantErr:   false,
			wantQuery: "a cat",
			wantTopK:  MaxTopK,
		},
		{
			name:      "top_k within range is unchanged",
			query:     "a dog",
			topK:      intPtr(5),
			wantErr:   false,
			wantQuery: "a dog",
			wantTopK:  5,
		},
		{
			name:      "top_k of one is allowed",
			query:     "x",
			topK:      intPtr(1),
			wantErr:   false,
			wantQuery: "x",
			wantTopK:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Validate(tt.query, tt.topK)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Validate(%q, %v) = nil error, want error", tt.query, tt.topK)
				}
				if tt.wantInvalid && !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("Validate(%q, %v) error = %v, want errors.Is ErrInvalidInput", tt.query, tt.topK, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate(%q, %v) unexpected error: %v", tt.query, tt.topK, err)
			}
			if got.Query != tt.wantQuery {
				t.Errorf("Query = %q, want %q", got.Query, tt.wantQuery)
			}
			if got.TopK != tt.wantTopK {
				t.Errorf("TopK = %d, want %d", got.TopK, tt.wantTopK)
			}
		})
	}
}

func TestValidatePolicyConstants(t *testing.T) {
	if DefaultTopK <= 0 {
		t.Errorf("DefaultTopK = %d, want positive", DefaultTopK)
	}
	if MaxTopK < DefaultTopK {
		t.Errorf("MaxTopK = %d, want >= DefaultTopK %d", MaxTopK, DefaultTopK)
	}
}
