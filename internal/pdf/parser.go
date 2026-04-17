// Package pdf provides basic text extraction from PDF files using only
// Go's standard library. It handles FlateDecode (zlib) compressed streams
// and BT/ET text blocks. Image-only or encrypted PDFs return empty string.
package pdf

import (
	"bytes"
	"compress/zlib"
	"io"
	"regexp"
	"strings"
)

var (
	streamRe   = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	btEtRe     = regexp.MustCompile(`(?s)BT\b(.*?)\bET\b`)
	tjRe       = regexp.MustCompile(`\(([^)\\]*(?:\\.[^)\\]*)*)\)\s*Tj`)
	tjArrayRe  = regexp.MustCompile(`\[([^\]]+)\]\s*TJ`)
	tjItemRe   = regexp.MustCompile(`\(([^)\\]*(?:\\.[^)\\]*)*)\)`)
	spaceRe    = regexp.MustCompile(`\s{2,}`)
	newlineRe  = regexp.MustCompile(`\n{3,}`)
)

// ExtractText pulls readable text from raw PDF bytes.
// Works for text-based PDFs with unencrypted content streams.
func ExtractText(data []byte) string {
	if !bytes.HasPrefix(data, []byte("%PDF")) {
		return ""
	}

	var sb strings.Builder

	for _, m := range streamRe.FindAllSubmatch(data, -1) {
		body := m[1]

		// Try zlib decompression (FlateDecode filter)
		if dec := tryDecompress(body); dec != nil {
			body = dec
		}

		if text := extractTextBlocks(body); text != "" {
			sb.WriteString(text)
			sb.WriteByte('\n')
		}
	}

	result := sb.String()
	result = spaceRe.ReplaceAllString(result, " ")
	result = newlineRe.ReplaceAllString(result, "\n\n")
	return strings.TrimSpace(result)
}

func tryDecompress(data []byte) []byte {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		return nil
	}
	return out
}

func extractTextBlocks(data []byte) string {
	var sb strings.Builder

	for _, block := range btEtRe.FindAllSubmatch(data, -1) {
		content := string(block[1])

		// (text) Tj — single string
		for _, m := range tjRe.FindAllStringSubmatch(content, -1) {
			if t := decodePDFString(m[1]); strings.TrimSpace(t) != "" {
				sb.WriteString(t)
				sb.WriteByte(' ')
			}
		}

		// [(text) spacing ...] TJ — array of strings with kerning
		for _, m := range tjArrayRe.FindAllStringSubmatch(content, -1) {
			for _, item := range tjItemRe.FindAllStringSubmatch(m[1], -1) {
				if t := decodePDFString(item[1]); strings.TrimSpace(t) != "" {
					sb.WriteString(t)
				}
			}
			sb.WriteByte(' ')
		}
	}

	return strings.TrimSpace(sb.String())
}

// decodePDFString handles PDF string escape sequences.
func decodePDFString(s string) string {
	var sb strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case '(', ')', '\\':
				sb.WriteByte(s[i+1])
			default:
				sb.WriteByte(s[i+1])
			}
			i += 2
		} else {
			sb.WriteByte(s[i])
			i++
		}
	}
	return sb.String()
}
