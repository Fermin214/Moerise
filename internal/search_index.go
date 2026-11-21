package moerise

import (
	"context"
	"database/sql"
	"encoding/hex"
	"slices"
	"strings"
	"time"
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
		where += " AND EXISTS (SELECT 1 FROM memo_tags mt WHERE mt.instance_id = m.instance_id AND mt.user_name = m.user_name AND mt.memo_name = m.name AND mt.tag = ?)"
		args = append(args, tag)
	}
	for _, bound := range []struct {
		value *time.Time
		op    string
	}{{request.createdFrom, ">="}, {request.createdUntil, "<"}} {
		if bound.value == nil {
			continue
		}
		where += " AND (julianday(substr(m.create_time, 1, 19)), " +
			"CAST(substr(m.create_time, 21, 6) AS INTEGER)) " + bound.op + " (julianday(?), ?)"
		args = append(args, bound.value.UTC().Truncate(time.Second).Format(time.RFC3339),
			(bound.value.Nanosecond()+999)/1000)
	}
	return where, args
}

func (s *Store) lexicalCandidates(ctx context.Context, request SearchRequest, tags bool) ([]searchCandidate, error) {
	return s.lexicalCandidatesWithLimit(ctx, request, tags, 100)
}

func searchKeywordSpec(request SearchRequest) ([]string, []string) {
	terms := searchTerms(request.Query, false)
	slices.Sort(terms)
	terms = slices.Compact(terms)
	words := slices.Compact(strings.Fields(strings.ToLower(request.Query)))
	conditions := make([]string, 0, len(words))
	for _, word := range words {
		word = strings.TrimPrefix(word, "#")
		if word != "" {
			conditions = append(conditions, word)
		}
	}
	return terms, conditions
}

func (s *Store) lexicalCandidatesWithLimit(ctx context.Context, request SearchRequest, tags bool, limit int) ([]searchCandidate, error) {
	where, args := searchFilter(s.Settings, request)
	terms, tagWords := searchKeywordSpec(request)
	from, order := "", ""
	if tags {
		conditions := make([]string, 0, len(tagWords))
		for _, word := range tagWords {
			conditions = append(conditions, "instr(lower(mt.tag), ?) > 0")
			args = append(args, word)
		}
		if len(conditions) == 0 {
			return nil, nil
		}
		where += " AND EXISTS (SELECT 1 FROM memo_tags mt WHERE mt.instance_id = m.instance_id AND mt.user_name = m.user_name AND mt.memo_name = m.name AND (" + strings.Join(conditions, " OR ") + "))"
		order = "m.name, c.ordinal"
	} else {
		if len(terms) == 0 {
			return nil, nil
		}
		from = " JOIN search_fts ON search_fts.rowid = c.id"
		where += " AND search_fts MATCH ?"
		args = append(args, `"`+strings.Join(terms, `" OR "`)+`"`)
		order = "bm25(search_fts), m.name, c.ordinal"
	}
	return s.rankedSearchCandidatesLimit(ctx, from, where, order, args, limit)
}

func (s *Store) searchKeywordNotesPage(ctx context.Context, request SearchRequest, limit, offset int) ([]clientSearchSource, error) {
	where, baseArgs := searchFilter(s.Settings, request)
	terms, tagWords := searchKeywordSpec(request)
	if len(terms) == 0 && len(tagWords) == 0 {
		return nil, nil
	}
	var ctes []string
	var union []string
	var args []any
	if len(terms) > 0 {
		ftsArgs := append([]any{`"` + strings.Join(terms, `" OR "`) + `"`}, baseArgs...)
		ctes = append(ctes,
			`fts_raw AS MATERIALIZED (SELECT m.name AS name, m.source_hash AS source_hash, bm25(search_fts) AS bm FROM search_fts JOIN search_chunks c ON c.id = search_fts.rowid JOIN memos m USING (instance_id, user_name, name) WHERE search_fts MATCH ? AND `+where+`)`,
			`fts_notes AS (SELECT name, source_hash, MIN(bm) AS best_bm FROM fts_raw GROUP BY name, source_hash)`,
			`fts_ranked AS (SELECT name, source_hash, 1.0 / (60.0 + ROW_NUMBER() OVER (ORDER BY best_bm, name)) AS score FROM fts_notes)`)
		union = append(union, `SELECT name, source_hash, score FROM fts_ranked`)
		args = append(args, ftsArgs...)
	}
	if len(tagWords) > 0 {
		tagConds := make([]string, 0, len(tagWords))
		tagArgs := append([]any{}, baseArgs...)
		for _, word := range tagWords {
			tagConds = append(tagConds, "instr(lower(mt.tag), ?) > 0")
			tagArgs = append(tagArgs, word)
		}
		ctes = append(ctes,
			`tag_notes AS (SELECT m.name AS name, m.source_hash AS source_hash FROM memos m WHERE `+where+` AND EXISTS (SELECT 1 FROM memo_tags mt WHERE mt.instance_id = m.instance_id AND mt.user_name = m.user_name AND mt.memo_name = m.name AND (`+strings.Join(tagConds, " OR ")+`)))`,
			`tag_ranked AS (SELECT name, source_hash, 1.0 / (60.0 + ROW_NUMBER() OVER (ORDER BY name)) AS score FROM tag_notes)`)
		union = append(union, `SELECT name, source_hash, score FROM tag_ranked`)
		args = append(args, tagArgs...)
	}
	ctes = append(ctes, `combined AS (`+strings.Join(union, " UNION ALL ")+`)`)
	query := `WITH ` + strings.Join(ctes, ", ") + `
		SELECT name, source_hash FROM (SELECT name, source_hash, SUM(score) AS total_score FROM combined GROUP BY name, source_hash)
		ORDER BY total_score DESC, name ASC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.indexReader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := []clientSearchSource{}
	for rows.Next() {
		var item clientSearchSource
		if err := rows.Scan(&item.Name, &item.Hash); err != nil {
			return nil, err
		}
		sources = append(sources, item)
	}
	return sources, rows.Err()
}

func (s *Store) rankedSearchCandidates(ctx context.Context, from, where, order string, args []any) ([]searchCandidate, error) {
	return s.rankedSearchCandidatesLimit(ctx, from, where, order, args, 100)
}

func (s *Store) rankedSearchCandidatesLimit(ctx context.Context, from, where, order string, args []any, limit int) ([]searchCandidate, error) {
	args = append(args, limit)
	rows, err := s.indexReader().QueryContext(ctx, `SELECT c.name, m.source_hash, c.ordinal,
		c.byte_start, c.byte_end, c.line_start, c.line_end, c.content
		FROM search_chunks c JOIN memos m USING (instance_id, user_name, name)`+from+
		" WHERE "+where+" ORDER BY "+order+" LIMIT ?", args...)
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
