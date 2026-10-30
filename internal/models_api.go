package moerise

import (
	"context"
	"errors"
	"net/http"
	"time"
)

func (a *ClientAPI) modelsStatus() (ClientModels, error) {
	s := a.app.Store
	embedding, llm := s.Settings.Embedding, s.Settings.LLM
	model := func(enabled bool, protocol string, baseURL, key, name *string) ClientModelStatus {
		return ClientModelStatus{enabled, configured(baseURL) != "" && configured(key) != "" && configured(name) != "",
			protocol, configured(name)}
	}
	result := ClientModels{Version: s.productVersion,
		Embedding:   model(embedding.Enabled, embedding.Protocol, embedding.BaseURL, embedding.APIKey, embedding.Model),
		LLM:         model(llm.Enabled, llm.Protocol, llm.BaseURL, llm.APIKey, llm.Model),
		IndexStatus: "disabled"}
	var err error
	result.VectorIndex, err = s.VectorStatus()
	if err != nil {
		return result, err
	}
	if embedding.Enabled {
		switch {
		case result.VectorIndex.Ready:
			result.IndexStatus = "ready"
		case result.VectorIndex.Building != nil && result.VectorIndex.Building.LastError != nil:
			result.IndexStatus = "unavailable"
		case result.VectorIndex.Building != nil:
			result.IndexStatus = "building"
		default:
			result.IndexStatus = "waiting"
		}
	}
	return result, nil
}

func (a *ClientAPI) modelsRoute(w http.ResponseWriter, r *http.Request) (any, int, error) {
	if r.URL.Path == "/api/app/v1/models" {
		if r.Method != http.MethodGet {
			return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
		}
		result, err := a.modelsStatus()
		return result, http.StatusOK, err
	}
	if r.Method != http.MethodPost {
		return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
	}
	var body struct {
		Version int64 `json:"version"`
	}
	if err := readClientJSON(w, r, 1024, []string{"version"}, &body); err != nil {
		return nil, 0, err
	}
	if body.Version < 1 {
		return nil, 0, failure("INVALID_ARGUMENT", "A positive settings version is required", 422, false)
	}
	if body.Version != a.app.Store.productVersion {
		return nil, 0, failure("SETTINGS_CONFLICT", "Settings have changed; reload before checking the model", 409, false)
	}
	// 留出同源代理的超时余量，文档与查询检查共用一次预算。
	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()
	started := time.Now()
	var err error
	if r.URL.Path == "/api/app/v1/models/embedding/check" {
		if a.app.Search.Provider == nil {
			return nil, 0, failure("EMBEDDING_DISABLED", "Embedding is disabled", 503, false)
		}
		_, err = checkEmbedding(ctx, a.app.Store.Settings.Embedding, a.app.Search.Provider)
	} else {
		if a.app.AI.Provider == nil {
			return nil, 0, failure("LLM_DISABLED", "LLM is disabled", 503, false)
		}
		_, err = checkLLM(ctx, a.app.Store.Settings.LLM, a.app.AI.Provider)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		err = failure("MODEL_CHECK_TIMEOUT", "Model connection check timed out", 503, true)
	}
	if err != nil {
		return nil, 0, err
	}
	return ClientModelCheck{body.Version, "ok", time.Since(started).Milliseconds()}, http.StatusOK, nil
}
