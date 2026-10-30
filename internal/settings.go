package moerise

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const DefaultInsightPrompt = "请围绕所选笔记探索它们之间的联系、思想变化、潜在矛盾和新的可能性。区分笔记原意与新提出的联想和假设。"

type InsightsConfig struct {
	DefaultPrompt string `json:"default_prompt"`
}

type ProductSettings struct {
	Timezone      string             `json:"timezone"`
	Review        ReviewConfig       `json:"review"`
	Notifications NotificationConfig `json:"notifications"`
	Embedding     EmbeddingConfig    `json:"embedding"`
	LLM           LLMConfig          `json:"llm"`
	Channel       ChannelConfig      `json:"channel"`
	Insights      InsightsConfig     `json:"insights"`
	Daily         *dailySettings     `json:"daily,omitempty"`
}

type dailySettings struct {
	Timezone      string             `json:"timezone"`
	Review        ReviewConfig       `json:"review"`
	Notifications NotificationConfig `json:"notifications"`
	Until         time.Time          `json:"until"`
}

type SecretUpdate struct {
	Action string  `json:"action"`
	Value  *string `json:"value,omitempty"`
}

type SettingsUpdate struct {
	Version  int64                      `json:"version"`
	Settings map[string]json.RawMessage `json:"settings,omitempty"`
	Secrets  map[string]SecretUpdate    `json:"secrets,omitempty"`
}

type SecretStatus struct {
	Configured bool `json:"configured"`
}

type ChannelCapabilities struct {
	Text   bool `json:"text"`
	Images bool `json:"images"`
	Record bool `json:"record"`
}

type SettingsView struct {
	Version              int64                   `json:"version"`
	Settings             map[string]any          `json:"settings"`
	Secrets              map[string]SecretStatus `json:"secrets"`
	ChannelCapabilities  ChannelCapabilities     `json:"channel_capabilities"`
	DefaultInsightPrompt string                  `json:"default_insight_prompt"`
}

func productSettings(s Settings) ProductSettings {
	return ProductSettings{s.Timezone, s.Review, s.Notifications, s.Embedding, s.LLM, s.Channel, s.Insights, s.daily}
}

func (p ProductSettings) apply(s Settings) Settings {
	s.Timezone, s.Review, s.Notifications = p.Timezone, p.Review, p.Notifications
	s.Embedding, s.LLM, s.Channel, s.Insights = p.Embedding, p.LLM, p.Channel, p.Insights
	s.daily = p.Daily
	return s
}

func newStateCipher(settings Settings, purpose string) (cipher.AEAD, error) {
	key, err := stateEncryptionKey(settings.Web.StateEncryptionKey)
	if err != nil {
		return nil, err
	}
	key, err = hkdf.Key(sha256.New, key, nil, "moerise/state/"+purpose+"/v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func settingsAAD(settings Settings, version int64) []byte {
	return []byte(settings.Memos.InstanceID + "\x00" + settings.Memos.User + "\x00" + strconv.FormatInt(version, 10))
}

func encryptProduct(settings Settings, version int64) ([]byte, error) {
	aead, err := newStateCipher(settings, "product_settings")
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(productSettings(settings))
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, body, settingsAAD(settings, version)), nil
}

func decryptProduct(settings Settings, version int64, body []byte) (Settings, error) {
	aead, err := newStateCipher(settings, "product_settings")
	if err != nil {
		return settings, err
	}
	bad := errors.New("cannot decrypt product settings with the configured state encryption key")
	if version < 1 || len(body) < aead.NonceSize()+aead.Overhead() {
		return settings, bad
	}
	plain, err := aead.Open(nil, body[:aead.NonceSize()], body[aead.NonceSize():], settingsAAD(settings, version))
	if err != nil {
		return settings, bad
	}
	var product ProductSettings
	if strictJSON(bytes.NewReader(plain), &product) != nil {
		return settings, bad
	}
	return product.apply(settings), nil
}

func storedProduct(db *sql.DB, settings Settings) (Settings, int64, error) {
	var version int64
	var body []byte
	err := db.QueryRow(`SELECT version, ciphertext FROM product_settings WHERE instance_id = ?`,
		settings.Memos.InstanceID).Scan(&version, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, 0, nil
	}
	if err != nil {
		return settings, 0, err
	}
	settings, err = decryptProduct(settings, version, body)
	return settings, version, err
}

// Deployment values initialize product settings once; subsequent loads use the saved version.
func loadPersistedProduct(settings Settings) (Settings, error) {
	path := filepath.Join(settings.StateDir, "state.sqlite3")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return settings, nil
	} else if err != nil {
		return settings, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return settings, err
	}
	slash := filepath.ToSlash(absolute)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	address := url.URL{Scheme: "file", Path: slash, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", address.String())
	if err != nil {
		return settings, err
	}
	defer db.Close()
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'product_settings')`).Scan(&exists); err != nil || !exists {
		return settings, err
	}
	saved, _, err := storedProduct(db, settings)
	return saved, err
}

func (s *Store) initializeProductSettings() error {
	saved, version, err := storedProduct(s.state, s.Settings)
	if err != nil {
		return err
	}
	if version > 0 {
		s.Settings, s.productVersion = saved, version
		return nil
	}
	if s.Settings.Web.StateEncryptionKey == "" {
		return nil
	}
	body, err := encryptProduct(s.Settings, 1)
	if err != nil {
		return err
	}
	_, err = s.state.Exec(`INSERT INTO product_settings VALUES (?, 1, ?, ?)`,
		s.Settings.Memos.InstanceID, body, nowText())
	if err == nil {
		s.productVersion = 1
	}
	return err
}

func secretFields(product *ProductSettings) map[string]**string {
	return map[string]**string{
		"llm_api_key": &product.LLM.APIKey, "embedding_api_key": &product.Embedding.APIKey,
		"app_secret": &product.Channel.AppSecret, "verification_token": &product.Channel.VerificationToken,
		"encrypt_key": &product.Channel.EncryptKey, "webhook_url": &product.Channel.WebhookURL,
		"webhook_secret": &product.Channel.WebhookSecret,
	}
}

func publicProduct(product ProductSettings) map[string]any {
	data, _ := json.Marshal(product)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	delete(result, "daily")
	result["notifications"].(map[string]any)["times"] = product.Notifications.Clocks()
	for _, section := range []string{"llm", "embedding"} {
		delete(result[section].(map[string]any), "api_key")
	}
	channel := result["channel"].(map[string]any)
	for _, key := range []string{"app_secret", "verification_token", "encrypt_key", "webhook_url", "webhook_secret"} {
		delete(channel, key)
	}
	return result
}

func (s *Store) reviewSettings(at time.Time) Settings {
	settings := s.Settings
	if previous := settings.daily; previous != nil && at.Before(previous.Until) {
		settings.Timezone, settings.Review = previous.Timezone, previous.Review
		enabled := settings.Notifications.Enabled
		settings.Notifications = previous.Notifications
		settings.Notifications.Enabled = enabled
	}
	return settings
}

func (s *Store) settingsView() SettingsView {
	product := productSettings(s.Settings)
	secrets := map[string]SecretStatus{}
	for key, field := range secretFields(&product) {
		secrets[key] = SecretStatus{configured(*field) != ""}
	}
	channel := product.Channel
	app := configured(channel.AppID) != "" && configured(channel.AppSecret) != ""
	record := channel.Kind == "feishu-app" && app && configured(channel.VerificationToken) != "" && configured(channel.UserOpenID) != ""
	text := record || channel.Kind == "feishu-webhook" && configured(channel.WebhookURL) != ""
	return SettingsView{s.productVersion, publicProduct(product), secrets,
		ChannelCapabilities{text, text && app, record}, DefaultInsightPrompt}
}

func mergeSettings(target map[string]any, changes map[string]json.RawMessage) error {
	bad := failure("INVALID_ARGUMENT", "Invalid settings fields", 422, false)
	for key, raw := range changes {
		previous, exists := target[key]
		if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return bad
		}
		if object, ok := previous.(map[string]any); ok {
			var patch map[string]json.RawMessage
			if json.Unmarshal(raw, &patch) != nil || patch == nil {
				return bad
			}
			if err := mergeSettings(object, patch); err != nil {
				return err
			}
		} else {
			var value any
			if json.Unmarshal(raw, &value) != nil {
				return bad
			}
			target[key] = value
		}
	}
	return nil
}

func (s *Store) prepareSettings(update SettingsUpdate) (Settings, error) {
	bad := failure("INVALID_ARGUMENT", "Invalid product settings", 422, false)
	if update.Version != s.productVersion {
		return s.Settings, failure("SETTINGS_CONFLICT", "Settings have changed; reload before saving", 409, false)
	}
	if s.productVersion == 0 {
		return s.Settings, failure("SETTINGS_UNAVAILABLE", "Configure the state encryption key before saving settings", 503, false)
	}
	product := productSettings(s.Settings)
	view := publicProduct(product)
	if err := mergeSettings(view, update.Settings); err != nil {
		return s.Settings, err
	}
	body, _ := json.Marshal(view)
	// Decode into the existing value so omitted secret fields retain their values.
	if strictJSON(bytes.NewReader(body), &product) != nil {
		return s.Settings, bad
	}
	fields := secretFields(&product)
	for name, operation := range update.Secrets {
		field, exists := fields[name]
		if !exists {
			return s.Settings, bad
		}
		switch operation.Action {
		case "keep", "clear":
			if operation.Value != nil {
				return s.Settings, bad
			}
			if operation.Action == "clear" {
				*field = nil
			}
		case "replace":
			value := configured(operation.Value)
			if value == "" || strings.ContainsAny(value, "*•●") || len(value) > 16384 {
				return s.Settings, bad
			}
			*field = &value
		default:
			return s.Settings, bad
		}
	}
	result := product.apply(s.Settings)
	if !utf8.ValidString(result.Insights.DefaultPrompt) || strings.TrimSpace(result.Insights.DefaultPrompt) == "" ||
		utf8.RuneCountInString(result.Insights.DefaultPrompt) > 16000 {
		return s.Settings, bad
	}
	if err := result.Validate(); err != nil {
		return s.Settings, bad
	}
	return result, nil
}

func (s *Store) persistSettings(settings Settings) error {
	version := s.productVersion + 1
	body, err := encryptProduct(settings, version)
	if err != nil {
		return err
	}
	result, err := s.state.Exec(`UPDATE product_settings SET version = ?, ciphertext = ?, updated_at = ?
		WHERE instance_id = ? AND version = ?`, version, body, nowText(), settings.Memos.InstanceID, s.productVersion)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err == nil && changed != 1 {
		return failure("SETTINGS_CONFLICT", "Settings have changed; reload before saving", 409, false)
	}
	if err == nil {
		s.productVersion = version
	}
	return err
}
