package moerise

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"slices"
)

func mapIndexChanged() error {
	return failure("MAP_INDEX_CHANGED", "Map index changed; retry after indexing completes", 409, true)
}

func decodeAttachmentsJSON(raw string) []map[string]any {
	if raw == "" || raw == "null" || raw == "[]" {
		return nil
	}
	var attachments []map[string]any
	if json.Unmarshal([]byte(raw), &attachments) != nil {
		return nil
	}
	return attachments
}

func (s *Store) mapSnapshot(ctx context.Context) (mapSnapshot, error) {
	snapshot := mapSnapshot{dimensions: s.Settings.Embedding.Dimensions, documents: []mapDocument{}}
	err := transactionContext(ctx, s.index, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT g.id, g.space_hash, g.projection_revision
			FROM vector_generations g JOIN projection_meta p USING (instance_id)
			WHERE g.instance_id = ? AND g.user_name = ? AND g.status = 'active'
			AND g.space_hash = ? AND g.projection_revision = p.revision`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User, embeddingSpace(s.Settings.Embedding)).
			Scan(&snapshot.generation, &snapshot.space, &snapshot.revision); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT n.name, n.source_hash, m.state, json_extract(m.payload_json, '$.tags'),
			COALESCE(json_extract(m.payload_json, '$.create_time'), ''),
			substr(COALESCE(json_extract(m.payload_json, '$.content'), ''), 1, 2048),
			COALESCE(json_extract(m.payload_json, '$.attachments'), '[]'),
			c.content_hash, c.vector
			FROM vector_notes n JOIN vector_generations g ON g.id = n.generation_id
			JOIN memos m ON m.instance_id = g.instance_id AND m.user_name = g.user_name AND m.name = n.name
			LEFT JOIN vector_chunks c USING (generation_id, name)
			WHERE n.generation_id = ? AND json_extract(m.payload_json, '$.creator') = ?
			ORDER BY n.name, c.ordinal`, snapshot.generation, s.Settings.Memos.User)
		if err != nil {
			return err
		}
		defer rows.Close()
		var current *mapDocument
		seen := map[string]bool{}
		for rows.Next() {
			var name, sourceHash, state, createTime, content, attachmentsJSON string
			var chunkHash sql.NullString
			var tags sql.NullString
			var data []byte
			if err := rows.Scan(&name, &sourceHash, &state, &tags, &createTime, &content, &attachmentsJSON, &chunkHash, &data); err != nil {
				return err
			}
			if current == nil || current.name != name {
				excerpt := ""
				if state == "NORMAL" {
					excerpt = mapPointExcerpt(content, decodeAttachmentsJSON(attachmentsJSON))
				}
				snapshot.documents = append(snapshot.documents, mapDocument{
					name: name, sourceHash: sourceHash, excerpt: excerpt, createTime: createTime,
				})
				current = &snapshot.documents[len(snapshot.documents)-1]
				current.keywords = mapKeywords(content)
				if tags.Valid && tags.String != "null" {
					if err := json.Unmarshal([]byte(tags.String), &current.tags); err != nil {
						return err
					}
				}
				seen = map[string]bool{}
			}
			if !chunkHash.Valid || seen[chunkHash.String] {
				continue
			}
			seen[chunkHash.String] = true
			if len(data) != 4*snapshot.dimensions {
				return failure("EMBEDDING_PROTOCOL", "Invalid stored map vector", 503, false)
			}
			vector := make([]float64, snapshot.dimensions)
			for j := range vector {
				vector[j] = float64(math.Float32frombits(binary.LittleEndian.Uint32(data[j*4:])))
			}
			if !mapNormalize(vector) {
				return failure("EMBEDDING_PROTOCOL", "Invalid stored map vector", 503, false)
			}
			if current.vector == nil {
				current.vector = make([]float64, snapshot.dimensions)
			}
			for j, value := range vector {
				current.vector[j] += value
			}
		}
		return rows.Err()
	})
	if err != nil {
		return mapSnapshot{}, err
	}
	for i := range snapshot.documents {
		document := &snapshot.documents[i]
		if !mapNormalize(document.vector) {
			document.vector = nil
		}
		data, err := json.Marshal(document.vector)
		if err != nil {
			return mapSnapshot{}, err
		}
		document.vectorHash = hashBytes(data)
	}
	return snapshot, ctx.Err()
}

func (s *Store) reconcileMapPoints(ctx context.Context, points []MapPoint, sinceRevision int64) ([]MapPoint, int64, bool, error) {
	if len(points) == 0 {
		return points, sinceRevision, false, nil
	}
	updated := points
	currentRevision := sinceRevision
	changed := false
	err := transactionContext(ctx, s.index, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT revision FROM projection_meta WHERE instance_id = ?`,
			s.Settings.Memos.InstanceID).Scan(&currentRevision)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || currentRevision == sinceRevision {
			return err
		}
		type liveMemo struct {
			state, sourceHash, createTime, content, attachments string
		}
		rows, err := tx.QueryContext(ctx, `SELECT name, state, source_hash,
			COALESCE(json_extract(payload_json, '$.create_time'), ''),
			substr(COALESCE(json_extract(payload_json, '$.content'), ''), 1, 2048),
			COALESCE(json_extract(payload_json, '$.attachments'), '[]')
			FROM memos WHERE instance_id = ? AND user_name = ?`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User)
		if err != nil {
			return err
		}
		defer rows.Close()
		live := make(map[string]liveMemo, len(points))
		for rows.Next() {
			var name string
			var item liveMemo
			if err := rows.Scan(&name, &item.state, &item.sourceHash, &item.createTime, &item.content, &item.attachments); err != nil {
				return err
			}
			live[name] = item
		}
		if err := rows.Err(); err != nil {
			return err
		}
		cloned := slices.Clone(points)
		for i, point := range cloned {
			item, exists := live[point.Name]
			if !exists || item.state != "NORMAL" {
				if point.Excerpt != "" {
					cloned[i].Excerpt = ""
					changed = true
				}
				continue
			}
			if point.SourceHash != item.sourceHash || point.Excerpt == "" {
				nextExcerpt := mapPointExcerpt(item.content, decodeAttachmentsJSON(item.attachments))
				if cloned[i].Excerpt != nextExcerpt || cloned[i].CreateTime != item.createTime || cloned[i].SourceHash != item.sourceHash {
					cloned[i].Excerpt = nextExcerpt
					cloned[i].CreateTime = item.createTime
					cloned[i].SourceHash = item.sourceHash
					changed = true
				}
			}
		}
		if changed {
			updated = cloned
		}
		return nil
	})
	return updated, currentRevision, changed, err
}
