package moerise

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
)

func TestMultiInsightKeepsSelectedSourcesAndRequiresBothSides(t *testing.T) {
	for _, mode := range []string{"equal-text", "one-sided", "omitted"} {
		t.Run(mode, func(t *testing.T) {
			memos := map[string]map[string]any{
				"memos/first":   rawMemo("first", "fictional basil"),
				"memos/second":  rawMemo("second", "fictional basil"),
				"memos/history": rawMemo("history", "fictional basil"),
			}
			if mode == "omitted" {
				memos["memos/second"]["state"] = "ARCHIVED"
			}
			provider := aiLLMFunc(func(_ context.Context, messages []LLMMessage, emit func(string) error) (LLMResult, error) {
				var input aiPromptData
				if json.Unmarshal([]byte(messages[1].Content), &input) != nil ||
					input.Mode != "multi" || input.Requested != 2 {
					t.Fatal("invalid multi-note prompt")
				}
				for _, item := range input.Evidence {
					if item.Name == "memos/history" {
						t.Fatal("multi-note insight added an unselected history note")
					}
				}
				output := "{\"type\":\"fact\",\"text\":\"The first note records basil.\",\"citations\":[\"E1\"]}\n"
				switch mode {
				case "equal-text":
					if len(input.Evidence) != 2 || input.Evidence[0].Content != input.Evidence[1].Content {
						t.Fatal("equal-text source identity was lost")
					}
					output += "{\"type\":\"interpretation\",\"text\":\"The selected notes repeat the same record.\",\"citations\":[\"E1\",\"E2\"]}\n"
				case "one-sided":
					output += "{\"type\":\"interpretation\",\"text\":\"The notes are connected.\",\"citations\":[\"E1\"]}\n"
				}
				output += "{\"type\":\"result\",\"status\":\"answered\",\"missing\":[]}\n"
				if err := emit(output); err != nil {
					return LLMResult{}, err
				}
				return LLMResult{Content: output, Model: "fictional-model", FinishReason: "stop"}, nil
			})
			service := insightFixture(t, memos, provider)
			result, err := service.Insight(context.Background(), AIRequest{Names: []string{"memos/first", "memos/second"}}, nil)
			if mode == "one-sided" {
				if errorCode(err) != "INVALID_CITATION" || result.Completed {
					t.Fatalf("accepted a one-sided cross-note interpretation: %+v %v", result, err)
				}
				return
			}
			if err != nil || !result.Completed || result.Kind != "multi" {
				t.Fatalf("multi-note insight failed: %+v %v", result, err)
			}
			if mode == "equal-text" && (result.Status != "answered" ||
				!slices.Equal(result.UsedSources, []string{"memos/first", "memos/second"})) {
				t.Fatalf("used provenance lost: %+v", result)
			}
			if mode == "omitted" && (result.Status != "insufficient" || len(result.Missing) == 0 ||
				!slices.Equal(result.Omitted, []string{"memos/second"}) ||
				!slices.Equal(result.UsedSources, []string{"memos/first"})) {
				t.Fatalf("omitted source treated as complete comparison: %+v", result)
			}
		})
	}
}
