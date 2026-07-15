package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/KevinH126/publift-evidence-search/internal/api"
	"github.com/KevinH126/publift-evidence-search/internal/domain"
	"log/slog"
	"os"
)

// ── Mocks ──

type mockStore struct {
	studies map[uuid.UUID]*domain.Study
	chunks  map[uuid.UUID][]domain.Chunk
}

func newMockStore() *mockStore {
	return &mockStore{
		studies: make(map[uuid.UUID]*domain.Study),
		chunks:  make(map[uuid.UUID][]domain.Chunk),
	}
}

func (m *mockStore) Ping(_ context.Context) error { return nil }

func (m *mockStore) CreateStudy(_ context.Context, study *domain.Study) error {
	study.ID = uuid.New()
	study.Status = "pending"
	study.CreatedAt = time.Now()
	study.UpdatedAt = time.Now()
	m.studies[study.ID] = study
	return nil
}

func (m *mockStore) GetStudy(_ context.Context, id uuid.UUID) (*domain.Study, error) {
	study, ok := m.studies[id]
	if !ok {
		return nil, nil
	}
	return study, nil
}

func (m *mockStore) ListStudies(_ context.Context, _ *uuid.UUID, _ int) ([]domain.Study, error) {
	var studies []domain.Study
	for _, s := range m.studies {
		studies = append(studies, *s)
	}
	return studies, nil
}

func (m *mockStore) DeleteStudy(_ context.Context, id uuid.UUID) error {
	if _, ok := m.studies[id]; !ok {
		return nil
	}
	delete(m.studies, id)
	return nil
}

func (m *mockStore) SemanticSearch(_ context.Context, _ []float32, _ domain.SearchRequest) ([]domain.SearchResult, error) {
	return []domain.SearchResult{{
		ChunkID: uuid.New(), StudyID: uuid.New(), Title: "Test Study", StudyType: "rct",
		Content: "test content", Score: 0.95,
	}}, nil
}

func (m *mockStore) KeywordSearch(_ context.Context, _ string, _ int) ([]domain.SearchResult, error) {
	return []domain.SearchResult{{
		ChunkID: uuid.New(), StudyID: uuid.New(), Title: "Test Study", StudyType: "rct",
		Content: "keyword match", Score: 0.7,
	}}, nil
}

func (m *mockStore) GetStudyChunks(_ context.Context, studyID uuid.UUID) ([]domain.Chunk, error) {
	return m.chunks[studyID], nil
}

func (m *mockStore) RelatedStudies(_ context.Context, _ []float32, _ int, _ uuid.UUID) ([]domain.RelatedStudy, error) {
	return []domain.RelatedStudy{}, nil
}

type mockCache struct{}

func (m *mockCache) Ping(_ context.Context) error { return nil }
func (m *mockCache) GetCachedSearch(_ context.Context, _ string, _ int) ([]domain.SearchResult, error) {
	return nil, nil // always cache miss
}
func (m *mockCache) CacheSearch(_ context.Context, _ string, _ int, _ []domain.SearchResult) error {
	return nil
}
func (m *mockCache) CheckRateLimit(_ context.Context, _ string) (bool, error) { return true, nil }
func (m *mockCache) EnqueueJob(_ context.Context, _ domain.Job) error         { return nil }

type mockEmbedder struct{}

func (m *mockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	embeddings := make([][]float32, len(texts))
	for i := range texts {
		embeddings[i] = make([]float32, 384)
		embeddings[i][0] = 0.1
	}
	return embeddings, nil
}
func (m *mockEmbedder) Health(_ context.Context) error { return nil }

// ── Test helpers ──

func newTestServer(t *testing.T) *api.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return api.NewServer(newMockStore(), &mockCache{}, &mockEmbedder{}, logger)
}

// ── Tests ──

func TestHealthCheck(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rr := httptest.NewRecorder()

	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["api"] != "healthy" {
		t.Errorf("expected api=healthy, got %q", body["api"])
	}
}

func TestUploadStudy(t *testing.T) {
	srv := newTestServer(t)

	tests := []struct {
		name       string
		filename   string
		content    string
		wantStatus int
	}{
		{"txt file accepted", "test.txt", "hello world", http.StatusAccepted},
		{"md file accepted", "readme.md", "# Hello", http.StatusAccepted},
		{"unsupported type rejected", "file.exe", "binary", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := multipart.NewWriter(&buf)
			fw, err := w.CreateFormFile("file", tt.filename)
			if err != nil {
				t.Fatalf("failed to create form file: %v", err)
			}
			if _, err := fw.Write([]byte(tt.content)); err != nil {
				t.Fatalf("failed to write form file: %v", err)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("failed to close writer: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/studies", &buf)
			req.Header.Set("Content-Type", w.FormDataContentType())
			rr := httptest.NewRecorder()

			srv.Router().ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("expected %d, got %d: %s", tt.wantStatus, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestUploadStudyWithMetadata(t *testing.T) {
	srv := newTestServer(t)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "schoenfeld2017.txt")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	if _, err := fw.Write([]byte("training volume and hypertrophy")); err != nil {
		t.Fatalf("failed to write form file: %v", err)
	}
	if err := w.WriteField("title", "Dose-response of resistance training volume"); err != nil {
		t.Fatalf("failed to write field: %v", err)
	}
	if err := w.WriteField("study_type", "Meta-Analysis"); err != nil {
		t.Fatalf("failed to write field: %v", err)
	}
	if err := w.WriteField("topic", "Hypertrophy"); err != nil {
		t.Fatalf("failed to write field: %v", err)
	}
	if err := w.WriteField("authors", "Schoenfeld, B.; Ogborn, D."); err != nil {
		t.Fatalf("failed to write field: %v", err)
	}
	if err := w.WriteField("year", "2017"); err != nil {
		t.Fatalf("failed to write field: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("failed to close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/studies", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rr := httptest.NewRecorder()

	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	var study domain.Study
	if err := json.NewDecoder(rr.Body).Decode(&study); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if study.StudyType != "meta-analysis" {
		t.Errorf("study_type should be normalized to lowercase, got %q", study.StudyType)
	}
	if study.Topic != "hypertrophy" {
		t.Errorf("topic should be normalized, got %q", study.Topic)
	}
	if len(study.Authors) != 2 {
		t.Errorf("expected 2 authors, got %v", study.Authors)
	}
	if study.Year == nil || *study.Year != 2017 {
		t.Errorf("expected year 2017, got %v", study.Year)
	}
}

func TestGetStudyNotFound(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/studies/"+uuid.New().String(), nil)
	rr := httptest.NewRecorder()

	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestGetStudyInvalidID(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/studies/not-a-uuid", nil)
	rr := httptest.NewRecorder()

	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestListStudies(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/studies", nil)
	rr := httptest.NewRecorder()

	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestSemanticSearch(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"valid query", `{"query":"does training to failure matter for hypertrophy","top_k":5}`, http.StatusOK},
		{"query with filters", `{"query":"protein intake","study_type":["meta-analysis"],"min_year":2015}`, http.StatusOK},
		{"empty query rejected", `{"query":"","top_k":5}`, http.StatusBadRequest},
		{"invalid json rejected", `{bad json}`, http.StatusBadRequest},
		{"default top_k applied", `{"query":"test"}`, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/search",
				bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()

			srv.Router().ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("%s: expected %d, got %d: %s", tt.name, tt.wantStatus, rr.Code, rr.Body.String())
			}

			if tt.wantStatus == http.StatusOK {
				var resp domain.SearchResponse
				if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}
				if resp.Query == "" {
					t.Error("response query should not be empty")
				}
				if resp.Results == nil {
					t.Error("results should not be nil")
				}
			}
		})
	}
}

func TestKeywordSearch(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/search/keyword",
		bytes.NewBufferString(`{"query":"training volume","top_k":3}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp domain.SearchResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Cached {
		t.Error("keyword search should never be cached")
	}
}

func TestDeleteStudy(t *testing.T) {
	srv := newTestServer(t)

	// Delete non-existent study should still return 204 (idempotent)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/studies/"+uuid.New().String(), nil)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rr.Code)
	}
}

func TestSearchResponseShape(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/search",
		bytes.NewBufferString(`{"query":"test query","top_k":3}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.Router().ServeHTTP(rr, req)

	var resp domain.SearchResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Latency == "" {
		t.Error("latency field should be populated")
	}
	for _, r := range resp.Results {
		if r.Score < 0 || r.Score > 1.01 {
			t.Errorf("score %f out of range [0,1]", r.Score)
		}
	}
}
