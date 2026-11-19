package moerise

import (
	"bytes"
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"time"

	"moerise/internal/telemetry"
)

var telemetryReceiverURL string

func telemetryEnabled(settings Settings) bool {
	target, err := url.Parse(telemetryReceiverURL)
	return settings.TelemetryForwarding && err == nil && target.Scheme == "https" && target.Hostname() != "" &&
		target.User == nil && target.RawQuery == "" && target.Fragment == ""
}

func telemetryTransport() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	transport.DisableKeepAlives = true
	return &http.Client{Transport: transport, Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (a *ClientAPI) forwardTelemetry(r *http.Request) (any, int, error) {
	if r.Method != http.MethodPost {
		return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
	}
	if !telemetryEnabled(a.app.Store.Settings) {
		return nil, 0, failure("TELEMETRY_DISABLED", "Statistics forwarding is disabled", 403, false)
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, 0, failure("UNSUPPORTED_MEDIA_TYPE", "Expected application/json", 415, false)
	}
	batch, err := telemetry.ReadBatch(r.Body)
	if err != nil {
		return nil, 0, failure("INVALID_ARGUMENT", "Invalid statistics batch", 422, false)
	}
	stamp := time.Now().Unix()
	result, err := a.app.Store.state.ExecContext(r.Context(), `INSERT INTO telemetry_send_limits VALUES (?, ?)
		ON CONFLICT(platform) DO UPDATE SET last_attempt=excluded.last_attempt WHERE last_attempt <= ?`,
		batch.Platform, stamp, stamp-86400)
	if err != nil {
		return nil, 0, err
	}
	claimed, err := result.RowsAffected()
	if err != nil || claimed == 0 {
		return nil, 204, err
	}
	data, err := json.Marshal(batch)
	if err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, telemetryReceiverURL, bytes.NewReader(data))
	if err != nil {
		return nil, 204, nil
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Moerise")
	if response, err := a.telemetryClient.Do(request); err == nil {
		response.Body.Close()
	}
	return nil, 204, nil
}
