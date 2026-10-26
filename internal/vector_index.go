package moerise

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strings"
	"time"
)

type VectorGeneration struct {
	ID                 string  `json:"id"`
	SpaceHash          string  `json:"space_hash"`
	Model              string  `json:"model"`
	Dimensions         int     `json:"dimensions"`
	ChunkVersion       string  `json:"chunk_version"`
	ProjectionRevision int64   `json:"projection_revision"`
	Notes              int     `json:"notes"`
	Chunks             int     `json:"chunks"`
	CompletedChunks    int     `json:"completed_chunks"`
	LastError          *string `json:"last_error"`
}

type VectorIndexStatus struct {
	ProjectionRevision int64             `json:"projection_revision"`
	Ready              bool              `json:"ready"`
	Active             *VectorGeneration `json:"active"`
	Building           *VectorGeneration `json:"building"`
}

func embeddingSpace(config EmbeddingConfig) string {
	data, _ := json.Marshal([]any{config.Provider, config.Protocol,
		strings.TrimRight(configured(config.BaseURL), "/"), configured(config.Model), config.Dimensions, ChunkVersion})
	return hashBytes(data)
}

func (s *Store) VectorStatus() (*VectorIndexStatus, error) {
	result := &VectorIndexStatus{}
	// One read transaction prevents combining counts and pointers from different publications.
	err := transaction(s.index, func(tx *sql.Tx) error {
		if err := tx.QueryRow("SELECT revision FROM projection_meta WHERE instance_id = ?", s.Settings.Memos.InstanceID).
			Scan(&result.ProjectionRevision); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT g.id, g.space_hash, g.model, g.dimensions, g.chunk_version,
			g.projection_revision, g.last_error, g.status,
			(SELECT COUNT(*) FROM vector_notes n WHERE n.generation_id = g.id),
			(SELECT COUNT(*) FROM vector_chunks c WHERE c.generation_id = g.id),
			(SELECT COUNT(*) FROM vector_chunks c WHERE c.generation_id = g.id AND c.vector IS NOT NULL)
			FROM vector_generations g WHERE g.instance_id = ? AND g.user_name = ?`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var generation VectorGeneration
			var status string
			if err := rows.Scan(&generation.ID, &generation.SpaceHash, &generation.Model, &generation.Dimensions,
				&generation.ChunkVersion, &generation.ProjectionRevision, &generation.LastError, &status,
				&generation.Notes, &generation.Chunks, &generation.CompletedChunks); err != nil {
				return err
			}
			if status == "active" {
				result.Active = &generation
			} else {
				result.Building = &generation
			}
		}
		return rows.Err()
	})
	result.Ready = s.Settings.Embedding.Enabled && result.Active != nil &&
		result.Active.SpaceHash == embeddingSpace(s.Settings.Embedding) &&
		result.Active.ProjectionRevision == result.ProjectionRevision
	return result, err
}

type VectorIndexService struct {
	Store    *Store
	Source   Source
	Provider EmbeddingProvider
}

func NewVectorIndexService(store *Store, source Source, provider EmbeddingProvider) *VectorIndexService {
	return &VectorIndexService{Store: store, Source: source, Provider: provider}
}

// prepare snapshots a complete projection and reuses vectors only within the same document space.
func (s *VectorIndexService) prepare() (string, error) {
	settings, config := s.Store.Settings, s.Store.Settings.Embedding
	space, id := embeddingSpace(config), ""
	err := transaction(s.Store.index, func(tx *sql.Tx) error {
		var revision int64
		if err := tx.QueryRow("SELECT revision FROM projection_meta WHERE instance_id = ?", settings.Memos.InstanceID).Scan(&revision); err != nil {
			return err
		}
		var activeSpace string
		var activeRevision int64
		err := tx.QueryRow(`SELECT space_hash, projection_revision FROM vector_generations
			WHERE instance_id = ? AND user_name = ? AND status = 'active'`,
			settings.Memos.InstanceID, settings.Memos.User).Scan(&activeSpace, &activeRevision)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && activeSpace == space && activeRevision == revision {
			_, err := tx.Exec("DELETE FROM vector_generations WHERE instance_id = ? AND user_name = ? AND status = 'building'",
				settings.Memos.InstanceID, settings.Memos.User)
			return err
		}
		var buildingSpace string
		var buildingRevision int64
		err = tx.QueryRow(`SELECT id, space_hash, projection_revision FROM vector_generations
			WHERE instance_id = ? AND user_name = ? AND status = 'building'`,
			settings.Memos.InstanceID, settings.Memos.User).Scan(&id, &buildingSpace, &buildingRevision)
		if err == nil && buildingSpace == space && buildingRevision == revision {
			return nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec("DELETE FROM vector_generations WHERE instance_id = ? AND user_name = ? AND status = 'building'",
			settings.Memos.InstanceID, settings.Memos.User); err != nil {
			return err
		}
		id = newID()
		if _, err := tx.Exec(`INSERT INTO vector_generations
			(id, instance_id, user_name, space_hash, provider, protocol, base_url, model, dimensions,
			chunk_version, projection_revision, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'building', ?)`,
			id, settings.Memos.InstanceID, settings.Memos.User, space, config.Provider, config.Protocol,
			strings.TrimRight(configured(config.BaseURL), "/"), configured(config.Model), config.Dimensions,
			ChunkVersion, revision, nowText()); err != nil {
			return err
		}
		rows, err := tx.Query("SELECT payload_json FROM memos WHERE instance_id = ? AND user_name = ? ORDER BY name",
			settings.Memos.InstanceID, settings.Memos.User)
		if err != nil {
			return err
		}
		// Close the cursor before inserting on the same SQLite connection.
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
			if memo.Creator != settings.Memos.User {
				continue
			}
			if _, err := tx.Exec("INSERT INTO vector_notes VALUES (?, ?, ?, ?)", id, memo.Name, memo.SourceHash, memo.ContentHash); err != nil {
				return err
			}
			for _, chunk := range ChunkText(memo.Content) {
				if _, err := tx.Exec(`INSERT INTO vector_chunks VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?,
					(SELECT vector FROM vector_cache WHERE space_hash = ? AND content_hash = ?))`,
					id, memo.Name, chunk.Ordinal, chunk.ByteStart, chunk.ByteEnd, chunk.LineStart, chunk.LineEnd,
					chunk.ContentHash, chunk.Content, space, chunk.ContentHash); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return id, err
}

type pendingVector struct {
	name, sourceHash, contentHash, text string
}

func (s *VectorIndexService) pending(id string) ([]pendingVector, error) {
	rows, err := s.Store.index.Query(`SELECT c.name, n.source_hash, c.content_hash, c.content
		FROM vector_chunks c JOIN vector_notes n ON n.generation_id = c.generation_id AND n.name = c.name
		WHERE c.generation_id = ? AND c.vector IS NULL ORDER BY c.name, c.ordinal LIMIT ?`,
		id, s.Store.Settings.Embedding.BatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []pendingVector{}
	for rows.Next() {
		var item pendingVector
		if err := rows.Scan(&item.name, &item.sourceHash, &item.contentHash, &item.text); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func encodeVector(vector []float32, dimensions int) ([]byte, error) {
	if len(vector) != dimensions {
		return nil, failure("EMBEDDING_PROTOCOL", "Invalid index vector dimensions", 503, false)
	}
	data, norm := make([]byte, len(vector)*4), float64(0)
	for i, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, failure("EMBEDDING_PROTOCOL", "Invalid index vector value", 503, false)
		}
		binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(value))
		norm += float64(value) * float64(value)
	}
	if norm == 0 {
		return nil, failure("EMBEDDING_PROTOCOL", "Empty index vector", 503, false)
	}
	return data, nil
}

func (s *VectorIndexService) save(id string, batch []pendingVector, result EmbeddingResult) error {
	if len(result.Vectors) != len(batch) {
		return failure("EMBEDDING_PROTOCOL", "Invalid index vector count", 503, false)
	}
	vectors := make([][]byte, len(batch))
	for i, vector := range result.Vectors {
		var err error
		if vectors[i], err = encodeVector(vector, s.Store.Settings.Embedding.Dimensions); err != nil {
			return err
		}
	}
	return transaction(s.Store.index, func(tx *sql.Tx) error {
		for i, item := range batch {
			if _, err := tx.Exec("INSERT OR REPLACE INTO vector_cache VALUES (?, ?, ?)",
				embeddingSpace(s.Store.Settings.Embedding), item.contentHash, vectors[i]); err != nil {
				return err
			}
			if _, err := tx.Exec("UPDATE vector_chunks SET vector = ? WHERE generation_id = ? AND content_hash = ?",
				vectors[i], id, item.contentHash); err != nil {
				return err
			}
		}
		_, err := tx.Exec("UPDATE vector_generations SET last_error = NULL WHERE id = ?", id)
		return err
	})
}

func (s *VectorIndexService) publish(id string) (bool, error) {
	published := false
	err := transaction(s.Store.index, func(tx *sql.Tx) error {
		var revision, expected int64
		var pending int
		if err := tx.QueryRow("SELECT revision FROM projection_meta WHERE instance_id = ?", s.Store.Settings.Memos.InstanceID).Scan(&revision); err != nil {
			return err
		}
		if err := tx.QueryRow("SELECT projection_revision FROM vector_generations WHERE id = ? AND status = 'building'", id).Scan(&expected); err != nil {
			return err
		}
		if err := tx.QueryRow("SELECT COUNT(*) FROM vector_chunks WHERE generation_id = ? AND vector IS NULL", id).Scan(&pending); err != nil {
			return err
		}
		if revision != expected || pending != 0 {
			return nil
		}
		if _, err := tx.Exec("DELETE FROM vector_generations WHERE instance_id = ? AND user_name = ? AND status = 'active'",
			s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE vector_generations SET status = 'active', published_at = ?, last_error = NULL WHERE id = ?", nowText(), id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM vector_cache WHERE NOT EXISTS
			(SELECT 1 FROM vector_chunks c JOIN vector_generations g ON g.id = c.generation_id
			 WHERE g.space_hash = vector_cache.space_hash AND c.content_hash = vector_cache.content_hash)`); err != nil {
			return err
		}
		published = true
		return nil
	})
	return published, err
}

// Step persists at most one model batch. A cancelled or failed batch leaves the old generation active.
func (s *VectorIndexService) Step(ctx context.Context) (worked bool, err error) {
	s.Store.vectorMu.Lock()
	defer s.Store.vectorMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !s.Store.Settings.Embedding.Enabled {
		return false, nil
	}
	status, err := s.Store.Status()
	if err != nil || !status.SourceOK || status.LastFull == nil {
		return false, err
	}
	id, err := s.prepare()
	if err != nil || id == "" {
		return false, err
	}
	defer func() {
		if err != nil && ctx.Err() == nil {
			_, saveErr := s.Store.index.Exec("UPDATE vector_generations SET last_error = ? WHERE id = ?", errorCode(err), id)
			err = errors.Join(err, saveErr)
		}
	}()
	batch, err := s.pending(id)
	if err != nil {
		return false, err
	}
	if _, err := s.Source.Identity(ctx); err != nil {
		return false, err
	}
	if len(batch) == 0 {
		return s.publish(id)
	}
	checked := map[string]bool{}
	for _, item := range batch {
		if checked[item.name] {
			continue
		}
		checked[item.name] = true
		fresh, err := s.Source.Get(ctx, item.name)
		if err != nil {
			return false, err
		}
		if fresh == nil || fresh.Creator != s.Store.Settings.Memos.User || fresh.SourceHash != item.sourceHash {
			if fresh != nil && fresh.Creator != s.Store.Settings.Memos.User {
				fresh = nil
			}
			return true, s.Store.Refresh(item.name, fresh)
		}
	}
	if _, err := s.Source.Identity(ctx); err != nil {
		return false, err
	}
	texts := make([]string, len(batch))
	for i, item := range batch {
		texts[i] = item.text
	}
	result, err := s.Provider.Embed(ctx, texts, EmbeddingDocument)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, s.save(id, batch, result)
}

func (s *VectorIndexService) Run(ctx context.Context) error {
	delay := time.Duration(0)
	for ctx.Err() == nil {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		worked, err := s.Step(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch {
		case err != nil:
			slog.Warn("vector indexing failed", "code", errorCode(err))
			delay = time.Duration(s.Store.Settings.Sync.RetrySeconds) * time.Second
		case worked:
			delay = 0
		default:
			delay = time.Second
		}
	}
	return ctx.Err()
}
