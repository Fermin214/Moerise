package moerise

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAIAnswerUsesFilteredEvidenceAndDistinguishesFailures(t *testing.T) {
	for _, mode := range []string{"answered", "insufficient", "no-hits", "model-unavailable"} {
		t.Run(mode, func(t *testing.T) {
			memos := map[string]map[string]any{
				"memos/basil": rawMemo("basil", "fictional basil"),
				"memos/sky":   rawMemo("sky", "fictional sky"),
			}
			memos["memos/basil"]["tags"] = []string{"garden"}
			calls := 0
			provider := aiLLMFunc(func(_ context.Context, messages []LLMMessage, emit func(string) error) (LLMResult, error) {
				calls++
				var input aiPromptData
				if json.Unmarshal([]byte(messages[1].Content), &input) != nil || input.Mode != "answer" ||
					input.Question != "basil" || len(input.Evidence) != 1 || input.Evidence[0].Name != "memos/basil" {
					t.Fatal("answer changed search filters or evidence")
				}
				if mode == "model-unavailable" {
					return LLMResult{}, llmUnavailable(false)
				}
				output := "{\"type\":\"fact\",\"text\":\"The note records basil.\",\"citations\":[\"E1\"]}\n"
				if mode == "insufficient" {
					output += "{\"type\":\"result\",\"status\":\"insufficient\",\"missing\":[\"No watering interval was recorded.\"]}\n"
				} else {
					output += "{\"type\":\"result\",\"status\":\"answered\",\"missing\":[]}\n"
				}
				if err := emit(output); err != nil {
					return LLMResult{}, err
				}
				return LLMResult{Content: output, Model: "fictional-model", FinishReason: "stop"}, nil
			})
			service := insightFixture(t, memos, provider)
			request := AIRequest{Question: "basil", Tags: []string{"garden"},
				From: "2020-01-01T00:00:00Z", Until: "2021-01-01T00:00:00Z"}
			if mode == "no-hits" {
				request.Question = "astronomy"
			}
			result, err := service.Answer(context.Background(), request, nil)
			if mode == "model-unavailable" {
				if errorCode(err) != "LLM_UNAVAILABLE" || result.Completed || calls != 1 {
					t.Fatalf("model failure was reported as evidence insufficiency: %+v %v", result, err)
				}
				return
			}
			if err != nil || !result.Completed || result.Kind != "answer" {
				t.Fatalf("answer failed: %+v %v", result, err)
			}
			if mode == "no-hits" {
				if result.Status != "insufficient" || len(result.References) != 0 || len(result.Missing) == 0 || calls != 0 {
					t.Fatalf("no evidence reached the model: %+v", result)
				}
			} else if result.Status != mode || len(result.References) != 1 || calls != 1 {
				t.Fatalf("incorrect answer status or citations: %+v", result)
			}
			if mode == "insufficient" && len(result.Missing) == 0 {
				t.Fatal("insufficient answer omitted the missing information")
			}
			if len(memos) != 2 {
				t.Fatal("answer created a source note")
			}
		})
	}
}
