package moerise

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestSemanticVectorPartitionsMatchFullScan(t *testing.T) {
	random := rand.New(rand.NewPCG(31, 67))
	query := make([]float32, 32)
	for i := range query {
		query[i] = 0.1 + random.Float32()
	}
	norm := vectorNorm(query)
	from, until := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)
	for _, count := range []int{0, 1, 99, 100, 101, 1001, 10000} {
		entries := make([]searchVector, count)
		for i := range entries {
			entry := &entries[i]
			entry.candidate = searchCandidate{name: fmt.Sprintf("memos/%05d", i/3), sourceHash: "original",
				chunk: TextChunk{Ordinal: i % 3, ByteStart: i * 10, Content: fmt.Sprintf("原文%d", i)}}
			entry.state, entry.tags, entry.createTime = "NORMAL", []string{"a", "b"}, "2026-01-03T00:00:00Z"
			if i%3 == 0 {
				entry.state = "ARCHIVED"
			}
			if i%5 == 0 {
				entry.tags = []string{"a"}
				entry.createTime = "2026-01-01T00:00:00Z"
			}
			entry.vector = make([]float32, len(query))
			for j := range entry.vector {
				entry.vector[j] = float32(random.NormFloat64())
				if i%7 == 0 {
					entry.vector[j] = query[j]
				}
			}
			entry.norm = vectorNorm(entry.vector)
		}
		random.Shuffle(len(entries), func(i, j int) { entries[i], entries[j] = entries[j], entries[i] })
		for _, request := range []SearchRequest{
			{State: "ALL"},
			{State: "NORMAL", Tags: []string{"a", "b"}},
			{State: "ARCHIVED", createdFrom: &from, createdUntil: &until},
			{State: "NORMAL", Tags: []string{"absent"}},
		} {
			all := []searchCandidate{}
			for _, entry := range entries {
				if !searchAccepts(request, entry.state, entry.tags) || !searchDateAccepts(request, entry.createTime) {
					continue
				}
				dot := float64(0)
				for j, value := range entry.vector {
					dot += float64(value) * float64(query[j])
				}
				if score := dot / (norm * entry.norm); score > 0 {
					item := entry.candidate
					item.score = score
					all = append(all, item)
				}
			}
			want := rankCandidatesForTest(all, false)
			for _, workers := range []int{1, 2, 3, 4} {
				got, err := rankSemanticVectors(t.Context(), request, entries, query, workers)
				if err != nil || len(got) != len(want) {
					t.Fatalf("count=%d workers=%d request=%+v got=%d want=%d err=%v", count, workers, request, len(got), len(want), err)
				}
				for i := range got {
					if !reflect.DeepEqual(got[i], want[i]) {
						t.Fatalf("count=%d workers=%d request=%+v rank=%d got=%+v want=%+v", count, workers, request, i, got[i], want[i])
					}
				}
			}
		}
	}
}

type semanticCancelContext struct {
	context.Context
	cancel context.CancelFunc
	polls  atomic.Int32
}

func (c *semanticCancelContext) Err() error {
	if c.polls.Add(1) == 3 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestSemanticVectorPartitionsCancelDuringScan(t *testing.T) {
	entries := make([]searchVector, 10000)
	for i := range entries {
		entries[i] = searchVector{state: "NORMAL", vector: []float32{1}, norm: 1,
			candidate: searchCandidate{name: fmt.Sprintf("memos/%05d", i)}}
	}
	for _, workers := range []int{1, 4} {
		ctx, cancel := context.WithCancel(t.Context())
		inFlight := &semanticCancelContext{Context: ctx, cancel: cancel}
		got, err := rankSemanticVectors(inFlight, SearchRequest{State: "NORMAL"}, entries, []float32{1}, workers)
		cancel()
		if !errors.Is(err, context.Canceled) || got != nil {
			t.Fatalf("workers=%d returned partial candidates after cancellation: count=%d err=%v", workers, len(got), err)
		}
	}
}

func TestSemanticBatchDotsMatchScalar(t *testing.T) {
	random := rand.New(rand.NewPCG(37, 71))
	for _, dimensions := range []int{0, 1, 3, 16, 31, 32, 1024, 1025, 4096} {
		for range 40 {
			query := make([]float32, dimensions)
			for i := range query {
				query[i] = float32(math.Ldexp(random.NormFloat64(), random.IntN(100)-50))
			}
			var entries [4]*searchVector
			var want [4]float64
			for i := range entries {
				entries[i] = &searchVector{vector: make([]float32, dimensions)}
				for j := range query {
					entries[i].vector[j] = float32(math.Ldexp(random.NormFloat64(), random.IntN(100)-50))
					want[i] += float64(entries[i].vector[j]) * float64(query[j])
				}
			}
			got := semanticBatchDots(entries, query)
			for i := range got {
				if math.Float64bits(got[i]) != math.Float64bits(want[i]) {
					t.Fatalf("dimensions=%d vector=%d got=%v want=%v", dimensions, i, got[i], want[i])
				}
			}
		}
	}
}

var semanticKernelResult []searchCandidate

func BenchmarkSemanticVectorKernel(b *testing.B) {
	random := rand.New(rand.NewPCG(31, 67))
	query := make([]float32, 1024)
	for i := range query {
		query[i] = 0.1 + random.Float32()
	}
	entries := make([]searchVector, 10000)
	for i := range entries {
		entry := &entries[i]
		entry.state = "NORMAL"
		entry.candidate = searchCandidate{name: fmt.Sprintf("memos/%05d", i)}
		entry.vector = make([]float32, len(query))
		for j := range entry.vector {
			entry.vector[j] = 0.1 + random.Float32()
		}
		entry.norm = vectorNorm(entry.vector)
	}
	for _, workers := range []int{1, 2, 4} {
		b.Run(fmt.Sprintf("workers%d", workers), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				items, err := rankSemanticVectors(context.Background(), SearchRequest{State: "NORMAL"}, entries, query, workers)
				if err != nil || len(items) != 100 {
					b.Fatalf("items=%d err=%v", len(items), err)
				}
				semanticKernelResult = items
			}
		})
	}
}
