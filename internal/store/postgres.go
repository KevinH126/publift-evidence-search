package store

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/kevinhart/semantic-search/internal/domain"
	"github.com/kevinhart/semantic-search/internal/ranking"
)

//go:embed migrate.sql
var migrationSQL string

// studyColumns is the canonical column list for the studies table, used by all
// SELECT queries so scan order stays in lockstep with the query.
const studyColumns = `id, title, authors, journal, year, doi, study_type, topic,
	population, sample_size, filename, file_type, file_size, status, error_msg,
	chunk_count, created_at, updated_at`

// Postgres wraps a connection pool and provides typed queries.
type Postgres struct {
	Pool *pgxpool.Pool
}

// NewPostgres creates a pool and runs migrations.
func NewPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	pg := &Postgres{Pool: pool}
	if err := pg.migrate(ctx); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return pg, nil
}

func (pg *Postgres) migrate(ctx context.Context) error {
	_, err := pg.Pool.Exec(ctx, migrationSQL)
	return err
}

// scanStudy scans a row selected with studyColumns into a Study.
func scanStudy(row pgx.Row, s *domain.Study) error {
	return row.Scan(&s.ID, &s.Title, &s.Authors, &s.Journal, &s.Year, &s.DOI,
		&s.StudyType, &s.Topic, &s.Population, &s.SampleSize, &s.Filename,
		&s.FileType, &s.FileSize, &s.Status, &s.ErrorMsg, &s.ChunkCount,
		&s.CreatedAt, &s.UpdatedAt)
}

// ── Study CRUD ──

func (pg *Postgres) CreateStudy(ctx context.Context, s *domain.Study) error {
	s.ID = uuid.New()
	s.Status = "pending"
	s.CreatedAt = time.Now()
	s.UpdatedAt = time.Now()
	if s.Authors == nil {
		s.Authors = []string{}
	}
	if s.StudyType == "" {
		s.StudyType = "unknown"
	}

	_, err := pg.Pool.Exec(ctx,
		`INSERT INTO studies
		   (id, title, authors, journal, year, doi, study_type, topic, population,
		    sample_size, filename, file_type, file_size, status, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		s.ID, s.Title, s.Authors, s.Journal, s.Year, s.DOI, s.StudyType, s.Topic,
		s.Population, s.SampleSize, s.Filename, s.FileType, s.FileSize, s.Status,
		s.CreatedAt, s.UpdatedAt,
	)
	return err
}

func (pg *Postgres) GetStudy(ctx context.Context, id uuid.UUID) (*domain.Study, error) {
	s := &domain.Study{}
	err := scanStudy(
		pg.Pool.QueryRow(ctx, `SELECT `+studyColumns+` FROM studies WHERE id = $1`, id),
		s,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return s, err
}

func (pg *Postgres) ListStudies(ctx context.Context, cursor *uuid.UUID, limit int) ([]domain.Study, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	var rows pgx.Rows
	var err error

	if cursor != nil {
		rows, err = pg.Pool.Query(ctx,
			`SELECT `+studyColumns+`
			 FROM studies WHERE created_at < (SELECT created_at FROM studies WHERE id = $1)
			 ORDER BY created_at DESC LIMIT $2`, *cursor, limit)
	} else {
		rows, err = pg.Pool.Query(ctx,
			`SELECT `+studyColumns+`
			 FROM studies ORDER BY created_at DESC LIMIT $1`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var studies []domain.Study
	for rows.Next() {
		var s domain.Study
		if err := scanStudy(rows, &s); err != nil {
			return nil, err
		}
		studies = append(studies, s)
	}
	return studies, rows.Err()
}

func (pg *Postgres) DeleteStudy(ctx context.Context, id uuid.UUID) error {
	tag, err := pg.Pool.Exec(ctx, `DELETE FROM studies WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("study not found")
	}
	return nil
}

func (pg *Postgres) UpdateStudyStatus(ctx context.Context, id uuid.UUID, status string, errMsg *string, chunkCount int) error {
	_, err := pg.Pool.Exec(ctx,
		`UPDATE studies SET status = $2, error_msg = $3, chunk_count = $4, updated_at = NOW()
		 WHERE id = $1`, id, status, errMsg, chunkCount)
	return err
}

// ── Chunk Operations ──

func (pg *Postgres) InsertChunks(ctx context.Context, chunks []domain.Chunk) error {
	batch := &pgx.Batch{}
	for _, c := range chunks {
		vec := pgvector.NewVector(c.Embedding)
		batch.Queue(
			`INSERT INTO chunks (id, study_id, content, embedding, page_num, chunk_index, token_count)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			uuid.New(), c.StudyID, c.Content, vec, c.PageNum, c.ChunkIndex, c.TokenCount,
		)
	}

	results := pg.Pool.SendBatch(ctx, batch)
	defer results.Close()

	for range chunks {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("insert chunk: %w", err)
		}
	}
	return nil
}

// ── Vector Search ──

// SemanticSearch runs a cosine-similarity search with optional domain filters,
// over-fetches a candidate pool, and re-ranks it by evidence strength before
// returning the top K results.
func (pg *Postgres) SemanticSearch(ctx context.Context, queryVec []float32, req domain.SearchRequest) ([]domain.SearchResult, error) {
	topK := req.TopK
	if topK <= 0 || topK > 50 {
		topK = 5
	}

	// Over-fetch so the evidence re-ranking has a meaningful pool to reorder.
	pool := topK * 5
	if pool < 25 {
		pool = 25
	}
	if pool > 100 {
		pool = 100
	}

	vec := pgvector.NewVector(queryVec)

	// $1 = query vector, $2 = pool limit; filter args are appended after.
	args := []any{vec, pool}
	var where []string

	if req.StudyID != nil {
		args = append(args, *req.StudyID)
		where = append(where, fmt.Sprintf("c.study_id = $%d", len(args)))
	}
	if len(req.StudyType) > 0 {
		args = append(args, req.StudyType) // []string -> text[]
		where = append(where, fmt.Sprintf("s.study_type = ANY($%d)", len(args)))
	}
	if t := strings.ToLower(strings.TrimSpace(req.Topic)); t != "" {
		args = append(args, t)
		where = append(where, fmt.Sprintf("s.topic = $%d", len(args)))
	}
	if req.MinYear != nil {
		args = append(args, *req.MinYear)
		where = append(where, fmt.Sprintf("s.year >= $%d", len(args)))
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT c.id, c.study_id, s.title, s.study_type, s.year, c.content,
		       c.page_num, c.chunk_index, 1 - (c.embedding <=> $1) AS score
		FROM chunks c JOIN studies s ON c.study_id = s.id
		%s
		ORDER BY c.embedding <=> $1
		LIMIT $2`, whereClause)

	rows, err := pg.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.SearchResult
	for rows.Next() {
		var r domain.SearchResult
		if err := rows.Scan(&r.ChunkID, &r.StudyID, &r.Title, &r.StudyType, &r.Year,
			&r.Content, &r.PageNum, &r.ChunkIndex, &r.Score); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Evidence-aware re-ranking: reorder the pool, then trim to top K.
	return ranking.Rerank(results, topK), nil
}

// KeywordSearch uses PostgreSQL full-text search as a comparison baseline.
func (pg *Postgres) KeywordSearch(ctx context.Context, query string, topK int) ([]domain.SearchResult, error) {
	if topK <= 0 || topK > 50 {
		topK = 5
	}

	rows, err := pg.Pool.Query(ctx,
		`SELECT c.id, c.study_id, s.title, s.study_type, s.year, c.content,
		        c.page_num, c.chunk_index, ts_rank(c.tsv, plainto_tsquery('english', $1)) AS score
		 FROM chunks c JOIN studies s ON c.study_id = s.id
		 WHERE c.tsv @@ plainto_tsquery('english', $1)
		 ORDER BY score DESC
		 LIMIT $2`, query, topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.SearchResult
	for rows.Next() {
		var r domain.SearchResult
		if err := rows.Scan(&r.ChunkID, &r.StudyID, &r.Title, &r.StudyType, &r.Year,
			&r.Content, &r.PageNum, &r.ChunkIndex, &r.Score); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// Ping checks the database connection.
func (pg *Postgres) Ping(ctx context.Context) error {
	return pg.Pool.Ping(ctx)
}

// GetStudyChunks returns all chunks for a study, including decoded embeddings.
func (pg *Postgres) GetStudyChunks(ctx context.Context, studyID uuid.UUID) ([]domain.Chunk, error) {
	rows, err := pg.Pool.Query(ctx,
		`SELECT id, study_id, content, embedding, page_num, chunk_index, token_count, created_at
		 FROM chunks WHERE study_id = $1 ORDER BY chunk_index`, studyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chunks []domain.Chunk
	for rows.Next() {
		var c domain.Chunk
		var vec pgvector.Vector
		if err := rows.Scan(&c.ID, &c.StudyID, &c.Content, &vec,
			&c.PageNum, &c.ChunkIndex, &c.TokenCount, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Embedding = vec.Slice()
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// RelatedStudies finds studies whose chunks are most similar to a query vector.
// Uses the best-matching-chunk-per-study strategy and excludes the source study.
func (pg *Postgres) RelatedStudies(ctx context.Context, vec []float32, topK int, excludeID uuid.UUID) ([]domain.RelatedStudy, error) {
	if topK <= 0 || topK > 20 {
		topK = 5
	}

	qvec := pgvector.NewVector(vec)

	rows, err := pg.Pool.Query(ctx, `
		SELECT
			c.study_id,
			s.title,
			s.study_type,
			s.year,
			s.chunk_count,
			1 - MIN(c.embedding <=> $1) AS score
		FROM chunks c
		JOIN studies s ON c.study_id = s.id
		WHERE c.study_id != $2
		  AND s.status = 'completed'
		GROUP BY c.study_id, s.title, s.study_type, s.year, s.chunk_count
		ORDER BY MIN(c.embedding <=> $1)
		LIMIT $3
	`, qvec, excludeID, topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []domain.RelatedStudy
	for rows.Next() {
		var r domain.RelatedStudy
		if err := rows.Scan(&r.StudyID, &r.Title, &r.StudyType, &r.Year, &r.ChunkCount, &r.Score); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (pg *Postgres) Close() {
	pg.Pool.Close()
}
