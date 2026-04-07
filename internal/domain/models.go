package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Study represents an ingested research paper / study in the exercise-science
// corpus. It carries bibliographic and evidence metadata alongside the
// processing status of its text.
type Study struct {
	ID uuid.UUID `json:"id"`

	// Bibliographic metadata
	Title      string   `json:"title"`
	Authors    []string `json:"authors"`
	Journal    string   `json:"journal"`
	Year       *int     `json:"year,omitempty"`
	DOI        string   `json:"doi,omitempty"`
	StudyType  string   `json:"study_type"`  // meta-analysis | systematic-review | rct | cohort | observational | review | case-study | unknown
	Topic      string   `json:"topic"`       // hypertrophy | strength | nutrition | recovery | programming | ...
	Population string   `json:"population"`  // e.g. "resistance-trained males"
	SampleSize *int     `json:"sample_size,omitempty"`

	// Source file + processing status
	Filename   string    `json:"filename"`
	FileType   string    `json:"file_type"`
	FileSize   int64     `json:"file_size"`
	Status     string    `json:"status"` // pending | processing | completed | failed
	ErrorMsg   *string   `json:"error_msg,omitempty"`
	ChunkCount int       `json:"chunk_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Chunk represents a text chunk of a study with its embedding vector.
type Chunk struct {
	ID         uuid.UUID `json:"id"`
	StudyID    uuid.UUID `json:"study_id"`
	Content    string    `json:"content"`
	Embedding  []float32 `json:"-"` // never sent to clients
	PageNum    *int      `json:"page_num,omitempty"`
	ChunkIndex int       `json:"chunk_index"`
	TokenCount *int      `json:"token_count,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// SearchRequest is the payload for POST /api/v1/search.
type SearchRequest struct {
	Query string `json:"query"`
	TopK  int    `json:"top_k,omitempty"` // default 5

	// Domain filters (all optional)
	StudyID   *uuid.UUID `json:"study_id,omitempty"`   // scope to a single study
	StudyType []string   `json:"study_type,omitempty"` // e.g. ["meta-analysis","rct"]
	Topic     string     `json:"topic,omitempty"`
	MinYear   *int       `json:"min_year,omitempty"`
}

// HasFilters reports whether any domain filter is set. Filtered queries bypass
// the query cache (the cache key is keyed only on query text + top_k).
func (r SearchRequest) HasFilters() bool {
	return r.StudyID != nil ||
		len(r.StudyType) > 0 ||
		strings.TrimSpace(r.Topic) != "" ||
		r.MinYear != nil
}

// SearchResult is a single ranked result.
type SearchResult struct {
	ChunkID    uuid.UUID `json:"chunk_id"`
	StudyID    uuid.UUID `json:"study_id"`
	Title      string    `json:"title"`
	StudyType  string    `json:"study_type"`
	Year       *int      `json:"year,omitempty"`
	Content    string    `json:"content"`
	PageNum    *int      `json:"page_num,omitempty"`
	ChunkIndex int       `json:"chunk_index"`
	Score      float64   `json:"score"` // cosine similarity 0..1 (ordering may also reflect evidence weight)
}

// SearchResponse wraps multiple results.
type SearchResponse struct {
	Query   string         `json:"query"`
	Results []SearchResult `json:"results"`
	Cached  bool           `json:"cached"`
	Latency string         `json:"latency"` // e.g. "12.3ms"
}

// EmbedRequest is sent to the Python sidecar.
type EmbedRequest struct {
	Texts []string `json:"texts"`
}

// EmbedResponse comes back from the Python sidecar.
type EmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Dimension  int         `json:"dimension"`
}

// Job represents a study processing job in the Redis queue.
type Job struct {
	StudyID  uuid.UUID `json:"study_id"`
	RawText  string    `json:"raw_text"`
	FileType string    `json:"file_type"`
}

// RelatedStudy is a result item from GET /studies/{id}/related.
type RelatedStudy struct {
	StudyID    uuid.UUID `json:"study_id"`
	Title      string    `json:"title"`
	StudyType  string    `json:"study_type"`
	Year       *int      `json:"year,omitempty"`
	ChunkCount int       `json:"chunk_count"`
	Score      float64   `json:"score"`
}

// SummaryResponse is returned by GET /studies/{id}/summary.
type SummaryResponse struct {
	StudyID uuid.UUID      `json:"study_id"`
	Title   string         `json:"title"`
	Chunks  []SearchResult `json:"summary_chunks"`
}

// Pagination helpers.
type ListParams struct {
	Cursor *uuid.UUID `json:"cursor,omitempty"`
	Limit  int        `json:"limit,omitempty"` // default 20, max 100
}

// ErrorResponse is the standard error envelope.
type ErrorResponse struct {
	Error   string `json:"error"`
	Code    int    `json:"code"`
	Details string `json:"details,omitempty"`
}
