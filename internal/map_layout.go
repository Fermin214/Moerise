package moerise

import (
	"context"
	"encoding/json"
	"math"
	"slices"
)

const mapAlgorithm = "pca-fixed-v1"
const mapPointGap = 0.22

type MapPoint struct {
	Name       string  `json:"name"`
	SourceHash string  `json:"source_hash"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Semantic   bool    `json:"semantic"`
	vectorHash string
}

type mapDocument struct {
	name, sourceHash, vectorHash string
	vector                       []float64
}

type mapSnapshot struct {
	generation, space string
	revision          int64
	dimensions        int
	documents         []mapDocument
}

type mapBasis struct {
	Mean  []float64   `json:"mean"`
	Axes  [][]float64 `json:"axes"`
	Scale float64     `json:"scale"`
}

type mapLayout struct {
	version string
	basis   mapBasis
	points  []MapPoint
}

func mapNorm(vector []float64) float64 {
	var square float64
	for _, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0
		}
		square += value * value
	}
	return math.Sqrt(square)
}

func mapNormalize(vector []float64) bool {
	norm := mapNorm(vector)
	if norm < 1e-12 {
		return false
	}
	for i := range vector {
		vector[i] /= norm
	}
	return true
}

func mapDot(a, b []float64) float64 {
	var value float64
	for i := range a {
		value += a[i] * b[i]
	}
	return value
}

// Multiply by covariance without constructing a dimensions-squared matrix.
func mapCovariance(ctx context.Context, documents []mapDocument, mean, axis []float64) ([]float64, error) {
	result := make([]float64, len(mean))
	count := 0
	for i, document := range documents {
		if i%64 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if len(document.vector) == 0 {
			continue
		}
		count++
		var dot float64
		for j, value := range document.vector {
			dot += (value - mean[j]) * axis[j]
		}
		for j, value := range document.vector {
			result[j] += (value - mean[j]) * dot
		}
	}
	if count != 0 {
		for j := range result {
			result[j] /= float64(count)
		}
	}
	return result, ctx.Err()
}

func mapOrthogonalize(vector []float64, axes [][]float64) {
	for _, axis := range axes {
		dot := mapDot(vector, axis)
		for j := range vector {
			vector[j] -= dot * axis[j]
		}
	}
}

func buildMapBasis(ctx context.Context, snapshot mapSnapshot) (mapBasis, error) {
	basis := mapBasis{Mean: make([]float64, snapshot.dimensions), Scale: 0.25}
	count := 0
	for i, document := range snapshot.documents {
		if i%64 == 0 && ctx.Err() != nil {
			return mapBasis{}, ctx.Err()
		}
		if len(document.vector) == 0 {
			continue
		}
		count++
		for j, value := range document.vector {
			basis.Mean[j] += value
		}
	}
	if count == 0 {
		return basis, nil
	}
	for j := range basis.Mean {
		basis.Mean[j] /= float64(count)
	}
	diagonal := make([]float64, snapshot.dimensions)
	for i, document := range snapshot.documents {
		if i%64 == 0 && ctx.Err() != nil {
			return mapBasis{}, ctx.Err()
		}
		for j, value := range document.vector {
			difference := value - basis.Mean[j]
			diagonal[j] += difference * difference / float64(count)
		}
	}
	for range 2 {
		axis := make([]float64, snapshot.dimensions)
		best, selected := -1.0, -1
		for j, variance := range diagonal {
			remaining := 1.0
			for _, previous := range basis.Axes {
				remaining -= previous[j] * previous[j]
			}
			if variance > best && remaining > 1e-12 {
				selected, best = j, variance
			}
		}
		if selected >= 0 {
			// Dense deterministic seeds avoid getting stuck on a coordinate-aligned lesser eigenvector.
			state := uint64(0x9e3779b97f4a7c15) + uint64(len(basis.Axes))
			for j := range axis {
				state ^= state << 13
				state ^= state >> 7
				state ^= state << 17
				axis[j] = 0.05 * (float64(state&65535)/32767.5 - 1)
			}
			axis[selected] = 1
			mapOrthogonalize(axis, basis.Axes)
			mapNormalize(axis)
		}
		for range 64 {
			next, err := mapCovariance(ctx, snapshot.documents, basis.Mean, axis)
			if err != nil {
				return mapBasis{}, err
			}
			mapOrthogonalize(next, basis.Axes)
			if !mapNormalize(next) {
				break
			}
			converged := math.Abs(mapDot(next, axis)) > 1-1e-10
			axis = next
			if converged {
				break
			}
		}
		// A fixed sign avoids mirrored maps on deterministic rebuilds.
		largest := 0
		for j, value := range axis {
			if math.Abs(value) > math.Abs(axis[largest]) {
				largest = j
			}
		}
		if axis[largest] < 0 {
			for j := range axis {
				axis[j] = -axis[j]
			}
		}
		covariance, err := mapCovariance(ctx, snapshot.documents, basis.Mean, axis)
		if err != nil {
			return mapBasis{}, err
		}
		variance := math.Max(0, mapDot(covariance, axis))
		basis.Scale = math.Max(basis.Scale, math.Sqrt(variance))
		for j := range diagonal {
			diagonal[j] = math.Max(0, diagonal[j]-variance*axis[j]*axis[j])
		}
		basis.Axes = append(basis.Axes, axis)
	}
	return basis, nil
}

type mapCell struct{ x, y int }
type mapGrid map[mapCell][]MapPoint

func (grid mapGrid) add(point MapPoint) {
	cell := mapCell{int(math.Floor(point.X / mapPointGap)), int(math.Floor(point.Y / mapPointGap))}
	grid[cell] = append(grid[cell], point)
}

func (grid mapGrid) free(x, y float64) bool {
	cell := mapCell{int(math.Floor(x / mapPointGap)), int(math.Floor(y / mapPointGap))}
	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			for _, point := range grid[mapCell{cell.x + dx, cell.y + dy}] {
				if math.Hypot(x-point.X, y-point.Y) < mapPointGap {
					return false
				}
			}
		}
	}
	return true
}

func (grid mapGrid) place(ctx context.Context, point MapPoint) (MapPoint, error) {
	x, y := point.X, point.Y
	for step := 0; !grid.free(point.X, point.Y); step++ {
		if step%64 == 0 && ctx.Err() != nil {
			return MapPoint{}, ctx.Err()
		}
		radius := mapPointGap * math.Sqrt(float64(step+1))
		angle := float64(step) * math.Pi * (3 - math.Sqrt(5))
		point.X, point.Y = x+radius*math.Cos(angle), y+radius*math.Sin(angle)
	}
	grid.add(point)
	return point, ctx.Err()
}

func buildMapLayout(ctx context.Context, snapshot mapSnapshot, previous *mapLayout) (*mapLayout, error) {
	layout := &mapLayout{points: make([]MapPoint, 0, len(snapshot.documents))}
	if previous != nil {
		layout.basis = previous.basis
	}
	if len(layout.basis.Axes) == 0 {
		basis, err := buildMapBasis(ctx, snapshot)
		if err != nil {
			return nil, err
		}
		layout.basis = basis
	}
	old := map[string]MapPoint{}
	if previous != nil {
		for _, point := range previous.points {
			old[point.Name] = point
		}
	}
	grid := mapGrid{}
	// Reserve every unchanged position before placing new or edited documents.
	for _, document := range snapshot.documents {
		if point, ok := old[document.name]; ok && point.vectorHash == document.vectorHash {
			grid.add(point)
		}
	}
	for i, document := range snapshot.documents {
		if i%64 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		point, unchanged := old[document.name]
		unchanged = unchanged && point.vectorHash == document.vectorHash
		if !unchanged {
			point = MapPoint{Name: document.name, Semantic: len(document.vector) != 0}
			if point.Semantic {
				for j, value := range document.vector {
					point.X += (value - layout.basis.Mean[j]) * layout.basis.Axes[0][j] / layout.basis.Scale
					point.Y += (value - layout.basis.Mean[j]) * layout.basis.Axes[1][j] / layout.basis.Scale
				}
			} else {
				// No-text notes are separate points, not fabricated semantic vectors.
				point.X = 4
			}
			var err error
			point, err = grid.place(ctx, point)
			if err != nil {
				return nil, err
			}
		}
		point.SourceHash, point.vectorHash = document.sourceHash, document.vectorHash
		layout.points = append(layout.points, point)
	}
	version, err := json.Marshal([]any{mapAlgorithm, snapshot.space, layout.basis, layout.points})
	if err != nil {
		return nil, err
	}
	layout.version = hashBytes(version)
	return layout, ctx.Err()
}

func validMapBasis(basis mapBasis, dimensions int) bool {
	if len(basis.Mean) != dimensions || basis.Scale < 0.25 || math.IsInf(basis.Scale, 0) || math.IsNaN(basis.Scale) ||
		(len(basis.Axes) != 0 && len(basis.Axes) != 2) {
		return false
	}
	for _, vector := range append(slices.Clone(basis.Axes), basis.Mean) {
		if len(vector) != dimensions {
			return false
		}
		for _, value := range vector {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return false
			}
		}
	}
	if len(basis.Axes) == 2 && (math.Abs(mapNorm(basis.Axes[0])-1) > 1e-6 ||
		(dimensions > 1 && math.Abs(mapNorm(basis.Axes[1])-1) > 1e-6) ||
		(dimensions == 1 && mapNorm(basis.Axes[1]) > 1e-6) || math.Abs(mapDot(basis.Axes[0], basis.Axes[1])) > 1e-6) {
		return false
	}
	return true
}
