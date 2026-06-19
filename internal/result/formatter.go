// Package result formats BigQuery search matches into the API response
// contract owned by this spec (image-search-api).
//
// The formatter (Format) converts each search.Row into a SearchResult,
// translating the raw COSINE distance into a similarity score and ordering
// results most-similar-first. Signed URL population is deliberately out of
// scope here: it is filled on demand by the handler/signed-URL generator.
package result

import (
	"sort"

	"github.com/aGFydWtp/big-query-image-search/internal/search"
)

// SearchResult is a single item in the search response. It is the stable API
// data contract shared with the HTTP handler; JSON field names must not change.
//
// Score is a cosine similarity in the sense of score = 1 - distance: HIGHER
// score means MORE similar. SignedURL and ContentType are optional and use
// omitempty so an absent value is not serialized.
type SearchResult struct {
	ImageURI    string  `json:"image_uri"`
	Score       float64 `json:"score"`
	SignedURL   string  `json:"signed_url,omitempty"`
	ContentType string  `json:"content_type,omitempty"`
}

// Format converts BigQuery search rows into the response contract.
//
//   - score = 1 - distance (cosine similarity; higher score = more similar).
//   - Results are sorted by distance ascending (== score descending == most
//     similar first). A stable sort guarantees deterministic ordering for rows
//     with equal distance.
//   - content_type is carried through as an optional field.
//   - signed_url is left empty; the handler populates it on demand.
//   - For 0 (or nil) rows, an empty NON-NIL slice is returned so JSON encodes
//     as [] rather than null (Requirement 4.4).
func Format(rows []search.Row) []SearchResult {
	results := make([]SearchResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, SearchResult{
			ImageURI:    row.ImageURI,
			Score:       1 - row.Distance,
			ContentType: row.ContentType,
		})
	}

	// score descending == distance ascending == most similar first.
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results
}
