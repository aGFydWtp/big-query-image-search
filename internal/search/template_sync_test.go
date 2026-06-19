package search

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddedTemplateMatchesSpecArtifact guards against drift between the
// co-located embedded template (templates/search.sql, required because go:embed
// cannot traverse parent directories) and the repository-root spec artifact
// sql/search.sql mandated by the design's File Structure Plan. They MUST be
// byte-identical so sql/search.sql remains the single authored source of truth.
func TestEmbeddedTemplateMatchesSpecArtifact(t *testing.T) {
	// internal/search -> repo root is two levels up.
	specPath := filepath.Join("..", "..", "sql", "search.sql")
	want, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("reading spec artifact %s: %v", specPath, err)
	}
	if string(want) != searchSQLTemplate {
		t.Errorf("embedded template drifted from %s; keep them byte-identical", specPath)
	}
}
