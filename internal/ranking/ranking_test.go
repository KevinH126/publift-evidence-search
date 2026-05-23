package ranking

import (
	"testing"

	"github.com/google/uuid"
	"github.com/KevinH126/publift-evidence-search/internal/domain"
)

func TestEvidenceWeight(t *testing.T) {
	tests := []struct {
		name      string
		studyType string
		want      float64
	}{
		{"meta-analysis outranks all", "meta-analysis", 1.15},
		{"rct is boosted", "rct", 1.08},
		{"case study is dampened", "case-study", 0.92},
		{"unknown is neutral", "unknown", 1.0},
		{"empty is neutral", "", 1.0},
		{"case-insensitive", "RCT", 1.08},
		{"trims whitespace", "  meta-analysis  ", 1.15},
		{"unrecognized is neutral", "editorial", 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EvidenceWeight(tt.studyType); got != tt.want {
				t.Errorf("EvidenceWeight(%q) = %v, want %v", tt.studyType, got, tt.want)
			}
		})
	}
}

func TestRerankPromotesStrongerEvidence(t *testing.T) {
	// The observational study has a marginally higher cosine similarity, but the
	// meta-analysis should win after the evidence boost (0.80*1.15 > 0.83*0.98).
	meta := domain.SearchResult{ChunkID: uuid.New(), StudyType: "meta-analysis", Score: 0.80}
	obs := domain.SearchResult{ChunkID: uuid.New(), StudyType: "observational", Score: 0.83}

	got := Rerank([]domain.SearchResult{obs, meta}, 5)

	if got[0].ChunkID != meta.ChunkID {
		t.Errorf("expected meta-analysis ranked first, got study type %q", got[0].StudyType)
	}
}

func TestRerankTrimsToK(t *testing.T) {
	candidates := []domain.SearchResult{
		{ChunkID: uuid.New(), StudyType: "rct", Score: 0.9},
		{ChunkID: uuid.New(), StudyType: "cohort", Score: 0.8},
		{ChunkID: uuid.New(), StudyType: "review", Score: 0.7},
	}

	got := Rerank(candidates, 2)
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got))
	}
}

func TestRerankDominantSimilarityStillWins(t *testing.T) {
	// A much better cosine match should not be overturned by a small evidence gap.
	strong := domain.SearchResult{ChunkID: uuid.New(), StudyType: "case-study", Score: 0.95}
	weak := domain.SearchResult{ChunkID: uuid.New(), StudyType: "meta-analysis", Score: 0.40}

	got := Rerank([]domain.SearchResult{weak, strong}, 5)
	if got[0].ChunkID != strong.ChunkID {
		t.Errorf("expected the far stronger match first regardless of study type")
	}
}
