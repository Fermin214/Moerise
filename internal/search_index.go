package moerise

import (
	"context"
	"database/sql"
	"encoding/hex"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Han bigrams retain local word order; unigrams provide a separate one-character path.
func searchTerms(text string, document bool) []string {
	terms := []string{}
	runes := []rune(strings.ToLower(text))
	for start := 0; start < len(runes); {
		end := start + 1
		if unicode.Is(unicode.Han, runes[start]) {
			for end < len(runes) && unicode.Is(unicode.Han, runes[end]) {
				end++
			}
			for i := start; i < end; i++ {
				if document || end-start == 1 {
					terms = append(terms, string(runes[i]))
				}
				if i+1 < end {
					terms = append(terms, string(runes[i:i+2]))
				}
			}
		} else if unicode.IsLetter(runes[start]) || unicode.IsDigit(runes[start]) {
			for end < len(runes) && !unicode.Is(unicode.Han, runes[end]) &&
				(unicode.IsLetter(runes[end]) || unicode.IsDigit(runes[end])) {
				end++
			}
			terms = append(terms, string(runes[start:end]))
		}
		start = end
	}
	// Hex keeps the FTS tokenizer from splitting or normalizing our own tokens.
	for i, term := range terms {
		terms[i] = "t" + hex.EncodeToString([]byte(term))
	}
	return terms
}

func replaceSearchMemo(tx *sql.Tx, settings Settings, memo Memo) error {
	args := []any{settings.Memos.InstanceID, settings.Memos.User, memo.Name}
	if _, err := tx.Exec("DELETE FROM search_notes WHERE instance_id = ? AND user_name = ? AND name = ?", args...); err != nil {
		return err
	}
	if memo.Creator != settings.Memos.User {
		return nil
	}
	if _, err := tx.Exec("INSERT INTO search_notes VALUES (?, ?, ?, ?)", append(args, memo.SourceHash)...); err != nil {
		return err
	}
	chunks := ChunkText(memo.Content)
	for i, chunk := range chunks {
		terms := searchTerms(chunk.Content, true)
		// Index a Han bigram crossing a split on both sides without moving source offsets.
		for _, boundary := range []int{i - 1, i} {
			if boundary < 0 || boundary+1 >= len(chunks) || chunks[boundary].ByteEnd != chunks[boundary+1].ByteStart {
				continue
			}
			left, _ := utf8.DecodeLastRuneInString(chunks[boundary].Content)
			right, _ := utf8.DecodeRuneInString(chunks[boundary+1].Content)
			if unicode.Is(unicode.Han, left) && unicode.Is(unicode.Han, right) {
				terms = append(terms, searchTerms(string([]rune{left, right}), false)...)
			}
		}
		if _, err := tx.Exec(`INSERT INTO search_chunks
			(instance_id, user_name, name, ordinal, byte_start, byte_end, line_start, line_end, content, tokens)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, settings.Memos.InstanceID, settings.Memos.User,
			memo.Name, chunk.Ordinal, chunk.ByteStart, chunk.ByteEnd, chunk.LineStart, chunk.LineEnd,
			chunk.Content, strings.Join(terms, " ")); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) prepareSearchIndex() error {
	return transaction(s.index, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT m.payload_json FROM memos m LEFT JOIN search_notes n
			USING (instance_id, user_name, name) WHERE m.instance_id = ? AND m.user_name = ?
			AND (n.source_hash IS NULL OR n.source_hash != m.source_hash)`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User)
		if err != nil {
			return err
		}
		var memos []Memo
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				rows.Close()
				return err
			}
			memo, err := decodeMemo(data)
			if err != nil {
				rows.Close()
				return err
			}
			memos = append(memos, memo)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, memo := range memos {
			if err := replaceSearchMemo(tx, s.Settings, memo); err != nil {
				return err
			}
		}
		return nil
	})
}

type searchCandidate struct {
	name, sourceHash string
	chunk            TextChunk
	score            float64
	signals          []string
}

const searchCandidates = 100

func searchFilter(settings Settings, request SearchRequest) (string, []any) {
	where := "m.instance_id = ? AND m.user_name = ?"
	args := []any{settings.Memos.InstanceID, settings.Memos.User}
	if request.State != "ALL" {
		where += " AND m.state = ?"
		args = append(args, request.State)
	}
	for _, tag := range request.Tags {
		where += " AND EXISTS (SELECT 1 FROM json_each(m.payload_json, '$.tags') WHERE value = ?)"
		args = append(args, tag)
	}
	return where, args
}

func (s *Store) lexicalCandidates(ctx context.Context, request SearchRequest, tags bool) ([]searchCandidate, error) {
	where, args := searchFilter(s.Settings, request)
	from, order := "", ""
	if tags {
		words := slices.Compact(strings.Fields(strings.ToLower(request.Query)))
		conditions := []string{}
		for _, word := range words {
			word = strings.TrimPrefix(word, "#")
			if word != "" {
				conditions = append(conditions, "instr(lower(value), ?) > 0")
				args = append(args, word)
			}
		}
		if len(conditions) == 0 {
			return nil, nil
		}
		where += " AND EXISTS (SELECT 1 FROM json_each(m.payload_json, '$.tags') WHERE " + strings.Join(conditions, " OR ") + ")"
		order = "m.name, c.ordinal"
	} else {
		terms := searchTerms(request.Query, false)
		slices.Sort(terms)
		terms = slices.Compact(terms)
		if len(terms) == 0 {
			return nil, nil
		}
		from = " JOIN search_fts ON search_fts.rowid = c.id"
		where += " AND search_fts MATCH ?"
		args = append(args, `"`+strings.Join(terms, `" OR "`)+`"`)
		order = "bm25(search_fts), m.name, c.ordinal"
	}
	return s.rankedSearchCandidates(ctx, from, where, order, args)
}

func (s *Store) rankedSearchCandidates(ctx context.Context, from, where, order string, args []any) ([]searchCandidate, error) {
	rows, err := s.index.QueryContext(ctx, `SELECT c.name, m.source_hash, c.ordinal,
		c.byte_start, c.byte_end, c.line_start, c.line_end, c.content
		FROM search_chunks c JOIN memos m USING (instance_id, user_name, name)`+from+
		" WHERE "+where+" ORDER BY "+order+" LIMIT 100", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []searchCandidate{}
	for rows.Next() {
		var item searchCandidate
		if err := rows.Scan(&item.name, &item.sourceHash, &item.chunk.Ordinal, &item.chunk.ByteStart,
			&item.chunk.ByteEnd, &item.chunk.LineStart, &item.chunk.LineEnd, &item.chunk.Content); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
