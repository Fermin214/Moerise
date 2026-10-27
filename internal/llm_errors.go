package moerise

import (
	"encoding/json"
	"io"
	"net/http"
)

const llmErrorBodyBytes = 16 * 1024

type llmProviderError struct {
	Type string `json:"type"`
	Code string `json:"code"`
}

// The caller owns the body. Upstream messages may contain credentials or input.
func llmHTTPFailure(response *http.Response) error {
	var envelope struct {
		Error llmProviderError `json:"error"`
	}
	if response.Body != nil {
		data, err := io.ReadAll(io.LimitReader(response.Body, llmErrorBodyBytes+1))
		if err == nil && len(data) <= llmErrorBodyBytes {
			var decoded struct {
				Error llmProviderError `json:"error"`
			}
			if json.Unmarshal(data, &decoded) == nil {
				envelope.Error = decoded.Error
			}
		}
	}
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return failure("LLM_AUTH", "LLM service rejected the configured credential", 503, false)
	case response.StatusCode == http.StatusTooManyRequests:
		for _, code := range []string{envelope.Error.Code, envelope.Error.Type} {
			switch code {
			case "insufficient_quota", "billing_hard_limit_reached", "billing_not_active",
				"billing_limit_reached", "usage_limit_reached", "quota_exceeded", "credit_balance_exhausted",
				"organization_spend_limit_exceeded", "project_spend_limit_exceeded", "organization_usage_limit_exceeded":
				return llmProviderFailure(code)
			}
		}
		return llmProviderFailure("rate_limit_exceeded")
	case response.StatusCode >= 500 && response.StatusCode < 600:
		return llmProviderFailure("server_error")
	default:
		return llmProviderFailure("")
	}
}

func llmProviderFailure(code string) error {
	switch code {
	case "server_error", "api_error", "internal_server_error", "overloaded_error":
		return failure("LLM_UNAVAILABLE", "LLM service request failed", 503, true)
	case "rate_limit_exceeded", "rate_limit_error":
		return failure("LLM_RATE_LIMITED", "LLM service rate limit exceeded", 503, true)
	default:
		return failure("LLM_REJECTED", "LLM service rejected the request", 503, false)
	}
}
