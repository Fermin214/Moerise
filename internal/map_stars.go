package moerise

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	starsAlgorithm     = "umap-balanced-v1"
	starsNamingVersion = 1
	layoutUnavailable  = "layout"
	layoutIncompatible = "layout_incompatible"
)

var (
	starFramePattern    = regexp.MustCompile(`^[a-f0-9]{32}$`)
	starTopicPattern    = regexp.MustCompile(`^topic-[a-f0-9]{16}$`)
	starDistrictPattern = regexp.MustCompile(`^district-[a-f0-9]{16}$`)
)

type starDocument struct {
	Name       string   `json:"name"`
	VectorHash string   `json:"vector_hash"`
	Tags       []string `json:"tags"`
	Keywords   []string `json:"keywords"`
}

type starPreviousPoint struct {
	Name       string      `json:"name"`
	VectorHash string      `json:"vector_hash"`
	X          float64     `json:"x"`
	Y          float64     `json:"y"`
	Stellar    *MapStellar `json:"stellar,omitempty"`
}

type starRequest struct {
	Scope                 string              `json:"scope"`
	Dimensions            int                 `json:"dimensions"`
	Matrix                []byte              `json:"matrix"`
	Documents             []starDocument      `json:"documents"`
	Frame                 string              `json:"frame"`
	PreviousPoints        []starPreviousPoint `json:"previous_points"`
	PreviousTopics        []MapTopic          `json:"previous_topics"`
	PreviousNamingVersion int                 `json:"previous_naming_version"`
}

type starResult struct {
	Algorithm     string     `json:"algorithm"`
	NamingVersion int        `json:"naming_version"`
	Frame         string     `json:"frame"`
	Points        []MapPoint `json:"points"`
	Topics        []MapTopic `json:"topics"`
	Links         []MapLink  `json:"links"`
}

func (s *MapService) probeLayout(ctx context.Context) string {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.workerURL, "/")+"/health", nil)
	if err != nil {
		return layoutUnavailable
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		return layoutUnavailable
	}
	defer response.Body.Close()
	var health struct {
		NamingVersion int `json:"naming_version"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&health) != nil {
		return layoutUnavailable
	}
	if health.NamingVersion != starsNamingVersion {
		return layoutIncompatible
	}
	return ""
}

func (s *MapService) computeStars(ctx context.Context, snapshot mapSnapshot, previous *mapLayout) (*mapLayout, error) {
	input := starRequest{Scope: hashBytes(canonicalJSON([]string{s.scope, snapshot.space})),
		Dimensions: snapshot.dimensions, Documents: []starDocument{}, Matrix: []byte{}}
	if previous != nil && previous.algorithm == starsAlgorithm {
		input.Frame, input.PreviousTopics = previous.frame, previous.topics
		input.PreviousNamingVersion = previous.namingVersion
		for _, point := range previous.points {
			if point.Semantic {
				input.PreviousPoints = append(input.PreviousPoints,
					starPreviousPoint{point.Name, point.vectorHash, point.X, point.Y, point.Stellar})
			}
		}
	}
	for _, document := range snapshot.documents {
		if len(document.vector) == 0 {
			continue
		}
		input.Documents = append(input.Documents,
			starDocument{document.name, document.vectorHash, document.tags, document.keywords})
		for _, value := range document.vector {
			input.Matrix = binary.LittleEndian.AppendUint32(input.Matrix, math.Float32bits(float32(value)))
		}
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.workerURL, "/")+"/layout", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 16 * time.Minute}).Do(request)
	if err != nil {
		return nil, failure("MAP_LAYOUT_UNAVAILABLE", "Stars layout worker is unavailable", 503, true)
	}
	defer response.Body.Close()
	var result starResult
	if response.StatusCode != http.StatusOK {
		return nil, failure("MAP_LAYOUT_UNAVAILABLE", "Stars layout generation failed", 503, true)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<20))
	if err := decoder.Decode(&result); err != nil {
		return nil, failure("MAP_LAYOUT_INVALID", "Stars layout worker returned invalid data", 503, true)
	}
	if result.Algorithm != starsAlgorithm || result.NamingVersion != starsNamingVersion {
		return nil, failure("MAP_LAYOUT_INCOMPATIBLE", "Upgrade and restart the stars layout worker with Core", 503, true)
	}
	if !starFramePattern.MatchString(result.Frame) || len(result.Points) != len(input.Documents) ||
		result.Topics == nil || result.Links == nil {
		return nil, failure("MAP_LAYOUT_INVALID", "Stars layout worker returned invalid data", 503, true)
	}
	byName := map[string]MapPoint{}
	for _, point := range result.Points {
		a := point.Stellar
		if _, exists := byName[point.Name]; exists || !finiteStar(point.X) || !finiteStar(point.Y) || a == nil ||
			!finiteStar(a.Seed) || a.Seed < 0 || a.Seed > 1 || !finiteStar(a.Radius) || a.Radius <= 0 || a.Radius > 4 ||
			!finiteStar(a.Luminosity) || a.Luminosity < 0 || a.Luminosity > 1 ||
			(a.Role != "ordinary" && a.Role != "core" && a.Role != "isolated") {
			return nil, failure("MAP_LAYOUT_INVALID", "Invalid stellar properties", 503, true)
		}
		byName[point.Name] = point
	}
	layout := &mapLayout{algorithm: starsAlgorithm, namingVersion: result.NamingVersion, frame: result.Frame, topics: result.Topics, links: result.Links,
		points: make([]MapPoint, 0, len(snapshot.documents))}
	for _, document := range snapshot.documents {
		point, exists := byName[document.name]
		if len(document.vector) != 0 && !exists {
			return nil, failure("MAP_LAYOUT_INVALID", "Stars layout is missing a note", 503, true)
		}
		point.Name, point.SourceHash, point.vectorHash = document.name, document.sourceHash, document.vectorHash
		point.Semantic, point.Tags = len(document.vector) != 0, append([]string{}, document.tags...)
		point.Excerpt, point.CreateTime = document.excerpt, document.createTime
		layout.points = append(layout.points, point)
	}
	if err := validateStarGroups(layout); err != nil {
		return nil, err
	}
	return layout, ctx.Err()
}

func finiteStar(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func validateStarGroups(layout *mapLayout) error {
	invalid := func() error { return failure("MAP_LAYOUT_INVALID", "Invalid stars topic membership", 503, true) }
	semantic := map[string]bool{}
	for _, point := range layout.points {
		semantic[point.Name] = point.Semantic
	}
	assigned, ids := map[string]bool{}, map[string]bool{}
	for _, topic := range layout.topics {
		if !starTopicPattern.MatchString(topic.ID) || ids[topic.ID] ||
			!finiteStar(topic.X) || !finiteStar(topic.Y) || len(topic.Label) > 600 || len(topic.Members) == 0 || len(topic.Districts) > 6 {
			return invalid()
		}
		ids[topic.ID] = true
		members := map[string]bool{}
		for _, name := range topic.Members {
			if !semantic[name] || assigned[name] {
				return invalid()
			}
			members[name], assigned[name] = true, true
		}
		local := map[string]bool{}
		for _, district := range topic.Districts {
			if !starDistrictPattern.MatchString(district.ID) || ids[district.ID] ||
				!finiteStar(district.X) || !finiteStar(district.Y) || len(district.Label) > 600 || len(district.Members) == 0 {
				return invalid()
			}
			ids[district.ID] = true
			for _, name := range district.Members {
				if !members[name] || local[name] {
					return invalid()
				}
				local[name] = true
			}
		}
	}
	if len(layout.links) > len(layout.points)*3 {
		return invalid()
	}
	edges := map[string]bool{}
	for _, link := range layout.links {
		a, b := link.Source, link.Target
		if a > b {
			a, b = b, a
		}
		key := a + "\x00" + b
		if !semantic[a] || !semantic[b] || a == b || edges[key] || !finiteStar(link.Weight) || link.Weight < .55 || link.Weight > 1 {
			return invalid()
		}
		edges[key] = true
	}
	return nil
}

func layoutWorkerURL() string {
	if address := strings.TrimSpace(os.Getenv("MOERISE_LAYOUT_URL")); address != "" {
		return address
	}
	return "http://127.0.0.1:8766"
}
