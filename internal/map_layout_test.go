package moerise

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"testing"
)

func mapTestDocument(name string, values ...float64) mapDocument {
	vector := slices.Clone(values)
	if !mapNormalize(vector) {
		vector = nil
	}
	data, _ := json.Marshal(vector)
	return mapDocument{name: "memos/" + name, sourceHash: name, vectorHash: hashBytes(data), vector: vector}
}

func mapPointByName(t *testing.T, layout *mapLayout, name string) MapPoint {
	t.Helper()
	for _, point := range layout.points {
		if point.Name == "memos/"+name {
			return point
		}
	}
	t.Fatalf("missing map point %s", name)
	return MapPoint{}
}

func TestMapSemanticProjectionAndDeterministicRebuild(t *testing.T) {
	snapshot := mapSnapshot{space: "test", dimensions: 3, documents: []mapDocument{
		mapTestDocument("a", 1, 0.04, 0), mapTestDocument("b", 1, -0.04, 0),
		mapTestDocument("c", -1, 0.04, 0), mapTestDocument("d", -1, -0.04, 0),
		mapTestDocument("e", 0, 1, 0), mapTestDocument("f", 0, -1, 0),
	}}
	first, err := buildMapLayout(t.Context(), snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildMapLayout(t.Context(), snapshot, nil)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("rebuild is not deterministic: %v", err)
	}
	a, b, c := mapPointByName(t, first, "a"), mapPointByName(t, first, "b"), mapPointByName(t, first, "c")
	if math.Hypot(a.X-b.X, a.Y-b.Y)*3 >= math.Hypot(a.X-c.X, a.Y-c.Y) {
		t.Fatalf("semantic neighbors not closer: a=%+v b=%+v c=%+v", a, b, c)
	}
	if math.Abs(mapDot(first.basis.Axes[0], first.basis.Axes[1])) > 1e-8 ||
		math.Abs(mapNorm(first.basis.Axes[0])-1) > 1e-8 || math.Abs(mapNorm(first.basis.Axes[1])-1) > 1e-8 {
		t.Fatalf("invalid principal axes: %+v", first.basis)
	}
}

func TestMapIncrementalChangesKeepEveryUnchangedCoordinate(t *testing.T) {
	snapshot := mapSnapshot{space: "test", dimensions: 3, documents: []mapDocument{
		mapTestDocument("a", 1, 0, 0), mapTestDocument("b", 0, 1, 0),
		mapTestDocument("c", -1, 0, 0), mapTestDocument("d", 0, -1, 0), mapTestDocument("file"),
	}}
	first, err := buildMapLayout(t.Context(), snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.documents = []mapDocument{
		mapTestDocument("a", 1, 0, 0), mapTestDocument("b", -1, 0.1, 0),
		mapTestDocument("d", 0, -1, 0), mapTestDocument("file"), mapTestDocument("new", 1, 0.01, 0),
	}
	snapshot.documents[0].sourceHash = "metadata-edited"
	second, err := buildMapLayout(t.Context(), snapshot, first)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "d", "file"} {
		before, after := mapPointByName(t, first, name), mapPointByName(t, second, name)
		if before.X != after.X || before.Y != after.Y {
			t.Fatalf("unrelated point moved: %s before=%+v after=%+v", name, before, after)
		}
	}
	before, after := mapPointByName(t, first, "b"), mapPointByName(t, second, "b")
	if before.X == after.X && before.Y == after.Y || first.version == second.version ||
		mapPointByName(t, second, "a").SourceHash != "metadata-edited" || !reflect.DeepEqual(first.basis, second.basis) {
		t.Fatalf("edited point/version/basis did not update correctly: %+v", second)
	}
	for _, point := range second.points {
		if point.Name == "memos/c" {
			t.Fatal("deleted point remained")
		}
	}
	for i, point := range second.points {
		for _, other := range second.points[i+1:] {
			if math.Hypot(point.X-other.X, point.Y-other.Y) < mapPointGap-1e-12 {
				t.Fatalf("points overlap: %+v %+v", point, other)
			}
		}
	}
}

func TestMapPrincipalAxisIsNotTrappedByLargestDiagonal(t *testing.T) {
	snapshot := mapSnapshot{space: "test", dimensions: 10}
	for i := range 8 {
		vector := make([]float64, 10)
		if i < 6 {
			for j := range 9 {
				vector[j] = 1.0 / 3
			}
		} else {
			vector[9] = 1
		}
		if i%2 == 0 {
			for j := range vector {
				vector[j] = -vector[j]
			}
		}
		snapshot.documents = append(snapshot.documents, mapTestDocument(fmt.Sprintf("%03d", i), vector...))
	}
	basis, err := buildMapBasis(t.Context(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	top := make([]float64, 10)
	for j := range 9 {
		top[j] = 1.0 / 3
	}
	if math.Abs(mapDot(basis.Axes[0], top)) < 0.999 {
		t.Fatalf("selected lesser invariant subspace: %+v", basis.Axes[0])
	}
}

func TestMapEmptySingletonDuplicateAndNoVectorNotes(t *testing.T) {
	for _, dimensions := range []int{1, 3} {
		snapshot := mapSnapshot{space: "test", dimensions: dimensions, documents: []mapDocument{}}
		empty, err := buildMapLayout(t.Context(), snapshot, nil)
		if err != nil || len(empty.points) != 0 || !validMapBasis(empty.basis, dimensions) {
			t.Fatalf("empty map: %+v %v", empty, err)
		}
		vector := make([]float64, dimensions)
		vector[0] = 1
		for i := range 30 {
			values := vector
			if i%3 == 0 {
				values = nil
			}
			snapshot.documents = append(snapshot.documents, mapTestDocument(fmt.Sprintf("%03d", i), values...))
		}
		layout, err := buildMapLayout(t.Context(), snapshot, empty)
		if err != nil || len(layout.points) != 30 {
			t.Fatalf("notes dropped: %+v %v", layout, err)
		}
		for i, point := range layout.points {
			if point.Semantic != (i%3 != 0) || math.IsNaN(point.X) || math.IsNaN(point.Y) {
				t.Fatalf("invalid point: %+v", point)
			}
			for _, other := range layout.points[i+1:] {
				if math.Hypot(point.X-other.X, point.Y-other.Y) < mapPointGap-1e-12 {
					t.Fatalf("duplicate notes cannot be separately selected: %+v %+v", point, other)
				}
			}
		}
		stable, err := buildMapLayout(t.Context(), snapshot, layout)
		if err != nil || !reflect.DeepEqual(stable, layout) {
			t.Fatalf("refresh moved duplicate notes: %v", err)
		}
	}
}

func TestMapCalculationAndCollisionPlacementCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	snapshot := mapSnapshot{dimensions: 3, documents: []mapDocument{mapTestDocument("a", 1, 0, 0)}}
	if _, err := buildMapLayout(ctx, snapshot, nil); err != context.Canceled {
		t.Fatalf("calculation ignored cancellation: %v", err)
	}
	grid := mapGrid{}
	grid.add(MapPoint{})
	if _, err := grid.place(ctx, MapPoint{}); err != context.Canceled {
		t.Fatalf("collision placement ignored cancellation: %v", err)
	}
}

func BenchmarkMapLayout(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("%d-notes-1024-dimensions", count), func(b *testing.B) {
			snapshot := mapSnapshot{dimensions: 1024, space: "benchmark"}
			state := uint64(19)
			for i := range count {
				vector := make([]float64, snapshot.dimensions)
				for j := range vector {
					state ^= state << 13
					state ^= state >> 7
					state ^= state << 17
					vector[j] = float64(state&65535)/32767.5 - 1
				}
				snapshot.documents = append(snapshot.documents, mapTestDocument(fmt.Sprintf("%05d", i), vector...))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				layout, err := buildMapLayout(b.Context(), snapshot, nil)
				if err != nil || len(layout.points) != count {
					b.Fatalf("layout failed: %v", err)
				}
			}
		})
	}
}
