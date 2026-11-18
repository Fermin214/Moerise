package moerise

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"time"
)

func mapIndexChanged() error {
	return failure("MAP_INDEX_CHANGED", "Map index changed; retry after indexing completes", 409, true)
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
		rows, err := tx.QueryContext(ctx, `SELECT n.name, n.source_hash, json_extract(m.payload_json, '$.tags'),
			substr(json_extract(m.payload_json, '$.content'), 1, 2048), c.content_hash, c.vector
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
			var name, sourceHash string
			var chunkHash sql.NullString
			var tags, content sql.NullString
			var data []byte
			if err := rows.Scan(&name, &sourceHash, &tags, &content, &chunkHash, &data); err != nil {
				return err
			}
			if current == nil || current.name != name {
				snapshot.documents = append(snapshot.documents, mapDocument{name: name, sourceHash: sourceHash})
				current = &snapshot.documents[len(snapshot.documents)-1]
				current.keywords = mapKeywords(content.String)
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

func (s *Store) loadLegacyLayout(ctx context.Context, space string) (*mapLayout, error) {
	layout := &mapLayout{points: []MapPoint{}}
	err := transactionContext(ctx, s.index, func(tx *sql.Tx) error {
		var basis, topics, links []byte
		var generatedAt string
		err := tx.QueryRowContext(ctx, `SELECT algorithm, version, basis_json, generation_id, projection_revision, generated_at,
			topics_json, links_json FROM map_layouts
			WHERE instance_id = ? AND user_name = ? AND space_hash = ? AND algorithm IN ('pca-topics-v3', 'pca-fixed-v2')`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User, space).
			Scan(&layout.algorithm, &layout.version, &basis, &layout.generation, &layout.revision, &generatedAt, &topics, &links)
		if err != nil {
			return err
		}
		layout.generatedAt, err = time.Parse(time.RFC3339Nano, generatedAt)
		if err != nil {
			return failure("MAP_LAYOUT_INVALID", "Stored map timestamp is invalid; rebuild the index", 503, false)
		}
		if json.Unmarshal(basis, &layout.basis) != nil || !validMapBasis(layout.basis, s.Settings.Embedding.Dimensions) {
			return failure("MAP_LAYOUT_INVALID", "Stored map layout is invalid; rebuild the index", 503, false)
		}
		if json.Unmarshal(topics, &layout.topics) != nil || json.Unmarshal(links, &layout.links) != nil ||
			layout.topics == nil || layout.links == nil {
			return failure("MAP_LAYOUT_INVALID", "Stored map topics are invalid; rebuild the index", 503, false)
		}
		rows, err := tx.QueryContext(ctx, `SELECT name, source_hash, vector_hash, x, y, semantic, tags_json
			FROM map_points WHERE instance_id = ? AND user_name = ? ORDER BY name`, s.Settings.Memos.InstanceID, s.Settings.Memos.User)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var point MapPoint
			var tags string
			if err := rows.Scan(&point.Name, &point.SourceHash, &point.vectorHash, &point.X, &point.Y, &point.Semantic, &tags); err != nil {
				return err
			}
			if json.Unmarshal([]byte(tags), &point.Tags) != nil || point.Tags == nil {
				return failure("MAP_LAYOUT_INVALID", "Stored map tags are invalid; rebuild the index", 503, false)
			}
			if math.IsNaN(point.X) || math.IsNaN(point.Y) || math.IsInf(point.X, 0) || math.IsInf(point.Y, 0) {
				return failure("MAP_LAYOUT_INVALID", "Stored map layout is invalid; rebuild the index", 503, false)
			}
			layout.points = append(layout.points, point)
		}
		return rows.Err()
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return layout, err
}
