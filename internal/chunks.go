package moerise

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const ChunkVersion = "paragraph-v1-2048"
const chunkRunes = 2048

var paragraphBreak = regexp.MustCompile(`\r?\n[ \t]*\r?\n(?:[ \t]*\r?\n)*`)

type TextChunk struct {
	Ordinal     int
	ByteStart   int
	ByteEnd     int
	LineStart   int
	LineEnd     int
	ContentHash string
	Content     string
}

// Offsets refer to the original UTF-8 body: bytes are half-open, lines are one-based.
func ChunkText(content string) []TextChunk {
	chunks := []TextChunk{}
	start, line := 0, 1
	for start < len(content) {
		end, count := start, 0
		for end < len(content) && count < chunkRunes {
			_, size := utf8.DecodeRuneInString(content[end:])
			end += size
			count++
		}
		if end < len(content) {
			if breaks := paragraphBreak.FindAllStringIndex(content[start:end], -1); len(breaks) > 0 {
				end = start + breaks[len(breaks)-1][1]
			} else if last := strings.LastIndexByte(content[start:end], '\n'); last >= 0 {
				end = start + last + 1
			}
		}
		text := content[start:end]
		if strings.TrimSpace(text) != "" {
			lastLine := line + strings.Count(strings.TrimSuffix(text, "\n"), "\n")
			chunks = append(chunks, TextChunk{len(chunks), start, end, line, lastLine, hashBytes([]byte(text)), text})
		}
		line += strings.Count(text, "\n")
		start = end
	}
	return chunks
}
