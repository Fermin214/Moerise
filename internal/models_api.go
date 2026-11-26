package moerise

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const modelCheckTimeout = 55 * time.Second

func isCandidateCheck(r *http.Request) bool {
	return r.Method == http.MethodPost &&
		(r.URL.Path == "/api/app/v1/models/embedding/check" || r.URL.Path == "/api/app/v1/channels/check")
}

func candidateCheckError(r *http.Request, err error) error {
	if errors.Is(r.Context().Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		if r.URL.Path == "/api/app/v1/channels/check" {
			return failure("CHANNEL_CHECK_TIMEOUT", "Channel connection check timed out", 503, true)
		}
		return failure("MODEL_CHECK_TIMEOUT", "Model connection check timed out", 503, true)
	}
	return err
}

func (a *ClientAPI) modelsStatus() (ClientModels, error) {
	s := a.app.Store
	embedding := s.Settings.Embedding
	model := func(enabled bool, protocol string, baseURL, key, name *string) ClientModelStatus {
		return ClientModelStatus{enabled, configured(baseURL) != "" && configured(key) != "" && configured(name) != "",
			protocol, configured(name)}
	}
	result := ClientModels{Version: s.productVersion,
		Embedding:   model(embedding.Enabled, embedding.Protocol, embedding.BaseURL, embedding.APIKey, embedding.Model),
		IndexStatus: "disabled"}
	var err error
	result.VectorIndex, err = s.VectorStatus()
	if err != nil {
		return result, err
	}
	if embedding.Enabled {
		building := result.VectorIndex.Building
		matching := building != nil && building.SpaceHash == embeddingSpace(embedding)
		switch {
		case result.VectorIndex.Ready:
			result.IndexStatus = "ready"
		case matching && building.LastError != nil:
			result.IndexStatus = "unavailable"
		case matching:
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
	var body settingsRequest
	if err := readClientJSON(w, r, 128*1024, []string{"version", "settings", "secrets"}, &body); err != nil {
		return nil, 0, err
	}
	if err := validateCheckFields(body, "embedding", nil, []string{"embedding_api_key"}); err != nil {
		return nil, 0, err
	}
	config, provider, err := a.embeddingCheckSnapshot(r.Context(), body.SettingsUpdate)
	if err != nil {
		return nil, 0, err
	}
	started := time.Now()
	if provider == nil {
		_, err = CheckEmbedding(r.Context(), config)
	} else {
		_, err = checkEmbedding(r.Context(), config, provider)
	}
	if err != nil {
		return nil, 0, err
	}
	return ClientModelCheck{body.Version, "ok", time.Since(started).Milliseconds()}, http.StatusOK, nil
}

func (a *ClientAPI) embeddingCheckSnapshot(ctx context.Context, update SettingsUpdate) (EmbeddingConfig, EmbeddingProvider, error) {
	if err := a.lockTimedRequest(ctx); err != nil {
		return EmbeddingConfig{}, nil, err
	}
	defer a.app.runtimeMu.RUnlock()
	store := a.app.Store
	if update.Version != store.productVersion {
		return EmbeddingConfig{}, nil, failure("SETTINGS_CONFLICT", "Settings have changed; reload before checking the model", 409, false)
	}
	if len(update.Settings) == 0 && len(update.Secrets) == 0 {
		provider := a.app.Search.Provider
		if provider == nil {
			return EmbeddingConfig{}, nil, failure("EMBEDDING_DISABLED", "Embedding is disabled", 503, false)
		}
		config := store.Settings.Embedding
		if _, production := provider.(*embeddingClient); production {
			config.BaseURL = new(configured(config.BaseURL))
			config.APIKey = new(configured(config.APIKey))
			config.Model = new(configured(config.Model))
			if config.QueryInstruction != nil {
				config.QueryInstruction = new(*config.QueryInstruction)
			}
			provider = nil
		}
		return config, provider, nil
	}
	settings, err := store.prepareSettings(update)
	if err != nil {
		return EmbeddingConfig{}, nil, err
	}
	if !settings.Embedding.Enabled {
		return EmbeddingConfig{}, nil, failure("EMBEDDING_DISABLED", "Embedding is disabled", 503, false)
	}
	return settings.Embedding, nil, nil
}
