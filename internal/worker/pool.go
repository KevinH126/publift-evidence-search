package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kevinhart/semantic-search/internal/chunker"
	"github.com/kevinhart/semantic-search/internal/domain"
	"github.com/kevinhart/semantic-search/internal/service"
	"github.com/kevinhart/semantic-search/internal/store"
)

// Pool manages a set of goroutine workers that process study indexing jobs.
type Pool struct {
	concurrency int
	pg          *store.Postgres
	redis       *store.Redis
	embedder    *service.Embedder
	logger      *slog.Logger
}

// NewPool creates a new worker pool.
func NewPool(concurrency int, pg *store.Postgres, redis *store.Redis, embedder *service.Embedder, logger *slog.Logger) *Pool {
	return &Pool{
		concurrency: concurrency,
		pg:          pg,
		redis:       redis,
		embedder:    embedder,
		logger:      logger,
	}
}

// Start launches N worker goroutines that consume jobs from the Redis queue.
// Blocks until ctx is cancelled.
func (p *Pool) Start(ctx context.Context) {
	p.logger.Info("starting worker pool", "concurrency", p.concurrency)

	for i := 0; i < p.concurrency; i++ {
		go p.worker(ctx, i)
	}

	<-ctx.Done()
	p.logger.Info("worker pool shutting down")
}

func (p *Pool) worker(ctx context.Context, id int) {
	p.logger.Info("worker started", "worker_id", id)

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("worker stopping", "worker_id", id)
			return
		default:
			job, err := p.redis.DequeueJob(ctx, 5*time.Second)
			if err != nil {
				if ctx.Err() != nil {
					return // context cancelled
				}
				p.logger.Error("dequeue error", "worker_id", id, "err", err)
				time.Sleep(1 * time.Second)
				continue
			}

			if job == nil {
				continue // timeout, no job available
			}

			p.logger.Info("processing job", "worker_id", id, "study_id", job.StudyID)
			if err := p.processJob(ctx, job); err != nil {
				p.logger.Error("job failed", "worker_id", id, "study_id", job.StudyID, "err", err)
			}
		}
	}
}

func (p *Pool) processJob(ctx context.Context, job *domain.Job) error {
	// Update status to processing
	if err := p.pg.UpdateStudyStatus(ctx, job.StudyID, "processing", nil, 0); err != nil {
		return fmt.Errorf("update status: %w", err)
	}

	// Retry logic: 3 attempts with exponential backoff
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		lastErr = p.processWithRetry(ctx, job)
		if lastErr == nil {
			return nil
		}

		p.logger.Warn("job attempt failed",
			"study_id", job.StudyID,
			"attempt", attempt,
			"err", lastErr,
		)

		if attempt < 3 {
			backoff := time.Duration(attempt*attempt) * time.Second
			time.Sleep(backoff)
		}
	}

	// All retries failed
	errMsg := lastErr.Error()
	if err := p.pg.UpdateStudyStatus(ctx, job.StudyID, "failed", &errMsg, 0); err != nil {
		p.logger.Error("failed to update status after failure", "err", err)
	}
	return fmt.Errorf("all retries exhausted: %w", lastErr)
}

func (p *Pool) processWithRetry(ctx context.Context, job *domain.Job) error {
	// Step 1: Chunk the text
	// PDF text is already extracted at upload time; the worker receives clean text.
	chunks := chunker.Chunk(job.RawText, chunker.DefaultChunkSize, chunker.DefaultChunkOverlap)
	if len(chunks) == 0 {
		return fmt.Errorf("no chunks produced from study")
	}

	p.logger.Info("chunked study", "study_id", job.StudyID, "chunk_count", len(chunks))

	// Step 2: Get embeddings from sidecar
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Content
	}

	embeddings, err := p.embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("embed chunks: %w", err)
	}

	if len(embeddings) != len(chunks) {
		return fmt.Errorf("embedding count mismatch: got %d, expected %d", len(embeddings), len(chunks))
	}

	// Step 3: Build chunk records
	dbChunks := make([]domain.Chunk, len(chunks))
	for i, c := range chunks {
		tokenCount := c.TokenCount
		dbChunks[i] = domain.Chunk{
			StudyID:    job.StudyID,
			Content:    c.Content,
			Embedding:  embeddings[i],
			ChunkIndex: c.ChunkIndex,
			TokenCount: &tokenCount,
		}
	}

	// Step 4: Insert chunks into database
	if err := p.pg.InsertChunks(ctx, dbChunks); err != nil {
		return fmt.Errorf("insert chunks: %w", err)
	}

	// Step 5: Update study status
	if err := p.pg.UpdateStudyStatus(ctx, job.StudyID, "completed", nil, len(chunks)); err != nil {
		return fmt.Errorf("update status: %w", err)
	}

	p.logger.Info("study processed successfully",
		"study_id", job.StudyID,
		"chunks", len(chunks),
	)

	return nil
}
