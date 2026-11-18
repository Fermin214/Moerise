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

func publishedStars(t *testing.T, service *MapService) *mapLayout {
	t.Helper()
	layout, _, err := service.Store.loadStars(t.Context(), embeddingSpace(service.Store.Settings.Embedding))
	if err != nil || layout == nil {
		t.Fatalf("read published stars: %v", err)
	}
	return layout
}

func TestMapSnapshotIncludesArchivedAndUnlocatedNotes(t *testing.T) {
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
	service, _ := testStarsService(t, store, nil)
	if err := service.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	initial := publishedStars(t, service)
	if len(initial.points) != 6 || initial.points[4].Name != "memos/e" || !initial.points[4].Semantic ||
		initial.points[5].Name != "memos/file" || initial.points[5].Semantic || len(provider.texts) != calls {
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
	restarted := publishedStars(t, NewMapService(reopened))
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
	service, index, memos, _, _ := readyMapService(t)
	store := service.Store
	snapshot, err := store.mapSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	initial := publishedStars(t, service)
	layout := *initial
	layout.parentVersion = initial.version
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.publishStars(ctx, snapshot, &layout); err != context.Canceled {
		t.Fatalf("canceled publication: %v", err)
	}
	changed, _ := normalizeMemo(rawMemo("a", "changed body"))
	memos[changed.Name] = rawMemo("a", "changed body")
	if err := store.Refresh(changed.Name, &changed); err != nil {
		t.Fatal(err)
	}
	if err := store.publishStars(t.Context(), snapshot, &layout); errorCode(err) != "MAP_INDEX_CHANGED" {
		t.Fatalf("stale publication: %v", err)
	}
	finishIndex(t, index)
	next, err := store.mapSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	layout.frame = strings.Repeat("a", 32)
	if _, err := store.index.Exec(`CREATE TRIGGER reject_head BEFORE UPDATE ON star_heads
		BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.publishStars(t.Context(), next, &layout); err == nil || !strings.Contains(err.Error(), "test failure") {
		t.Fatalf("expected publication to fail before updating the head: %v", err)
	}
	var count int
	if err := store.index.QueryRow("SELECT COUNT(*) FROM star_snapshots").Scan(&count); err != nil || count != 1 ||
		!reflect.DeepEqual(initial, publishedStars(t, service)) {
		t.Fatalf("partial publication changed snapshots: count=%d %v", count, err)
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
	if err := reopened.index.QueryRow("SELECT COUNT(*) FROM star_snapshots").Scan(&count); err != nil || count != 0 {
		t.Fatalf("index rebuild retained derived coordinates: %d %v", count, err)
	}
}
