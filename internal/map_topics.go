package moerise

import (
	"context"
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Topic membership and links use normalized document vectors, independently of
// the two-dimensional projection. Tags and keywords only name the groups.
func buildMapTopics(ctx context.Context, snapshot mapSnapshot, layout *mapLayout) error {
	layout.topics, layout.links = []MapTopic{}, []MapLink{}
	documents := []int{}
	for i, document := range snapshot.documents {
		if len(document.vector) != 0 {
			documents = append(documents, i)
		}
	}
	if len(documents) == 0 {
		return ctx.Err()
	}
	limit := min(12, max(1, int(math.Ceil(math.Sqrt(float64(len(documents))/3)))))
	seeds := []int{documents[0]}
	nearest := make([]float64, len(documents))
	for i := range nearest {
		nearest[i] = -1
	}
	for len(seeds) < limit {
		last := snapshot.documents[seeds[len(seeds)-1]].vector
		farthest, similarity := -1, 1.0
		for i, index := range documents {
			if i%64 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			nearest[i] = math.Max(nearest[i], mapDot(snapshot.documents[index].vector, last))
			if nearest[i] < similarity {
				farthest, similarity = index, nearest[i]
			}
		}
		// Do not split an essentially identical collection into arbitrary themes.
		if farthest < 0 || similarity > 0.84 {
			break
		}
		seeds = append(seeds, farthest)
	}
	centers := make([][]float64, len(seeds))
	for i, index := range seeds {
		centers[i] = slices.Clone(snapshot.documents[index].vector)
	}
	groups := make([][]int, len(seeds))
	for range 5 {
		for i := range groups {
			groups[i] = nil
		}
		for i, index := range documents {
			if i%32 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			vector := snapshot.documents[index].vector
			group, best := 0, -math.MaxFloat64
			for j, center := range centers {
				if similarity := mapDot(vector, center); similarity > best {
					group, best = j, similarity
				}
			}
			groups[group] = append(groups[group], index)
		}
		for i, members := range groups {
			if len(members) == 0 {
				continue
			}
			center := make([]float64, snapshot.dimensions)
			for _, index := range members {
				for j, value := range snapshot.documents[index].vector {
					center[j] += value
				}
			}
			if mapNormalize(center) {
				centers[i] = center
			}
		}
	}
	tags, keywords := mapTermCounts(snapshot.documents, documents)
	usedLabels := map[string]bool{}
	for i, members := range groups {
		if len(members) == 0 {
			continue
		}
		topic := MapTopic{ID: "topic-" + hashBytes([]byte(snapshot.documents[seeds[i]].name))[:16], Members: []string{}}
		for _, index := range members {
			point := layout.points[index]
			topic.X += point.X / float64(len(members))
			topic.Y += point.Y / float64(len(members))
			topic.Members = append(topic.Members, point.Name)
		}
		localTags, localKeywords := mapTermCounts(snapshot.documents, members)
		topic.Label = mapTopicLabel(localTags, tags, len(members), len(documents), usedLabels, true)
		if topic.Label == "" {
			topic.Label = mapTopicLabel(localKeywords, keywords, len(members), len(documents), usedLabels, false)
		}
		usedLabels[topic.Label] = true
		districts, err := buildMapDistricts(ctx, snapshot, layout, members, topic.Label)
		if err != nil {
			return err
		}
		topic.Districts = districts
		layout.topics = append(layout.topics, topic)
	}

	// Bound neighbor search for large collections. Projection order and evenly
	// spaced samples only nominate candidates; every edge is verified in the
	// original vector space. At most three outgoing neighbors per document.
	type neighbor struct {
		index int
		score float64
	}
	seen := map[[2]int]bool{}
	for _, members := range groups {
		ordered := slices.Clone(members)
		slices.SortFunc(ordered, func(a, b int) int {
			if delta := layout.points[a].X - layout.points[b].X; delta != 0 {
				if delta < 0 {
					return -1
				}
				return 1
			}
			return strings.Compare(snapshot.documents[a].name, snapshot.documents[b].name)
		})
		for position, index := range ordered {
			if position%32 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			candidates := map[int]bool{}
			for j := max(0, position-6); j < min(len(ordered), position+7); j++ {
				candidates[ordered[j]] = true
			}
			for j := 0; j < min(12, len(ordered)); j++ {
				candidates[ordered[j*len(ordered)/min(12, len(ordered))]] = true
			}
			for _, seed := range seeds {
				candidates[seed] = true
			}
			best := []neighbor{}
			for other := range candidates {
				if other == index {
					continue
				}
				score := math.Min(1, mapDot(snapshot.documents[index].vector, snapshot.documents[other].vector))
				if score >= 0.55 {
					best = append(best, neighbor{other, score})
				}
			}
			slices.SortFunc(best, func(a, b neighbor) int {
				if a.score != b.score {
					if a.score > b.score {
						return -1
					}
					return 1
				}
				return strings.Compare(snapshot.documents[a.index].name, snapshot.documents[b.index].name)
			})
			for _, other := range best[:min(3, len(best))] {
				a, b := index, other.index
				if snapshot.documents[a].name > snapshot.documents[b].name {
					a, b = b, a
				}
				if key := [2]int{a, b}; !seen[key] {
					seen[key] = true
					layout.links = append(layout.links, MapLink{Source: snapshot.documents[a].name,
						Target: snapshot.documents[b].name, Weight: math.Round(other.score*1e6) / 1e6})
				}
			}
		}
	}
	slices.SortFunc(layout.links, func(a, b MapLink) int {
		if order := strings.Compare(a.Source, b.Source); order != 0 {
			return order
		}
		return strings.Compare(a.Target, b.Target)
	})
	return ctx.Err()
}

// Subtopics are clustered within each theme in the original vector space.
// Identical collections remain a single region.
func buildMapDistricts(ctx context.Context, snapshot mapSnapshot, layout *mapLayout, members []int, label string) ([]MapDistrict, error) {
	if len(members) < 12 {
		return nil, nil
	}
	limit := min(6, max(2, int(math.Ceil(math.Sqrt(float64(len(members))/6)))))
	seeds := []int{members[0]}
	nearest := make([]float64, len(members))
	for i := range nearest {
		nearest[i] = -1
	}
	for len(seeds) < limit {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		last := snapshot.documents[seeds[len(seeds)-1]].vector
		farthest, similarity := -1, 1.0
		for i, index := range members {
			nearest[i] = math.Max(nearest[i], mapDot(snapshot.documents[index].vector, last))
			if nearest[i] < similarity {
				farthest, similarity = index, nearest[i]
			}
		}
		if farthest < 0 || similarity > 0.92 {
			break
		}
		seeds = append(seeds, farthest)
	}
	if len(seeds) < 2 {
		return nil, nil
	}
	centers := make([][]float64, len(seeds))
	groups := make([][]int, len(seeds))
	for i, index := range seeds {
		centers[i] = slices.Clone(snapshot.documents[index].vector)
	}
	for range 5 {
		for i := range groups {
			groups[i] = nil
		}
		for i, index := range members {
			if i%32 == 0 && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			best, score := 0, -math.MaxFloat64
			for j, center := range centers {
				if value := mapDot(snapshot.documents[index].vector, center); value > score {
					best, score = j, value
				}
			}
			groups[best] = append(groups[best], index)
		}
		for i, group := range groups {
			if len(group) == 0 {
				continue
			}
			center := make([]float64, snapshot.dimensions)
			for _, index := range group {
				for j, value := range snapshot.documents[index].vector {
					center[j] += value
				}
			}
			if mapNormalize(center) {
				centers[i] = center
			}
		}
	}
	tags, keywords := mapTermCounts(snapshot.documents, members)
	used := map[string]bool{label: true}
	result := []MapDistrict{}
	for i, group := range groups {
		if len(group) == 0 {
			continue
		}
		district := MapDistrict{ID: "district-" + hashBytes([]byte(snapshot.documents[seeds[i]].name))[:16], Members: []string{}}
		for _, index := range group {
			point := layout.points[index]
			district.X += point.X / float64(len(group))
			district.Y += point.Y / float64(len(group))
			district.Members = append(district.Members, point.Name)
		}
		localTags, localKeywords := mapTermCounts(snapshot.documents, group)
		district.Label = mapTopicLabel(localTags, tags, len(group), len(members), used, true)
		if district.Label == "" {
			district.Label = mapTopicLabel(localKeywords, keywords, len(group), len(members), used, false)
		}
		used[district.Label] = true
		result = append(result, district)
	}
	if len(result) < 2 {
		return nil, nil
	}
	return result, ctx.Err()
}

func mapTermCounts(documents []mapDocument, members []int) (map[string]int, map[string]int) {
	tags, keywords := map[string]int{}, map[string]int{}
	for _, index := range members {
		for terms, counts := range []map[string]int{tags, keywords} {
			seen := map[string]bool{}
			values := documents[index].tags
			if terms == 1 {
				values = documents[index].keywords
			}
			for _, term := range values {
				term = strings.TrimSpace(term)
				if term != "" && !seen[term] {
					counts[term]++
					seen[term] = true
				}
			}
		}
	}
	return tags, keywords
}

func mapTopicLabel(local, global map[string]int, size, total int, used map[string]bool, tag bool) string {
	label, best := "", 0.0
	for term, count := range local {
		if used[term] || (!tag && count < min(2, size)) {
			continue
		}
		length := utf8.RuneCountInString(term)
		if length < 2 || length > 28 {
			continue
		}
		score := float64(count) / float64(size) * (1 + math.Log(float64(total+1)/float64(global[term]+1)))
		if !tag {
			score *= math.Log1p(float64(length))
		}
		if score > best || score == best && term < label {
			label, best = term, score
		}
	}
	return label
}

// Extract short, repeated terms locally; no model request or cached body is
// included in the map response. Native tags take precedence for topic naming.
func mapKeywords(content string) []string {
	stop := "的了是在和与也就都而或把被让这那一个我们你它有不着地得为及到很更"
	englishStop := " the and for with that this from have will your you are was were has its into then than not but "
	terms := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(content), func(r rune) bool {
		return !unicode.IsLetter(r) || unicode.Is(unicode.Han, r) && strings.ContainsRune(stop, r)
	}) {
		runes := []rune(word)
		if len(runes) < 2 {
			continue
		}
		if !unicode.Is(unicode.Han, runes[0]) {
			if len(runes) >= 3 && len(runes) <= 24 && !strings.Contains(englishStop, " "+word+" ") {
				terms[word] = true
			}
		} else if len(runes) <= 6 {
			terms[word] = true
		} else {
			for length := 2; length <= 4; length++ {
				for i := 0; i+length <= len(runes); i++ {
					terms[string(runes[i:i+length])] = true
				}
			}
		}
		if len(terms) >= 128 {
			break
		}
	}
	result := make([]string, 0, len(terms))
	for term := range terms {
		result = append(result, term)
	}
	slices.Sort(result)
	return result
}
