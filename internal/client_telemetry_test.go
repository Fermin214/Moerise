package moerise

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"moerise/internal/telemetry"
)

func TestTelemetryIsolatedForwardingAndFailure(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, identity := settingsSession(t, api)
	store := api.app.Store
	originalTarget := telemetryReceiverURL
	t.Cleanup(func() { telemetryReceiverURL = originalTarget })
	var sends atomic.Int32
	var redirects atomic.Int32
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			redirects.Add(1)
			return
		}
		sends.Add(1)
		data, err := telemetry.ReadBatch(r.Body)
		if err != nil || data.ClientVersion != "0.3.0" || data.CoreVersion != "unknown" || r.URL.RawQuery != "" ||
			r.Header.Get("User-Agent") != "Moerise" || len(r.Header.Values("Cookie")) != 0 {
			t.Errorf("unexpected forwarding envelope")
		}
		for _, key := range []string{"Authorization", "X-CSRF-Token", "Origin", "Referer", "X-Request-ID", "X-Forwarded-For"} {
			if r.Header.Get(key) != "" {
				t.Errorf("propagated header %s", key)
			}
		}
		http.Redirect(w, r, "/redirect", http.StatusTemporaryRedirect)
	}))
	defer receiver.Close()
	api.telemetryClient.Transport = receiver.Client().Transport
	const body = `{"schema":1,"platform":"web","client_version":"0.3.0","core_version":"unknown","system_major":"unknown","rows":[{"operation":"save_ack","result":"unknown","source":"user","duration":"unknown","count":2},{"operation":"insight","result":"success","source":"user","duration":"lt3s","count":1}]}`
	origin := fixture.settings.Web.PublicOrigin
	post := func(data string, session *http.Cookie, csrf string, status int) {
		t.Helper()
		response := clientCall(api, "POST", "/api/app/v1/telemetry", data, session, origin, csrf)
		if response.Code != status {
			t.Fatalf("submit: %d %s", response.Code, response.Body.String())
		}
	}
	capabilities := func(allowed bool) {
		t.Helper()
		response := clientCall(api, "GET", "/api/app/v1/capabilities", "", nil, "", "")
		var value struct {
			Version   string
			Telemetry struct {
				Schema  int
				Allowed bool
			}
		}
		if json.Unmarshal(response.Body.Bytes(), &value) != nil || value.Version != Version || value.Telemetry.Schema != 1 || value.Telemetry.Allowed != allowed {
			t.Fatal("incorrect public capability", response.Code)
		}
	}
	store.Settings.TelemetryForwarding = true
	for _, target := range []string{"", "http://example.test/v1/batches", "https://owner@example.test/batch", "https://example.test/?destination=other"} {
		telemetryReceiverURL = target
		capabilities(false)
		post(body, cookie, identity.CSRFToken, 403)
	}
	telemetryReceiverURL = receiver.URL + "/v1/batches"
	store.Settings.TelemetryForwarding = false
	capabilities(false)
	post(body, cookie, identity.CSRFToken, 403)
	store.Settings.TelemetryForwarding = true
	capabilities(true)
	post(body, nil, "", 401)
	post(body, cookie, "", 403)
	post(strings.Replace(body, `"schema":1`, `"endpoint":"https://elsewhere.test/","schema":1`, 1), cookie, identity.CSRFToken, 422)
	if sends.Load() != 0 {
		t.Fatal("unexpected network traffic without a valid permitted batch")
	}
	for _, platform := range []string{"web", "android", "web", "android"} {
		post(strings.Replace(body, `"web"`, `"`+platform+`"`, 1), cookie, identity.CSRFToken, 204)
	}
	if sends.Load() != 2 || redirects.Load() != 0 {
		t.Fatal("forwarding did not respect the fixed endpoint and per-platform quota")
	}
	if _, err := store.state.Exec("UPDATE telemetry_send_limits SET last_attempt=0"); err != nil {
		t.Fatal(err)
	}
	receiver.Close()
	post(body, cookie, identity.CSRFToken, 204)
	if err := store.state.QueryRow("SELECT last_attempt FROM telemetry_send_limits WHERE platform='web'").Scan(new(int64)); err != nil {
		t.Fatal(err)
	}
	if reply := clientCall(api, "GET", "/api/app/v1/notes", "", cookie, "", ""); reply.Code != http.StatusOK {
		t.Fatalf("statistics failure affected note requests: %d", reply.Code)
	}
}
