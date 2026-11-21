package moerise

import (
	"math"
	"time"
)

type mapDocument struct {
	name, sourceHash, vectorHash string
	vector                       []float64
	tags                         []string
	keywords                     []string
	excerpt                      string
	createTime                   string
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
	points        []MapPoint
	topics        []MapTopic
	links         []MapLink
	generation    string
	revision      int64
	generatedAt   time.Time
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
