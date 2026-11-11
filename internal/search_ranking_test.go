package moerise

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func rankingCandidates(count int, order string) []searchCandidate {
	random := rand.New(rand.NewPCG(17, 43))
	items := make([]searchCandidate, count)
	for i := range items {
		score := float64(random.IntN(200)) / 200
		switch order {
		case "ascending":
			score = float64(i)
		case "descending":
			score = float64(count - i)
		case "ties":
			score = 1
		}
		items[i] = searchCandidate{name: fmt.Sprintf("memos/%05d", (count-i)/3),
			sourceHash: "verified-source", score: score, chunk: TextChunk{Ordinal: (count - i) % 3, Content: "完整原文"}}
	}
	return items
}

func rankCandidatesForTest(items []searchCandidate, bounded bool) []searchCandidate {
	if bounded {
		result := make([]searchCandidate, 0, searchCandidates+1)
		for _, item := range items {
			result = keepSearchCandidate(result, item)
		}
		return result
	}
	result := slices.Clone(items)
	slices.SortFunc(result, func(a, b searchCandidate) int {
		if a.score > b.score {
			return -1
		}
		if a.score < b.score {
			return 1
		}
		if order := strings.Compare(a.name, b.name); order != 0 {
			return order
		}
		return a.chunk.Ordinal - b.chunk.Ordinal
	})
	return result[:min(len(result), searchCandidates)]
}

func TestSemanticCandidateSelectionMatchesFullSort(t *testing.T) {
	for _, count := range []int{0, 1, 99, 100, 101, 1000, 10000} {
		for _, order := range []string{"random", "ascending", "descending", "ties"} {
			items := rankingCandidates(count, order)
			expected, actual := rankCandidatesForTest(items, false), rankCandidatesForTest(items, true)
			if !slices.EqualFunc(actual, expected, func(a, b searchCandidate) bool { return reflect.DeepEqual(a, b) }) {
				t.Fatalf("ranking differs: count=%d order=%s", count, order)
			}
		}
	}
}

var rankingBenchmarkResult []searchCandidate

func BenchmarkSemanticCandidateSelection(b *testing.B) {
	for _, order := range []string{"random", "ascending", "descending", "ties"} {
		items := rankingCandidates(10000, order)
		for _, bounded := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/bounded=%t", order, bounded), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					rankingBenchmarkResult = rankCandidatesForTest(items, bounded)
				}
			})
		}
	}
}
