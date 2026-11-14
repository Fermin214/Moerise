package moerise

import (
	"context"
	"math"
)

func anchorMapPoint(point *MapPoint, vector []float64, anchors []mapAnchor) {
	best, similarity := -1, -math.MaxFloat64
	for i, anchor := range anchors {
		if value := mapDot(vector, anchor.Vector); value > similarity {
			best, similarity = i, value
		}
	}
	if best >= 0 {
		anchor := anchors[best]
		point.X = anchor.X + (point.X-anchor.FromX)*anchor.Scale
		point.Y = anchor.Y + (point.Y-anchor.FromY)*anchor.Scale
	}
}

// Keep topic clouds apart without reassigning semantic membership. Persisted
// anchors project future notes into the same frame, leaving old notes stable.
func separateMapTopics(ctx context.Context, snapshot mapSnapshot, layout *mapLayout) error {
	indices := map[string]int{}
	for i, document := range snapshot.documents {
		indices[document.name] = i
	}
	anchors, radii := make([]mapAnchor, len(layout.topics)), make([]float64, len(layout.topics))
	for i, topic := range layout.topics {
		anchor := mapAnchor{Vector: make([]float64, snapshot.dimensions), FromX: topic.X, FromY: topic.Y, X: topic.X * 2, Y: topic.Y * 2}
		var radius float64
		for _, name := range topic.Members {
			index := indices[name]
			for j, value := range snapshot.documents[index].vector {
				anchor.Vector[j] += value
			}
			point := layout.points[index]
			radius = math.Max(radius, math.Hypot(point.X-topic.X, point.Y-topic.Y))
		}
		if !mapNormalize(anchor.Vector) {
			copy(anchor.Vector, snapshot.documents[indices[topic.Members[0]]].vector)
		}
		radii[i] = 0.5 + 0.35*math.Sqrt(float64(len(topic.Members)*len(layout.topics))/float64(max(1, len(snapshot.documents))))
		anchor.Scale = math.Min(3, radii[i]/math.Max(0.01, radius))
		anchors[i] = anchor
	}
	for range 100 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		for i := range anchors {
			for j := i + 1; j < len(anchors); j++ {
				dx, dy := anchors[j].X-anchors[i].X, anchors[j].Y-anchors[i].Y
				distance, gap := math.Hypot(dx, dy), radii[i]+radii[j]+0.45
				if distance >= gap {
					continue
				}
				if distance < 1e-8 {
					angle := float64(i*13+j) * 2.399963
					dx, dy, distance = math.Cos(angle)*0.001, math.Sin(angle)*0.001, 0.001
				}
				force := (gap - distance) * 0.3
				x, y := dx/distance*force, dy/distance*force
				anchors[i].X -= x
				anchors[i].Y -= y
				anchors[j].X += x
				anchors[j].Y += y
			}
		}
	}
	layout.basis.Anchors = anchors
	grid := newMapGrid(len(layout.points))
	for i := range layout.points {
		point := &layout.points[i]
		if point.Semantic {
			anchorMapPoint(point, snapshot.documents[i].vector, anchors)
		} else {
			for _, anchor := range anchors {
				point.X = math.Max(point.X, anchor.X+2)
			}
		}
		placed, err := grid.place(ctx, *point)
		if err != nil {
			return err
		}
		*point = placed
	}
	return ctx.Err()
}
