// Package ranking implements evidence-aware re-ranking of semantic search
// results. Pure vector similarity treats every study equally; in evidence-based
// practice a meta-analysis or RCT should outrank a single observational study
// on an otherwise comparable match. Rerank encodes that hierarchy as a
// multiplier applied to the cosine similarity score.
package ranking

import (
	"sort"
	"strings"

	"github.com/kevinhart/semantic-search/internal/domain"
)

// evidenceWeights maps a normalized study type to a ranking multiplier. The
// values follow the standard evidence pyramid used in exercise-science reviews.
var evidenceWeights = map[string]float64{
	"meta-analysis":     1.15,
	"systematic-review": 1.12,
	"rct":               1.08,
	"crossover":         1.05,
	"cohort":            1.00,
	"observational":     0.98,
	"review":            0.96,
	"case-study":        0.92,
}

// EvidenceWeight returns the ranking multiplier for a study type. Unknown or
// empty types are treated as neutral (1.0).
func EvidenceWeight(studyType string) float64 {
	if w, ok := evidenceWeights[strings.ToLower(strings.TrimSpace(studyType))]; ok {
		return w
	}
	return 1.0
}

// blended returns the evidence-weighted score used purely for ordering.
func blended(r domain.SearchResult) float64 {
	return r.Score * EvidenceWeight(r.StudyType)
}

// Rerank re-orders semantic search candidates by a blend of cosine similarity
// and evidence strength, then returns the top k. The displayed Score is left as
// the raw cosine similarity — only the ordering reflects the evidence boost, so
// callers can still show an honest similarity number.
func Rerank(candidates []domain.SearchResult, k int) []domain.SearchResult {
	sort.SliceStable(candidates, func(i, j int) bool {
		return blended(candidates[i]) > blended(candidates[j])
	})
	if k > 0 && len(candidates) > k {
		candidates = candidates[:k]
	}
	return candidates
}
