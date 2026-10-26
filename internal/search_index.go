package moerise

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"log/slog"
	"math"
	"sync"
	"time"
)

type vectorEntry struct {
	Name        string
	ContentHash string
	Start       int
	End         int
}

type vectorMatrix struct {
	Space      string
	Revision   int64
	Dimensions int
	Entries    []vectorEntry
	Values     []float32
}

type SearchService struct {
	Store    *Store
	Source   Source
	Embedder Embedder
	indexMu  sync.Mutex
	cacheMu  sync.Mutex
	matrix   *vectorMatrix
	queries  chan struct{}
}

func NewSearchService(store *Store, source Source) *SearchService {
	s := &SearchService{Store: store, Source: source, queries: make(chan struct{}, 2)}
	if store.Settings.Embedding.Enabled() {
		s.Embedder = NewEmbeddingClient(store.Settings.Embedding)
	}
	return s
}

func (s *SearchService) spaceID() string {
	c := s.Store.Settings.Embedding
	return hashBytes(canonicalJSON([]any{s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User,
		c.Protocol, configured(c.BaseURL), configured(c.Model), c.Dimensions, chunkVersion}))
}

func (s *SearchService) verify(ctx context.Context) error {
	return NewRecordService(s.Store, s.Source).verify(ctx)
}

func (s *SearchService) live(ctx context.Context, name string) (*Memo, error) {
	memo, err := s.Source.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if memo != nil && (memo.Name != name || memo.Creator != s.Store.Settings.Memos.User) {
		memo = nil
	}
	return memo, s.Store.Refresh(name, memo)
}

func (s *SearchService) pending(space string, limit int) ([]Memo, error) {
	rows, err := s.Store.index.Query(`SELECT m.payload_json FROM memos m LEFT JOIN embedding_memos e
		ON e.space_id = ? AND e.name = m.name WHERE m.instance_id = ? AND m.user_name = ?
		AND (e.name IS NULL OR e.content_hash != m.content_hash) ORDER BY m.name LIMIT ?`,
		space, s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Memo{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		memo, err := decodeMemo(data)
		if err != nil {
			return nil, err
		}
		result = append(result, memo)
	}
	return result, rows.Err()
}

// IndexStep persists each completed memo. A restart resumes only missing content.
func (s *SearchService) IndexStep(ctx context.Context) (resultErr error) {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	config := s.Store.Settings.Embedding
	if !config.Enabled() || s.Embedder == nil {
		return nil
	}
	state, err := s.Store.Status()
	if err != nil || state.LastFull == nil {
		return err
	}
	if err := s.verify(ctx); err != nil {
		return err
	}
	space := s.spaceID()
	_, err = s.Store.index.Exec(`INSERT INTO embedding_spaces(id, model, chunk_version, status, updated_at)
		VALUES (?, ?, ?, 'building', ?) ON CONFLICT(id) DO NOTHING`, space, configured(config.Model), chunkVersion, nowText())
	if err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			_, _ = s.Store.index.Exec("UPDATE embedding_spaces SET last_error = ?, updated_at = ? WHERE id = ?", errorCode(resultErr), nowText(), space)
		}
	}()
	if err := transaction(s.Store.index, func(tx *sql.Tx) error {
		result, err := tx.Exec(`DELETE FROM embedding_memos WHERE space_id = ? AND name NOT IN
			(SELECT name FROM memos WHERE instance_id = ? AND user_name = ?)`, space,
			s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		_, err = tx.Exec("UPDATE embedding_spaces SET revision = revision + 1 WHERE id = ?", space)
		return err
	}); err != nil {
		return err
	}
	memos, err := s.pending(space, config.BatchSize)
	if err != nil {
		return err
	}
	for _, projected := range memos {
		memo, err := s.live(ctx, projected.Name)
		if err != nil {
			return err
		}
		if memo == nil {
			continue
		}
		chunks := chunkMemo(memo.Content)
		var count, oldCount, dimensions int
		if err := s.Store.index.QueryRow("SELECT dimensions FROM embedding_spaces WHERE id = ?", space).Scan(&dimensions); err != nil {
			return err
		}
		if err := s.Store.index.QueryRow("SELECT COUNT(*) FROM embedding_chunks WHERE space_id = ?", space).Scan(&count); err != nil {
			return err
		}
		if err := s.Store.index.QueryRow("SELECT COUNT(*) FROM embedding_chunks WHERE space_id = ? AND name = ?", space, memo.Name).Scan(&oldCount); err != nil {
			return err
		}
		if count-oldCount+len(chunks) > config.MaxChunks {
			return upstreamError("EMBEDDING_CAPACITY", "Embedding chunk limit reached; raise the configured limit or reduce the corpus")
		}
		vectors := [][]float32{}
		for offset := 0; offset < len(chunks); offset += config.BatchSize {
			texts := []string{}
			for _, chunk := range chunks[offset:min(offset+config.BatchSize, len(chunks))] {
				texts = append(texts, chunk.Text)
			}
			batch, err := s.Embedder.Embed(ctx, texts)
			if err != nil {
				return err
			}
			if len(batch) != len(texts) {
				return upstreamError("EMBEDDING_PROTOCOL", "Invalid embedding batch length")
			}
			for _, vector := range batch {
				if dimensions == 0 {
					dimensions = len(vector)
				}
				if config.Dimensions > 0 && dimensions != config.Dimensions {
					return upstreamError("EMBEDDING_PROTOCOL", "Configured embedding dimension differs from provider")
				}
				if err := normalizeVector(vector, dimensions); err != nil {
					return err
				}
				vectors = append(vectors, vector)
			}
		}
		if int64(count-oldCount+len(chunks))*int64(dimensions)*4 > maxVectorBytes {
			return upstreamError("EMBEDDING_CAPACITY", "Embedding matrix exceeds the 96 MiB memory budget")
		}
		// The model request may outlive an edit or deletion; publish only the version still visible.
		fresh, err := s.live(ctx, memo.Name)
		if err != nil {
			return err
		}
		if fresh == nil || fresh.ContentHash != memo.ContentHash {
			continue
		}
		if err := s.verify(ctx); err != nil {
			return err
		}
		if err := transaction(s.Store.index, func(tx *sql.Tx) error {
			var hash string
			if err := tx.QueryRow("SELECT content_hash FROM memos WHERE instance_id = ? AND user_name = ? AND name = ?",
				s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User, memo.Name).Scan(&hash); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				return err
			}
			if hash != memo.ContentHash {
				return nil
			}
			if _, err := tx.Exec("DELETE FROM embedding_memos WHERE space_id = ? AND name = ?", space, memo.Name); err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT INTO embedding_memos VALUES (?, ?, ?)", space, memo.Name, memo.ContentHash); err != nil {
				return err
			}
			for i, chunk := range chunks {
				if _, err := tx.Exec("INSERT INTO embedding_chunks VALUES (?, ?, ?, ?, ?, ?, ?)",
					space, memo.Name, i, chunk.Start, chunk.End, hashBytes([]byte(chunk.Text)), vectorBytes(vectors[i])); err != nil {
					return err
				}
			}
			_, err := tx.Exec("UPDATE embedding_spaces SET dimensions = ?, revision = revision + 1, last_error = NULL, updated_at = ? WHERE id = ?",
				dimensions, nowText(), space)
			return err
		}); err != nil {
			return err
		}
	}
	return transaction(s.Store.index, func(tx *sql.Tx) error {
		var remaining int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM memos m LEFT JOIN embedding_memos e ON e.space_id = ? AND e.name = m.name
			WHERE m.instance_id = ? AND m.user_name = ? AND (e.name IS NULL OR e.content_hash != m.content_hash)`,
			space, s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User).Scan(&remaining); err != nil {
			return err
		}
		if remaining != 0 {
			return nil
		}
		if _, err := tx.Exec("UPDATE embedding_spaces SET status = 'retired' WHERE status = 'active' AND id != ?", space); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE embedding_spaces SET status = 'active', last_error = NULL, updated_at = ? WHERE id = ?", nowText(), space); err != nil {
			return err
		}
		_, err := tx.Exec("DELETE FROM embedding_spaces WHERE id != ?", space)
		return err
	})
}

func (s *SearchService) loadMatrix() (vectorIndex, error) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	space := s.spaceID()
	var dimensions int
	var revision int64
	err := s.Store.index.QueryRow("SELECT dimensions, revision FROM embedding_spaces WHERE id = ? AND status = 'active'", space).Scan(&dimensions, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, upstreamError("SEARCH_NOT_READY", "The configured embedding index is still building")
	}
	if err != nil {
		return nil, err
	}
	if s.matrix != nil && s.matrix.Space == space && s.matrix.Revision == revision {
		return s.matrix, nil
	}
	result := &vectorMatrix{Space: space, Revision: revision, Dimensions: dimensions}
	var count int
	if err := s.Store.index.QueryRow("SELECT COUNT(*) FROM embedding_chunks WHERE space_id = ?", space).Scan(&count); err != nil {
		return nil, err
	}
	if dimensions < 0 || dimensions > 4096 || count > s.Store.Settings.Embedding.MaxChunks || int64(count)*int64(dimensions)*4 > maxVectorBytes {
		return nil, upstreamError("EMBEDDING_CAPACITY", "Embedding matrix exceeds configured capacity")
	}
	result.Entries = make([]vectorEntry, 0, count)
	result.Values = make([]float32, 0, count*dimensions)
	rows, err := s.Store.index.Query(`SELECT c.name, e.content_hash, c.start_rune, c.end_rune, c.vector
		FROM embedding_chunks c JOIN embedding_memos e ON c.space_id = e.space_id AND c.name = e.name
		WHERE c.space_id = ? ORDER BY c.name, c.position`, space)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var entry vectorEntry
		var data []byte
		if err := rows.Scan(&entry.Name, &entry.ContentHash, &entry.Start, &entry.End, &data); err != nil {
			return nil, err
		}
		if len(data) != dimensions*4 || dimensions < 1 || dimensions > 4096 || entry.Start < 0 || entry.End <= entry.Start ||
			(len(result.Values)+dimensions)*4 > maxVectorBytes || len(result.Entries) >= s.Store.Settings.Embedding.MaxChunks {
			return nil, upstreamError("EMBEDDING_CAPACITY", "Embedding matrix is invalid or exceeds configured capacity")
		}
		for i := 0; i < dimensions; i++ {
			value := math.Float32frombits(binary.LittleEndian.Uint32(data[4*i:]))
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return nil, upstreamError("EMBEDDING_PROTOCOL", "Stored embedding values are invalid")
			}
			result.Values = append(result.Values, value)
		}
		result.Entries = append(result.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.matrix = result
	return result, nil
}

func (s *SearchService) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if err := s.IndexStep(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("embedding indexing deferred", "code", errorCode(err))
				timer.Reset(30 * time.Second)
			} else {
				timer.Reset(time.Second)
			}
		}
	}
}

func (s *SearchService) IndexStatus() (SearchIndexStatus, error) {
	result := SearchIndexStatus{Enabled: s.Store.Settings.Embedding.Enabled(), ChunkVersion: chunkVersion}
	if !result.Enabled {
		result.Status = "disabled"
		return result, nil
	}
	result.Model, result.SpaceID = configured(s.Store.Settings.Embedding.Model), s.spaceID()
	err := s.Store.index.QueryRow("SELECT status, dimensions, last_error FROM embedding_spaces WHERE id = ?", result.SpaceID).
		Scan(&result.Status, &result.Dimensions, &result.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		result.Status = "pending"
	} else if err != nil {
		return result, err
	}
	if err := s.Store.index.QueryRow("SELECT COUNT(*) FROM embedding_chunks WHERE space_id = ?", result.SpaceID).Scan(&result.Chunks); err != nil {
		return result, err
	}
	err = s.Store.index.QueryRow(`SELECT COUNT(*) FROM memos m LEFT JOIN embedding_memos e ON e.space_id = ? AND e.name = m.name
		WHERE m.instance_id = ? AND m.user_name = ? AND (e.name IS NULL OR e.content_hash != m.content_hash)`,
		result.SpaceID, s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User).Scan(&result.PendingMemos)
	result.VectorBytes = int64(result.Chunks) * int64(result.Dimensions) * 4
	return result, err
}
