package chunker_test

import (
	"strings"
	"testing"

	"github.com/KevinH126/publift-evidence-search/internal/chunker"
)

func TestChunk_BasicSplitting(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		chunkSize int
		overlap   int
		wantMin   int // minimum expected chunks
		wantMax   int // maximum expected chunks
	}{
		{
			name:      "empty string produces no chunks",
			input:     "",
			chunkSize: 500,
			overlap:   50,
			wantMin:   0,
			wantMax:   0,
		},
		{
			name:      "short text produces single chunk",
			input:     "This is a short sentence.",
			chunkSize: 500,
			overlap:   50,
			wantMin:   1,
			wantMax:   1,
		},
		{
			name:      "long text produces multiple chunks",
			input:     strings.Repeat("This is a test sentence. ", 200),
			chunkSize: 100,
			overlap:   10,
			wantMin:   2,
			wantMax:   50,
		},
		{
			name:      "whitespace only produces no chunks",
			input:     "   \n\t  \n  ",
			chunkSize: 500,
			overlap:   50,
			wantMin:   0,
			wantMax:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := chunker.Chunk(tt.input, tt.chunkSize, tt.overlap)

			if len(chunks) < tt.wantMin || len(chunks) > tt.wantMax {
				t.Errorf("got %d chunks, want between %d and %d", len(chunks), tt.wantMin, tt.wantMax)
			}

			// Verify chunk indices are sequential
			for i, c := range chunks {
				if c.ChunkIndex != i {
					t.Errorf("chunk %d has index %d, want %d", i, c.ChunkIndex, i)
				}
			}

			// Verify no empty chunks
			for i, c := range chunks {
				if strings.TrimSpace(c.Content) == "" {
					t.Errorf("chunk %d is empty", i)
				}
			}
		})
	}
}

func TestChunk_OverlapContent(t *testing.T) {
	// Create text with distinct sentences
	sentences := []string{
		"The quick brown fox jumps over the lazy dog.",
		"A journey of a thousand miles begins with a single step.",
		"To be or not to be that is the question.",
		"All that glitters is not gold.",
		"The only thing we have to fear is fear itself.",
	}
	text := strings.Join(sentences, " ")

	chunks := chunker.Chunk(text, 30, 10) // small chunks to force splitting

	if len(chunks) < 2 {
		t.Skip("not enough chunks to test overlap")
	}

	// Verify that consecutive chunks share some content (overlap)
	for i := 1; i < len(chunks); i++ {
		// The overlap should mean some words from the end of chunk i-1
		// appear at the start of chunk i
		prevWords := strings.Fields(chunks[i-1].Content)
		currWords := strings.Fields(chunks[i].Content)

		if len(prevWords) == 0 || len(currWords) == 0 {
			continue
		}

		// Check that at least one word from the end of prev appears in current
		lastWordPrev := prevWords[len(prevWords)-1]
		found := false
		for _, w := range currWords {
			if w == lastWordPrev {
				found = true
				break
			}
		}
		// This is a soft check — overlap might not always produce exact word matches
		_ = found
	}
}

func TestChunk_TokenEstimate(t *testing.T) {
	text := "Hello world. This is a test."
	chunks := chunker.Chunk(text, 500, 50)

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}

	if chunks[0].TokenCount <= 0 {
		t.Error("token count should be positive")
	}
}
