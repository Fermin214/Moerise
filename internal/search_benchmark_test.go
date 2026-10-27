package moerise

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const benchmarkNotes = 10000

// Dense, deterministic vectors and unique texts prevent duplicate-content cache reuse.
type benchmarkEmbedding struct {
	dimensions int
	calls      int
	texts      int
}

func (p *benchmarkEmbedding) Close() {}

func (p *benchmarkEmbedding) Embed(ctx context.Context, texts []string, purpose EmbeddingPurpose) (EmbeddingResult, error) {
	if purpose == EmbeddingDocument {
		p.calls++
		p.texts += len(texts)
	}
	result := EmbeddingResult{Vectors: make([][]float32, len(texts))}
	for i, text := range texts {
		hash := fnv.New64a()
		_, _ = hash.Write([]byte(text))
		state := hash.Sum64()
		vector := make([]float32, p.dimensions)
		for j := range vector {
			state ^= state << 13
			state ^= state >> 7
			state ^= state << 17
			vector[j] = 0.1 + float32(state&65535)/65535
		}
		result.Vectors[i] = vector
	}
	return result, ctx.Err()
}

func searchBenchmarkFixture(b *testing.B, batch int) (*Store, *MemosClient, *benchmarkEmbedding) {
	b.Helper()
	settings := DefaultSettings()
	settings.Memos.InstanceID, settings.Memos.User = "benchmark", "users/owner"
	settings.Memos.BaseURL = "http://memos.test"
	settings.Embedding = embeddingTestConfig("http://embedding.test")
	settings.Embedding.Dimensions = 1024
	settings.Embedding.BatchSize = batch
	root := b.TempDir()
	settings.StateDir, settings.IndexDir = filepath.Join(root, "state"), filepath.Join(root, "index")
	store, err := NewStore(settings)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	memos := make(map[string]map[string]any, benchmarkNotes)
	for i := range benchmarkNotes {
		raw := rawMemo(fmt.Sprintf("%05d", i), fmt.Sprintf("Semantic benchmark note %05d. Topic %d. #benchmark", i, i%20))
		memo, err := normalizeMemo(raw)
		if err != nil {
			b.Fatal(err)
		}
		memos[memo.Name] = raw
		if err := store.Refresh(memo.Name, &memo); err != nil {
			b.Fatal(err)
		}
	}
	if err := store.Advance(nowText(), true); err != nil {
		b.Fatal(err)
	}
	source := fakeClient(settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		raw := memos[strings.TrimPrefix(r.URL.Path, "/api/v1/")]
		if raw == nil {
			return 404, nil, nil
		}
		return 200, raw, nil
	})
	b.Cleanup(source.Close)
	return store, source, &benchmarkEmbedding{dimensions: settings.Embedding.Dimensions}
}

func buildSearchBenchmarkIndex(b *testing.B, store *Store, source Source, provider *benchmarkEmbedding) {
	b.Helper()
	index := NewVectorIndexService(store, source, provider)
	for {
		worked, err := index.Step(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		if !worked {
			break
		}
	}
	status, err := store.VectorStatus()
	if err != nil || !status.Ready || status.Active.Chunks != benchmarkNotes ||
		status.Active.CompletedChunks != benchmarkNotes || provider.texts != benchmarkNotes {
		b.Fatalf("published unique vectors: status=%+v texts=%d err=%v", status, provider.texts, err)
	}
}

// Linux RSS includes the fixture, SQLite and Go runtime; HWM is process lifetime.
func reportSearchBenchmarkMemory(b *testing.B) {
	b.Helper()
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "VmRSS:":
			b.ReportMetric(value*1024, "process-RSS-bytes")
		case "VmHWM:":
			b.ReportMetric(value*1024, "process-peak-RSS-bytes")
		}
	}
}

func reportSearchBenchmarkLatency(b *testing.B, elapsed []int64) {
	b.Helper()
	slices.Sort(elapsed)
	if len(elapsed) > 0 {
		b.ReportMetric(float64(elapsed[(len(elapsed)-1)/2])/1e6, "p50-ms")
		b.ReportMetric(float64(elapsed[(len(elapsed)*95+99)/100-1])/1e6, "p95-ms")
	}
	reportSearchBenchmarkMemory(b)
}

// Measure projection-to-publication with an empty vector cache on every iteration.
// Projection import, remote model latency and network transport are excluded.
func BenchmarkVectorIndex10000x1024(b *testing.B) {
	for _, batch := range []int{5, 20} {
		b.Run(fmt.Sprintf("batch%d", batch), func(b *testing.B) {
			store, source, provider := searchBenchmarkFixture(b, batch)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				b.StopTimer()
				if _, err := store.index.Exec("DELETE FROM vector_generations; DELETE FROM vector_cache"); err != nil {
					b.Fatal(err)
				}
				provider.calls, provider.texts = 0, 0
				b.StartTimer()
				buildSearchBenchmarkIndex(b, store, source, provider)
			}
			b.StopTimer()
			b.ReportMetric(float64(provider.calls), "model-batches/op")
			b.ReportMetric(float64(benchmarkNotes)*float64(b.N)/b.Elapsed().Seconds(), "chunks/s")
			var disk int64
			for _, name := range []string{"index.sqlite3", "index.sqlite3-wal", "index.sqlite3-shm"} {
				if info, err := os.Stat(filepath.Join(store.Settings.IndexDir, name)); err == nil {
					disk += info.Size()
				}
			}
			b.ReportMetric(float64(disk), "index-files-bytes")
			reportSearchBenchmarkMemory(b)
		})
	}
}

// Each iteration starts a new application matrix cache; filesystem pages may be warm.
func BenchmarkSemanticCold10000x1024(b *testing.B) {
	store, source, provider := searchBenchmarkFixture(b, 20)
	buildSearchBenchmarkIndex(b, store, source, provider)
	request := SearchRequest{Query: "semantic benchmark question", Mode: "semantic", State: "NORMAL"}
	elapsed := make([]int64, 0, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		start := time.Now()
		service := NewSearchService(store, source, provider)
		candidates, status, err := service.semanticCandidates(context.Background(), request)
		elapsed = append(elapsed, time.Since(start).Nanoseconds())
		if err != nil || !status.Used || len(candidates) != searchCandidates {
			b.Fatalf("cold semantic query: candidates=%d status=%+v err=%v", len(candidates), status, err)
		}
	}
	b.StopTimer()
	reportSearchBenchmarkLatency(b, elapsed)
}

// Build and warm the published matrix before measuring candidate scans.
func BenchmarkSemanticCache10000x1024(b *testing.B) {
	store, source, provider := searchBenchmarkFixture(b, 20)
	buildSearchBenchmarkIndex(b, store, source, provider)
	service := NewSearchService(store, source, provider)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	matrix, err := service.vectors(context.Background())
	if err != nil || len(matrix.entries) != benchmarkNotes {
		b.Fatalf("published matrix: %v", err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	request := SearchRequest{Query: "semantic benchmark question", Mode: "semantic", State: "NORMAL"}
	elapsed := make([]int64, 0, b.N)
	b.ReportAllocs()
	b.SetBytes(int64(benchmarkNotes * store.Settings.Embedding.Dimensions * 4))
	b.ResetTimer()
	for b.Loop() {
		start := time.Now()
		candidates, status, err := service.semanticCandidates(context.Background(), request)
		elapsed = append(elapsed, time.Since(start).Nanoseconds())
		if err != nil || !status.Used || len(candidates) != searchCandidates || service.matrix != matrix {
			b.Fatalf("semantic cache query: candidates=%d status=%+v err=%v", len(candidates), status, err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(retained), "matrix-retained-heap-bytes")
	b.ReportMetric(float64(benchmarkNotes*store.Settings.Embedding.Dimensions*4), "matrix-vector-bytes")
	reportSearchBenchmarkLatency(b, elapsed)
}

// Full service calls include fusion, identity checks and ten live source rereads.
// Worker latency excludes time waiting for a free worker.
func BenchmarkSearch10000x1024(b *testing.B) {
	store, source, provider := searchBenchmarkFixture(b, 20)
	buildSearchBenchmarkIndex(b, store, source, provider)
	service := NewSearchService(store, source, provider)
	if _, err := service.vectors(context.Background()); err != nil {
		b.Fatal(err)
	}
	for _, mode := range []string{"semantic", "hybrid"} {
		for _, workers := range []int{1, 2, 4} {
			b.Run(fmt.Sprintf("%s/workers%d", mode, workers), func(b *testing.B) {
				request := SearchRequest{Query: "semantic benchmark", Mode: mode, State: "NORMAL", Limit: 10}
				elapsed := make([]int64, b.N)
				var next atomic.Int64
				var group sync.WaitGroup
				b.ReportAllocs()
				b.ResetTimer()
				for range workers {
					group.Go(func() {
						for {
							i := int(next.Add(1) - 1)
							if i >= b.N {
								return
							}
							start := time.Now()
							response, err := service.Search(context.Background(), request)
							elapsed[i] = time.Since(start).Nanoseconds()
							if err != nil || !response.Semantic.Used || len(response.Results) != request.Limit {
								b.Errorf("service search: results=%d semantic=%+v err=%v", len(response.Results), response.Semantic, err)
								return
							}
						}
					})
				}
				group.Wait()
				b.StopTimer()
				b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "queries/s")
				reportSearchBenchmarkLatency(b, elapsed)
			})
		}
	}
}
