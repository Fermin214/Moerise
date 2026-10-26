package moerise

import (
	"context"
	"math"
	"slices"
	"strings"
)

// vectorIndex isolates exact retrieval from the persistent embedding lifecycle.
type vectorIndex interface {
	spaceID() string
	dimension() int
	size() int
	memoVector(string, string) []float32
	nearest(context.Context, []float32, map[string]bool, int) ([]searchCandidate, error)
}

func (m *vectorMatrix) spaceID() string { return m.Space }
func (m *vectorMatrix) dimension() int  { return m.Dimensions }
func (m *vectorMatrix) size() int       { return len(m.Entries) }

func (m *vectorMatrix) memoVector(name, hash string) []float32 {
	vector := make([]float32, m.Dimensions)
	count := 0
	for i, entry := range m.Entries {
		if entry.Name != name || entry.ContentHash != hash {
			continue
		}
		count++
		for j, value := range m.Values[i*m.Dimensions : (i+1)*m.Dimensions] {
			vector[j] += value
		}
	}
	if count == 0 || normalizeVector(vector, m.Dimensions) != nil {
		return nil
	}
	return vector
}

func (matrix *vectorMatrix) nearest(ctx context.Context, query []float32, allowed map[string]bool, limit int) ([]searchCandidate, error) {
	best := map[string]searchCandidate{}
	for i, entry := range matrix.Entries {
		if i%256 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !allowed[entry.Name] {
			continue
		}
		var score float64
		for j, value := range matrix.Values[i*matrix.Dimensions : (i+1)*matrix.Dimensions] {
			score += float64(value) * float64(query[j])
		}
		score = math.Max(-1, math.Min(1, score))
		if score < 0.25 {
			continue
		}
		if previous, ok := best[entry.Name]; !ok || score > *previous.Similarity {
			best[entry.Name] = searchCandidate{entry.Name, entry.ContentHash, entry.Start, entry.End, 0, &score, []string{"semantic"}}
		}
	}
	result := make([]searchCandidate, 0, len(best))
	for _, candidate := range best {
		result = append(result, candidate)
	}
	slices.SortFunc(result, func(a, b searchCandidate) int {
		if *a.Similarity != *b.Similarity {
			if *a.Similarity > *b.Similarity {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return result[:min(limit, len(result))], nil
}
