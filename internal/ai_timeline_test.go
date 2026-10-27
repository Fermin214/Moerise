package moerise

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestAITimelineRecallsEarlyAndLateWithoutFillingGaps(t *testing.T) {
	memos := map[string]map[string]any{
		"memos/z-early": rawMemo("z-early", "fictional basil; this text mentions 2026"),
		"memos/sky":     rawMemo("sky", "fictional sky"),
		"memos/until":   rawMemo("until", "basil outside the requested interval"),
	}
	memos["memos/z-early"]["updateTime"] = "2026-01-01T00:00:00Z"
	memos["memos/sky"]["createTime"] = "2021-06-01T00:00:00Z"
	memos["memos/until"]["createTime"] = "2024-01-01T00:00:00Z"
	for i := range 24 {
		name := fmt.Sprintf("a-late-%02d", i)
		memos["memos/"+name] = rawMemo(name, "basil was recorded later")
		memos["memos/"+name]["createTime"] = "2023-06-01T00:00:00Z"
	}
	provider := aiLLMFunc(func(_ context.Context, messages []LLMMessage, emit func(string) error) (LLMResult, error) {
		var input aiPromptData
		if json.Unmarshal([]byte(messages[1].Content), &input) != nil ||
			input.Mode != "timeline" || len(input.Periods) != 4 ||
			len(messages[0].Content)+len(messages[1].Content) > 24000 {
			t.Fatal("invalid timeline context or periods")
		}
		early, late := "", ""
		for _, item := range input.Evidence {
			if item.Name == "memos/z-early" {
				early = item.ID
			}
			if strings.HasPrefix(item.Name, "memos/a-late-") {
				late = item.ID
			}
			if item.Name == "memos/sky" || item.Name == "memos/until" {
				t.Fatal("timeline included unrelated or boundary-excluded evidence")
			}
		}
		if early == "" || late == "" {
			t.Fatal("newer matches displaced an early or late period")
		}
		records := []map[string]any{
			{"type": "fact", "text": "The early note mentions basil.", "citations": []string{early}},
			{"type": "fact", "text": "A later note also records basil.", "citations": []string{late}},
			{"type": "interpretation", "text": "The records show two observed periods.", "citations": []string{early, late}},
			{"type": "result", "status": "answered", "missing": []string{}},
		}
		var output strings.Builder
		for _, record := range records {
			body, _ := json.Marshal(record)
			output.Write(body)
			output.WriteByte('\n')
		}
		if err := emit(output.String()); err != nil {
			return LLMResult{}, err
		}
		return LLMResult{Content: output.String(), Model: "fictional-model", FinishReason: "stop"}, nil
	})
	service := insightFixture(t, memos, provider)
	result, err := service.Answer(context.Background(), AIRequest{Question: "basil", Timeline: true,
		From: "2020-01-01T00:00:00Z", Until: "2024-01-01T00:00:00Z"}, nil)
	if err != nil || !result.Completed || result.Kind != "timeline" || len(result.Timeline) != 4 {
		t.Fatalf("timeline failed: %+v %v", result, err)
	}
	if !slices.Contains(result.Timeline[0].RepresentativeNames, "memos/z-early") ||
		!slices.Equal(result.Timeline[0].StatementIndexes, []int{0, 2}) ||
		result.Timeline[3].Empty || !slices.Equal(result.Timeline[3].StatementIndexes, []int{1, 2}) {
		t.Fatalf("creation time was confused with update or body dates: %+v", result.Timeline)
	}
	for _, period := range result.Timeline[1:3] {
		if !period.Empty || len(period.RepresentativeNames) != 0 || len(period.StatementIndexes) != 0 ||
			period.MissingReason != "NO_MATCHING_EVIDENCE" {
			t.Fatalf("filled an unsupported period: %+v", period)
		}
	}
}

func TestAITimelineKeepsEmptyAndBudgetLimitedPeriodsExplicit(t *testing.T) {
	for _, mode := range []string{"empty", "budget"} {
		t.Run(mode, func(t *testing.T) {
			memos := map[string]map[string]any{
				"memos/early": rawMemo("early", "basil"),
				"memos/late":  rawMemo("late", strings.Repeat("basil \"\n", 1000)),
			}
			memos["memos/late"]["createTime"] = "2023-01-01T00:00:00Z"
			calls := 0
			provider := aiLLMFunc(func(_ context.Context, messages []LLMMessage, emit func(string) error) (LLMResult, error) {
				calls++
				if len(messages[0].Content)+len(messages[1].Content) > 10000 {
					t.Fatal("joined timeline evidence exceeded the total byte budget")
				}
				output := "{\"type\":\"fact\",\"text\":\"An early note records basil.\",\"citations\":[\"E1\"]}\n" +
					"{\"type\":\"result\",\"status\":\"answered\",\"missing\":[]}\n"
				if err := emit(output); err != nil {
					return LLMResult{}, err
				}
				return LLMResult{Content: output, Model: "fictional-model", FinishReason: "stop"}, nil
			})
			service := insightFixture(t, memos, provider)
			service.Search.Store.Settings.LLM.ContextBytes = 10000
			request := AIRequest{Question: "basil", Timeline: true,
				From: "2020-01-01T00:00:00Z", Until: "2024-01-01T00:00:00Z"}
			if mode == "empty" {
				request.Question = "astronomy"
			}
			result, err := service.Answer(context.Background(), request, nil)
			if err != nil || !result.Completed || result.Status != "insufficient" || len(result.Timeline) != 4 {
				t.Fatalf("missing timeline evidence appeared complete: %+v %v", result, err)
			}
			if mode == "empty" && calls != 0 {
				t.Fatal("an empty timeline called the model")
			}
			if mode == "budget" && (calls != 1 || result.Timeline[3].MissingReason != "CONTEXT_BUDGET" ||
				!result.Timeline[3].Empty || !slices.Contains(result.Omitted, "memos/late")) {
				t.Fatalf("budget loss was hidden: %+v", result)
			}
		})
	}
}
