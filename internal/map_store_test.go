package moerise

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func indexedMapLayout(t *testing.T, store *Store) (mapSnapshot, *mapLayout) {
	t.Helper()
	snapshot, err := store.mapSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	previous, err := store.loadMapLayout(t.Context(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := buildMapLayout(t.Context(), snapshot, previous)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.publishMapLayout(t.Context(), snapshot, layout); err != nil {
		t.Fatal(err)
	}
	return snapshot, layout
}

func TestMapStorageSnapshotRestartIncrementalAndSpaceIsolation(t *testing.T) {
	store, index, memos, provider := indexFixture(t)
	file := rawMemo("file", "")
	file["attachments"] = []any{map[string]any{"name": "attachments/file", "type": "application/pdf"}}
	memos["memos/file"] = file
	memo, _ := normalizeMemo(file)
	if err := store.Refresh(memo.Name, &memo); err != nil {
		t.Fatal(err)
	}
	finishIndex(t, index)
	calls := len(provider.texts)
	snapshot, initial := indexedMapLayout(t, store)
	if len(initial.points) != 6 || len(snapshot.documents) != 6 || mapPointByName(t, initial, "file").Semantic ||
		!mapPointByName(t, initial, "e").Semantic || len(provider.texts) != calls {
		t.Fatalf("snapshot lost archive/file or called model: %+v", initial.points)
	}
	data, err := json.Marshal(initial.points)
	if err != nil || strings.Contains(string(data), "body a") || strings.Contains(string(data), "vector_hash") {
		t.Fatalf("map exposed cached body or internal vector: %s %v", data, err)
	}
	reopened, err := NewStore(store.Settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	_, restarted := indexedMapLayout(t, reopened)
	if !reflect.DeepEqual(initial, restarted) {
		t.Fatal("restart changed layout")
	}
	if err := store.SourceHealth(true, "", "0.31.0"); err != nil {
		t.Fatal(err)
	}
	newMemo := testMemo(t, "new", "new text")
	memos[newMemo.Name] = rawMemo("new", "new text")
	if err := store.Refresh(newMemo.Name, &newMemo); err != nil {
		t.Fatal(err)
	}
	if _, err := store.mapSnapshot(t.Context()); err != sql.ErrNoRows {
		t.Fatalf("snapshot mixed stale generation and new projection: %v", err)
	}
	finishIndex(t, index)
	newSnapshot, incremental := indexedMapLayout(t, store)
	if newSnapshot.generation == snapshot.generation || initial.version == incremental.version || len(incremental.points) != 7 {
		t.Fatal("incremental version or point set did not advance")
	}
	for _, point := range initial.points {
		after := mapPointByName(t, incremental, strings.TrimPrefix(point.Name, "memos/"))
		if !reflect.DeepEqual(after, point) {
			t.Fatalf("generation replacement moved old point: %+v %+v", point, after)
		}
	}
	model := "another-space"
	store.Settings.Embedding.Model = &model
	finishIndex(t, index)
	changed, err := store.mapSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if previous, err := store.loadMapLayout(t.Context(), changed); err != nil || previous != nil {
		t.Fatalf("new space reused old basis: %+v %v", previous, err)
	}
	_, switched := indexedMapLayout(t, store)
	var count int
	if err := store.index.QueryRow("SELECT COUNT(*) FROM map_layouts").Scan(&count); err != nil || count != 1 || switched.version == incremental.version {
		t.Fatalf("old-space layout not replaced: count=%d error=%v", count, err)
	}
}

func TestMapAlgorithmUpgradeRebuildsOnlyDerivedLayout(t *testing.T) {
	store, index, _, _ := indexFixture(t)
	finishIndex(t, index)
	snapshot, initial := indexedMapLayout(t, store)
	if _, err := store.index.Exec("UPDATE map_layouts SET algorithm = 'pca-fixed-v1'"); err != nil {
		t.Fatal(err)
	}
	previous, err := store.loadMapLayout(t.Context(), snapshot)
	if err != nil || previous != nil {
		t.Fatalf("algorithm upgrade reused old density layout: %v", err)
	}
	_, rebuilt := indexedMapLayout(t, store)
	if !reflect.DeepEqual(initial.points, rebuilt.points) {
		t.Fatal("deterministic derived rebuild changed the source point set")
	}
	var algorithm string
	if err := store.index.QueryRow("SELECT algorithm FROM map_layouts").Scan(&algorithm); err != nil || algorithm != mapAlgorithm {
		t.Fatalf("algorithm metadata not replaced: %q %v", algorithm, err)
	}
}

func TestMapSnapshotAveragesDistinctNormalizedChunks(t *testing.T) {
	store, index, _, _ := indexFixture(t)
	finishIndex(t, index)
	status, _ := store.VectorStatus()
	id, dimensions := status.Active.ID, store.Settings.Embedding.Dimensions
	vector := make([]float32, dimensions)
	vector[1] = 40
	encoded, err := encodeVector(vector, dimensions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.index.Exec(`INSERT INTO vector_chunks
		SELECT generation_id, name, 1, byte_start, byte_end, line_start, line_end, 'distinct', content, ?
		FROM vector_chunks WHERE generation_id = ? AND name = 'memos/a' AND ordinal = 0`, encoded, id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.index.Exec(`INSERT INTO vector_chunks
		SELECT generation_id, name, 2, byte_start, byte_end, line_start, line_end, content_hash, content, vector
		FROM vector_chunks WHERE generation_id = ? AND name = 'memos/a' AND ordinal = 0`, id); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.mapSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.documents) != 5 || math.Abs(snapshot.documents[0].vector[0]-math.Sqrt(0.5)) > 1e-8 ||
		math.Abs(snapshot.documents[0].vector[1]-math.Sqrt(0.5)) > 1e-8 {
		t.Fatalf("chunk repetition or magnitude changed document weight: %+v", snapshot.documents[0].vector[:2])
	}
	if _, err := store.index.Exec("UPDATE vector_chunks SET vector = ? WHERE generation_id = ? AND name = 'memos/a' AND ordinal = 1", []byte{1}, id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.mapSnapshot(t.Context()); errorCode(err) != "EMBEDDING_PROTOCOL" {
		t.Fatalf("invalid stored vector accepted: %v", err)
	}
}

func TestMapPublicationIsAtomicAndRejectsStaleOrCanceledSnapshots(t *testing.T) {
	store, index, memos, _ := indexFixture(t)
	finishIndex(t, index)
	snapshot, initial := indexedMapLayout(t, store)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.publishMapLayout(ctx, snapshot, initial); err != context.Canceled {
		t.Fatalf("canceled publication: %v", err)
	}
	changed, _ := normalizeMemo(rawMemo("a", "changed body"))
	memos[changed.Name] = rawMemo("a", "changed body")
	if err := store.Refresh(changed.Name, &changed); err != nil {
		t.Fatal(err)
	}
	if err := store.publishMapLayout(t.Context(), snapshot, initial); errorCode(err) != "MAP_INDEX_CHANGED" {
		t.Fatalf("stale publication: %v", err)
	}
	previous, err := store.loadMapLayout(t.Context(), snapshot)
	if err != nil || !reflect.DeepEqual(previous, initial) {
		t.Fatalf("failed publication damaged old layout: %v", err)
	}
	finishIndex(t, index)
	next, _ := store.mapSnapshot(t.Context())
	layout, _ := buildMapLayout(t.Context(), next, initial)
	layout.points = append(layout.points, layout.points[0])
	if err := store.publishMapLayout(t.Context(), next, layout); err == nil {
		t.Fatal("duplicate point publication unexpectedly succeeded")
	}
	previous, err = store.loadMapLayout(t.Context(), snapshot)
	if err != nil || !reflect.DeepEqual(previous, initial) {
		t.Fatalf("partial publication replaced old layout: %v", err)
	}
}

func TestIndexRebuildKeepsProductSettings(t *testing.T) {
	store, index, _, _ := indexFixture(t)
	store.Settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	if err := store.initializeProductSettings(); err != nil {
		t.Fatal(err)
	}
	finishIndex(t, index)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RebuildIndex(store.Settings); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(store.Settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	var version int64
	if err := reopened.state.QueryRow("SELECT version FROM product_settings").Scan(&version); err != nil || version != 1 {
		t.Fatalf("index rebuild changed product settings: %d %v", version, err)
	}
	var count int
	if err := reopened.index.QueryRow("SELECT COUNT(*) FROM map_points").Scan(&count); err != nil || count != 0 {
		t.Fatalf("index rebuild retained derived coordinates: %d %v", count, err)
	}
}
