package api

import (
	"context"

	"github.com/google/uuid"
	"github.com/kevinhart/semantic-search/internal/domain"
)

// StudyStore is the persistence contract the API depends on.
type StudyStore interface {
	Ping(ctx context.Context) error
	CreateStudy(ctx context.Context, study *domain.Study) error
	GetStudy(ctx context.Context, id uuid.UUID) (*domain.Study, error)
	ListStudies(ctx context.Context, cursor *uuid.UUID, limit int) ([]domain.Study, error)
	DeleteStudy(ctx context.Context, id uuid.UUID) error
	SemanticSearch(ctx context.Context, queryVec []float32, req domain.SearchRequest) ([]domain.SearchResult, error)
	KeywordSearch(ctx context.Context, query string, topK int) ([]domain.SearchResult, error)
	GetStudyChunks(ctx context.Context, studyID uuid.UUID) ([]domain.Chunk, error)
	RelatedStudies(ctx context.Context, vec []float32, topK int, excludeID uuid.UUID) ([]domain.RelatedStudy, error)
}

// SearchCache is the caching + queue contract the API depends on.
type SearchCache interface {
	Ping(ctx context.Context) error
	GetCachedSearch(ctx context.Context, query string, topK int) ([]domain.SearchResult, error)
	CacheSearch(ctx context.Context, query string, topK int, results []domain.SearchResult) error
	CheckRateLimit(ctx context.Context, ip string) (bool, error)
	EnqueueJob(ctx context.Context, job domain.Job) error
}

// EmbedderService is the embedding contract the API depends on.
type EmbedderService interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Health(ctx context.Context) error
}
