package moerise

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestLLMHTTPFailure(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		code   string
		retry  bool
	}{
		{"quota-code", 429, `{"error":{"code":"insufficient_quota","message":"private input"}}`, "LLM_REJECTED", false},
		{"quota-type", 429, `{"error":{"type":"insufficient_quota","code":"rate_limit_exceeded"}}`, "LLM_REJECTED", false},
		{"billing-limit", 429, `{"error":{"code":"billing_hard_limit_reached"}}`, "LLM_REJECTED", false},
		{"organization-spend-limit", 429, `{"error":{"code":"organization_spend_limit_exceeded"}}`, "LLM_REJECTED", false},
		{"project-spend-limit", 429, `{"error":{"code":"project_spend_limit_exceeded"}}`, "LLM_REJECTED", false},
		{"organization-usage-limit", 429, `{"error":{"code":"organization_usage_limit_exceeded"}}`, "LLM_REJECTED", false},
		{"spend-limit-type", 429, `{"error":{"type":"organization_spend_limit_exceeded","code":"rate_limit_exceeded"}}`, "LLM_REJECTED", false},
		{"temporary", 429, `{"error":{"code":"rate_limit_exceeded","message":"private input"}}`, "LLM_RATE_LIMITED", true},
		{"slow-down-http-status", 429, `{"error":{"code":"slow_down"}}`, "LLM_RATE_LIMITED", true},
		{"temporary-type", 429, `{"error":{"type":"rate_limit_error","code":null}}`, "LLM_RATE_LIMITED", true},
		{"proxy-rate-limit", 429, `<html>private input</html>`, "LLM_RATE_LIMITED", true},
		{"malformed", 429, `{"error":{"code":"insufficient_quota"}`, "LLM_RATE_LIMITED", true},
		{"credential", 401, `{"error":{"code":"server_error"}}`, "LLM_AUTH", false},
		{"forbidden", 403, `{"error":{"message":"private input"}}`, "LLM_AUTH", false},
		{"server", 503, `private input`, "LLM_UNAVAILABLE", true},
		{"overloaded-http-status", 503, `{"error":{"code":"server_is_overloaded"}}`, "LLM_UNAVAILABLE", true},
		{"unavailable-http-status", 503, `{"error":{"code":"service_unavailable_error"}}`, "LLM_UNAVAILABLE", true},
		{"bad-request", 400, `{"error":{"type":"overloaded_error","message":"private input"}}`, "LLM_REJECTED", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := io.NopCloser(strings.NewReader(test.body))
			defer body.Close()
			err := llmHTTPFailure(&http.Response{StatusCode: test.status, Body: body})
			var service *ServiceError
			if !errors.As(err, &service) || service.Code != test.code || service.Retryable != test.retry ||
				strings.Contains(err.Error(), "private input") {
				t.Fatalf("HTTP failure classification: %+v", err)
			}
		})
	}
}

type llmCountingErrorBody struct {
	read int
}

func (body *llmCountingErrorBody) Read(data []byte) (int, error) {
	for index := range data {
		data[index] = 'x'
	}
	body.read += len(data)
	return len(data), nil
}

func (body *llmCountingErrorBody) Close() error { return nil }

func TestLLMHTTPFailureBoundsErrorBody(t *testing.T) {
	body := &llmCountingErrorBody{}
	err := llmHTTPFailure(&http.Response{StatusCode: 503, Body: body})
	if body.read > llmErrorBodyBytes+1 || errorCode(err) != "LLM_UNAVAILABLE" {
		t.Fatalf("unbounded error body: read=%d, err=%v", body.read, err)
	}
}

func TestLLMProviderFailure(t *testing.T) {
	for _, test := range []struct {
		code  string
		want  string
		retry bool
	}{
		{"server_error", "LLM_UNAVAILABLE", true},
		{"api_error", "LLM_UNAVAILABLE", true},
		{"overloaded_error", "LLM_UNAVAILABLE", true},
		{"internal_server_error", "LLM_UNAVAILABLE", true},
		{"rate_limit_exceeded", "LLM_RATE_LIMITED", true},
		{"rate_limit_error", "LLM_RATE_LIMITED", true},
		{"insufficient_quota", "LLM_REJECTED", false},
		{"organization_spend_limit_exceeded", "LLM_REJECTED", false},
		{"project_spend_limit_exceeded", "LLM_REJECTED", false},
		{"organization_usage_limit_exceeded", "LLM_REJECTED", false},
		{"slow_down", "LLM_REJECTED", false},
		{"server_is_overloaded", "LLM_REJECTED", false},
		{"service_unavailable_error", "LLM_REJECTED", false},
		{"invalid_prompt", "LLM_REJECTED", false},
		{"private input", "LLM_REJECTED", false},
		{"", "LLM_REJECTED", false},
	} {
		t.Run(test.code, func(t *testing.T) {
			err := llmProviderFailure(test.code)
			var service *ServiceError
			if !errors.As(err, &service) || service.Code != test.want || service.Retryable != test.retry ||
				strings.Contains(err.Error(), "private input") {
				t.Fatalf("provider failure classification: %+v", err)
			}
		})
	}
}
