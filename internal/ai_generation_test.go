package moerise

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type aiLLMFunc func(context.Context, []LLMMessage, func(string) error) (LLMResult, error)

func (f aiLLMFunc) Generate(ctx context.Context, messages []LLMMessage, emit func(string) error) (LLMResult, error) {
	return f(ctx, messages, emit)
}

func (aiLLMFunc) Close() {}

const insightOutput = "{\"type\":\"fact\",\"text\":\"The fictional note records basil.\",\"citations\":[\"E1\"]}\n" +
	"{\"type\":\"interpretation\",\"text\":\"A possible gardening topic.\",\"citations\":[\"E1\"]}\n" +
	"{\"type\":\"question\",\"text\":\"What would be useful to record next?\",\"citations\":[]}\n" +
	"{\"type\":\"result\",\"status\":\"answered\",\"missing\":[]}\n"

func insightFixture(t *testing.T, memos map[string]map[string]any, provider LLMProvider) *AIService {
	t.Helper()
	evidence := evidenceFixture(t, memos)
	evidence.Search.Store.Settings.LLM = DefaultSettings().LLM
	evidence.Search.Store.Settings.LLM.Enabled = true
	return NewAIService(evidence.Search, provider)
}

func TestAIInsightCitationsAndCompletion(t *testing.T) {
	memos := map[string]map[string]any{"memos/seed": rawMemo("seed", "fictional basil")}
	var calls int
	provider := aiLLMFunc(func(_ context.Context, messages []LLMMessage, emit func(string) error) (LLMResult, error) {
		calls++
		if len(messages) != 2 || len(messages[0].Content)+len(messages[1].Content) > 24000 {
			t.Fatal("prompt exceeded the total context budget")
		}
		var input aiPromptData
		if json.Unmarshal([]byte(messages[1].Content), &input) != nil || input.Mode != "single" ||
			len(input.Evidence) != 1 || input.Evidence[0].ID != "E1" || input.Evidence[0].Content != "fictional basil" {
			t.Fatal("model did not receive server-owned evidence")
		}
		for _, part := range strings.SplitAfter(insightOutput, "\n") {
			if err := emit(part); err != nil {
				return LLMResult{}, err
			}
		}
		return LLMResult{Content: insightOutput, Model: "fictional-model", FinishReason: "stop"}, nil
	})
	service := insightFixture(t, memos, provider)
	events := []string{}
	result, err := service.Insight(context.Background(), AIRequest{Names: []string{"memos/seed"}}, func(event AIEvent) error {
		events = append(events, event.Type)
		if event.Content != nil && ValidateCitations(event.Content.Citations, []Evidence{{ID: "E1"}}) != nil {
			t.Fatal("published an invalid reference")
		}
		return nil
	})
	if err != nil || !result.Completed || result.Status != "answered" || len(result.Statements) != 3 ||
		result.Statements[0].Kind != "fact" || result.Statements[1].Kind != "interpretation" ||
		result.Statements[2].Kind != "question" || len(result.References) != 1 ||
		result.Model != "fictional-model" || result.Metrics.FirstContentMS == nil ||
		strings.Join(events, ",") != "references,content,content,content,complete" || calls != 1 {
		t.Fatalf("invalid insight delivery: %+v %v %v", result, events, err)
	}
}

func TestAIInsightFailureCannotComplete(t *testing.T) {
	for _, mode := range []string{"invalid-citation", "uncited-fact", "missing-end", "model-failure",
		"edit-before-content", "delete-before-content", "foreign-before-content", "edit-before-complete", "callback"} {
		t.Run(mode, func(t *testing.T) {
			memos := map[string]map[string]any{"memos/seed": rawMemo("seed", "fictional basil")}
			sentinel := errors.New("consumer stopped")
			calls, content, complete := 0, 0, 0
			provider := aiLLMFunc(func(_ context.Context, _ []LLMMessage, emit func(string) error) (LLMResult, error) {
				calls++
				output := insightOutput
				switch mode {
				case "invalid-citation":
					output = strings.ReplaceAll(output, `"E1"`, `"E99"`)
				case "uncited-fact":
					output = strings.ReplaceAll(output, `["E1"]`, `[]`)
				case "missing-end", "model-failure":
					output = strings.SplitAfter(output, "\n")[0]
				case "edit-before-content":
					memos["memos/seed"]["content"] = "changed"
				case "delete-before-content":
					delete(memos, "memos/seed")
				case "foreign-before-content":
					memos["memos/seed"]["creator"] = "users/other"
				}
				if err := emit(output); err != nil {
					return LLMResult{}, err
				}
				if mode == "edit-before-complete" {
					memos["memos/seed"]["content"] = "changed"
				}
				if mode == "model-failure" {
					return LLMResult{}, llmUnavailable(true)
				}
				return LLMResult{Content: output, Model: "fictional-model", FinishReason: "stop"}, nil
			})
			service := insightFixture(t, memos, provider)
			result, err := service.Insight(context.Background(), AIRequest{Names: []string{"memos/seed"}}, func(event AIEvent) error {
				if event.Type == "complete" {
					complete++
				}
				if event.Type == "content" {
					content++
					if mode == "callback" {
						return sentinel
					}
				}
				return nil
			})
			if err == nil || result.Completed || complete != 0 || calls != 1 {
				t.Fatalf("failed generation completed or replayed: %+v %v", result, err)
			}
			expected := map[string]string{
				"invalid-citation": "INVALID_CITATION", "uncited-fact": "LLM_PROTOCOL",
				"missing-end": "LLM_PROTOCOL", "model-failure": "LLM_UNAVAILABLE",
				"edit-before-content": "SOURCE_CHANGED", "delete-before-content": "SOURCE_NOT_FOUND",
				"foreign-before-content": "SOURCE_NOT_FOUND", "edit-before-complete": "SOURCE_CHANGED",
			}[mode]
			if expected != "" && errorCode(err) != expected {
				t.Fatalf("wrong failure: %v", err)
			}
			if mode == "callback" && !errors.Is(err, sentinel) {
				t.Fatalf("callback error was lost: %v", err)
			}
			if strings.HasSuffix(mode, "before-content") && content != 0 {
				t.Fatal("published content after source changed")
			}
		})
	}
}

func TestAIInsightNoEvidenceAndDisabled(t *testing.T) {
	calls := 0
	provider := aiLLMFunc(func(context.Context, []LLMMessage, func(string) error) (LLMResult, error) {
		calls++
		return LLMResult{}, errors.New("must not call")
	})
	service := insightFixture(t, map[string]map[string]any{"memos/seed": rawMemo("seed", "fictional basil")}, provider)
	result, err := service.Insight(context.Background(), AIRequest{Names: []string{"memos/seed"}, Tags: []string{"absent"}}, nil)
	if err != nil || !result.Completed || result.Status != "insufficient" || len(result.References) != 0 ||
		len(result.Missing) == 0 || calls != 0 {
		t.Fatalf("empty evidence called the model: %+v %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err = service.Insight(ctx, AIRequest{Names: []string{"memos/seed"}, Tags: []string{"absent"}}, func(event AIEvent) error {
		if event.Type == "references" {
			cancel()
		}
		return nil
	})
	if errorCode(err) != "LLM_CANCELED" || result.Completed || calls != 0 {
		t.Fatalf("canceled empty evidence request completed: %+v %v", result, err)
	}
	service.Search.Store.Settings.LLM.Enabled = false
	result, err = service.Insight(context.Background(), AIRequest{Names: []string{"memos/seed"}}, nil)
	if errorCode(err) != "LLM_DISABLED" || result.Completed || calls != 0 {
		t.Fatalf("disabled model generated content: %+v %v", result, err)
	}
}
