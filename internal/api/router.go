package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/KevinH126/publift-evidence-search/internal/domain"
	"github.com/KevinH126/publift-evidence-search/internal/pdf"
)

const maxUploadSize = 10 << 20 // 10MB

// Server holds dependencies for the HTTP handlers.
type Server struct {
	pg       StudyStore
	redis    SearchCache
	embedder EmbedderService
	logger   *slog.Logger
}

// NewServer creates a new API server with all dependencies.
func NewServer(pg StudyStore, redis SearchCache, embedder EmbedderService, logger *slog.Logger) *Server {
	return &Server{pg: pg, redis: redis, embedder: embedder, logger: logger}
}

// Router builds the chi router with all routes and middleware.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(s.requestLogger)
	r.Use(s.rateLimiter)

	// Serve static demo frontend
	fileServer := http.FileServer(http.Dir("static"))
	r.Handle("/static/*", http.StripPrefix("/static/", fileServer))
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "static/index.html")
	})

	// API routes
	r.Route("/api/v1", func(r chi.Router) {
		// Studies
		r.Post("/studies", s.uploadStudy)
		r.Get("/studies", s.listStudies)
		r.Get("/studies/{id}", s.getStudy)
		r.Delete("/studies/{id}", s.deleteStudy)
		r.Get("/studies/{id}/summary", s.studySummary)
		r.Get("/studies/{id}/related", s.relatedStudies)

		// Search
		r.Post("/search", s.semanticSearch)
		r.Post("/search/keyword", s.keywordSearch)

		// Health
		r.Get("/health", s.healthCheck)
	})

	return r
}

// ── Handlers ──

func (s *Server) uploadStudy(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid file upload", err.Error())
		return
	}
	defer file.Close()

	// Validate file type
	ext := strings.ToLower(strings.TrimPrefix(header.Filename[strings.LastIndex(header.Filename, "."):], "."))
	if ext != "txt" && ext != "md" && ext != "pdf" {
		writeError(w, http.StatusBadRequest, "unsupported file type", "allowed: txt, md, pdf")
		return
	}

	// Read file content
	content, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read file", err.Error())
		return
	}

	// For PDFs, extract text here so the worker receives clean text
	rawText := string(content)
	if ext == "pdf" {
		rawText = pdf.ExtractText(content)
		if strings.TrimSpace(rawText) == "" {
			writeError(w, http.StatusUnprocessableEntity, "could not extract text from PDF",
				"file may be image-only or encrypted — try a text-based PDF")
			return
		}
	}

	// Build study record from the file + optional bibliographic form fields.
	study := parseStudyMetadata(r)
	study.Filename = header.Filename
	study.FileType = ext
	study.FileSize = header.Size
	if strings.TrimSpace(study.Title) == "" {
		study.Title = header.Filename // fall back to filename as the display title
	}

	if err := s.pg.CreateStudy(r.Context(), study); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create study", err.Error())
		return
	}

	// Enqueue processing job
	job := domain.Job{
		StudyID:  study.ID,
		RawText:  rawText,
		FileType: ext,
	}
	if err := s.redis.EnqueueJob(r.Context(), job); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enqueue job", err.Error())
		return
	}

	s.logger.Info("study uploaded", "id", study.ID, "title", study.Title,
		"study_type", study.StudyType, "size", study.FileSize)
	writeJSON(w, http.StatusAccepted, study)
}

// parseStudyMetadata pulls optional bibliographic fields from the multipart form.
// All fields are optional; sensible defaults are applied downstream.
func parseStudyMetadata(r *http.Request) *domain.Study {
	study := &domain.Study{
		Title:      strings.TrimSpace(r.FormValue("title")),
		Journal:    strings.TrimSpace(r.FormValue("journal")),
		DOI:        strings.TrimSpace(r.FormValue("doi")),
		StudyType:  normalizeEnum(r.FormValue("study_type")),
		Topic:      normalizeEnum(r.FormValue("topic")),
		Population: strings.TrimSpace(r.FormValue("population")),
		Authors:    splitList(r.FormValue("authors")),
	}
	if study.StudyType == "" {
		study.StudyType = "unknown"
	}
	if y := parseIntPtr(r.FormValue("year")); y != nil {
		study.Year = y
	}
	if n := parseIntPtr(r.FormValue("sample_size")); n != nil {
		study.SampleSize = n
	}
	return study
}

func normalizeEnum(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	// Authors may be given as "Last, First; Last, First", where ';' (or a
	// newline) separates authors and the comma is part of a single name. Only
	// fall back to splitting on ',' when no ';'/newline delimiter is present.
	sep := func(r rune) bool { return r == ';' || r == '\n' }
	if !strings.ContainsAny(v, ";\n") {
		sep = func(r rune) bool { return r == ',' || r == '\n' }
	}
	parts := strings.FieldsFunc(v, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func parseIntPtr(v string) *int {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		return &n
	}
	return nil
}

func (s *Server) getStudy(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid study ID", "")
		return
	}

	study, err := s.pg.GetStudy(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error", err.Error())
		return
	}
	if study == nil {
		writeError(w, http.StatusNotFound, "study not found", "")
		return
	}

	writeJSON(w, http.StatusOK, study)
}

func (s *Server) listStudies(w http.ResponseWriter, r *http.Request) {
	var cursor *uuid.UUID
	if c := r.URL.Query().Get("cursor"); c != "" {
		id, err := uuid.Parse(c)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor", "")
			return
		}
		cursor = &id
	}

	studies, err := s.pg.ListStudies(r.Context(), cursor, 20)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error", err.Error())
		return
	}

	if studies == nil {
		studies = []domain.Study{}
	}
	writeJSON(w, http.StatusOK, studies)
}

func (s *Server) deleteStudy(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid study ID", "")
		return
	}

	if err := s.pg.DeleteStudy(r.Context(), id); err != nil {
		if err.Error() == "study not found" {
			writeError(w, http.StatusNotFound, "study not found", "")
			return
		}
		writeError(w, http.StatusInternalServerError, "database error", err.Error())
		return
	}

	s.logger.Info("study deleted", "id", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) semanticSearch(w http.ResponseWriter, r *http.Request) {
	var req domain.SearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	if strings.TrimSpace(req.Query) == "" {
		writeError(w, http.StatusBadRequest, "query cannot be empty", "")
		return
	}
	if req.TopK <= 0 {
		req.TopK = 5
	}

	start := time.Now()

	// Only unfiltered queries are cacheable — the cache key is keyed on query
	// text + top_k, so caching a filtered result would leak across filter sets.
	cacheable := !req.HasFilters()

	if cacheable {
		cached, err := s.redis.GetCachedSearch(r.Context(), req.Query, req.TopK)
		if err != nil {
			s.logger.Warn("cache read error", "err", err)
		}
		if cached != nil {
			writeJSON(w, http.StatusOK, domain.SearchResponse{
				Query:   req.Query,
				Results: cached,
				Cached:  true,
				Latency: fmt.Sprintf("%.1fms", float64(time.Since(start).Microseconds())/1000),
			})
			return
		}
	}

	// Embed query
	embeddings, err := s.embedder.Embed(r.Context(), []string{req.Query})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "embedding service error", err.Error())
		return
	}

	// Search (with domain filters + evidence-aware re-ranking)
	results, err := s.pg.SemanticSearch(r.Context(), embeddings[0], req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search error", err.Error())
		return
	}

	if results == nil {
		results = []domain.SearchResult{}
	}

	if cacheable {
		if err := s.redis.CacheSearch(r.Context(), req.Query, req.TopK, results); err != nil {
			s.logger.Warn("cache write error", "err", err)
		}
	}

	writeJSON(w, http.StatusOK, domain.SearchResponse{
		Query:   req.Query,
		Results: results,
		Cached:  false,
		Latency: fmt.Sprintf("%.1fms", float64(time.Since(start).Microseconds())/1000),
	})
}

func (s *Server) keywordSearch(w http.ResponseWriter, r *http.Request) {
	var req domain.SearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	if strings.TrimSpace(req.Query) == "" {
		writeError(w, http.StatusBadRequest, "query cannot be empty", "")
		return
	}
	if req.TopK <= 0 {
		req.TopK = 5
	}

	start := time.Now()
	results, err := s.pg.KeywordSearch(r.Context(), req.Query, req.TopK)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search error", err.Error())
		return
	}

	if results == nil {
		results = []domain.SearchResult{}
	}

	writeJSON(w, http.StatusOK, domain.SearchResponse{
		Query:   req.Query,
		Results: results,
		Cached:  false,
		Latency: fmt.Sprintf("%.1fms", float64(time.Since(start).Microseconds())/1000),
	})
}

// relatedStudies finds studies semantically similar to a given study.
// It averages the source study's chunk embeddings, then queries for other
// studies whose chunks are most similar to that centroid.
func (s *Server) relatedStudies(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid study ID", "")
		return
	}

	study, err := s.pg.GetStudy(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error", err.Error())
		return
	}
	if study == nil {
		writeError(w, http.StatusNotFound, "study not found", "")
		return
	}
	if study.Status != "completed" {
		writeError(w, http.StatusConflict, "study not ready", "study must be in completed status")
		return
	}

	chunks, err := s.pg.GetStudyChunks(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chunks", err.Error())
		return
	}
	if len(chunks) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "study has no chunks", "")
		return
	}

	centroid := averageEmbeddings(chunks)

	topK := 5
	results, err := s.pg.RelatedStudies(r.Context(), centroid, topK, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search error", err.Error())
		return
	}
	if results == nil {
		results = []domain.RelatedStudy{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"source_study": study.Title,
		"related":      results,
	})
}

// studySummary returns the most representative chunks of a study as an
// extractive summary. It computes the centroid of all chunk embeddings and
// returns the top N chunks closest to that centroid.
func (s *Server) studySummary(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid study ID", "")
		return
	}

	study, err := s.pg.GetStudy(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error", err.Error())
		return
	}
	if study == nil {
		writeError(w, http.StatusNotFound, "study not found", "")
		return
	}
	if study.Status != "completed" {
		writeError(w, http.StatusConflict, "study not ready", "study must be in completed status")
		return
	}

	chunks, err := s.pg.GetStudyChunks(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chunks", err.Error())
		return
	}
	if len(chunks) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "study has no chunks", "")
		return
	}

	centroid := averageEmbeddings(chunks)
	topN := 3
	if len(chunks) < topN {
		topN = len(chunks)
	}

	summaryChunks := topChunksByCentroid(chunks, centroid, topN)

	writeJSON(w, http.StatusOK, domain.SummaryResponse{
		StudyID: id,
		Title:   study.Title,
		Chunks:  summaryChunks,
	})
}

func (s *Server) healthCheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	health := map[string]string{}

	if err := s.pg.Ping(ctx); err != nil {
		health["postgres"] = "unhealthy: " + err.Error()
	} else {
		health["postgres"] = "healthy"
	}

	if err := s.redis.Ping(ctx); err != nil {
		health["redis"] = "unhealthy: " + err.Error()
	} else {
		health["redis"] = "healthy"
	}

	if err := s.embedder.Health(ctx); err != nil {
		health["embedder"] = "unhealthy: " + err.Error()
	} else {
		health["embedder"] = "healthy"
	}

	health["api"] = "healthy"

	status := http.StatusOK
	for _, v := range health {
		if strings.HasPrefix(v, "unhealthy") {
			status = http.StatusServiceUnavailable
			break
		}
	}

	writeJSON(w, status, health)
}

// ── Middleware ──

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		s.logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration", time.Since(start).String(),
			"ip", r.RemoteAddr,
		)
	})
}

func (s *Server) rateLimiter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			next.ServeHTTP(w, r)
			return
		}

		ip := r.RemoteAddr
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			ip = strings.Split(forwarded, ",")[0]
		}

		allowed, err := s.redis.CheckRateLimit(r.Context(), ip)
		if err != nil {
			s.logger.Warn("rate limit check error", "err", err)
			next.ServeHTTP(w, r)
			return
		}

		if !allowed {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded", "60 requests per minute")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// ── Embedding helpers ──

// averageEmbeddings computes the element-wise mean of all chunk embeddings.
func averageEmbeddings(chunks []domain.Chunk) []float32 {
	if len(chunks) == 0 {
		return nil
	}
	dim := len(chunks[0].Embedding)
	centroid := make([]float32, dim)
	for _, c := range chunks {
		for i, v := range c.Embedding {
			centroid[i] += v
		}
	}
	n := float32(len(chunks))
	for i := range centroid {
		centroid[i] /= n
	}
	return centroid
}

// topChunksByCentroid picks the topN chunks with the highest cosine similarity
// to the centroid, returning them as SearchResult values sorted by score desc.
func topChunksByCentroid(chunks []domain.Chunk, centroid []float32, topN int) []domain.SearchResult {
	type scored struct {
		chunk domain.Chunk
		score float64
	}

	scores := make([]scored, len(chunks))
	for i, c := range chunks {
		scores[i] = scored{chunk: c, score: cosineSimilarity(c.Embedding, centroid)}
	}

	// Partial selection sort for topN (avoids full sort on large studies)
	for i := 0; i < topN && i < len(scores); i++ {
		best := i
		for j := i + 1; j < len(scores); j++ {
			if scores[j].score > scores[best].score {
				best = j
			}
		}
		scores[i], scores[best] = scores[best], scores[i]
	}

	results := make([]domain.SearchResult, topN)
	for i := 0; i < topN; i++ {
		c := scores[i].chunk
		results[i] = domain.SearchResult{
			ChunkID:    c.ID,
			StudyID:    c.StudyID,
			ChunkIndex: c.ChunkIndex,
			Content:    c.Content,
			PageNum:    c.PageNum,
			Score:      scores[i].score,
		}
	}
	return results
}

func cosineSimilarity(a, b []float32) float64 {
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// ── Helpers ──

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Default().Error("failed to encode JSON response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string, details string) {
	writeJSON(w, status, domain.ErrorResponse{
		Error:   msg,
		Code:    status,
		Details: details,
	})
}
