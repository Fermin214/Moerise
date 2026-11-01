package moerise

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
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
		rows, err := tx.QueryContext(ctx, `SELECT n.name, n.source_hash, c.content_hash, c.vector
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
			var data []byte
			if err := rows.Scan(&name, &sourceHash, &chunkHash, &data); err != nil {
				return err
			}
			if current == nil || current.name != name {
				snapshot.documents = append(snapshot.documents, mapDocument{name: name, sourceHash: sourceHash})
				current = &snapshot.documents[len(snapshot.documents)-1]
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

func (s *Store) loadMapLayout(ctx context.Context, snapshot mapSnapshot) (*mapLayout, error) {
	layout := &mapLayout{points: []MapPoint{}}
	err := transactionContext(ctx, s.index, func(tx *sql.Tx) error {
		var basis []byte
		err := tx.QueryRowContext(ctx, `SELECT version, basis_json FROM map_layouts
			WHERE instance_id = ? AND user_name = ? AND space_hash = ? AND algorithm = ?`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User, snapshot.space, mapAlgorithm).Scan(&layout.version, &basis)
		if err != nil {
			return err
		}
		if json.Unmarshal(basis, &layout.basis) != nil || !validMapBasis(layout.basis, snapshot.dimensions) {
			return failure("MAP_LAYOUT_INVALID", "Stored map layout is invalid; rebuild the index", 503, false)
		}
		rows, err := tx.QueryContext(ctx, `SELECT name, source_hash, vector_hash, x, y, semantic
			FROM map_points WHERE instance_id = ? AND user_name = ? ORDER BY name`, s.Settings.Memos.InstanceID, s.Settings.Memos.User)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var point MapPoint
			if err := rows.Scan(&point.Name, &point.SourceHash, &point.vectorHash, &point.X, &point.Y, &point.Semantic); err != nil {
				return err
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

func (s *Store) publishMapLayout(ctx context.Context, snapshot mapSnapshot, layout *mapLayout) error {
	return transactionContext(ctx, s.index, func(tx *sql.Tx) error {
		var generation string
		err := tx.QueryRowContext(ctx, `SELECT g.id FROM vector_generations g JOIN projection_meta p USING (instance_id)
			WHERE g.instance_id = ? AND g.user_name = ? AND g.status = 'active'
			AND g.space_hash = ? AND g.projection_revision = ? AND p.revision = g.projection_revision`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User, snapshot.space, snapshot.revision).Scan(&generation)
		if errors.Is(err, sql.ErrNoRows) || err == nil && generation != snapshot.generation {
			return mapIndexChanged()
		}
		if err != nil {
			return err
		}
		var version string
		err = tx.QueryRowContext(ctx, `SELECT version FROM map_layouts
			WHERE instance_id = ? AND user_name = ? AND space_hash = ? AND algorithm = ?`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User, snapshot.space, mapAlgorithm).Scan(&version)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && version == layout.version {
			_, err = tx.ExecContext(ctx, `UPDATE map_layouts SET generation_id = ?, projection_revision = ?
				WHERE instance_id = ? AND user_name = ?`, snapshot.generation, snapshot.revision,
				s.Settings.Memos.InstanceID, s.Settings.Memos.User)
			return err
		}
		basis, err := json.Marshal(layout.basis)
		if err != nil {
			return err
		}
		// Replace metadata and the complete point set together, retaining no old-space points.
		if _, err := tx.ExecContext(ctx, "DELETE FROM map_layouts WHERE instance_id = ? AND user_name = ?",
			s.Settings.Memos.InstanceID, s.Settings.Memos.User); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO map_layouts VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User, snapshot.space, mapAlgorithm, snapshot.generation,
			snapshot.revision, layout.version, string(basis)); err != nil {
			return err
		}
		for _, point := range layout.points {
			if _, err := tx.ExecContext(ctx, `INSERT INTO map_points VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				s.Settings.Memos.InstanceID, s.Settings.Memos.User, point.Name, point.SourceHash,
				point.vectorHash, point.X, point.Y, point.Semantic); err != nil {
				return err
			}
		}
		return nil
	})
}
