package moerise

import (
	"reflect"
	"slices"
	"testing"
)

func TestMapTopicsAndLinksUseOriginalVectorsWithoutFileRelations(t *testing.T) {
	snapshot := mapSnapshot{space: "topics", dimensions: 3}
	for i, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		x, tag := 1.0, "软件工程"
		if i >= 4 {
			x, tag = -1, "植物园艺"
		}
		document := mapTestDocument(name, x, float64(i%4)*0.005, 0)
		document.tags = []string{tag}
		snapshot.documents = append(snapshot.documents, document)
	}
	snapshot.documents = append(snapshot.documents, mapTestDocument("file"))
	layout, err := buildMapLayout(t.Context(), snapshot, nil)
	if err != nil || len(layout.topics) != 2 || len(layout.links) == 0 {
		t.Fatalf("semantic topics missing: %+v %v", layout, err)
	}
	labels, members := []string{}, map[string]bool{}
	for _, topic := range layout.topics {
		labels = append(labels, topic.Label)
		for _, name := range topic.Members {
			if name == "memos/file" || members[name] {
				t.Fatalf("nonsemantic or duplicated member: %s", name)
			}
			members[name] = true
		}
	}
	slices.Sort(labels)
	if !reflect.DeepEqual(labels, []string{"植物园艺", "软件工程"}) || len(members) != 8 {
		t.Fatalf("topics lost their names or members: %+v", layout.topics)
	}
	for _, link := range layout.links {
		if link.Source == "memos/file" || link.Target == "memos/file" || link.Weight < 0.55 ||
			(link.Source < "memos/e") != (link.Target < "memos/e") {
			t.Fatalf("false semantic relation: %+v", link)
		}
	}
	oldLinks := slices.Clone(layout.links)
	oldTopics := slices.Clone(layout.topics)
	for i := range layout.points {
		layout.points[i].X, layout.points[i].Y = float64(i%2), float64(i%3)
	}
	if err := buildMapTopics(t.Context(), snapshot, layout); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(oldLinks, layout.links) {
		t.Fatal("two-dimensional distances changed the semantic relationships")
	}
	for i, topic := range layout.topics {
		if topic.ID != oldTopics[i].ID || topic.Label != oldTopics[i].Label || !reflect.DeepEqual(topic.Members, oldTopics[i].Members) {
			t.Fatal("two-dimensional distances changed topic membership")
		}
	}
}

func TestMapDistrictMembershipUsesVectorsAndCoversItsParent(t *testing.T) {
	snapshot := mapSnapshot{space: "districts", dimensions: 3}
	layout := &mapLayout{}
	members := []int{}
	for i := range 48 {
		name, label, y := string(rune('a'+i)), "存储", 0.5
		if i >= 24 {
			label, y = "网络", -0.5
		}
		document := mapTestDocument(name, 1, y, float64(i%24)*0.001)
		document.tags = []string{"技术", label}
		snapshot.documents = append(snapshot.documents, document)
		layout.points = append(layout.points, MapPoint{Name: document.name, X: float64(i), Y: 0})
		members = append(members, i)
	}
	districts, err := buildMapDistricts(t.Context(), snapshot, layout, members, "技术")
	if err != nil || len(districts) != 2 {
		t.Fatalf("missing vector subtopics: %+v %v", districts, err)
	}
	seen := map[string]bool{}
	for _, district := range districts {
		if district.Label != "存储" && district.Label != "网络" {
			t.Fatalf("unexpected district name: %s", district.Label)
		}
		for _, name := range district.Members {
			if seen[name] {
				t.Fatalf("overlapping district membership: %s", name)
			}
			seen[name] = true
		}
	}
	if len(seen) != len(members) {
		t.Fatal("districts do not cover their parent")
	}
	for i := range layout.points {
		layout.points[i].X, layout.points[i].Y = float64(i%7), float64(i%3)
	}
	moved, err := buildMapDistricts(t.Context(), snapshot, layout, members, "技术")
	if err != nil {
		t.Fatal(err)
	}
	for i := range moved {
		if moved[i].ID != districts[i].ID || !reflect.DeepEqual(moved[i].Members, districts[i].Members) {
			t.Fatal("projection changed semantic district membership")
		}
	}
	for i := range snapshot.documents {
		snapshot.documents[i].vector = []float64{1, 0, 0}
	}
	identical, err := buildMapDistricts(t.Context(), snapshot, layout, members, "技术")
	if err != nil || len(identical) != 0 {
		t.Fatalf("identical vectors gained arbitrary districts: %+v %v", identical, err)
	}
}
