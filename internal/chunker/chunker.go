package chunker

import (
	"strings"
	"unicode"
)

const (
	DefaultChunkSize    = 500  // target tokens per chunk
	DefaultChunkOverlap = 50   // overlap tokens between chunks
	ApproxCharsPerToken = 4    // rough approximation for English text
)

// ChunkResult holds a single chunk with metadata.
type ChunkResult struct {
	Content    string
	ChunkIndex int
	TokenCount int
}

// Chunk splits text into overlapping chunks.
// It tries to split on paragraph boundaries first, then sentences, then words.
func Chunk(text string, chunkSize, overlap int) []ChunkResult {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	if overlap <= 0 {
		overlap = DefaultChunkOverlap
	}
	if overlap >= chunkSize {
		overlap = chunkSize / 10
	}

	// Normalize whitespace
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	// Split into sentences as base units
	sentences := splitSentences(text)
	if len(sentences) == 0 {
		return nil
	}

	var chunks []ChunkResult
	chunkCharSize := chunkSize * ApproxCharsPerToken
	overlapCharSize := overlap * ApproxCharsPerToken

	var current strings.Builder
	currentLen := 0
	idx := 0

	for i := 0; i < len(sentences); i++ {
		sent := sentences[i]
		sentLen := len(sent)

		// If adding this sentence exceeds chunk size and we have content, finalize chunk
		if currentLen+sentLen > chunkCharSize && currentLen > 0 {
			chunks = append(chunks, ChunkResult{
				Content:    strings.TrimSpace(current.String()),
				ChunkIndex: idx,
				TokenCount: estimateTokens(current.String()),
			})
			idx++

			// Overlap: go back and find sentences that fit in overlap window
			current.Reset()
			currentLen = 0
			overlapStart := findOverlapStart(sentences, i, overlapCharSize)
			for j := overlapStart; j < i; j++ {
				current.WriteString(sentences[j])
				current.WriteString(" ")
				currentLen += len(sentences[j]) + 1
			}
		}

		current.WriteString(sent)
		current.WriteString(" ")
		currentLen += sentLen + 1
	}

	// Final chunk
	if currentLen > 0 {
		finalText := strings.TrimSpace(current.String())
		if finalText != "" {
			chunks = append(chunks, ChunkResult{
				Content:    finalText,
				ChunkIndex: idx,
				TokenCount: estimateTokens(finalText),
			})
		}
	}

	return chunks
}

// splitSentences splits text into sentences using basic punctuation rules.
func splitSentences(text string) []string {
	var sentences []string
	var current strings.Builder

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		current.WriteRune(runes[i])

		// Check for sentence-ending punctuation followed by space or end
		if isSentenceEnd(runes[i]) {
			if i == len(runes)-1 || unicode.IsSpace(runes[i+1]) || unicode.IsUpper(runes[i+1]) {
				sent := strings.TrimSpace(current.String())
				if sent != "" {
					sentences = append(sentences, sent)
				}
				current.Reset()
			}
		}
	}

	// Remaining text
	if remaining := strings.TrimSpace(current.String()); remaining != "" {
		sentences = append(sentences, remaining)
	}

	return sentences
}

func isSentenceEnd(r rune) bool {
	return r == '.' || r == '!' || r == '?' || r == '\n'
}

// findOverlapStart finds the earliest sentence index that fits within the overlap window,
// looking backwards from position `end`.
func findOverlapStart(sentences []string, end, overlapSize int) int {
	totalLen := 0
	start := end
	for j := end - 1; j >= 0; j-- {
		totalLen += len(sentences[j]) + 1
		if totalLen > overlapSize {
			break
		}
		start = j
	}
	return start
}

func estimateTokens(text string) int {
	// Rough estimate: ~4 chars per token for English
	charCount := 0
	for _, r := range text {
		if !unicode.IsSpace(r) {
			charCount++
		}
	}
	tokens := charCount / ApproxCharsPerToken
	if tokens == 0 && charCount > 0 {
		tokens = 1
	}
	return tokens
}
