package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KevinH126/publift-evidence-search/internal/service"
)

// mockSidecar starts a fake embedding sidecar for testing.
func mockSidecar(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func happySidecar(t *testing.T, dim int) *httptest.Server {
	return mockSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Texts []string `json:"texts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
			return
		}

		embeddings := make([][]float32, len(req.Texts))
		for i := range req.Texts {
			vec := make([]float32, dim)
			vec[0] = float32(i) * 0.1
			embeddings[i] = vec
		}

		if err := json.NewEncoder(w).Encode(map[string]any{
			"embeddings": embeddings,
			"dimension":  dim,
		}); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	})
}

func TestEmbed_BasicBatch(t *testing.T) {
	srv := happySidecar(t, 384)
	embedder := service.NewEmbedder(srv.URL)

	texts := []string{"hello world", "foo bar", "test sentence"}
	embeddings, err := embedder.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(embeddings) != len(texts) {
		t.Errorf("got %d embeddings, want %d", len(embeddings), len(texts))
	}
	for i, emb := range embeddings {
		if len(emb) != 384 {
			t.Errorf("embedding %d: got dim %d, want 384", i, len(emb))
		}
	}
}

func TestEmbed_EmptyInput(t *testing.T) {
	srv := happySidecar(t, 384)
	embedder := service.NewEmbedder(srv.URL)

	embeddings, err := embedder.Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(embeddings) != 0 {
		t.Errorf("expected empty result, got %d embeddings", len(embeddings))
	}
}

func TestEmbed_SidecarError(t *testing.T) {
	srv := mockSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	})
	embedder := service.NewEmbedder(srv.URL)

	_, err := embedder.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Error("expected error from sidecar 500, got nil")
	}
}

func TestEmbed_LargeBatchSplitting(t *testing.T) {
	callCount := 0
	srv := mockSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req struct {
			Texts []string `json:"texts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
			return
		}

		embeddings := make([][]float32, len(req.Texts))
		for i := range req.Texts {
			embeddings[i] = make([]float32, 384)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"embeddings": embeddings,
			"dimension":  384,
		}); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	})

	embedder := service.NewEmbedder(srv.URL)

	// 65 texts should split into 2 batches (64 + 1)
	texts := make([]string, 65)
	for i := range texts {
		texts[i] = "sentence"
	}

	embeddings, err := embedder.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(embeddings) != 65 {
		t.Errorf("got %d embeddings, want 65", len(embeddings))
	}
	if callCount != 2 {
		t.Errorf("expected 2 sidecar calls for 65 texts, got %d", callCount)
	}
}

func TestHealth_OK(t *testing.T) {
	srv := mockSidecar(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(map[string]any{"status": "ok", "model_loaded": true}); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	})
	embedder := service.NewEmbedder(srv.URL)

	if err := embedder.Health(context.Background()); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHealth_Unreachable(t *testing.T) {
	embedder := service.NewEmbedder("http://localhost:19999")

	if err := embedder.Health(context.Background()); err == nil {
		t.Error("expected error for unreachable sidecar, got nil")
	}
}
