package moerise

import (
	"context"
	"strings"
)

func (s *AIService) Answer(ctx context.Context, request AIRequest, emit func(AIEvent) error) (AIResult, error) {
	if len(request.Names) != 0 || strings.TrimSpace(request.Question) == "" {
		return AIResult{}, failure("INVALID_ARGUMENT", "Answer requires a question and search filters", 422, false)
	}
	if request.Timeline {
		return s.generate(ctx, "timeline", request, emit)
	}
	return s.generate(ctx, "answer", request, emit)
}
