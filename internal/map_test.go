package moerise

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mapSource struct {
	Source
	list     func(context.Context, string, *int64, string) ([]Memo, string, error)
	identity func(context.Context) (string, error)
}

func (s mapSource) ListPage(ctx context.Context, state string, since *int64, token string) ([]Memo, string, error) {
	if s.list != nil {
		return s.list(ctx, state, since, token)
	}
	return s.Source.ListPage(ctx, state, since, token)
}

func (s mapSource) Identity(ctx context.Context) (string, error) {
	if s.identity != nil {
		return s.identity(ctx)
	}
	return s.Source.Identity(ctx)
}

func mapServiceFixture(t *testing.T) (*MapService, *VectorIndexService, map[string]map[string]any, *indexEmbedding) {
	t.Helper()
	store, index, memos, provider := indexFixture(t)
	notes := NewNoteService(store.Settings, store, index.Source)
	return NewMapService(store, notes), index, memos, provider
}

func TestMapServiceReadStatusesAndEmpty(t *testing.T) {
	service, index, memos, _ := mapServiceFixture(t)
	service.Store.Settings.Embedding.Enabled = false
	result, err := service.Read(t.Context())
	if err != nil || result.Status != "disabled" || result.Points == nil || len(result.Points) != 0 || result.LayoutVersion != "" {
		t.Fatalf("disabled map: %+v %v", result, err)
	}
	service.Store.Settings.Embedding.Enabled = true
	result, err = service.Read(t.Context())
	if err != nil || result.Status != "waiting" {
		t.Fatalf("waiting map: %+v %v", result, err)
	}
	id, err := index.prepare()
	if err != nil {
		t.Fatal(err)
	}
	result, err = service.Read(t.Context())
	if err != nil || result.Status != "building" || result.LayoutVersion != "" {
		t.Fatalf("building map: %+v %v", result, err)
	}
	if _, err := service.Store.index.Exec("UPDATE vector_generations SET last_error = 'EMBEDDING_AUTH' WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	result, err = service.Read(t.Context())
	if err != nil || result.Status != "unavailable" || result.VectorGeneration != "" || len(result.Points) != 0 {
		t.Fatalf("failed map: %+v %v", result, err)
	}
	for name := range memos {
		delete(memos, name)
		if err := service.Store.Refresh(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	finishIndex(t, index)
	result, err = service.Read(t.Context())
	if err != nil || result.Status != "empty" || result.LayoutVersion == "" || result.VectorGeneration == "" || result.Points == nil {
		t.Fatalf("empty map indistinguishable from unconfigured: %+v %v", result, err)
	}
}

func TestMapServiceChecksAllSourcesAndBothIdentitiesWithoutModelCalls(t *testing.T) {
	service, index, _, provider := mapServiceFixture(t)
	finishIndex(t, index)
	calls, identities, gets := len(provider.texts), 0, map[string]int{}
	source := service.Notes.Source
	service.Notes.Source = mapSource{Source: source,
		list: func(ctx context.Context, state string, since *int64, token string) ([]Memo, string, error) {
			memos, next, err := source.ListPage(ctx, state, since, token)
			for _, memo := range memos {
				gets[memo.Name]++
			}
			return memos, next, err
		}, identity: func(ctx context.Context) (string, error) {
			identities++
			return source.Identity(ctx)
		}}
	first, err := service.Read(t.Context())
	if err != nil || first.Status != "ready" || len(first.Points) != 5 || len(gets) != 5 || identities != 2 || calls != len(provider.texts) {
		t.Fatalf("source or model boundaries: result=%+v gets=%v identities=%d error=%v", first, gets, identities, err)
	}
	second, err := service.Read(t.Context())
	if err != nil || !reflect.DeepEqual(first, second) || identities != 4 {
		t.Fatalf("cached layout bypassed source recheck or moved: %v", err)
	}
	for name, count := range gets {
		if count != 2 {
			t.Fatalf("cached point %s bypassed source check: %d", name, count)
		}
	}
}

func TestMapServiceRejectsSourceChangesDeletionAndForeignAuthor(t *testing.T) {
	for _, change := range []string{"body", "archive", "delete", "author"} {
		t.Run(change, func(t *testing.T) {
			service, index, memos, _ := mapServiceFixture(t)
			finishIndex(t, index)
			if _, err := service.Read(t.Context()); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "body":
				memos["memos/a"]["content"] = "external change"
			case "archive":
				memos["memos/a"]["state"] = "ARCHIVED"
			case "delete":
				delete(memos, "memos/a")
			case "author":
				memos["memos/a"]["creator"] = "users/foreign"
			}
			result, err := service.Read(t.Context())
			if errorCode(err) != "MAP_SOURCE_CHANGED" || result.Points != nil {
				t.Fatalf("stale or foreign point leaked: %+v %v", result, err)
			}
			finishIndex(t, index)
			result, err = service.Read(t.Context())
			if err != nil || result.Status != "ready" {
				t.Fatalf("source refresh did not recover: %+v %v", result, err)
			}
			want := 5
			if change == "delete" || change == "author" {
				want = 4
			}
			if len(result.Points) != want {
				t.Fatalf("source refresh kept unavailable point: %+v", result.Points)
			}
		})
	}
}

func TestMapNativeTagsAreLiveMetadataNotLayoutWeights(t *testing.T) {
	service, index, memos, provider := mapServiceFixture(t)
	finishIndex(t, index)
	first, err := service.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	calls := len(provider.texts)
	tags := []string{"R&D", "C++", "\u4e2d\u6587"}
	memos["memos/a"]["tags"] = tags
	if _, err := service.Read(t.Context()); errorCode(err) != "MAP_SOURCE_CHANGED" {
		t.Fatalf("tag change bypassed source check: %v", err)
	}
	finishIndex(t, index)
	second, err := service.Read(t.Context())
	if err != nil || !reflect.DeepEqual(second.Points[0].Tags, tags) || second.LayoutVersion == first.LayoutVersion ||
		second.LayoutFrame != first.LayoutFrame || len(provider.texts) != calls {
		t.Fatalf("native tags not preserved or triggered model: %+v %v", second, err)
	}
	for i, point := range first.Points {
		if point.X != second.Points[i].X || point.Y != second.Points[i].Y {
			t.Fatal("tags changed layout weights")
		}
	}
}

func TestMapLayoutFrameChangesWithVectorSpaceNotIncrementalPublication(t *testing.T) {
	service, index, memos, _ := mapServiceFixture(t)
	finishIndex(t, index)
	first, err := service.Read(t.Context())
	if err != nil || len(first.LayoutFrame) != 64 {
		t.Fatalf("missing layout frame: %+v %v", first, err)
	}
	memo := testMemo(t, "new", "new note")
	memos[memo.Name] = rawMemo("new", "new note")
	if err := service.Store.Refresh(memo.Name, &memo); err != nil {
		t.Fatal(err)
	}
	finishIndex(t, index)
	incremental, err := service.Read(t.Context())
	if err != nil || incremental.LayoutFrame != first.LayoutFrame || incremental.LayoutVersion == first.LayoutVersion {
		t.Fatalf("incremental update reset frame: %+v %v", incremental, err)
	}
	model := "new-space"
	service.Store.Settings.Embedding.Model = &model
	finishIndex(t, index)
	switched, err := service.Read(t.Context())
	if err != nil || switched.LayoutFrame == first.LayoutFrame || switched.LayoutFrame == "" {
		t.Fatalf("space change retained old frame: %+v %v", switched, err)
	}
}

func TestMapServiceSourceFaultAndLateIndexRaceNeverPublishPartialLayout(t *testing.T) {
	for _, cause := range []string{"get", "identity", "index"} {
		t.Run(cause, func(t *testing.T) {
			service, index, _, _ := mapServiceFixture(t)
			finishIndex(t, index)
			source := service.Notes.Source
			identities := 0
			service.Notes.Source = mapSource{Source: source,
				list: func(ctx context.Context, state string, since *int64, token string) ([]Memo, string, error) {
					if cause == "get" && state == "ARCHIVED" {
						return nil, "", upstreamError("UPSTREAM_UNAVAILABLE", "unavailable")
					}
					return source.ListPage(ctx, state, since, token)
				}, identity: func(ctx context.Context) (string, error) {
					identities++
					if identities == 2 {
						if cause == "identity" {
							return "", upstreamError("IDENTITY_MISMATCH", "changed identity")
						}
						if cause == "index" {
							memo := testMemo(t, "new", "changed projection")
							if err := service.Store.Refresh(memo.Name, &memo); err != nil {
								return "", err
							}
						}
					}
					return source.Identity(ctx)
				}}
			result, err := service.Read(t.Context())
			if err == nil || result.Points != nil || cause == "index" && errorCode(err) != "MAP_INDEX_CHANGED" {
				t.Fatalf("fault returned partial map: %+v %v", result, err)
			}
			var count int
			if err := service.Store.index.QueryRow("SELECT COUNT(*) FROM map_layouts").Scan(&count); err != nil || count != 0 {
				t.Fatalf("fault published layout: %d %v", count, err)
			}
		})
	}
}

func TestMapFullPaginationFailuresNeverRefreshOrPublishPartialSources(t *testing.T) {
	for _, fault := range []string{"later-page", "archive", "token-loop", "duplicate", "limit"} {
		t.Run(fault, func(t *testing.T) {
			service, index, _, _ := mapServiceFixture(t)
			finishIndex(t, index)
			before, err := service.Store.mapSnapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			source := service.Notes.Source
			service.Store.Settings.Memos.MaxPages = 2
			service.Notes.Source = mapSource{Source: source, list: func(ctx context.Context, state string, since *int64, token string) ([]Memo, string, error) {
				if state == "ARCHIVED" {
					return nil, "", upstreamError("UPSTREAM_UNAVAILABLE", "archive unavailable")
				}
				if token == "" {
					memo := testMemo(t, "a", "changed source in first page")
					next := "next"
					if fault == "archive" {
						next = ""
					}
					return []Memo{memo}, next, nil
				}
				switch fault {
				case "later-page":
					return nil, "", upstreamError("UPSTREAM_UNAVAILABLE", "page unavailable")
				case "duplicate":
					return []Memo{testMemo(t, "a", "duplicate")}, "", nil
				case "limit":
					return nil, "third", nil
				default:
					return nil, "next", nil
				}
			}}
			result, err := service.Read(t.Context())
			if err == nil || result.Points != nil {
				t.Fatalf("pagination fault returned points: %v", err)
			}
			after, snapshotErr := service.Store.mapSnapshot(t.Context())
			if snapshotErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("partial source scan changed the index: %v", snapshotErr)
			}
			var count int
			if err := service.Store.index.QueryRow("SELECT COUNT(*) FROM map_layouts").Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial source scan published layout: %d %v", count, err)
			}
		})
	}
}

func TestMapSourceAdditionCannotReturnAnEmptyOrIncompleteMap(t *testing.T) {
	service, index, memos, _ := mapServiceFixture(t)
	finishIndex(t, index)
	memos["memos/new"] = rawMemo("new", "unindexed source")
	result, err := service.Read(t.Context())
	if errorCode(err) != "MAP_SOURCE_CHANGED" || result.Points != nil {
		t.Fatalf("unindexed source disappeared from a successful map: %+v %v", result, err)
	}
	finishIndex(t, index)
	result, err = service.Read(t.Context())
	if err != nil || len(result.Points) != 6 {
		t.Fatalf("source addition did not recover: %+v %v", result, err)
	}
}

func TestMapServiceDeadlineAndQueueCancellation(t *testing.T) {
	service, index, _, _ := mapServiceFixture(t)
	finishIndex(t, index)
	entered, done := make(chan struct{}), make(chan error, 1)
	var gets atomic.Int32
	service.Notes.Source = mapSource{Source: service.Notes.Source, list: func(ctx context.Context, state string, _ *int64, _ string) ([]Memo, string, error) {
		gets.Add(1)
		close(entered)
		<-ctx.Done()
		return nil, "", ctx.Err()
	}}
	ctx, cancel := context.WithCancel(t.Context())
	go func() { _, err := service.Read(ctx); done <- err }()
	<-entered
	queued, stop := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer stop()
	result, err := service.Read(queued)
	if errorCode(err) != "MAP_RESOURCE_LIMIT" || result.Points != nil || gets.Load() != 1 {
		t.Fatalf("queued map ignored budget: %+v %v", result, err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("active read did not cancel: %v", err)
	}
	var count int
	if err := service.Store.index.QueryRow("SELECT COUNT(*) FROM map_layouts").Scan(&count); err != nil || count != 0 {
		t.Fatalf("canceled read published: %d %v", count, err)
	}
}

func TestMapDatabaseWaitsRespectTheRequestBudget(t *testing.T) {
	for _, stage := range []string{"health", "index", "refresh"} {
		t.Run(stage, func(t *testing.T) {
			service, index, _, _ := mapServiceFixture(t)
			finishIndex(t, index)
			var held *sql.Tx
			if stage != "refresh" {
				db := service.Store.index
				if stage == "health" {
					db = service.Store.state
				}
				var err error
				held, err = db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				service.Notes.Source = mapSource{Source: service.Notes.Source, list: func(ctx context.Context, state string, _ *int64, _ string) ([]Memo, string, error) {
					if state == "ARCHIVED" {
						return nil, "", nil
					}
					var err error
					held, err = service.Store.index.BeginTx(ctx, nil)
					memo := testMemo(t, "a", "external edit")
					return []Memo{memo}, "", err
				}}
			}
			var once sync.Once
			release := func() {
				once.Do(func() {
					if held != nil {
						_ = held.Rollback()
					}
				})
			}
			defer release()
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
			defer cancel()
			if stage != "refresh" {
				// The timer prevents a regression from hanging the suite behind a held connection.
				timer := time.AfterFunc(time.Second, release)
				defer timer.Stop()
			}
			started := time.Now()
			result, err := service.Read(ctx)
			if time.Since(started) > 500*time.Millisecond || errorCode(err) != "MAP_RESOURCE_LIMIT" || result.Points != nil {
				t.Fatalf("%s database wait ignored budget: %+v %v", stage, result, err)
			}
		})
	}
}

func TestMapSQLiteBusyWaitRespectsRequestBudget(t *testing.T) {
	service, index, _, _ := mapServiceFixture(t)
	finishIndex(t, index)
	blocker, err := openDB(filepath.Join(service.Store.Settings.IndexDir, "index.sqlite3"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	held, err := blocker.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = held.Rollback() }) }
	defer release()
	timer := time.AfterFunc(time.Second, release)
	defer timer.Stop()
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := service.Read(ctx)
	if time.Since(started) > 500*time.Millisecond || errorCode(err) != "MAP_RESOURCE_LIMIT" || result.Points != nil {
		t.Fatalf("SQLite writer wait ignored budget: elapsed=%v result=%+v error=%v", time.Since(started), result, err)
	}
}

func clientMapIndex(t *testing.T, api *ClientAPI) *VectorIndexService {
	t.Helper()
	store := api.app.Store
	store.Settings.Embedding = embeddingTestConfig("http://embedding.test")
	for _, name := range []string{"a", "b", "archived"} {
		raw := rawMemo(name, "private body "+name)
		if name == "archived" {
			raw["state"] = "ARCHIVED"
		}
		memo, _ := normalizeMemo(raw)
		if err := store.Refresh(memo.Name, &memo); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Advance(nowText(), true); err != nil {
		t.Fatal(err)
	}
	source := fakeClient(store.Settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		if r.URL.Path == "/api/v1/memos" {
			items := []any{}
			for _, name := range []string{"a", "b", "archived"} {
				raw := rawMemo(name, "private body "+name)
				if name == "archived" {
					raw["state"] = "ARCHIVED"
				}
				if raw["state"] == r.URL.Query().Get("state") {
					items = append(items, raw)
				}
			}
			return 200, map[string]any{"memos": items}, nil
		}
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/memos/")
		raw := rawMemo(name, "private body "+name)
		if name == "archived" {
			raw["state"] = "ARCHIVED"
		}
		return 200, raw, nil
	})
	t.Cleanup(source.Close)
	api.app.Notes.Source = source
	index := NewVectorIndexService(store, source, &indexEmbedding{dimensions: store.Settings.Embedding.Dimensions})
	finishIndex(t, index)
	return index
}
