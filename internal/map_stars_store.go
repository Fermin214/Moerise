package moerise

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type storedStarPoint struct {
	Point      MapPoint `json:"point"`
	VectorHash string   `json:"vector_hash"`
}

func (s *Store) loadStars(ctx context.Context, space string) (*mapLayout, string, error) {
	var layout *mapLayout
	var token string
	err := transactionContext(ctx, s.index, func(tx *sql.Tx) error {
		var version sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT version, rebuild_token FROM star_heads
			WHERE instance_id = ? AND user_name = ? AND space_hash = ?`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User, space).Scan(&version, &token)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || !version.Valid {
			return err
		}
		layout = &mapLayout{}
		var stamp, points, topics, links string
		if err := tx.QueryRowContext(ctx, `SELECT algorithm, naming_version, frame, generation_id, projection_revision,
			generated_at, points_json, topics_json, links_json FROM star_snapshots
			WHERE version = ? AND instance_id = ? AND user_name = ? AND space_hash = ?`,
			version.String, s.Settings.Memos.InstanceID, s.Settings.Memos.User, space).
			Scan(&layout.algorithm, &layout.namingVersion, &layout.frame, &layout.generation, &layout.revision,
				&stamp, &points, &topics, &links); err != nil {
			return err
		}
		layout.version = version.String
		var stored []storedStarPoint
		var errTime error
		layout.generatedAt, errTime = time.Parse(time.RFC3339Nano, stamp)
		if errTime != nil || json.Unmarshal([]byte(points), &stored) != nil ||
			json.Unmarshal([]byte(topics), &layout.topics) != nil || json.Unmarshal([]byte(links), &layout.links) != nil ||
			stored == nil || layout.topics == nil || layout.links == nil {
			return failure("MAP_LAYOUT_INVALID", "Stored stars layout is invalid", 503, false)
		}
		layout.points = make([]MapPoint, len(stored))
		for i, point := range stored {
			a := point.Point.Stellar
			if !memoPattern.MatchString(point.Point.Name) || !finiteStar(point.Point.X) || !finiteStar(point.Point.Y) ||
				point.Point.Semantic && (a == nil || !finiteStar(a.Seed) || a.Seed < 0 || a.Seed > 1 ||
					!finiteStar(a.Radius) || a.Radius <= 0 || a.Radius > 4 ||
					!finiteStar(a.Luminosity) || a.Luminosity < 0 || a.Luminosity > 1 ||
					a.Role != "ordinary" && a.Role != "core" && a.Role != "isolated") {
				return failure("MAP_LAYOUT_INVALID", "Stored stellar properties are invalid", 503, false)
			}
			layout.points[i] = point.Point
			layout.points[i].vectorHash = point.VectorHash
		}
		if layout.algorithm != starsAlgorithm || !starFramePattern.MatchString(layout.frame) {
			return failure("MAP_LAYOUT_INVALID", "Stored stars metadata is invalid", 503, false)
		}
		return validateStarGroups(layout)
	})
	return layout, token, err
}

func (s *Store) requestStarsRebuild(ctx context.Context, space string) error {
	token := hashBytes([]byte(rand.Text()))
	_, err := s.index.ExecContext(ctx, `INSERT INTO star_heads(instance_id, user_name, space_hash, rebuild_token)
		VALUES (?, ?, ?, ?) ON CONFLICT(instance_id, user_name) DO UPDATE SET
		version = CASE WHEN star_heads.space_hash = excluded.space_hash THEN star_heads.version ELSE NULL END,
		space_hash = excluded.space_hash, rebuild_token = excluded.rebuild_token`,
		s.Settings.Memos.InstanceID, s.Settings.Memos.User, space, token)
	return err
}

func (s *Store) publishStars(ctx context.Context, snapshot mapSnapshot, layout *mapLayout) error {
	stored := make([]storedStarPoint, len(layout.points))
	for i, point := range layout.points {
		stored[i] = storedStarPoint{point, point.vectorHash}
	}
	points, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	topics, _ := json.Marshal(layout.topics)
	links, _ := json.Marshal(layout.links)
	layout.version = hashBytes(canonicalJSON([]any{layout.algorithm, layout.namingVersion, snapshot.space, layout.frame,
		stored, layout.topics, layout.links}))
	if layout.generatedAt.IsZero() {
		layout.generatedAt = time.Now().UTC()
	}
	err = transactionContext(ctx, s.index, func(tx *sql.Tx) error {
		var generation string
		err := tx.QueryRowContext(ctx, `SELECT g.id FROM vector_generations g JOIN projection_meta p USING(instance_id)
			WHERE g.instance_id = ? AND g.user_name = ? AND g.status = 'active' AND g.space_hash = ?
			AND g.projection_revision = ? AND p.revision = g.projection_revision`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User, snapshot.space, snapshot.revision).Scan(&generation)
		if errors.Is(err, sql.ErrNoRows) || err == nil && generation != snapshot.generation {
			return mapIndexChanged()
		}
		if err != nil {
			return err
		}
		var version sql.NullString
		var token string
		err = tx.QueryRowContext(ctx, `SELECT version, rebuild_token FROM star_heads
			WHERE instance_id = ? AND user_name = ? AND space_hash = ?`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User, snapshot.space).Scan(&version, &token)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if version.String != layout.parentVersion || token != layout.rebuildToken {
			return mapIndexChanged()
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO star_snapshots
			(version, instance_id, user_name, space_hash, algorithm, naming_version, frame, generation_id, projection_revision,
			generated_at, points_json, topics_json, links_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			layout.version, s.Settings.Memos.InstanceID, s.Settings.Memos.User, snapshot.space,
			layout.algorithm, layout.namingVersion, layout.frame, snapshot.generation, snapshot.revision,
			layout.generatedAt.Format(time.RFC3339Nano), string(points), string(topics), string(links)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO star_heads(instance_id, user_name, space_hash, version)
			VALUES (?, ?, ?, ?) ON CONFLICT(instance_id, user_name) DO UPDATE SET
			space_hash = excluded.space_hash, version = excluded.version, rebuild_token = ''`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User, snapshot.space, layout.version); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM star_snapshots WHERE instance_id = ? AND user_name = ?
			AND version NOT IN (SELECT version FROM star_snapshots WHERE instance_id = ? AND user_name = ?
			ORDER BY generated_at DESC LIMIT 2) AND version NOT IN (SELECT version FROM star_heads WHERE version IS NOT NULL)`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User, s.Settings.Memos.InstanceID, s.Settings.Memos.User)
		return err
	})
	if err == nil {
		layout.generation, layout.revision = snapshot.generation, snapshot.revision
	}
	return err
}
