package moerise

import (
	"math"
	"slices"
	"time"
)

type mapDocument struct {
	name, sourceHash, vectorHash string
	vector                       []float64
	tags                         []string
	keywords                     []string
}

type mapSnapshot struct {
	generation, space string
	revision          int64
	dimensions        int
	documents         []mapDocument
}

type mapLayout struct {
	algorithm     string
	frame         string
	parentVersion string
	rebuildToken  string
	version       string
	basis         mapBasis
	points        []MapPoint
	topics        []MapTopic
	links         []MapLink
	generation    string
	revision      int64
	generatedAt   time.Time
}

type mapBasis struct {
	Mean    []float64   `json:"mean"`
	Axes    [][]float64 `json:"axes"`
	Scale   float64     `json:"scale"`
	Anchors []mapAnchor `json:"anchors,omitempty"`
}

type mapAnchor struct {
	Vector []float64 `json:"vector"`
	FromX  float64   `json:"from_x"`
	FromY  float64   `json:"from_y"`
	X      float64   `json:"x"`
	Y      float64   `json:"y"`
	Scale  float64   `json:"scale"`
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
	if len(basis.Axes) == 2 {
		var dot float64
		for index, value := range basis.Axes[0] {
			dot += value * basis.Axes[1][index]
		}
		if math.Abs(mapNorm(basis.Axes[0])-1) > 1e-6 ||
			(dimensions > 1 && math.Abs(mapNorm(basis.Axes[1])-1) > 1e-6) ||
			(dimensions == 1 && mapNorm(basis.Axes[1]) > 1e-6) || math.Abs(dot) > 1e-6 {
			return false
		}
	}
	if len(basis.Anchors) > 12 {
		return false
	}
	for _, anchor := range basis.Anchors {
		if len(anchor.Vector) != dimensions || math.Abs(mapNorm(anchor.Vector)-1) > 1e-6 || anchor.Scale <= 0 {
			return false
		}
		for _, value := range append(slices.Clone(anchor.Vector), anchor.FromX, anchor.FromY, anchor.X, anchor.Y, anchor.Scale) {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return false
			}
		}
	}
	return true
}
