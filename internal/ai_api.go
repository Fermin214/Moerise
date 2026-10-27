package moerise

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"unicode/utf8"
)

const aiRequestBytes = 64 * 1024

func readAIRequest(reader io.Reader) (AIRequest, error) {
	bad := failure("INVALID_ARGUMENT", "Invalid AI request parameters", 422, false)
	data, err := io.ReadAll(io.LimitReader(reader, aiRequestBytes+1))
	if err != nil || len(data) > aiRequestBytes || !utf8.Valid(data) {
		return AIRequest{}, bad
	}
	// Reject duplicate keys and case variants as well as unknown fields.
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return AIRequest{}, bad
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return AIRequest{}, bad
		}
		name, ok := key.(string)
		if !ok || seen[name] || !slices.Contains([]string{
			"names", "question", "tags", "state", "from", "until", "timeline", "stream",
		}, name) {
			return AIRequest{}, bad
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return AIRequest{}, bad
		}
	}
	var request AIRequest
	if strictJSON(bytes.NewReader(data), &request) != nil {
		return AIRequest{}, bad
	}
	return request, nil
}

func aiPublicError(err error) *ServiceError {
	var service *ServiceError
	if !errors.As(err, &service) {
		return &ServiceError{"INTERNAL", "Internal AI service error", 500, true}
	}
	public := *service
	switch service.Code {
	case "INVALID_ARGUMENT":
		public.Message, public.Status = "Invalid AI request parameters", 422
	case "LLM_CONTEXT_LIMIT", "EVIDENCE_BUDGET_EXCEEDED", "INSUFFICIENT_EVIDENCE":
		public.Message, public.Status = "AI evidence cannot satisfy this request", 422
	case "SOURCE_NOT_FOUND":
		public.Message, public.Status = "AI evidence source is unavailable", 404
	case "SOURCE_CHANGED":
		public.Message, public.Status = "AI evidence changed during generation", 409
	case "LLM_DISABLED":
		public.Message, public.Status = "LLM is disabled", 503
	case "LLM_AUTH", "LLM_UNAVAILABLE", "LLM_RATE_LIMITED", "LLM_REJECTED",
		"LLM_PROTOCOL", "LLM_OUTPUT_LIMIT", "INVALID_CITATION":
		public.Message, public.Status = "AI generation failed", 503
	case "UPSTREAM_UNAVAILABLE", "UPSTREAM_AUTH", "UPSTREAM_PROTOCOL",
		"UPSTREAM_REQUEST_REJECTED", "UNSUPPORTED_UPSTREAM", "IDENTITY_MISMATCH":
		public.Message, public.Status = "AI evidence source could not be verified", 503
	case "LLM_TIMEOUT":
		public.Message, public.Status, public.Retryable = "AI request timed out", 504, false
	case "LLM_CANCELED":
		public.Message, public.Status, public.Retryable = "AI request was canceled", 499, false
	default:
		return &ServiceError{"INTERNAL", "Internal AI service error", 500, true}
	}
	return &public
}

func (a *API) invokeAI(ctx context.Context, insight bool, request AIRequest, emit func(AIEvent) error) (result AIResult, err error) {
	defer func() {
		if recover() != nil {
			result, err = AIResult{}, failure("INTERNAL", "Internal AI service error", 500, true)
		}
	}()
	if _, err := normalizeEvidence(request); err != nil {
		return AIResult{}, err
	}
	if insight && (len(request.Names) == 0 || request.Timeline) {
		return AIResult{}, failure("INVALID_ARGUMENT", "Invalid insight parameters", 422, false)
	}
	if a.AI == nil {
		return AIResult{}, failure("LLM_DISABLED", "LLM is disabled", 503, false)
	}
	if insight {
		return a.AI.Insight(ctx, request, emit)
	}
	return a.AI.Answer(ctx, request, emit)
}

func (a *API) aiRoute(writer http.ResponseWriter, request *http.Request) (any, int, error) {
	if request.Method != http.MethodPost {
		return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
	}
	access, err := a.authenticate(request, "notes:read")
	if err != nil {
		return nil, 0, err
	}
	if !slices.Contains(access.Scopes, "ai:generate") {
		return nil, 0, failure("FORBIDDEN", "This token does not permit model usage", 403, false)
	}
	if request.URL.RawQuery != "" || request.URL.ForceQuery {
		return nil, 0, failure("INVALID_ARGUMENT", "AI parameters must be in the JSON body", 422, false)
	}
	if header := request.Header.Get("Content-Type"); header != "" {
		media, _, err := mime.ParseMediaType(header)
		if err != nil || media != "application/json" {
			return nil, 0, failure("INVALID_ARGUMENT", "AI requests require JSON", 422, false)
		}
	}
	body, err := readAIRequest(http.MaxBytesReader(writer, request.Body, aiRequestBytes))
	if err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	insight := request.URL.Path == "/api/ext/v1/insights"
	if !body.Stream {
		result, err := a.invokeAI(ctx, insight, body, nil)
		if err != nil {
			return nil, 0, aiPublicError(err)
		}
		if !result.Completed {
			return nil, 0, aiPublicError(aiOutputError())
		}
		return result, 200, nil
	}
	started, contentDelivered := false, false
	var transportError error
	controller := http.NewResponseController(writer)
	send := func(kind string, value any) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if !started {
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.Header().Set("X-Accel-Buffering", "no")
			writer.WriteHeader(http.StatusOK)
			started = true
		}
		frame := append([]byte("event: "+kind+"\ndata: "), data...)
		frame = append(frame, '\n', '\n')
		var written int
		if written, err = writer.Write(frame); err == nil && written != len(frame) {
			err = io.ErrShortWrite
		}
		if err == nil {
			err = controller.Flush()
		}
		if err != nil {
			transportError = err
			cancel()
		}
		return err
	}
	result, err := a.invokeAI(ctx, insight, body, func(event AIEvent) error {
		switch event.Type {
		case "references":
			if event.References == nil {
				event.References = []Evidence{}
			}
			return send("references", map[string]any{"type": "references", "references": event.References})
		case "content":
			contentDelivered = true
			return send(event.Type, event)
		case "complete":
			// Commit completion only after the service has returned successfully.
			return nil
		default:
			return aiOutputError()
		}
	})
	if transportError != nil || ctx.Err() != nil {
		return nil, 0, nil
	}
	if err == nil && !result.Completed {
		err = aiOutputError()
	}
	if err != nil {
		public := aiPublicError(err)
		if !started {
			return nil, 0, public
		}
		if contentDelivered {
			public.Retryable = false
		}
		_ = send("error", map[string]any{"type": "error", "completed": false, "error": map[string]any{
			"code": public.Code, "message": public.Message, "retryable": public.Retryable,
			"request_id": writer.Header().Get("X-Request-ID"),
		}})
		return nil, 0, nil
	}
	_ = send("complete", AIEvent{Type: "complete", Result: &result})
	return nil, 0, nil
}
